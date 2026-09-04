package blockpoller

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/derr"
	"github.com/streamingfast/firehose-core/rpc"
	"github.com/stretchr/testify/require"
)

// TestBlockPoller_ParallelPolling_FetchesConcurrently is the headline guarantee of
// batched polling: with a batch size above one, several blocks are in flight at the
// same time, and the run loop still fires them strictly in order.
func TestBlockPoller_ParallelPolling_FetchesConcurrently(t *testing.T) {
	const firstBlock, stopBlock, batchSize = 100, 120, 5

	fetcher := newParallelTestFetcher(firstBlock, stopBlock, 20*time.Millisecond)
	handler := &recordingBlockHandler{}
	poller := newParallelTestPoller(t, fetcher, handler, "c0", "c1", "c2")

	stop := uint64(stopBlock)
	require.NoError(t, poller.Run(firstBlock, &stop, batchSize))

	maxInFlight, _, _ := fetcher.stats()
	require.Greater(t, maxInFlight, 1, "a batch size of %d must put several blocks in flight at once", batchSize)
	require.LessOrEqual(t, maxInFlight, batchSize, "no more than the batch size may be in flight")

	require.Equal(t, blockRange(firstBlock, stopBlock-1), handler.firedBlocks(),
		"blocks must be fired in order even though they were fetched out of order")
}

// TestBlockPoller_SequentialPolling_FetchesOneBlockAtATime pins the other side of the
// contract: a batch size of one keeps the poller strictly sequential, with no
// read-ahead. That mode is the one where `delayBetweenFetch` applies, so fetching
// ahead there would silently bypass the configured rate limit.
func TestBlockPoller_SequentialPolling_FetchesOneBlockAtATime(t *testing.T) {
	const firstBlock, stopBlock = 100, 110

	fetcher := newParallelTestFetcher(firstBlock, stopBlock, 5*time.Millisecond)
	handler := &recordingBlockHandler{}
	poller := newParallelTestPoller(t, fetcher, handler, "c0", "c1")

	stop := uint64(stopBlock)
	require.NoError(t, poller.Run(firstBlock, &stop, 1))

	maxInFlight, _, fetchCount := fetcher.stats()
	require.Equal(t, 1, maxInFlight, "a batch size of 1 must never fetch two blocks at once")

	require.NotContains(t, fetchCount, uint64(stopBlock), "no block must be fetched past the stop block")
	for blkNum, count := range fetchCount {
		// Block `firstBlock` is fetched twice: once to resolve the starting block, once
		// by the run loop itself.
		expected := 1
		if blkNum == firstBlock {
			expected = 2
		}

		require.Equal(t, expected, count, "block %d was fetched %d time(s), the poller must not read ahead", blkNum, count)
	}
}

// TestBlockPoller_ParallelPolling_SpreadsAcrossClients covers the reason each block
// poll gets its own rotated client list: consecutive blocks in a batch must hit
// different endpoints instead of all hammering the first one.
func TestBlockPoller_ParallelPolling_SpreadsAcrossClients(t *testing.T) {
	const firstBlock, stopBlock, batchSize = 100, 116, 4

	fetcher := newParallelTestFetcher(firstBlock, stopBlock, 0)
	handler := &recordingBlockHandler{}
	names := []string{"c0", "c1", "c2", "c3"}
	poller := newParallelTestPoller(t, fetcher, handler, names...)

	stop := uint64(stopBlock)
	require.NoError(t, poller.Run(firstBlock, &stop, batchSize))

	_, servedBy, _ := fetcher.stats()
	for blkNum := uint64(firstBlock + 1); blkNum < stopBlock; blkNum++ {
		require.Equal(t, names[blkNum%batchSize], servedBy[blkNum],
			"block %d should have started on client %s", blkNum, names[blkNum%batchSize])
	}

	require.Len(t, mapValuesSet(servedBy), len(names), "every client must have served at least one block")
}

// TestBlockPoller_FetchFailureIsFatalOnlyOnTheDemandPath pins how a failed batch is
// handled on each of the two paths into `loadNextBlocks`.
//
// On the demand path the run loop is blocked waiting for that exact block, so
// giving up has to shut the poller down — otherwise `requestBlock` would wait
// forever for a block nobody is fetching anymore. On the optimistic path nothing is
// waiting: the poller is reading ahead, the block may simply not exist yet, and the
// failure must be swallowed.
func TestBlockPoller_FetchFailureIsFatalOnlyOnTheDemandPath(t *testing.T) {
	tests := []struct {
		name            string
		speculative     bool
		expectTerminate bool
	}{
		{"demand fetch failure shuts the poller down", false, true},
		{"optimistic fetch failure is swallowed", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := rpc.NewClients(time.Second, rpc.NewRollingStrategyAlwaysUseFirst[*namedClient](), logger)
			clients.Add(&namedClient{name: "c0"})

			poller := New[*namedClient](alwaysFailingFetcher{}, &recordingBlockHandler{}, clients, WithLogger[*namedClient](logger))
			poller.fetchBlockRetryCount = 0
			poller.resetOptimisticallyPolledBlocks()

			poller.triggerLoadNextBlocks(200, 1, tt.speculative)

			require.Eventually(t, func() bool { return !poller.fetching.Load() }, 5*time.Second, time.Millisecond,
				"the batch should have completed")

			require.Equal(t, tt.expectTerminate, poller.IsTerminating())
		})
	}
}

// TestBlockPoller_LoadNextBlocksIsSingleFlight guards the flag that keeps one batch in
// flight at a time. It is claimed with a compare-and-swap before the goroutine is
// spawned: setting it inside the goroutine would let two callers racing on
// `requestBlock` both start a batch, fetching the same blocks twice.
func TestBlockPoller_LoadNextBlocksIsSingleFlight(t *testing.T) {
	const firstBlock, headBlock = 100, 200

	fetcher := newParallelTestFetcher(firstBlock, headBlock, 20*time.Millisecond)

	clients := rpc.NewClients(5*time.Second, rpc.NewRollingStrategyAlwaysUseFirst[*namedClient](), logger)
	clients.Add(&namedClient{name: "c0"})

	poller := New[*namedClient](fetcher, &recordingBlockHandler{}, clients, WithLogger[*namedClient](logger))
	poller.resetOptimisticallyPolledBlocks()

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { poller.triggerLoadNextBlocks(firstBlock, 1, true) })
	}
	wg.Wait()

	require.Eventually(t, func() bool { return !poller.fetching.Load() }, 5*time.Second, time.Millisecond)

	_, _, fetchCount := fetcher.stats()
	require.Equal(t, 1, fetchCount[firstBlock], "20 concurrent triggers must result in a single fetch")
}

// TestBlockPoller_ResetDiscardsInFlightBatch covers the interaction between a reorg
// and optimistic polling. Dropping the cache is not enough on its own: a batch
// started before the reorg is still running, and would otherwise write blocks from
// the abandoned chain back into a cache that was just cleared, to be served later as
// if they were current.
func TestBlockPoller_ResetDiscardsInFlightBatch(t *testing.T) {
	const firstBlock = 100

	fetcher := newParallelTestFetcher(firstBlock, 200, 50*time.Millisecond)

	clients := rpc.NewClients(5*time.Second, rpc.NewRollingStrategyAlwaysUseFirst[*namedClient](), logger)
	clients.Add(&namedClient{name: "c0"})

	poller := New[*namedClient](fetcher, &recordingBlockHandler{}, clients, WithLogger[*namedClient](logger))
	poller.resetOptimisticallyPolledBlocks()

	poller.triggerLoadNextBlocks(firstBlock, 1, true)

	select {
	case <-fetcher.started:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "the batch never started")
	}

	// The reorg lands while the fetch is still in flight.
	poller.resetOptimisticallyPolledBlocks()

	require.Eventually(t, func() bool { return !poller.fetching.Load() }, 5*time.Second, time.Millisecond)

	poller.optimisticallyPolledBlocksLock.Lock()
	defer poller.optimisticallyPolledBlocksLock.Unlock()
	require.Empty(t, poller.optimisticallyPolledBlocks,
		"a block fetched before the reset belongs to an abandoned chain and must be dropped")
}

// TestBlockPoller_BatchSizeIsClamped guards the `blockToFetch % batchSize` used to pick
// a starting client: a batch size of zero would divide by zero.
func TestBlockPoller_BatchSizeIsClamped(t *testing.T) {
	for _, batchSize := range []int{-1, 0} {
		t.Run(fmt.Sprintf("batch_size_%d", batchSize), func(t *testing.T) {
			const firstBlock, stopBlock = 100, 103

			fetcher := newParallelTestFetcher(firstBlock, stopBlock, 0)
			handler := &recordingBlockHandler{}
			poller := newParallelTestPoller(t, fetcher, handler, "c0")

			stop := uint64(stopBlock)
			require.NotPanics(t, func() {
				require.NoError(t, poller.Run(firstBlock, &stop, batchSize))
			})

			require.Equal(t, blockRange(firstBlock, stopBlock-1), handler.firedBlocks())
		})
	}
}

// TestBlockPoller_DelayBetweenFetchIsRespected runs the poller inside a synctest
// bubble: the clock is virtual, so a delay measured in seconds costs no wall time
// and the assertion on elapsed time is exact rather than a tolerance window.
func TestBlockPoller_DelayBetweenFetchIsRespected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const firstBlock, stopBlock = 100, 105
		const delay = 2 * time.Second

		fetcher := newParallelTestFetcher(firstBlock, stopBlock, 0)
		handler := &recordingBlockHandler{}

		clients := rpc.NewClients(5*time.Second, rpc.NewRollingStrategyAlwaysUseFirst[*namedClient](), logger)
		clients.Add(&namedClient{name: "c0"})

		poller := New[*namedClient](fetcher, handler, clients,
			WithLogger[*namedClient](logger),
			WithDelayBetweenFetch[*namedClient](delay),
		)

		stop := uint64(stopBlock)
		started := time.Now()
		require.NoError(t, poller.Run(firstBlock, &stop, 1))
		elapsed := time.Since(started)

		require.Equal(t, blockRange(firstBlock, stopBlock-1), handler.firedBlocks())

		// The delay is applied before every fetch but the first one, which has no
		// preceding fetch to wait after. On top of that, every block costs one
		// `optimisticPollInterval` tick: the run loop asks for the block, that request
		// starts the fetch, and the loop then waits one tick before seeing the result.
		fired := time.Duration(len(handler.firedBlocks()))
		expected := (fired-1)*delay + fired*optimisticPollInterval
		require.Equal(t, expected, elapsed,
			"%d blocks separated by %s should take exactly %s", fired, delay, expected)

		// Let the optimistic prefetch goroutines drain before the bubble ends.
		poller.Shutdown(nil)
		synctest.Wait()
	})
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// namedClient is an RPC endpoint identified by name, so tests can assert which
// endpoint served which block.
type namedClient struct {
	name string
}

// parallelTestFetcher is a chain of linear blocks (no fork), instrumented to report
// how the poller fetched them: peak concurrency, which client served each block,
// and how many times each block was requested.
type parallelTestFetcher struct {
	// firstBlock and headBlock bound the chain. Blocks above headBlock are reported
	// as unavailable and fail if fetched anyway, mirroring an RPC endpoint asked for
	// a block that is not produced yet.
	firstBlock uint64
	headBlock  uint64
	fetchDelay time.Duration

	// started receives every block number as its fetch begins, so tests can act
	// while a batch is still in flight.
	started chan uint64

	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	servedBy    map[uint64]string
	fetchCount  map[uint64]int
}

func newParallelTestFetcher(firstBlock, headBlock uint64, fetchDelay time.Duration) *parallelTestFetcher {
	return &parallelTestFetcher{
		firstBlock: firstBlock,
		headBlock:  headBlock,
		fetchDelay: fetchDelay,
		started:    make(chan uint64, 256),
		servedBy:   map[uint64]string{},
		fetchCount: map[uint64]int{},
	}
}

func (f *parallelTestFetcher) IsBlockAvailable(blkNum uint64) bool {
	return blkNum <= f.headBlock
}

func (f *parallelTestFetcher) Fetch(ctx context.Context, client *namedClient, blkNum uint64) (*pbbstream.Block, bool, error) {
	f.mu.Lock()
	f.inFlight++
	f.maxInFlight = max(f.maxInFlight, f.inFlight)
	f.fetchCount[blkNum]++
	if _, alreadyServed := f.servedBy[blkNum]; !alreadyServed {
		f.servedBy[blkNum] = client.name
	}
	f.mu.Unlock()

	select {
	case f.started <- blkNum:
	default:
	}

	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	if f.fetchDelay > 0 {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(f.fetchDelay):
		}
	}

	if blkNum > f.headBlock {
		return nil, false, derr.NewFatalError(fmt.Errorf("block %d is not produced yet", blkNum))
	}

	return linearBlk(blkNum, f.firstBlock), false, nil
}

func (f *parallelTestFetcher) stats() (maxInFlight int, servedBy map[uint64]string, fetchCount map[uint64]int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	servedBy = map[uint64]string{}
	for k, v := range f.servedBy {
		servedBy[k] = v
	}

	fetchCount = map[uint64]int{}
	for k, v := range f.fetchCount {
		fetchCount[k] = v
	}

	return f.maxInFlight, servedBy, fetchCount
}

// recordingBlockHandler keeps the exact sequence of fired blocks.
type recordingBlockHandler struct {
	mu    sync.Mutex
	fired []uint64
}

func (h *recordingBlockHandler) Init() {}

func (h *recordingBlockHandler) Handle(blk *pbbstream.Block) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.fired = append(h.fired, blk.Number)
	return nil
}

func (h *recordingBlockHandler) firedBlocks() []uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]uint64(nil), h.fired...)
}

// linearBlk builds block `num` on a single, fork-free chain.
func linearBlk(num uint64, libNum uint64) *pbbstream.Block {
	parent := ""
	if num > 0 {
		parent = fmt.Sprintf("%da", num-1)
	}

	return blk(fmt.Sprintf("%da", num), parent, libNum)
}

func blockRange(from, to uint64) []uint64 {
	var out []uint64
	for num := from; num <= to; num++ {
		out = append(out, num)
	}

	return out
}

func newParallelTestPoller(t *testing.T, fetcher *parallelTestFetcher, handler BlockHandler, clientNames ...string) *BlockPoller[*namedClient] {
	t.Helper()

	clients := rpc.NewClients(5*time.Second, rpc.NewRollingStrategyAlwaysUseFirst[*namedClient](), logger)
	for _, name := range clientNames {
		clients.Add(&namedClient{name: name})
	}

	return New[*namedClient](fetcher, handler, clients, WithLogger[*namedClient](logger))
}

// alwaysFailingFetcher stands in for an RPC endpoint that cannot serve the block,
// either because it is not produced yet or because every endpoint is down.
type alwaysFailingFetcher struct{}

func (alwaysFailingFetcher) IsBlockAvailable(uint64) bool { return true }

func (alwaysFailingFetcher) Fetch(ctx context.Context, client *namedClient, blkNum uint64) (*pbbstream.Block, bool, error) {
	return nil, false, derr.NewFatalError(fmt.Errorf("block %d cannot be fetched", blkNum))
}

func mapValuesSet(m map[uint64]string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, v := range m {
		out[v] = struct{}{}
	}

	return out
}
