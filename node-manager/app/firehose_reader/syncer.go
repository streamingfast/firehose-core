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

package firehose_reader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mostynb/go-grpc-compression/zstd"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/blockstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dgrpc"
	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/firehose/client"
	nodeManager "github.com/streamingfast/firehose-core/node-manager"
	"github.com/streamingfast/firehose-core/node-manager/mindreader"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

type syncer struct {
	*shutter.Shutter
	appCtx             context.Context
	blockStreamServer  *blockstream.Server
	config             Config
	firehoseConfig     FirehoseConfig
	logger             *zap.Logger
	store              dstore.Store
	testModeComparator *mindreader.TestModeComparator
}

func (s *syncer) Run() error {
	stateFileContent, err := os.ReadFile(s.config.StateFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("unable to read cursor file: %w", err)
	}

	firehoseClient, connClose, grpcCallOpts, err := client.NewFirehoseClient(
		s.firehoseConfig.Endpoint,
		s.firehoseConfig.ApiToken,
		s.firehoseConfig.ApiKey,
		s.firehoseConfig.InsecureConn,
		s.firehoseConfig.PlaintextConn,
	)
	if err != nil {
		return err
	}
	defer connClose()

	grpcCallOpts = append(grpcCallOpts, grpc.UseCompressor(zstd.Name))

	lastCursor := string(stateFileContent)
	if len(lastCursor) > 0 {
		cur, err := bstream.CursorFromOpaque(lastCursor)
		if err != nil {
			return fmt.Errorf("decoding cursor from state file %q: %w", s.config.StateFile, err)
		}

		if cur.Block.Num() < s.config.StartBlockNum {
			s.logger.Warn("cursor in state file is older than configured start block, discarding it",
				zap.Uint64("cursor_block_num", cur.Block.Num()),
				zap.Uint64("start_block_num", s.config.StartBlockNum),
				zap.String("state_file", s.config.StateFile),
			)
			if err := os.Remove(s.config.StateFile); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("removing stale state file %q: %w", s.config.StateFile, err)
			}
			lastCursor = ""
			s.logger.Info("stalled stale file removed, restarting from start block num", zap.Uint64("start_block_num", s.config.StartBlockNum))
		} else {
			s.logger.Info("found state file, continuing previous run", zap.String("cursor", lastCursor), zap.String("state_file", s.config.StateFile))
		}
	} else {
		s.logger.Info("no state file found, starting from block num", zap.Uint64("start_block_num", s.config.StartBlockNum))
	}

	metricsManager := nodeManager.NewMetricsAndReadinessManager(HeadBlockTimeDrift, HeadBlockNumber, HeadBlockRelativeTime, AppReadiness, s.config.ReadinessMaxLatency, nodeManager.WithFinalizedBlockNumberMetric(FinalizedBlockNumber))
	go metricsManager.Launch()

	stats := newSyncerStats(metricsManager)
	stats.StartPeriodicLogToZap(s.appCtx, s.logger, 15*time.Second)
	defer stats.StopPeriodicLogToZap()

	s.logger.Info("starting block syncer")
	for {
		stream, err := firehoseClient.Blocks(s.appCtx, &pbfirehose.Request{
			StartBlockNum: int64(s.config.StartBlockNum),
			StopBlockNum:  s.config.StopBlockNum,
			Cursor:        lastCursor,
		}, grpcCallOpts...)
		if err != nil {
			s.logger.Error("unable to get blocks", zap.Error(err))
			time.Sleep(5 * time.Second)
			continue
		}

		for {
			response, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					s.logger.Info("end of stream, stop block has been reached")
					return nil
				}

				if dgrpcError := dgrpc.AsGRPCError(err); dgrpcError != nil {
					switch dgrpcError.Code() {
					case codes.Unauthenticated:
						return fmt.Errorf("stream failure: %w", err)

					case codes.InvalidArgument:
						return fmt.Errorf("stream invalid: %w", err)
					}
				}

				if errors.Is(err, context.Canceled) {
					s.logger.Info("context canceled, stopping syncer")
					return nil
				}

				s.logger.Error("unable to receive block", zap.Error(err))
				time.Sleep(5 * time.Second)
				break
			}

			pbBlock := &pbbstream.Block{
				Number:    response.Metadata.Num,
				Id:        response.Metadata.Id,
				ParentId:  response.Metadata.ParentId,
				Timestamp: response.Metadata.Time,
				LibNum:    response.Metadata.LibNum,
				ParentNum: response.Metadata.ParentNum,
				Payload:   response.Block,
			}

			// In test mode, compare blocks instead of writing them
			if s.testModeComparator != nil {
				if err := s.testModeComparator.CompareBlock(s.appCtx, pbBlock); err != nil {
					s.logger.Warn("failed to compare block in test mode",
						zap.Uint64("block_num", pbBlock.Number),
						zap.String("block_id", pbBlock.Id),
						zap.Error(err),
					)
					// Don't fail on comparison errors, just log and continue
				}
			} else {
				// Normal mode: write blocks to disk
				oneBlockFilename := bstream.BlockFileNameWithSuffix(pbBlock, s.config.OneBlockSuffix)
				s.logger.Debug("writing block", zap.Uint64("block_num", pbBlock.Number), zap.String("filename", oneBlockFilename))

				pr, pw := io.Pipe()

				//write block data to pipe, and then close to signal end of data
				go func(block *pbbstream.Block) {
					var err error
					defer func() {
						pw.CloseWithError(err)
					}()

					bw, err := bstream.NewDBinBlockWriter(pw)
					if err != nil {
						s.Shutdown(fmt.Errorf("creating block writer: %w", err))
						return
					}

					err = bw.Write(block)
					if err != nil {
						s.Shutdown(fmt.Errorf("writing block: %w", err))
						return
					}
				}(pbBlock)

				err = s.store.WriteObject(s.appCtx, oneBlockFilename, pr)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						s.logger.Info("context canceled, stopping syncer")
						return nil
					}

					return fmt.Errorf("writing block %d to %s: %w", pbBlock.Number, oneBlockFilename, err)
				}

				s.logger.Debug("block written", zap.Uint64("block_num", pbBlock.Number), zap.String("filename", oneBlockFilename))
			}

			// Only push blocks to block stream server in normal mode (not in test mode)
			if s.testModeComparator == nil {
				if err := s.blockStreamServer.PushBlock(pbBlock); err != nil {
					return fmt.Errorf("pushing block %d to blockstream server: %w", pbBlock.Number, err)
				}
			}

			BlockWriteCount.Inc()
			metricsManager.UpdateHeadBlock(pbBlock)

			lastCursor = response.Cursor

			err = s.writeCursor(lastCursor)
			if err != nil {
				return fmt.Errorf("writing cursor: %w", err)
			}
		}
	}
}

func (s *syncer) writeCursor(cursor string) error {
	err := os.WriteFile(s.config.StateFile, []byte(cursor), 0644)
	if err != nil {
		return fmt.Errorf("failed to write cursor to state file: %w", err)
	}

	return nil
}

type syncerStats struct {
	blockRate      *dmetrics.AvgRatePromCounter
	metricsManager *nodeManager.MetricsAndReadinessManager

	cancelPeriodicLogger context.CancelFunc
}

func newSyncerStats(metricsManager *nodeManager.MetricsAndReadinessManager) *syncerStats {
	return &syncerStats{
		metricsManager: metricsManager,
		blockRate:      dmetrics.MustNewAvgRateFromPromCounter(BlockWriteCount, 1*time.Second, 30*time.Second, "blocks"),
	}
}

func (s *syncerStats) StartPeriodicLogToZap(ctx context.Context, logger *zap.Logger, logEach time.Duration) {
	ctx, s.cancelPeriodicLogger = context.WithCancel(ctx)

	go func() {
		ticker := time.NewTicker(logEach)
		for {
			select {
			case <-ticker.C:
				logger.Info("reader node firehose statistics", s.ZapFields()...)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *syncerStats) StopPeriodicLogToZap() {
	if s.cancelPeriodicLogger != nil {
		s.cancelPeriodicLogger()
	}
}

func (s *syncerStats) ZapFields() []zap.Field {
	fields := []zap.Field{
		zap.Stringer("block_rate", s.blockRate),
		zap.Stringer("last_block", s.metricsManager.GetHeadBlock()),
	}

	return fields
}
