package blockpoller

import "go.uber.org/zap"

type Option func(*BlockPoller)

func WithBlockFetchRetryCount(v uint64) Option {
	return func(p *BlockPoller) {
		p.fetchBlockRetryCount = v
	}
}

func WithStoringState(stateStorePath string) Option {
	return func(p *BlockPoller) {
		p.stateStorePath = stateStorePath
	}
}

func WithLogger(logger *zap.Logger) Option {
	return func(p *BlockPoller) {
		p.logger = logger
	}
}
