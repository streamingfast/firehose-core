package merger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/shutter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chainBlock creates a OneBlockFile for a simple linear chain.
// Block N has ID fmt(N), prevID fmt(prevN), and lib=libNum.
// Callers must never pass num==prevNum (no self-referential genesis blocks).
func chainBlock(num, prevNum, lib uint64) *bstream.OneBlockFile {
	filename := fmt.Sprintf("%010d-%016xa-%016xa-%d-suffix", num, num, prevNum, lib)
	return bstream.MustNewOneBlockFile(filename)
}

// newRunTestMerger creates a Merger wired to io, ready to call run() directly.
// No gRPC server, no background pruners.
func newRunTestMerger(io IOInterface, firstStreamableBlock, bundleSize uint64, maxThreads int) *Merger {
	m := &Merger{
		Shutter:            shutter.New(),
		io:                 io,
		timeBetweenPolling: 0,
		logger:             testLogger,
	}
	m.bundler = NewBundler(firstStreamableBlock, 0, firstStreamableBlock, bundleSize, io, maxThreads, m.Shutdown)
	m.OnTerminating(func(_ error) { m.bundler.WaitForMerges() })
	return m
}

// libRef creates a lightweight BlockRef — sufficient for Reset() to set the forkable's inclusive LIB.
func libRef(num uint64) bstream.BlockRef {
	return bstream.NewBlockRef(fmt.Sprintf("%016xa", num), num)
}

// buildChain returns OneBlockFiles for blocks [from, to].
// Each block's libNum equals its own block number, so it becomes the LIB as
// soon as the next block is added to the forkable's chain.
// 'from' must be > 0 so that prevNum(from) = from-1, avoiding a self-referential genesis.
func buildChain(from, to uint64, skipped map[uint64]bool) []*bstream.OneBlockFile {
	if from == 0 {
		panic("buildChain: from must be > 0 to avoid self-referential genesis blocks")
	}
	out := make([]*bstream.OneBlockFile, 0, to-from+1)
	for n := from; n <= to; n++ {
		if skipped != nil && skipped[n] {
			continue
		}
		out = append(out, chainBlock(n, n-1, n)) // lib=n means block n itself is the LIB
	}
	return out
}

var testRepeaterRan bool

// // This forces an automatic test run to check for race conditions. This one runs a single time
// func TestMergerRun_MergeFailureResetsToFailedBase_Repeatedly(t *testing.T) {
// 	if testRepeaterRan {
// 		return
// 	}
// 	testRepeaterRan = true
// 	for i := 0; i < 15; i++ {
// 		t.Run(fmt.Sprintf("TestMergerRun_MergeFailureResetsToFailedBase_%d", i), func(t *testing.T) {
// 			TestMergerRun_MergeFailureResetsToFailedBase(t)
// 		})
// 	}
// }

// TestMergerRun_FailedBundleCausesShutdown tests the core scenario:
//   - bundle size 10, blocks 10..35 available (0-9 already merged)
//   - merge for bundle 10-19 fails
//   - running with multiple threads
//
// We ensure that the `LowestUnmergedBlockNum()` func never returns a value above 10 so that the file deleter would not risk deleting unmerged files
// We ensure that range "20-29" is merged
func TestMergerRun_FailedBundleCausesShutdown(t *testing.T) {
	const (
		bundleSize = uint64(10)
	)

	blocks := buildChain(10, 35, nil)

	var mu sync.Mutex
	var mergeCountPerBase = map[uint64]int{}
	var highestSeenLowestUnmergedBlockNum uint64
	var mergeAttempts []uint64
	var walkCalls []uint64

	var merger *Merger

	testIO := &TestMergerIO{
		NextBundleFunc: func(_ context.Context, lowestBaseBlock uint64) (uint64, bstream.BlockRef, error) {
			// Simulate that blocks 0-9 are already merged: return base=10 with a lib ref.
			if lowestBaseBlock < 10 {
				return 10, libRef(9), nil
			}
			return lowestBaseBlock, nil, nil
		},
		WalkOneBlockFilesFunc: func(_ context.Context, inclusiveLowerBlock uint64, callback func(*bstream.OneBlockFile) error) error {
			mu.Lock()
			walkCalls = append(walkCalls, inclusiveLowerBlock)
			if seenLowest := merger.bundler.getSafeBaseBlockNum(); seenLowest > highestSeenLowestUnmergedBlockNum {
				highestSeenLowestUnmergedBlockNum = seenLowest
			}
			mu.Unlock()

			for _, blk := range blocks {
				if blk.Num < inclusiveLowerBlock {
					continue
				}
				if err := callback(blk); err != nil {
					return err
				}
			}
			return nil
		},
		MergeAndStoreFunc: func(_ context.Context, inclusiveLowerBlock uint64, _ []*bstream.OneBlockFile) error {
			mu.Lock()
			mergeCountPerBase[inclusiveLowerBlock]++
			mergeAttempts = append(mergeAttempts, inclusiveLowerBlock)
			mu.Unlock()
			if inclusiveLowerBlock == 10 {
				time.Sleep(time.Millisecond * 100)
				return errors.New("injected merge failure for bundle 10")
			}

			return nil
		},
	}

	merger = newRunTestMerger(testIO, 0, bundleSize, 4)

	var err error
	go func() {
		err = merger.run()
	}()
	select {
	case <-time.After(time.Second):
		t.Fail()
	case <-merger.Terminated():
	}
	require.NoError(t, err)
	merger.bundler.WaitForMerges()

	assert.Equal(t, 10, int(highestSeenLowestUnmergedBlockNum), "bundler lowestUnmergedBlockNum should never go above 10")

	// Bundle 10-19 must have been attempted (and failed).
	assert.Equalf(t, mergeCountPerBase[10], 1, "bundle 10 should have been attempted a single time: mergeCalls: %v, walkCalls: %v", mergeAttempts, walkCalls)

	// Bundle 20-29 must have been attempted (and succeeded).
	assert.Equalf(t, mergeCountPerBase[20], 1, "bundle 20 should have been attempted a single time: mergeCalls: %v, walkCalls: %v", mergeAttempts, walkCalls)

}

// TestMergerRun_HoleInOneBlockFiles_TriggersCheckLoop verifies that when a gap in the
// one-block file sequence causes more than bundleSize*4 unlinkable blocks in a single walk,
// the merger returns errCheckLoop and retries rather than continuing to walk (possibly millions of blocks...)
func TestMergerRun_HoleInOneBlockFiles_TriggersCheckLoop(t *testing.T) {
	const bundleSize = uint64(10) // maxUnlinkableBlocks = 40

	// blocks 10-19 form one complete bundle; blocks 61-120 follow a gap of 40+ missing blocks.
	var allBlocks []*bstream.OneBlockFile
	allBlocks = append(allBlocks, buildChain(10, 19, nil)...)
	allBlocks = append(allBlocks, buildChain(61, 120, nil)...) // 60 blocks: 41+ are unlinkable

	walkCallCount := 0
	var merger *Merger

	testIO := &TestMergerIO{
		NextBundleFunc: func(_ context.Context, lowestBaseBlock uint64) (uint64, bstream.BlockRef, error) {
			if lowestBaseBlock < 10 {
				return 10, libRef(9), nil
			}
			return lowestBaseBlock, nil, nil
		},
		WalkOneBlockFilesFunc: func(_ context.Context, inclusiveLowerBlock uint64, callback func(*bstream.OneBlockFile) error) error {
			walkCallCount++
			if walkCallCount >= 3 {
				merger.Shutdown(nil)
				return nil
			}
			for _, blk := range allBlocks {
				if blk.Num < inclusiveLowerBlock {
					continue
				}
				if err := callback(blk); err != nil {
					return err
				}
			}
			return nil
		},
		MergeAndStoreFunc: func(_ context.Context, _ uint64, _ []*bstream.OneBlockFile) error {
			return nil
		},
	}

	merger = newRunTestMerger(testIO, 0, bundleSize, 1)
	err := merger.run()
	require.NoError(t, err)
	merger.bundler.WaitForMerges()

	// errCheckLoop should have fired on walks 1 and 2, causing the outer loop to retry.
	assert.GreaterOrEqual(t, walkCallCount, 2, "expected multiple walk calls: errCheckLoop should cause retries, not shutdown")
}

// TestMergerRun_LargeLibJumpDoesNotTriggerCheckLoop verifies that a LIB jump much larger
// than bundleSize*4 (590 >> 40) firing many bundles at once does NOT trigger errCheckLoop.
//
// Scenario: blocks 10-950 all report lib=10, so only block 10 is immediately irreversible.
// Blocks 951-1000 report lib=600, causing the LIB to jump by 590 in a single step — far
// exceeding maxUnlinkableBlocks (40). All blocks are a sequential chain so none are
// unlinkable; the merger must produce bundles 10, 20, …, 590 without errCheckLoop.
func TestMergerRun_LargeLibJumpDoesNotTriggerCheckLoop(t *testing.T) {
	const bundleSize = uint64(10) // maxUnlinkableBlocks = bundleSize*4 = 40

	// blocks 10-950: lib stuck at 10 → only block 10 becomes irreversible immediately.
	// blocks 951-1000: lib jumps to 600 → blocks 11-600 all become irreversible at once.
	var blocks []*bstream.OneBlockFile
	for n := uint64(10); n <= 950; n++ {
		blocks = append(blocks, chainBlock(n, n-1, 10))
	}
	for n := uint64(951); n <= 1000; n++ {
		blocks = append(blocks, chainBlock(n, n-1, 600))
	}

	var mu sync.Mutex
	mergeCountPerBase := map[uint64]int{}
	checkLoopTriggered := false
	walkCallCount := 0
	var merger *Merger

	testIO := &TestMergerIO{
		NextBundleFunc: func(_ context.Context, lowestBaseBlock uint64) (uint64, bstream.BlockRef, error) {
			if lowestBaseBlock < 10 {
				return 10, libRef(9), nil
			}
			return lowestBaseBlock, nil, nil
		},
		WalkOneBlockFilesFunc: func(_ context.Context, inclusiveLowerBlock uint64, callback func(*bstream.OneBlockFile) error) error {
			walkCallCount++
			if walkCallCount >= 2 {
				merger.Shutdown(nil)
				return nil
			}
			for _, blk := range blocks {
				if blk.Num < inclusiveLowerBlock {
					continue
				}
				if err := callback(blk); err != nil {
					if errors.Is(err, errCheckLoop) {
						checkLoopTriggered = true
					}
					return err
				}
			}
			return nil
		},
		MergeAndStoreFunc: func(_ context.Context, inclusiveLowerBlock uint64, _ []*bstream.OneBlockFile) error {
			mu.Lock()
			mergeCountPerBase[inclusiveLowerBlock]++
			mu.Unlock()
			return nil
		},
	}

	merger = newRunTestMerger(testIO, 0, bundleSize, 4)
	err := merger.run()
	require.NoError(t, err)
	merger.bundler.WaitForMerges()

	assert.False(t, checkLoopTriggered, "errCheckLoop must NOT be triggered when a large LIB jump fires many bundles at once")

	// Bundles 10, 20, …, 590 should each have been merged exactly once.
	mu.Lock()
	defer mu.Unlock()
	for base := uint64(10); base <= 590; base += bundleSize {
		assert.Equalf(t, 1, mergeCountPerBase[base], "bundle at base %d should be merged exactly once", base)
	}
}

func TestMergerRun_CheckAlreadyMergedAfterManyBlocks(t *testing.T) {
	const (
		bundleSize = uint64(10)
	)

	blocks := append(buildChain(10, 35, nil), buildChain(37, 1000, nil)...) // chain with missing block 36

	var mu sync.Mutex
	var mergeCountPerBase = map[uint64]int{}
	var highestSeenLowestUnmergedBlockNum uint64
	var mergeAttempts []uint64
	var walkCalls []uint64

	walkCallCount := 0
	nextBundleCount := 0
	highestCheckedOneBlock := uint64(0)

	var merger *Merger

	testIO := &TestMergerIO{
		NextBundleFunc: func(_ context.Context, lowestBaseBlock uint64) (uint64, bstream.BlockRef, error) {
			mu.Lock()
			nextBundleCount++
			mu.Unlock()
			// Simulate that blocks 0-9 are already merged: return base=10 with a lib ref.
			if lowestBaseBlock < 10 {
				return 10, libRef(9), nil
			}
			return lowestBaseBlock, nil, nil
		},
		WalkOneBlockFilesFunc: func(_ context.Context, inclusiveLowerBlock uint64, callback func(*bstream.OneBlockFile) error) error {
			mu.Lock()
			walkCalls = append(walkCalls, inclusiveLowerBlock)
			walkCallCount++
			callNum := walkCallCount
			if seenLowest := merger.bundler.getSafeBaseBlockNum(); seenLowest > highestSeenLowestUnmergedBlockNum {
				highestSeenLowestUnmergedBlockNum = seenLowest
			}
			mu.Unlock()

			// after a few iterations we shut down
			if callNum > 10 {
				merger.Shutdown(nil)
				return nil
			}

			for _, blk := range blocks {
				if blk.Num < inclusiveLowerBlock {
					continue
				}
				if blk.Num > highestCheckedOneBlock {
					highestCheckedOneBlock = blk.Num
				}
				if err := callback(blk); err != nil {
					return err
				}
			}
			return nil
		},
		MergeAndStoreFunc: func(_ context.Context, inclusiveLowerBlock uint64, _ []*bstream.OneBlockFile) error {
			mu.Lock()
			mergeCountPerBase[inclusiveLowerBlock]++
			mergeAttempts = append(mergeAttempts, inclusiveLowerBlock)
			mu.Unlock()
			return nil
		},
	}

	merger = newRunTestMerger(testIO, 0, bundleSize, 4)
	err := merger.run()
	require.NoError(t, err)
	merger.bundler.WaitForMerges()

	assert.Greater(t, nextBundleCount, 10, "nextBundleCount should be greater than 10")
	assert.Less(t, highestCheckedOneBlock, uint64(200), "highestCheckedOneBlock should be less than 200")

}

// TestMergerRun_ConsecutiveWalkErrorsTriggerShutdown verifies the circuit breaker:
// a persistently failing WalkOneBlockFiles must make run() return
// "too many consecutive errors" after 10 attempts instead of retrying forever.
func TestMergerRun_ConsecutiveWalkErrorsTriggerShutdown(t *testing.T) {
	walkCalls := 0
	testIO := &TestMergerIO{
		WalkOneBlockFilesFunc: func(_ context.Context, _ uint64, _ func(*bstream.OneBlockFile) error) error {
			walkCalls++
			return errors.New("persistent store failure")
		},
	}

	merger := newRunTestMerger(testIO, 0, 10, 1)

	errCh := make(chan error, 1)
	go func() { errCh <- merger.run() }()

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "too many consecutive errors")
		assert.Equal(t, 10, walkCalls, "circuit breaker should trip after exactly 10 consecutive errors")
	case <-time.After(5 * time.Second):
		t.Fatal("merger did not shut down on persistent walk errors")
	}
}

// forkAwareTestIO adds ForkAwareIOInterface on top of TestMergerIO so the
// forked-blocks pruner can be exercised in tests.
type forkAwareTestIO struct {
	*TestMergerIO
	deleteForkedCalls *atomic.Int64
}

func (f *forkAwareTestIO) DeleteForkedBlocksAsync(_, _ uint64) { f.deleteForkedCalls.Add(1) }
func (f *forkAwareTestIO) MoveForkedBlocks(_ context.Context, _ []*bstream.OneBlockFile) {}

// TestPrunersStopOnShutdown verifies that both pruner goroutines observe
// merger termination and stop deleting files after Shutdown.
func TestPrunersStopOnShutdown(t *testing.T) {
	var walkCalls, deleteForkedCalls atomic.Int64

	testIO := &forkAwareTestIO{
		TestMergerIO: &TestMergerIO{
			WalkOneBlockFilesFunc: func(_ context.Context, _ uint64, _ func(*bstream.OneBlockFile) error) error {
				walkCalls.Add(1)
				return nil
			},
		},
		deleteForkedCalls: &deleteForkedCalls,
	}

	m := &Merger{
		Shutter:                    shutter.New(),
		io:                         testIO,
		logger:                     testLogger,
		timeBetweenPruning:         time.Millisecond,
		pruningDistanceToLIB:       100,
		oneBlockFilesPruneDistance: 100,
		firstStreamableBlock:       2,
	}
	// bundler base 1000 makes the pruning target non-zero (1000 - 100 = 900)
	m.bundler = NewBundler(1000, 0, 2, 100, testIO, 1, m.Shutdown)

	m.startOldFilesPruner()
	m.startForkedBlocksPruner()

	require.Eventually(t, func() bool {
		return walkCalls.Load() >= 1 && deleteForkedCalls.Load() >= 1
	}, 2*time.Second, time.Millisecond, "pruners never ran")

	m.Shutdown(nil)

	time.Sleep(20 * time.Millisecond) // let any in-flight iteration finish
	walksAfter, deletesAfter := walkCalls.Load(), deleteForkedCalls.Load()
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, walksAfter, walkCalls.Load(), "old-files pruner kept running after shutdown")
	assert.Equal(t, deletesAfter, deleteForkedCalls.Load(), "forked-blocks pruner kept running after shutdown")
}
