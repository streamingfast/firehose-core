package blockpoller

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/firehose-core/rpc"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// linChainFetcher is a thread-safe fetcher for a simple linear chain. It is used
// to exercise parallel block fetching (blockFetchBatchSize > 1). It deliberately
// delays LOWER block numbers MORE than higher ones, so the concurrent fetches
// complete out of order — the poller must still fire blocks to the handler in
// strict ascending order. The sleep uses the synctest fake clock, so completion
// order is scrambled deterministically with no real wall-clock delay.
type linChainFetcher struct {
	libNum   uint64
	maxBlock uint64

	mu      sync.Mutex
	fetched int
}

func (f *linChainFetcher) IsBlockAvailable(uint64) bool { return true }

func (f *linChainFetcher) Fetch(ctx context.Context, c any, num uint64) (*pbbstream.Block, bool, error) {
	// higher block number => shorter delay => completes earlier (scrambled completion order)
	time.Sleep(time.Duration(f.maxBlock-num+1) * 5 * time.Millisecond)
	f.mu.Lock()
	f.fetched++
	f.mu.Unlock()
	return blk(fmt.Sprintf("%da", num), fmt.Sprintf("%da", num-1), f.libNum), false, nil
}

func TestBlockPoller_parallelFetchPreservesOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const first, lib, stop = uint64(100), uint64(100), uint64(110)

		expect := make([]*pbbstream.Block, 0, stop-first)
		for n := first; n < stop; n++ {
			expect = append(expect, blk(fmt.Sprintf("%da", n), fmt.Sprintf("%da", n-1), lib))
		}

		fetcher := &linChainFetcher{libNum: lib, maxBlock: stop}
		finalizer := newTestBlockFinalizer(t, expect)

		clients := rpc.NewClients[any](time.Second, rpc.NewRollingStrategyAlwaysUseFirst[any](), zap.NewNop())
		clients.Add(new(any))

		poller := New[any](fetcher, finalizer, clients)
		// Shut down so the background monitoring goroutine exits; otherwise
		// synctest.Test would report a leaked (durably blocked) goroutine.
		defer poller.Shutdown(nil)
		poller.fetchBlockRetryCount = 0

		stopBlock := stop
		// blockFetchBatchSize=10: blocks 100..109 are fetched concurrently and complete
		// out of order, but must be fired in order. finalizer.check asserts both the
		// exact set and the order.
		require.NoError(t, poller.Run(first, &stopBlock, 10))

		finalizer.check(t)
	})
}
