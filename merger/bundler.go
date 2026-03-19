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
	"sync"
	"time"

	"github.com/abourget/llerrgroup"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	"github.com/streamingfast/firehose-core/merger/metrics"
	"github.com/streamingfast/logging"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

var ErrStopBlockReached = errors.New("stop block reached")
var ErrFirstBlockAfterInitialStreamableBlock = errors.New("received first block after inital streamable block")
var errTerminating = errors.New("terminating")
var errCheckLoop = errors.New("max blocks read over bundle, continuing to next loop")

type Bundler struct {
	sync.Mutex

	io IOInterface

	baseBlockNum uint64

	bundleSize                 uint64
	bundleError                chan error
	eg                         *llerrgroup.Group
	maxMergingThreads          int
	inFlightMu                 sync.Mutex
	inFlightBundles            map[uint64]bool
	stopBlock                  uint64
	enforceNextBlockOnBoundary bool
	firstStreamableBlock       uint64

	seenBlockFiles     map[string]*bstream.OneBlockFile
	irreversibleBlocks []*bstream.OneBlockFile
	forkable           *forkable.Forkable

	blockTimestampInFlight *atomic.Bool
	blockTimestampLastRun  *atomic.Int64 // unix nanos

	logger *zap.Logger
}

var logger, _ = logging.PackageLogger("merger", "github.com/streamingfast/firehose-core/merger/bundler")

func NewBundler(startBlock, stopBlock, firstStreamableBlock, bundleSize uint64, io IOInterface, maxMergingThreads int) *Bundler {
	if maxMergingThreads < 1 {
		maxMergingThreads = 1
	}
	b := &Bundler{
		bundleSize:             bundleSize,
		io:                     io,
		bundleError:            make(chan error, 1),
		firstStreamableBlock:   firstStreamableBlock,
		stopBlock:              stopBlock,
		eg:                     llerrgroup.New(maxMergingThreads),
		maxMergingThreads:      maxMergingThreads,
		inFlightBundles:        make(map[uint64]bool),
		seenBlockFiles:         make(map[string]*bstream.OneBlockFile),
		blockTimestampInFlight: atomic.NewBool(false),
		blockTimestampLastRun:  atomic.NewInt64(0),
		logger:                 logger,
	}
	b.Reset(toBaseNum(startBlock, bundleSize), nil)
	return b
}

// TooFarAhead indicates a sane number of one-blocks to walk over current baseBlockNum before you should re-evaluate if merged-blocks exist or if maybe you skipped some blocks
func (b *Bundler) TooFarAhead(blk uint64) bool {
	return blk > b.baseBlockNum+b.bundleSize*uint64(b.maxMergingThreads+4) // 5x ahead is safe, no chain skips that many blocks
}

// LowestUnmergedBlockNum can be called from a different thread.
// It returns the lowest block number that has not yet been confirmed merged,
// which is the minimum in-flight merge base (or current baseBlockNum if none in flight).
// This is used as a safe pruning boundary: one-block-files below this value are safe to delete.
func (b *Bundler) LowestUnmergedBlockNum() uint64 {
	b.Lock()
	cur := b.baseBlockNum
	b.Unlock()

	b.inFlightMu.Lock()
	defer b.inFlightMu.Unlock()
	for base := range b.inFlightBundles {
		if base < cur {
			cur = base
		}
	}
	return cur
}

// WaitForMerges blocks until all in-flight async merges have completed.
func (b *Bundler) WaitForMerges() {
	_ = b.eg.Wait()
}

func (b *Bundler) HandleBlockFile(obf *bstream.OneBlockFile) error {
	b.seenBlockFiles[obf.CanonicalName] = obf
	return b.forkable.ProcessBlock(obf.ToBstreamBlock(), obf) // forkable will call our own b.ProcessBlock() on irreversible blocks only
}

func (b *Bundler) forkedBlocksInCurrentBundle() (out []*bstream.OneBlockFile) {
	highBoundary := b.baseBlockNum + b.bundleSize

	// remove irreversible blocks from map (they will be merged and deleted soon)
	for _, block := range b.irreversibleBlocks {
		delete(b.seenBlockFiles, block.CanonicalName)
	}

	// identify and then delete remaining blocks from map, return them as forks
	for name, block := range b.seenBlockFiles {
		if block.Num < b.baseBlockNum {
			delete(b.seenBlockFiles, name) // too old, just cleaning up the map of lingering old blocks
		}
		if block.Num < highBoundary {
			out = append(out, block)
			delete(b.seenBlockFiles, name)
		}
	}
	return
}

func (b *Bundler) Reset(nextBase uint64, lib bstream.BlockRef) {
	options := []forkable.Option{
		forkable.WithFilters(bstream.StepIrreversible),
		forkable.HoldBlocksUntilLIB(),
		forkable.WithWarnOnUnlinkableBlocks(100), // don't warn too soon, sometimes oneBlockFiles are uploaded out of order from mindreader (on remote I/O)
	}
	if lib != nil {
		options = append(options, forkable.WithInclusiveLIB(lib))
		b.enforceNextBlockOnBoundary = false // we don't need to check first block because we know it will be linked to lib
	} else {
		b.enforceNextBlockOnBoundary = true
	}
	b.forkable = forkable.New(b, options...)

	b.Lock()
	b.baseBlockNum = nextBase
	b.irreversibleBlocks = nil
	b.Unlock()
}

func (b *Bundler) ProcessBlock(_ *pbbstream.Block, obj interface{}) error {
	obf := obj.(bstream.ObjectWrapper).WrappedObject().(*bstream.OneBlockFile)
	if obf.Num < b.baseBlockNum {
		// we may be receiving an inclusive LIB just before our bundle, ignore it
		return nil
	}

	if b.enforceNextBlockOnBoundary {
		if obf.Num != b.baseBlockNum && obf.Num != b.firstStreamableBlock {
			//{"severity":"ERROR","timestamp":"2023-11-07T12:28:34.735713163-05:00","logger":"merger","message":"expecting to start at block `base_block_num` but got block `block_num` (and we have no previous blockID to align with..). First streamable block is configured to be: `first_streamable_block`",
			//"base_block_num":22207900,
			//"block_num":22208900,
			//"first_streamable_block":22207900,
			//"logging.googleapis.com/labels":{},"serviceContext":{"service":"unknown"}}
			b.logger.Error(
				"expecting to start at block `base_block_num` but got block `block_num` (and we have no previous blockID to align with..). First streamable block is configured to be: `first_streamable_block`",
				zap.Uint64("base_block_num", b.baseBlockNum),
				zap.Uint64("block_num", obf.Num),
				zap.Uint64("first_streamable_block", b.firstStreamableBlock),
			)
			return ErrFirstBlockAfterInitialStreamableBlock
		}
		b.enforceNextBlockOnBoundary = false
	}

	if obf.Num < b.baseBlockNum+b.bundleSize {
		b.Lock()
		metrics.AppReadiness.SetReady()
		b.irreversibleBlocks = append(b.irreversibleBlocks, obf)
		metrics.HeadBlockNumber.SetUint64(obf.Num)
		b.Unlock()
		if time.Since(time.Unix(0, b.blockTimestampLastRun.Load())) >= time.Second*5 && b.blockTimestampInFlight.CompareAndSwap(false, true) {
			b.blockTimestampLastRun.Store(time.Now().UnixNano())
			go func() {
				defer b.blockTimestampInFlight.Store(false)
				t, err := readBlockTimestamp(context.Background(), obf, b.io.OpenOneBlockFile)
				if err != nil {
					b.logger.Debug("cannot read block timestamp for head drift metric", zap.Error(err))
					return
				}
				metrics.HeadBlockTimeDrift.SetBlockTime(t)
			}()
		}
		return nil
	}

	select {
	case err := <-b.bundleError:
		return err
	default:
	}

	forkedBlocks := b.forkedBlocksInCurrentBundle()
	blocksToBundle := b.irreversibleBlocks
	baseBlockNum := b.baseBlockNum

	// Track in-flight before Stop() so LowestUnmergedBlockNum() is conservative even while waiting for a slot.
	b.inFlightMu.Lock()
	b.inFlightBundles[baseBlockNum] = true
	b.inFlightMu.Unlock()

	// Stop() blocks until a slot is free; Go() launches the goroutine. Errors are never returned
	// through eg (they go to bundleError), so Stop() will never return true.
	b.eg.Stop()
	b.eg.Go(func() error {
		defer func() {
			b.inFlightMu.Lock()
			delete(b.inFlightBundles, baseBlockNum)
			b.inFlightMu.Unlock()
		}()
		if err := b.io.MergeAndStore(context.Background(), baseBlockNum, blocksToBundle); err != nil {
			b.bundleError <- err
			return nil // errors are consumed via bundleError, not propagated through eg
		}
		if forkableIO, ok := b.io.(ForkAwareIOInterface); ok {
			forkableIO.MoveForkedBlocks(context.Background(), forkedBlocks)
		}
		// we do not delete bundled blocks here, they get pruned later. keeping the blocks from the last bundle is useful for bootstrapping
		return nil
	})

	b.Lock()
	// we keep the last block of the bundle, only deleting it on next merge, to facilitate joining to one-block-filled hub
	lastBlock := b.irreversibleBlocks[len(b.irreversibleBlocks)-1]
	b.irreversibleBlocks = []*bstream.OneBlockFile{lastBlock, obf}
	b.baseBlockNum += b.bundleSize
	for obf.Num > b.baseBlockNum+b.bundleSize { // skip more merged-block-files
		capturedBase := b.baseBlockNum
		b.inFlightMu.Lock()
		b.inFlightBundles[capturedBase] = true
		b.inFlightMu.Unlock()
		if b.eg.Stop() {
			break
		}
		b.eg.Go(func() error { // lastBlock will be excluded from bundle but is useful to bundler
			defer func() {
				b.inFlightMu.Lock()
				delete(b.inFlightBundles, capturedBase)
				b.inFlightMu.Unlock()
			}()
			if err := b.io.MergeAndStore(context.Background(), capturedBase, []*bstream.OneBlockFile{lastBlock}); err != nil {
				b.bundleError <- err
			}
			return nil
		})
		b.baseBlockNum += b.bundleSize
	}
	b.Unlock()

	if b.stopBlock != 0 && b.baseBlockNum >= b.stopBlock {
		return ErrStopBlockReached
	}

	return nil
}

// String can be called from a different thread
func (b *Bundler) String() string {
	b.Lock()
	defer b.Unlock()

	var firstBlock, lastBlock string
	length := len(b.irreversibleBlocks)
	if length != 0 {
		firstBlock = b.irreversibleBlocks[0].String()
		lastBlock = b.irreversibleBlocks[length-1].String()
	}

	return fmt.Sprintf(
		"bundle_size: %d, base_block_num: %d, first_block: %s, last_block: %s, length: %d",
		b.bundleSize,
		b.baseBlockNum,
		firstBlock,
		lastBlock,
		length,
	)
}
