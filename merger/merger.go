// Copyright 2019 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package merger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
)

type Merger struct {
	*shutter.Shutter
	grpcListenAddr string

	io                   IOInterface
	firstStreamableBlock uint64
	logger               *zap.Logger

	timeBetweenPolling time.Duration

	timeBetweenPruning         time.Duration
	pruningDistanceToLIB       uint64
	oneBlockFilesPruneDistance uint64

	bundler *Bundler
}

func NewMerger(
	logger *zap.Logger,
	grpcListenAddr string,
	io IOInterface,

	firstStreamableBlock uint64,
	bundleSize uint64,
	pruningDistanceToLIB uint64,
	oneBlockFilesPruneDistance uint64,
	timeBetweenPruning time.Duration,
	timeBetweenPolling time.Duration,
	stopBlock uint64,
	maxMergingThreads int,
) *Merger {
	// floor at bundleSize so we never delete not-yet-merged one-block files
	if oneBlockFilesPruneDistance < bundleSize {
		if oneBlockFilesPruneDistance != 0 {
			logger.Warn("one-block-files prune distance is below the bundle size, raising it to the bundle size",
				zap.Uint64("requested", oneBlockFilesPruneDistance),
				zap.Uint64("bundle_size", bundleSize),
			)
		}
		oneBlockFilesPruneDistance = bundleSize
	}

	m := &Merger{
		Shutter:                    shutter.New(),
		grpcListenAddr:             grpcListenAddr,
		io:                         io,
		firstStreamableBlock:       firstStreamableBlock,
		pruningDistanceToLIB:       pruningDistanceToLIB,
		oneBlockFilesPruneDistance: oneBlockFilesPruneDistance,
		timeBetweenPolling:         timeBetweenPolling,
		timeBetweenPruning:         timeBetweenPruning,
		logger:                     logger,
	}

	m.bundler = NewBundler(firstStreamableBlock, stopBlock, firstStreamableBlock, bundleSize, io, maxMergingThreads, m.Shutdown)
	m.OnTerminating(func(_ error) { m.bundler.WaitForMerges() }) // wait for all in-flight async merges to complete

	return m
}

func (m *Merger) Run() {
	m.logger.Info("starting merger")

	m.startGRPCServer()

	m.startOldFilesPruner()
	m.startForkedBlocksPruner()

	err := m.run()
	if err != nil {
		m.logger.Error("merger returned error", zap.Error(err))
	}
	m.Shutdown(err)
}

func (m *Merger) startForkedBlocksPruner() {
	forkableIO, ok := m.io.(ForkAwareIOInterface)
	if !ok {
		return
	}
	m.logger.Info("starting pruning of forked files",
		zap.Uint64("pruning_distance_to_lib", m.pruningDistanceToLIB),
		zap.Duration("time_between_pruning", m.timeBetweenPruning),
	)

	go func() {
		delay := m.timeBetweenPruning // do not start pruning immediately
		for {
			time.Sleep(delay)
			now := time.Now()

			pruningTarget := m.pruningTarget(m.pruningDistanceToLIB)
			forkableIO.DeleteForkedBlocksAsync(bstream.GetProtocolFirstStreamableBlock, pruningTarget)

			if spentTime := time.Since(now); spentTime < m.timeBetweenPruning {
				delay = m.timeBetweenPruning - spentTime
			}
		}
	}()

}

func (m *Merger) startOldFilesPruner() {
	m.logger.Info("starting pruning of unused (old) one-block-files",
		zap.Uint64("pruning_distance_to_lib", m.oneBlockFilesPruneDistance),
		zap.Duration("time_between_pruning", m.timeBetweenPruning),
	)
	go func() {
		delay := m.timeBetweenPruning // do not start pruning immediately

		unfinishedDelay := time.Second * 5
		if unfinishedDelay > delay {
			unfinishedDelay = delay / 2
		}

		ctx := context.Background()
		for {
			time.Sleep(delay)

			var toDelete []*bstream.OneBlockFile

			pruningTarget := m.pruningTarget(m.oneBlockFilesPruneDistance)
			if pruningTarget == 0 {
				m.logger.Debug("skipping file deletion until we have a pruning target")
				continue
			}

			delay = m.timeBetweenPruning
			err := m.io.WalkOneBlockFiles(ctx, m.firstStreamableBlock, func(obf *bstream.OneBlockFile) error {
				if obf.Num < pruningTarget {
					toDelete = append(toDelete, obf)
				}
				if len(toDelete) >= DefaultFilesDeleteBatchSize {
					delay = unfinishedDelay
					return ErrStopBlockReached
				}
				return nil
			})
			if err != nil && !errors.Is(err, ErrStopBlockReached) {
				m.logger.Warn("error while walking oneBlockFiles", zap.Error(err))
			}

			m.io.DeleteAsync(toDelete)
		}
	}()
}

func (m *Merger) pruningTarget(distance uint64) uint64 {
	bundlerBase := m.bundler.getSafeBaseBlockNum()
	if distance > bundlerBase {
		return 0
	}

	return bundlerBase - distance
}

func (m *Merger) run() error {
	ctx := context.Background()

	var holeFoundLogged bool
	var consecutiveErrors int
	for {
		now := time.Now()
		if m.IsTerminating() {
			return nil
		}

		base, lib, err := m.io.NextBundle(ctx, m.bundler.baseBlockNum)
		if err != nil {
			if errors.Is(err, ErrHoleFound) {
				if holeFoundLogged {
					m.logger.Debug("found hole in merged files. this is not normal behavior unless reprocessing batches", zap.Error(err))
				} else {
					holeFoundLogged = true
					m.logger.Warn("found hole in merged files (next occurrence will show up as Debug)", zap.Error(err))
				}
			} else {
				return err
			}
		}

		if m.bundler.stopBlock != 0 && base > m.bundler.stopBlock {
			if err == ErrStopBlockReached {
				m.logger.Info("stop block reached")
				return nil
			}
		}

		if base > m.bundler.baseBlockNum { // means we jump forward because the merged-blocks have been produced by someone else
			m.bundler.Reset(base, lib)
		}

		unlinkableCount := 0
		maxUnlinkableBlocks := int(m.bundler.bundleSize * 4)
		lastBase := m.bundler.baseBlockNum

		err = m.io.WalkOneBlockFiles(ctx, base, func(obf *bstream.OneBlockFile) error {
			if m.IsTerminating() {
				return errTerminating
			}
			if lastBase != m.bundler.baseBlockNum { // reset count every time we do a bundle
				unlinkableCount = 0
				lastBase = m.bundler.baseBlockNum
			}
			if obf.Num > m.bundler.baseBlockNum && !m.bundler.forkable.Linkable(obf.ToBstreamBlock()) {
				unlinkableCount++
				if unlinkableCount > maxUnlinkableBlocks {
					m.logger.Info("too many unlinkable blocks, continuing to next loop", zap.Uint64("base", obf.Num), zap.Int("unlinkable_count", unlinkableCount), zap.Stringer("last_seen_block", obf))
					return errCheckLoop // we have too many unlinkable blocks, continue to next loop in case
				}
			}
			return m.bundler.HandleBlockFile(obf)
		})

		switch err {
		case nil:
			consecutiveErrors = 0
		case errCheckLoop:
			continue
		case errTerminating:
			return nil
		case ErrStopBlockReached:
			m.logger.Info("stop block reached")
			return nil
		default:
			consecutiveErrors = 0
			consecutiveErrors++
			if consecutiveErrors >= 10 {
				return fmt.Errorf("too many consecutive errors: %w", err)
			}
			m.logger.Warn("error walking one block files, will retry", zap.Error(err))
		}

		if spentTime := time.Since(now); spentTime < m.timeBetweenPolling {
			time.Sleep(m.timeBetweenPolling - spentTime)
		}
	}
}
