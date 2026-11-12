package merger

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/stretchr/testify/require"
)

// oneBlockFileWithTime opens any one-block file as a DBIN file holding a block
// whose time is its number in seconds since the epoch.
func oneBlockFileWithTime(t *testing.T) func(context.Context, *bstream.OneBlockFile) (io.ReadCloser, error) {
	return func(_ context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(mergedBlocksContent(t, testMergedBlock(t, obf.Num)))), nil
	}
}

// blockTimeRecorder collects the block times a bundler reports to the head
// drift metric.
type blockTimeRecorder struct {
	mu       sync.Mutex
	reported []time.Time
}

func (r *blockTimeRecorder) set(t time.Time) {
	r.mu.Lock()
	r.reported = append(r.reported, t)
	r.mu.Unlock()
}

func (r *blockTimeRecorder) lastIs(blockNum uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reported) > 0 && r.reported[len(r.reported)-1].Equal(time.Unix(int64(blockNum), 0))
}

// openCounter counts one-block file opens per block number.
type openCounter struct {
	mu     sync.Mutex
	opened map[uint64]int
}

func (c *openCounter) add(num uint64) {
	c.mu.Lock()
	if c.opened == nil {
		c.opened = map[uint64]int{}
	}
	c.opened[num]++
	c.mu.Unlock()
}

func (c *openCounter) count(num uint64) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opened[num]
}

// holdFirstOpen wraps opener so the first open blocks until release is closed,
// and signals on started when it begins.
func holdFirstOpen(opener func(context.Context, *bstream.OneBlockFile) (io.ReadCloser, error)) (wrapped func(context.Context, *bstream.OneBlockFile) (io.ReadCloser, error), started, release chan struct{}) {
	started = make(chan struct{})
	release = make(chan struct{})
	var once sync.Once
	wrapped = func(ctx context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
		once.Do(func() {
			close(started)
			<-release
		})
		return opener(ctx, obf)
	}
	return wrapped, started, release
}

// TestBundlerHeadBlockTimeFollowsNewestBlockOfBurst reproduces a merger restart on
// Ethereum: a whole backlog of irreversible blocks is bundled at once, then nothing
// for minutes until the next epoch is finalized. The throttled read started by the
// first block of the burst must not be the last word: the end-of-pass refresh has
// to report the newest block.
func TestBundlerHeadBlockTimeFollowsNewestBlockOfBurst(t *testing.T) {
	opener, firstRead, releaseFirstRead := holdFirstOpen(oneBlockFileWithTime(t))
	testIO := &TestMergerIO{OpenOneBlockFileFunc: opener}

	var rec blockTimeRecorder
	b := NewBundler(100, 0, 100, 100, testIO, 1, nil)
	b.headTime.readInterval = time.Millisecond
	b.headTime.setBlockTime = rec.set

	for _, obf := range buildChain(100, 150, nil) {
		require.NoError(t, b.HandleBlockFile(obf))
	}
	<-firstRead // held until the whole burst is bundled
	close(releaseFirstRead)

	head := b.headTime.head.Load()
	require.NotNil(t, head)
	require.Greater(t, head.Num, uint64(100), "the burst must have moved the head past its first block")

	// a refresh landing while the first read runs is dropped, the next pass retries it
	require.Eventually(t, func() bool {
		b.RefreshHeadBlockTime()
		return rec.lastIs(head.Num)
	}, 2*time.Second, 5*time.Millisecond, "head block time never reached the newest irreversible block %d", head.Num)
}

func TestBundlerRefreshHeadBlockTimeSkipsAlreadyReadBlock(t *testing.T) {
	var opens openCounter
	opener := oneBlockFileWithTime(t)
	testIO := &TestMergerIO{
		OpenOneBlockFileFunc: func(ctx context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
			opens.add(obf.Num)
			return opener(ctx, obf)
		},
	}

	b := NewBundler(100, 0, 100, 100, testIO, 1, nil)
	b.headTime.readInterval = time.Millisecond
	b.headTime.setBlockTime = func(time.Time) {}

	for _, obf := range buildChain(100, 110, nil) {
		require.NoError(t, b.HandleBlockFile(obf))
	}
	head := b.headTime.head.Load()
	require.NotNil(t, head)

	require.Eventually(t, func() bool {
		b.RefreshHeadBlockTime()
		return b.headTime.readNum.Load() == head.Num && !b.headTime.inFlight.Load()
	}, 2*time.Second, 5*time.Millisecond)

	// the head was read, so these return without starting a read
	b.RefreshHeadBlockTime()
	b.RefreshHeadBlockTime()
	require.False(t, b.headTime.inFlight.Load(), "a refresh of an already read block started a read")
	require.Equal(t, 1, opens.count(head.Num), "block %d opened more than once", head.Num)
}

func TestBundlerRefreshHeadBlockTimeDoesNotRetryFailedBlock(t *testing.T) {
	var opens openCounter
	testIO := &TestMergerIO{
		OpenOneBlockFileFunc: func(_ context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
			opens.add(obf.Num)
			return nil, errors.New("injected read failure")
		},
	}

	b := NewBundler(100, 0, 100, 100, testIO, 1, nil)
	b.headTime.readInterval = time.Millisecond
	b.headTime.setBlockTime = func(time.Time) { t.Error("no block time can be read") }

	for _, obf := range buildChain(100, 110, nil) {
		require.NoError(t, b.HandleBlockFile(obf))
	}
	head := b.headTime.head.Load()
	require.NotNil(t, head)

	require.Eventually(t, func() bool {
		b.RefreshHeadBlockTime()
		return b.headTime.readNum.Load() == head.Num && !b.headTime.inFlight.Load()
	}, 2*time.Second, 5*time.Millisecond)

	b.RefreshHeadBlockTime()
	b.RefreshHeadBlockTime()
	require.False(t, b.headTime.inFlight.Load(), "a refresh of a failed block started a read")
	require.Equal(t, 1, opens.count(head.Num), "failed block %d opened more than once", head.Num)
}

func TestBundlerBundleClosingBlockMovesHead(t *testing.T) {
	testIO := &TestMergerIO{OpenOneBlockFileFunc: oneBlockFileWithTime(t)}

	b := NewBundler(10, 0, 10, 10, testIO, 1, nil)
	b.headTime.readInterval = time.Millisecond
	b.headTime.setBlockTime = func(time.Time) {}

	// block 20 closes bundle 10-19, it is the last irreversible block fed
	for _, obf := range buildChain(10, 20, nil) {
		require.NoError(t, b.HandleBlockFile(obf))
	}
	b.WaitForMerges()

	head := b.headTime.head.Load()
	require.NotNil(t, head)
	require.Equal(t, uint64(20), head.Num)
}

// TestMergerRunReportsNewestBlockTimeAfterBurst checks the merger loop refreshes
// the head block time after each pass: the first read is held until the whole
// burst is walked, so only the end-of-pass refresh can reach the newest block.
func TestMergerRunReportsNewestBlockTimeAfterBurst(t *testing.T) {
	blocks := buildChain(10, 60, nil)
	opener, firstRead, releaseFirstRead := holdFirstOpen(oneBlockFileWithTime(t))

	var walkOnce sync.Once
	walked := make(chan struct{})
	testIO := &TestMergerIO{
		NextBundleFunc: func(_ context.Context, lowestBaseBlock uint64) (uint64, bstream.BlockRef, error) {
			if lowestBaseBlock < 10 {
				return 10, libRef(9), nil
			}
			return lowestBaseBlock, nil, nil
		},
		WalkOneBlockFilesFunc: func(_ context.Context, inclusiveLowerBlock uint64, callback func(*bstream.OneBlockFile) error) error {
			for _, blk := range blocks {
				if blk.Num < inclusiveLowerBlock {
					continue
				}
				if err := callback(blk); err != nil {
					return err
				}
			}
			walkOnce.Do(func() { close(walked) })
			return nil
		},
		OpenOneBlockFileFunc: opener,
	}

	merger := newRunTestMerger(testIO, 0, 100, 1)
	var rec blockTimeRecorder
	merger.bundler.headTime.readInterval = time.Millisecond
	merger.bundler.headTime.setBlockTime = rec.set

	done := make(chan error, 1)
	go func() { done <- merger.run() }()
	defer func() {
		merger.Shutdown(nil)
		require.NoError(t, <-done)
	}()

	<-firstRead
	<-walked
	close(releaseFirstRead)

	head := merger.bundler.headTime.head.Load()
	require.NotNil(t, head)
	require.Greater(t, head.Num, uint64(10), "the burst must have moved the head past its first block")

	require.Eventually(t, func() bool { return rec.lastIs(head.Num) }, 2*time.Second, 5*time.Millisecond,
		"merger never reported the newest irreversible block %d", head.Num)
}

// TestBundlerRefreshHeadBlockTimeIsThrottled checks the end-of-pass refresh
// shares the read throttle, so a chain whose head moves every pass is not read
// once per pass.
func TestBundlerRefreshHeadBlockTimeIsThrottled(t *testing.T) {
	var opens openCounter
	opener := oneBlockFileWithTime(t)
	testIO := &TestMergerIO{
		OpenOneBlockFileFunc: func(ctx context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
			opens.add(obf.Num)
			return opener(ctx, obf)
		},
	}

	var rec blockTimeRecorder
	b := NewBundler(100, 0, 100, 100, testIO, 1, nil)
	b.headTime.readInterval = time.Hour
	b.headTime.setBlockTime = rec.set

	chain := buildChain(100, 150, nil)
	require.NoError(t, b.HandleBlockFile(chain[0]))
	first := b.headTime.head.Load()
	require.Eventually(t, func() bool { return rec.lastIs(first.Num) }, 2*time.Second, 5*time.Millisecond)

	for _, obf := range chain[1:] {
		require.NoError(t, b.HandleBlockFile(obf))
		b.RefreshHeadBlockTime()
	}
	require.NotEqual(t, first.Num, b.headTime.head.Load().Num, "the head must have moved")
	require.False(t, b.headTime.inFlight.Load(), "a refresh inside the throttle interval started a read")

	total := 0
	for _, obf := range chain {
		total += opens.count(obf.Num)
	}
	require.Equal(t, 1, total, "only the first block may be read inside the throttle interval")
}
