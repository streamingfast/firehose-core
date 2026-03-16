package merger

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setPbBlock(obf *bstream.OneBlockFile) {
	//pbb := &pbbstream.Block{
	//	Number: obf.Num,
	//}
	//out, err := proto.Marshal(pbb)
	//if err != nil {
	//	panic(err)
	//}
	//obf.MemoizeData = out
}

var block98 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000098-0000000000000098a-0000000000000097a-96-suffix")
	setPbBlock(obf)
	return obf
}
var block99 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000099-0000000000000099a-0000000000000098a-97-suffix")
	setPbBlock(obf)
	return obf

}
var block100 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000100-0000000000000100a-0000000000000099a-98-suffix")
	setPbBlock(obf)
	return obf
}
var block101 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000101-0000000000000101a-0000000000000100a-99-suffix")
	setPbBlock(obf)
	return obf
}
var block102Final100 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000102-0000000000000102a-0000000000000101a-100-suffix")
	setPbBlock(obf)
	return obf
}
var block103Final101 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000103-0000000000000103a-0000000000000102a-101-suffix")
	setPbBlock(obf)
	return obf
}
var block104Final102 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000104-0000000000000104a-0000000000000103a-102-suffix")
	setPbBlock(obf)
	return obf
}
var block105Final103 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000105-0000000000000105a-0000000000000104a-103-suffix")
	setPbBlock(obf)
	return obf
}
var block106Final104 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000106-0000000000000106a-0000000000000105a-104-suffix")
	setPbBlock(obf)
	return obf
}

var block507Final106 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000507-0000000000000507a-0000000000000106a-106-suffix")
	setPbBlock(obf)
	return obf
}
var block608Final507 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000608-0000000000000608a-0000000000000507a-507-suffix")
	setPbBlock(obf)
	return obf
}
var block609Final608 = func() *bstream.OneBlockFile {
	obf := bstream.MustNewOneBlockFile("0000000609-0000000000000609a-0000000000000608a-608-suffix")
	setPbBlock(obf)
	return obf
}

func TestNewBundler(t *testing.T) {
	b := NewBundler(100, 200, 2, 100, nil, 1)
	require.NotNil(t, b)
	assert.EqualValues(t, 100, b.bundleSize)
	assert.EqualValues(t, 200, b.stopBlock)
	assert.NotNil(t, b.bundleError)
	assert.NotNil(t, b.seenBlockFiles)
}

// twoMergesBlocks is the canonical input that triggers exactly 2 async bundle merges
// (at bases 100 and 102) when fed to a Bundler with mergeSize=2 seeded with block100+block101.
var twoMergesBlocks = []*bstream.OneBlockFile{
	block100(), block101(), block102Final100(), block103Final101(),
	block104Final102(), block105Final103(), block106Final104(),
}

func TestBundlerParallelMergesRunConcurrently(t *testing.T) {
	// With maxMergingThreads=2 and 2 bundle triggers, both goroutines should enter
	// MergeAndStore before either is allowed to return, proving true parallelism.
	started := make(chan struct{}, 2)
	release := make(chan struct{})

	b := NewBundler(100, 700, 2, 2, &TestMergerIO{
		MergeAndStoreFunc: func(_ context.Context, _ uint64, _ []*bstream.OneBlockFile) error {
			started <- struct{}{}
			<-release
			return nil
		},
	}, 2)
	b.irreversibleBlocks = []*bstream.OneBlockFile{block100(), block101()}

	feedDone := make(chan error, 1)
	go func() {
		for _, blk := range twoMergesBlocks {
			if err := b.HandleBlockFile(blk); err != nil {
				feedDone <- err
				return
			}
		}
		feedDone <- nil
	}()

	// Both merges must start before either finishes (they're blocked on release).
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent merges to start")
		}
	}

	close(release)
	require.NoError(t, <-feedDone)
	b.WaitForMerges()
}

func TestBundlerLowestUnmergedBlockNumSafeWhileMerging(t *testing.T) {
	// Verify that LowestUnmergedBlockNum() returns the lowest in-flight merge base so the
	// pruner never deletes one-block-files from a bundle still being merged.
	started := make(chan struct{}, 2)
	release := make(chan struct{})

	b := NewBundler(100, 700, 2, 2, &TestMergerIO{
		MergeAndStoreFunc: func(_ context.Context, _ uint64, _ []*bstream.OneBlockFile) error {
			started <- struct{}{}
			<-release
			return nil
		},
	}, 2)
	b.irreversibleBlocks = []*bstream.OneBlockFile{block100(), block101()}

	feedDone := make(chan error, 1)
	go func() {
		for _, blk := range twoMergesBlocks {
			if err := b.HandleBlockFile(blk); err != nil {
				feedDone <- err
				return
			}
		}
		feedDone <- nil
	}()

	// Wait for both bundles (100 and 102) to be in-flight.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent merges to start")
		}
	}

	// Both bundles in-flight: BaseBlockNum must return 100 (the lower one),
	// not 104 (the current advancing base), to prevent premature file deletion.
	assert.EqualValues(t, 100, b.LowestUnmergedBlockNum())

	close(release)
	require.NoError(t, <-feedDone)
	b.WaitForMerges()

	// All merges done: BaseBlockNum returns the current base.
	assert.EqualValues(t, 104, b.LowestUnmergedBlockNum())
}

func TestBundlerSemaphoreLimitsParallelism(t *testing.T) {
	// With maxMergingThreads=1, the second bundle must wait for the first to finish
	// before its goroutine can start. Verify that at most 1 merge runs at a time.
	var mu sync.Mutex
	inFlight, maxObserved := 0, 0

	b := NewBundler(100, 700, 2, 2, &TestMergerIO{
		MergeAndStoreFunc: func(_ context.Context, _ uint64, _ []*bstream.OneBlockFile) error {
			mu.Lock()
			inFlight++
			if inFlight > maxObserved {
				maxObserved = inFlight
			}
			mu.Unlock()

			time.Sleep(5 * time.Millisecond) // hold the slot briefly so overlap is detectable

			mu.Lock()
			inFlight--
			mu.Unlock()
			return nil
		},
	}, 1)
	b.irreversibleBlocks = []*bstream.OneBlockFile{block100(), block101()}

	for _, blk := range twoMergesBlocks {
		require.NoError(t, b.HandleBlockFile(blk))
	}
	b.WaitForMerges()

	assert.Equal(t, 1, maxObserved, "with maxMergingThreads=1 at most 1 merge should run at a time")
}

func TestBundlerReset(t *testing.T) {
	b := NewBundler(100, 200, 2, 2, nil, 1) // merge every 2 blocks

	b.irreversibleBlocks = []*bstream.OneBlockFile{block100(), block101()}
	b.Reset(102, block100().ToBstreamBlock().AsRef())
	assert.Nil(t, b.irreversibleBlocks)
	assert.EqualValues(t, 102, b.baseBlockNum)

}

func TestBundlerMergeKeepOne(t *testing.T) {

	tests := []struct {
		name            string
		inBlocks        []*bstream.OneBlockFile
		mergeSize       uint64
		expectRemaining []*bstream.OneBlockFile
		expectBase      uint64
		expectMerged    []uint64
	}{
		{
			name: "vanilla",
			inBlocks: []*bstream.OneBlockFile{
				block100(),
				block101(),
				block102Final100(),
				block103Final101(),
				block104Final102(),
			},
			mergeSize: 2,
			expectRemaining: []*bstream.OneBlockFile{
				block101(),
				block102Final100(),
			},
			expectBase:   102,
			expectMerged: []uint64{100},
		},
		{
			name: "vanilla_plus_one",
			inBlocks: []*bstream.OneBlockFile{
				block100(),
				block101(),
				block102Final100(),
				block103Final101(),
				block104Final102(),
				block105Final103(),
			},
			mergeSize: 2,
			expectRemaining: []*bstream.OneBlockFile{
				block101(),
				block102Final100(),
				block103Final101(),
			},
			expectBase:   102,
			expectMerged: []uint64{100},
		},
		{
			name: "twoMerges",
			inBlocks: []*bstream.OneBlockFile{
				block100(),
				block101(),
				block102Final100(),
				block103Final101(),
				block104Final102(),
				block105Final103(),
				block106Final104(),
			},
			mergeSize: 2,
			expectRemaining: []*bstream.OneBlockFile{
				block103Final101(),
				block104Final102(),
			},
			expectBase:   104,
			expectMerged: []uint64{100, 102},
		},
		{
			name: "big_hole",
			inBlocks: []*bstream.OneBlockFile{
				block100(),
				block101(),
				block102Final100(),
				block103Final101(),
				block104Final102(),
				block105Final103(),
				block106Final104(),
				block507Final106(),
				block608Final507(),
				block609Final608(),
			},
			mergeSize: 100,
			expectRemaining: []*bstream.OneBlockFile{
				block507Final106(), // last from bundle 500
				block608Final507(), // the only irreversible block from current bundle
			},
			expectBase:   600,
			expectMerged: []uint64{100, 200, 300, 400, 500},
		},
	}

	for _, c := range tests {

		t.Run(c.name, func(t *testing.T) {
			var merged []uint64
			b := NewBundler(100, 700, 2, c.mergeSize, &TestMergerIO{
				MergeAndStoreFunc: func(_ context.Context, inclusiveLowerBlock uint64, _ []*bstream.OneBlockFile) (err error) {
					merged = append(merged, inclusiveLowerBlock)
					return nil
				},
			}, 1) // merge every 2 blocks
			b.irreversibleBlocks = []*bstream.OneBlockFile{block100(), block101()}

			for _, blk := range c.inBlocks {
				require.NoError(t, b.HandleBlockFile(blk))
			}

			// wait for all in-flight merges to complete
			b.WaitForMerges()

			assert.Equal(t, c.expectMerged, merged)
			assert.Equal(t, c.expectRemaining, b.irreversibleBlocks)
			assert.Equal(t, int(c.expectBase), int(b.baseBlockNum))
		})
	}
}
