package blockpoller

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/firehose-core/rpc"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// constantFetcher is a thread-safe fetcher that always succeeds, used to
// exercise concurrent access to the poller internals.
type constantFetcher struct{}

func (constantFetcher) IsBlockAvailable(uint64) bool { return true }

func (constantFetcher) Fetch(_ context.Context, _ any, num uint64) (*pbbstream.Block, bool, error) {
	return blk(fmt.Sprintf("%da", num), fmt.Sprintf("%da", num-1), num), false, nil
}

// TestBlockPoller_FetchBlockWithHashConcurrentWithBatchFetch ensures that
// fetchBlockWithHash, which resets the optimisticallyPolledBlocks map, is
// properly synchronized with the batch-fetch (loadNextBlocks) goroutine that
// writes to the same map. Run with -race: it fails if the map reset is done
// without holding optimisticallyPolledBlocksLock.
func TestBlockPoller_FetchBlockWithHashConcurrentWithBatchFetch(t *testing.T) {
	clients := rpc.NewClients[any](time.Second, rpc.NewRollingStrategyAlwaysUseFirst[any](), zap.NewNop())
	clients.Add(new(any))

	p := New[any](constantFetcher{}, &TestNoopBlockFinalizer{}, clients)
	p.fetchBlockRetryCount = 0
	p.optimisticallyPolledBlocks = map[uint64]*BlockItem{}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 1; i <= 25; i++ {
			assert.NoError(t, p.loadNextBlocks(uint64(i*10), 5))
		}
	}()

	go func() {
		defer wg.Done()
		for i := 1; i <= 25; i++ {
			_, err := p.fetchBlockWithHash(uint64(i), "a")
			assert.NoError(t, err)
		}
	}()

	wg.Wait()
}
