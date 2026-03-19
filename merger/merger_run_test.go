package merger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

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
		bundler:            NewBundler(firstStreamableBlock, 0, firstStreamableBlock, bundleSize, io, maxThreads),
		io:                 io,
		timeBetweenPolling: 0,
		logger:             testLogger,
	}
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

// TestMergerRun_FailedBundleDoesNotDeleteUnmergedOrRemergeOtherBundles tests the core scenario:
//   - bundle size 10, blocks 10..35 available (0-9 already merged)
//   - merge for bundle 10-19 fails every time
//   - running with multiple threads
//
// We ensure that the `LowestUnmergedBlockNum()` func never returns a value above 10 so that the file deleter would not risk deleting unmerged files
// We ensure that range "20-29" is not merged multiple times (it succeeds every time)
func TestMergerRun_FailedBundleDoesNotDeleteUnmergedOrRemergeOtherBundles(t *testing.T) {
	const (
		bundleSize = uint64(10)
	)

	blocks := buildChain(10, 35, nil)

	var mu sync.Mutex
	var mergeCountPerBase = map[uint64]int{}
	var highestSeenLowestUnmergedBlockNum uint64
	var mergeAttempts []uint64
	var walkCalls []uint64

	walkCallCount := 0
	mergeFailedOnce := false

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
			walkCallCount++
			callNum := walkCallCount
			if seenLowest := merger.bundler.LowestUnmergedBlockNum(); seenLowest > highestSeenLowestUnmergedBlockNum {
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
				if err := callback(blk); err != nil {
					return err
				}
			}
			return nil
		},
		MergeAndStoreFunc: func(_ context.Context, inclusiveLowerBlock uint64, _ []*bstream.OneBlockFile) error {
			mu.Lock()
			mergeCountPerBase[inclusiveLowerBlock]++
			shouldFail := inclusiveLowerBlock == 10 //&& !mergeFailedOnce
			if shouldFail {
				mergeFailedOnce = true
			}
			mergeAttempts = append(mergeAttempts, inclusiveLowerBlock)
			mu.Unlock()

			if shouldFail {
				return errors.New("injected merge failure for bundle 10")
			}
			return nil
		},
	}

	merger = newRunTestMerger(testIO, 0, bundleSize, 4)
	err := merger.run()
	require.NoError(t, err)
	merger.bundler.WaitForMerges()

	mu.Lock()
	assert.Equal(t, 10, int(highestSeenLowestUnmergedBlockNum), "bundler lowestUnmergedBlockNum should never go above 10")

	// Bundle 10-19 must have been attempted (and failed).
	assert.Greaterf(t, mergeCountPerBase[10], 1, "bundle 10 should have been attempted multiple times: mergeCalls: %v, walkCalls: %v", mergeAttempts, walkCalls)
	assert.True(t, mergeFailedOnce, "bundle 10 should have failed once")

	for base, count := range mergeCountPerBase {
		if base == 10 {
			continue // already checked above
		}
		assert.Less(t, count, 2, "bundler must not merge the same base block twice, base %d merged %d times", base, count)
	}
	mu.Unlock()

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
			if seenLowest := merger.bundler.LowestUnmergedBlockNum(); seenLowest > highestSeenLowestUnmergedBlockNum {
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

	//mu.Lock()
	//assert.Equal(t, 10, int(highestSeenLowestUnmergedBlockNum), "bundler lowestUnmergedBlockNum should never go above 10")

	// Bundle 10-19 must have been attempted (and failed).
	//assert.Greaterf(t, mergeCountPerBase[10], 1, "bundle 10 should have been attempted multiple times: mergeCalls: %v, walkCalls: %v", mergeAttempts, walkCalls)

	//for base, count := range mergeCountPerBase {
	//	if base == 10 {
	//		continue // already checked above
	//	}
	//	assert.Less(t, count, 2, "bundler must not merge the same base block twice, base %d merged %d times", base, count)
	//
	//mu.Unlock()

}
