package firehose_reader

import (
	"context"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dmetrics"
	"go.uber.org/zap"
	"time"
)

type firehoseReaderStats struct {
	lastBlock pbbstream.BlockRef
	blockRate *dmetrics.AvgRatePromCounter

	cancelPeriodicLogger context.CancelFunc
}

func newFirehoseReaderStats() *firehoseReaderStats {
	return &firehoseReaderStats{
		lastBlock: pbbstream.BlockRef{},
		blockRate: dmetrics.MustNewAvgRateFromPromCounter(BlockReadCount, 1*time.Second, 30*time.Second, "blocks"),
	}
}

func (s *firehoseReaderStats) StartPeriodicLogToZap(ctx context.Context, logger *zap.Logger, logEach time.Duration) {
	ctx, s.cancelPeriodicLogger = context.WithCancel(ctx)

	go func() {
		ticker := time.NewTicker(logEach)
		for {
			select {
			case <-ticker.C:
				logger.Info("reader node statistics", s.ZapFields()...)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *firehoseReaderStats) StopPeriodicLogToZap() {
	if s.cancelPeriodicLogger != nil {
		s.cancelPeriodicLogger()
	}
}

func (s *firehoseReaderStats) ZapFields() []zap.Field {
	fields := []zap.Field{
		zap.Stringer("block_rate", s.blockRate),
		zap.Uint64("last_block_num", s.lastBlock.Num),
		zap.String("last_block_id", s.lastBlock.Id),
	}

	return fields
}
