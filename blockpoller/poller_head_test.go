package blockpoller

import (
	"context"
	"testing"
	"time"

	"github.com/streamingfast/firehose-core/rpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type testHeadBlockFetcher struct {
	*TestBlockFetcher[any]
	head uint64
}

func (f *testHeadBlockFetcher) FetchHeadBlockNumber(ctx context.Context, client any) (uint64, error) {
	return f.head, nil
}

func TestBlockPoller_warnIfAboveHeadBlock(t *testing.T) {
	cases := []struct {
		name       string
		fetcher    BlockFetcher[any]
		startBlock uint64
		expectWarn bool
	}{
		{"above head", &testHeadBlockFetcher{head: 50}, 60, true},
		{"at head", &testHeadBlockFetcher{head: 50}, 50, false},
		{"below head", &testHeadBlockFetcher{head: 50}, 40, false},
		{"fetcher without head block number", &TestBlockFetcher[any]{}, 60, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.WarnLevel)
			clients := rpc.NewClients[any](1*time.Second, rpc.NewRollingStrategyAlwaysUseFirst[any](), zap.NewNop())
			clients.Add(new(any))

			poller := &BlockPoller[any]{blockFetcher: c.fetcher, clients: clients}
			poller.warnIfAboveHeadBlock(c.startBlock, zap.New(core))

			warnings := logs.FilterMessage("start block is above the chain head block, the poller waits until the chain reaches it").All()
			if !c.expectWarn {
				assert.Empty(t, warnings)
				return
			}

			require.Len(t, warnings, 1)
			fields := warnings[0].ContextMap()
			assert.Equal(t, uint64(60), fields["start_block"])
			assert.Equal(t, uint64(50), fields["head_block"])
			assert.Equal(t, uint64(10), fields["blocks_ahead"])
		})
	}
}
