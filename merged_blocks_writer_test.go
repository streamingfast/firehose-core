package firecore

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/anypb"
)

func testBlock(num uint64) *pbbstream.Block {
	id := fmt.Sprintf("%08x", num)
	parentID := ""
	if num > 0 {
		parentID = fmt.Sprintf("%08x", num-1)
	}
	return &pbbstream.Block{
		Number:   num,
		Id:       id,
		ParentId: parentID,
		LibNum:   0,
		Payload:  &anypb.Any{TypeUrl: "type.googleapis.com/sf.test.Block"},
	}
}

func writtenFiles(t *testing.T, store dstore.Store) map[string][]uint64 {
	t.Helper()
	out := map[string][]uint64{}
	err := store.Walk(context.Background(), "", func(filename string) error {
		reader, err := store.OpenObject(context.Background(), filename)
		require.NoError(t, err)
		defer reader.Close()

		blockReader, err := bstream.NewDBinBlockReader(reader)
		require.NoError(t, err)

		var nums []uint64
		for {
			blk, err := blockReader.Read()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			nums = append(nums, blk.Number)
		}
		out[filename] = nums
		return nil
	})
	require.NoError(t, err)
	return out
}

func TestMergedBlocksWriterBundleSize1000(t *testing.T) {
	store := dstore.NewMockStore(nil)
	writer := &MergedBlocksWriter{
		Store:      store,
		BundleSize: 1000,
		Logger:     zap.NewNop(),
	}

	for i := uint64(0); i < 2000; i++ {
		require.NoError(t, writer.ProcessBlock(testBlock(i), nil))
	}

	files := writtenFiles(t, store)
	require.Len(t, files, 2)
	assert.Len(t, files["0000000000"], 1000)
	assert.Len(t, files["0000001000"], 1000)
	assert.Equal(t, uint64(0), files["0000000000"][0])
	assert.Equal(t, uint64(999), files["0000000000"][999])
	assert.Equal(t, uint64(1000), files["0000001000"][0])
	assert.Equal(t, uint64(1999), files["0000001000"][999])
}

func TestMergedBlocksWriterDefaultBundleSize(t *testing.T) {
	store := dstore.NewMockStore(nil)
	writer := &MergedBlocksWriter{
		Store:  store,
		Logger: zap.NewNop(),
	}

	for i := uint64(0); i < 200; i++ {
		require.NoError(t, writer.ProcessBlock(testBlock(i), nil))
	}

	files := writtenFiles(t, store)
	require.Len(t, files, 2)
	assert.Len(t, files["0000000000"], 100)
	assert.Len(t, files["0000000100"], 100)
}

func TestMergedBlocksWriterInitialBoundary(t *testing.T) {
	store := dstore.NewMockStore(nil)
	writer := &MergedBlocksWriter{
		Store:      store,
		BundleSize: 1000,
		Logger:     zap.NewNop(),
	}

	// starting on a boundary above 0 is accepted
	require.NoError(t, writer.ProcessBlock(testBlock(3000), nil))
	assert.Equal(t, uint64(3000), writer.LowBlockNum)

	// starting off-boundary (and not on the first streamable block) is rejected
	writer = &MergedBlocksWriter{
		Store:      store,
		BundleSize: 1000,
		Logger:     zap.NewNop(),
	}
	require.Error(t, writer.ProcessBlock(testBlock(3500), nil))
}

func TestMergedBlocksWriterSkippedBlocks(t *testing.T) {
	// a chain hole spanning a bundle boundary must flush the previous bundle
	store := dstore.NewMockStore(nil)
	writer := &MergedBlocksWriter{
		Store:      store,
		BundleSize: 1000,
		Logger:     zap.NewNop(),
	}

	for i := uint64(0); i < 1900; i++ {
		require.NoError(t, writer.ProcessBlock(testBlock(i), nil))
	}
	// blocks 1900-2099 do not exist on this chain, block 2100 arrives next
	require.NoError(t, writer.ProcessBlock(testBlock(2100), nil))

	files := writtenFiles(t, store)
	require.Len(t, files, 2)
	assert.Len(t, files["0000000000"], 1000)
	assert.Len(t, files["0000001000"], 900)
	assert.Equal(t, uint64(2000), writer.LowBlockNum)
}

func TestMergedBlocksWriterMultiBundleGap(t *testing.T) {
	// a gap spanning more than one bundle must not produce misnamed files, and
	// must keep the merged-blocks sequence contiguous by writing a file per
	// skipped boundary (filesource stalls on a missing file)
	store := dstore.NewMockStore(nil)
	writer := &MergedBlocksWriter{
		Store:      store,
		BundleSize: 100,
		Logger:     zap.NewNop(),
	}

	require.NoError(t, writer.ProcessBlock(testBlock(5), nil))
	// blocks 6..249 do not exist on this chain
	require.NoError(t, writer.ProcessBlock(testBlock(250), nil))
	assert.Equal(t, uint64(200), writer.LowBlockNum)

	for i := uint64(251); i < 300; i++ {
		require.NoError(t, writer.ProcessBlock(testBlock(i), nil))
	}

	files := writtenFiles(t, store)
	require.Len(t, files, 3)
	assert.Equal(t, []uint64{5}, files["0000000000"])
	// the skipped boundary carries the previous bundle's last block (5), which
	// is below the file's base num and is skipped by filesource on read
	assert.Equal(t, []uint64{5}, files["0000000100"])
	assert.Equal(t, uint64(250), files["0000000200"][0])
	assert.Equal(t, uint64(299), files["0000000200"][49])
}

func TestMergedBlocksWriterMultiBundleGapFourBundles(t *testing.T) {
	store := dstore.NewMockStore(nil)
	writer := &MergedBlocksWriter{
		Store:      store,
		BundleSize: 100,
		Logger:     zap.NewNop(),
	}

	require.NoError(t, writer.ProcessBlock(testBlock(5), nil))
	// blocks 6..449 do not exist on this chain
	require.NoError(t, writer.ProcessBlock(testBlock(450), nil))
	assert.Equal(t, uint64(400), writer.LowBlockNum)

	// every skipped boundary gets a contiguous carry-over file so there is no
	// hole in the sequence up to the block's own window
	files := writtenFiles(t, store)
	require.Len(t, files, 4)
	assert.Equal(t, []uint64{5}, files["0000000000"])
	assert.Equal(t, []uint64{5}, files["0000000100"])
	assert.Equal(t, []uint64{5}, files["0000000200"])
	assert.Equal(t, []uint64{5}, files["0000000300"])
}

func TestLowBoundaryFor(t *testing.T) {
	assert.Equal(t, uint64(12300), LowBoundaryFor(12345, 100))
	assert.Equal(t, uint64(12000), LowBoundaryFor(12345, 1000))
	assert.Equal(t, uint64(0), LowBoundaryFor(99, 100))
	assert.Equal(t, uint64(12300), LowBoundary(12345), "LowBoundary keeps the default-100 behavior")
}

func TestValidateMergedBlocksBundleSize(t *testing.T) {
	assert.NoError(t, ValidateMergedBlocksBundleSize(100))
	assert.NoError(t, ValidateMergedBlocksBundleSize(1000))
	assert.NoError(t, ValidateMergedBlocksBundleSize(5000))
	assert.Error(t, ValidateMergedBlocksBundleSize(0))
	assert.Error(t, ValidateMergedBlocksBundleSize(50))
	assert.Error(t, ValidateMergedBlocksBundleSize(250))
	assert.Error(t, ValidateMergedBlocksBundleSize(101))
}
