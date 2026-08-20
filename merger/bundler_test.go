package merger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
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
	b := NewBundler(100, 200, 2, 100, nil, 1, nil)
	require.NotNil(t, b)
	assert.EqualValues(t, 100, b.bundleSize)
	assert.EqualValues(t, 200, b.stopBlock)
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
	}, 2, nil)
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
	}, 1, nil)
	b.irreversibleBlocks = []*bstream.OneBlockFile{block100(), block101()}

	for _, blk := range twoMergesBlocks {
		require.NoError(t, b.HandleBlockFile(blk))
	}
	b.WaitForMerges()

	assert.Equal(t, 1, maxObserved, "with maxMergingThreads=1 at most 1 merge should run at a time")
}

func TestBundlerReset(t *testing.T) {
	b := NewBundler(100, 200, 2, 2, nil, 1, nil) // merge every 2 blocks

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
			}, 1, nil) // merge every 2 blocks
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

// testObjWrapper wraps a OneBlockFile the way the forkable does when it calls ProcessBlock.
type testObjWrapper struct{ obf *bstream.OneBlockFile }

func (w testObjWrapper) WrappedObject() any { return w.obf }

func TestBundlerProcessBlockTerminatingReleasesLock(t *testing.T) {
	// A merge failure marks the errgroup as stopped. When ProcessBlock is inside the
	// skip-forward loop at that moment, it returns errTerminating: the bundler lock
	// must not be left held, otherwise pruners calling getSafeBaseBlockNum deadlock.
	started := make(chan struct{}, 1)
	proceed := make(chan struct{})

	b := NewBundler(100, 0, 2, 100, &TestMergerIO{
		MergeAndStoreFunc: func(_ context.Context, _ uint64, _ []*bstream.OneBlockFile) error {
			select {
			case started <- struct{}{}:
			default:
			}
			<-proceed
			return errors.New("merge failed")
		},
	}, 1, func(error) {})
	b.enforceNextBlockOnBoundary = false
	b.irreversibleBlocks = []*bstream.OneBlockFile{block100(), block101()}

	done := make(chan error, 1)
	go func() {
		// block 1000 triggers the merge of bundle 100 (which blocks, then fails), then
		// enters the skip-forward loop where eg.Stop() turns true once the merge has failed
		done <- b.ProcessBlock(nil, testObjWrapper{obf: chainBlock(1000, 999, 999)})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first merge never started")
	}
	close(proceed) // let the merge fail, stopping the errgroup

	select {
	case err := <-done:
		require.ErrorIs(t, err, errTerminating)
	case <-time.After(5 * time.Second):
		t.Fatal("ProcessBlock did not return")
	}

	// the bundler lock must be free after ProcessBlock returned errTerminating
	got := make(chan uint64, 1)
	go func() { got <- b.getSafeBaseBlockNum() }()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("bundler lock leaked: getSafeBaseBlockNum deadlocked")
	}
}

// TestBundlerSkipLoopBoundary verifies bundle attribution when a gap in irreversible
// blocks ends near a bundle boundary. A bundle covers [base, base+size): a block whose
// number is exactly base+size belongs to the NEXT bundle, so the skip-forward loop must
// also fire on equality, otherwise that block gets attributed to the previous bundle.
func TestBundlerSkipLoopBoundary(t *testing.T) {
	obf := func(name string) *bstream.OneBlockFile { return bstream.MustNewOneBlockFile(name) }

	commonChain := []*bstream.OneBlockFile{
		block100(), block101(), block102Final100(), block103Final101(),
		block104Final102(), block105Final103(), block106Final104(),
	}

	tests := []struct {
		name            string
		gapBlocks       []*bstream.OneBlockFile
		expectMerged    []uint64
		expectBase      uint64
		expectRemaining []*bstream.OneBlockFile
	}{
		{
			// block 300 == base(200)+size(100) after the first merge: it belongs to
			// bundle 300, so bundle 200 must be skip-merged
			name: "gap_ends_exactly_on_boundary",
			gapBlocks: []*bstream.OneBlockFile{
				obf("0000000300-0000000000000300a-0000000000000106a-106-suffix"),
				obf("0000000301-0000000000000301a-0000000000000300a-300-suffix"),
				obf("0000000302-0000000000000302a-0000000000000301a-301-suffix"),
			},
			expectMerged: []uint64{100, 200},
			expectBase:   300,
			expectRemaining: []*bstream.OneBlockFile{
				block106Final104(),
				obf("0000000300-0000000000000300a-0000000000000106a-106-suffix"),
				obf("0000000301-0000000000000301a-0000000000000300a-300-suffix"),
			},
		},
		{
			name: "gap_ends_one_past_boundary",
			gapBlocks: []*bstream.OneBlockFile{
				obf("0000000301-0000000000000301a-0000000000000106a-106-suffix"),
				obf("0000000302-0000000000000302a-0000000000000301a-301-suffix"),
				obf("0000000303-0000000000000303a-0000000000000302a-302-suffix"),
			},
			expectMerged: []uint64{100, 200},
			expectBase:   300,
			expectRemaining: []*bstream.OneBlockFile{
				block106Final104(),
				obf("0000000301-0000000000000301a-0000000000000106a-106-suffix"),
				obf("0000000302-0000000000000302a-0000000000000301a-301-suffix"),
			},
		},
		{
			// block 200 == base(100)+size(100): triggers the main merge only, no skip
			name: "gap_ends_exactly_on_next_bundle_start",
			gapBlocks: []*bstream.OneBlockFile{
				obf("0000000200-0000000000000200a-0000000000000106a-106-suffix"),
				obf("0000000201-0000000000000201a-0000000000000200a-200-suffix"),
				obf("0000000202-0000000000000202a-0000000000000201a-201-suffix"),
			},
			expectMerged: []uint64{100},
			expectBase:   200,
			expectRemaining: []*bstream.OneBlockFile{
				block106Final104(),
				obf("0000000200-0000000000000200a-0000000000000106a-106-suffix"),
				obf("0000000201-0000000000000201a-0000000000000200a-200-suffix"),
			},
		},
	}

	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			var merged []uint64
			b := NewBundler(100, 700, 2, 100, &TestMergerIO{
				MergeAndStoreFunc: func(_ context.Context, inclusiveLowerBlock uint64, _ []*bstream.OneBlockFile) error {
					merged = append(merged, inclusiveLowerBlock)
					return nil
				},
			}, 1, nil)
			b.irreversibleBlocks = []*bstream.OneBlockFile{block100(), block101()}

			for _, blk := range append(append([]*bstream.OneBlockFile{}, commonChain...), c.gapBlocks...) {
				require.NoError(t, b.HandleBlockFile(blk))
			}
			b.WaitForMerges()

			assert.Equal(t, c.expectMerged, merged)
			assert.Equal(t, int(c.expectBase), int(b.baseBlockNum))
			assert.Equal(t, c.expectRemaining, b.irreversibleBlocks)
		})
	}
}

// TestBundlerSkipsWholeBundles covers a chain that skips block numbers over one or more
// full bundle ranges. Merged-blocks boundaries must stay contiguous (a reader walks them
// one bundle at a time), so every skipped boundary has to get its own file, and blocks
// landing after the gap must go to the bundle their number belongs to, not to the one the
// merger was filling when the gap started. This drives the real DStoreIO so it also
// asserts what actually lands in the store.
func TestBundlerSkipsWholeBundles(t *testing.T) {
	mk := func(num, prev, lib uint64) *bstream.OneBlockFile {
		return bstream.MustNewOneBlockFile(fmt.Sprintf("%010d-%016da-%016da-%d-suffix", num, num, prev, lib))
	}
	oneBlockContent := func(num uint64) []byte {
		out := new(bytes.Buffer)
		w, err := bstream.NewDBinBlockWriter(out)
		require.NoError(t, err)
		anyB, err := anypb.New(&test.Block{Number: num})
		require.NoError(t, err)
		require.NoError(t, w.Write(&pbbstream.Block{Number: num, Id: fmt.Sprintf("%016da", num), Payload: anyB}))
		return out.Bytes()
	}

	oneBlockStore := dstore.NewMockStore(nil)
	oneBlockStore.OpenObjectFunc = func(_ context.Context, name string) (io.ReadCloser, error) {
		num, _, _, _, _, err := bstream.ParseFilename(name)
		require.NoError(t, err)
		return io.NopCloser(bytes.NewReader(oneBlockContent(num))), nil
	}

	var mu sync.Mutex
	written := map[string][]byte{}
	mergedStore := dstore.NewMockStore(func(base string, f io.Reader) error {
		data, err := io.ReadAll(f)
		require.NoError(t, err)
		mu.Lock()
		defer mu.Unlock()
		written[base] = data
		return nil
	})

	mio := NewDStoreIO(testLogger, oneBlockStore, mergedStore, nil, 1, 0, 100, 0)
	b := NewBundler(100, 0, 2, 100, mio, 1, nil)

	chain := []*bstream.OneBlockFile{
		block100(), block101(), block102Final100(), block103Final101(),
		block104Final102(), block105Final103(), block106Final104(),
		mk(320, 106, 106), mk(321, 320, 320), mk(322, 321, 321), // skips bundle 200 entirely
		mk(450, 322, 322), mk(451, 450, 450),
		mk(900, 451, 451), mk(901, 900, 900), mk(902, 901, 901), // skips bundles 500 to 800
	}
	for _, blk := range chain {
		require.NoError(t, b.HandleBlockFile(blk))
	}
	b.WaitForMerges()

	readBundle := func(t *testing.T, base string) []uint64 {
		t.Helper()
		data, ok := written[base]
		require.True(t, ok, "no merged-blocks file written for bundle %s", base)

		r, err := bstream.NewDBinBlockReader(bytes.NewReader(data))
		require.NoError(t, err, "bundle %s is not a readable merged-blocks file", base)

		var nums []uint64
		for {
			blk, err := r.Read()
			if err != nil {
				require.ErrorIs(t, err, io.EOF)
				return nums
			}
			nums = append(nums, blk.Number)
		}
	}

	// every boundary between the first and the last merged bundle has a file, and no
	// block ever lands outside of the bundle its number belongs to
	assert.Equal(t, []uint64{100, 101, 102, 103, 104, 105, 106}, readBundle(t, "0000000100"))
	assert.Empty(t, readBundle(t, "0000000200"))
	assert.Equal(t, []uint64{320, 321, 322}, readBundle(t, "0000000300"))
	assert.Equal(t, []uint64{450, 451}, readBundle(t, "0000000400"))
	for _, base := range []string{"0000000500", "0000000600", "0000000700", "0000000800"} {
		assert.Empty(t, readBundle(t, base), "bundle %s", base)
	}

	assert.Equal(t, 8, len(written))
	assert.Equal(t, uint64(900), b.baseBlockNum)
}
