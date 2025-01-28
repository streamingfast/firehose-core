package blockpoller

import (
	"time"

	"go.uber.org/zap"
)

type Option[C any] func(*BlockPoller[C])

func WithDelayBetweenFetch[C any](v time.Duration) Option[C] {
	return func(p *BlockPoller[C]) {
		p.delayBetweenFetch = v
	}
}

func WithBlockFetchRetryCount[C any](v uint64) Option[C] {
	return func(p *BlockPoller[C]) {
		p.fetchBlockRetryCount = v
	}
}

func WithStoringState[C any](stateStorePath string) Option[C] {
	return func(p *BlockPoller[C]) {
		p.stateStorePath = stateStorePath
	}
}

// IgnoreCursor ensures the poller will ignore the cursor and start from the startBlockNum
// the cursor will still be saved as the poller progresses
func IgnoreCursor[C any]() Option[C] {
	return func(p *BlockPoller[C]) {
		p.ignoreCursor = true
	}
}

func WithLogger[C any](logger *zap.Logger) Option[C] {
	return func(p *BlockPoller[C]) {
		p.logger = logger
	}
}
