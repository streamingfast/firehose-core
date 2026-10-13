package mergeblock

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dbin"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// testChainBlock is a minimal firecore.Block implementation, only used to
// instantiate the generic command (the chain is not used by the command).
type testChainBlock struct {
	*pbbstream.Block
}

func (b testChainBlock) GetFirehoseBlockID() string           { return b.Id }
func (b testChainBlock) GetFirehoseBlockNumber() uint64       { return b.Number }
func (b testChainBlock) GetFirehoseBlockParentID() string     { return b.ParentId }
func (b testChainBlock) GetFirehoseBlockParentNumber() uint64 { return b.ParentNum }
func (b testChainBlock) GetFirehoseBlockTime() time.Time      { return time.Time{} }

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

func fillStore(t *testing.T, store dstore.Store, bundleSize uint64, from, upTo uint64) {
	t.Helper()
	writer := &firecore.MergedBlocksWriter{
		Store:       store,
		LowBlockNum: from,
		BundleSize:  bundleSize,
		Logger:      zap.NewNop(),
	}
	for i := from; i <= upTo; i++ {
		require.NoError(t, writer.ProcessBlock(testBlock(i), nil))
	}
}

func readStore(t *testing.T, store dstore.Store) map[string][]uint64 {
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

func runResize(t *testing.T, srcURL, dstURL string, start, stop uint64, sourceSize, targetSize uint64, flags ...string) error {
	t.Helper()
	cmd := NewToolsResizeMergedBlocksCmd(&firecore.Chain[testChainBlock]{}, zap.NewNop())
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.Flags().Set("source-bundle-size", fmt.Sprintf("%d", sourceSize)))
	require.NoError(t, cmd.Flags().Set("target-bundle-size", fmt.Sprintf("%d", targetSize)))
	for _, flag := range flags {
		name, value, _ := strings.Cut(flag, "=")
		require.NoError(t, cmd.Flags().Set(name, value))
	}
	return cmd.RunE(cmd, []string{srcURL, dstURL, fmt.Sprintf("%d", start), fmt.Sprintf("%d", stop)})
}

func TestResizeMergedBlocksUpsize(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)

	// blocks 0..2099 in 100-blocks files: the stream needs to read the stop
	// block (2000) from the source, so the file 0000002000 must exist
	fillStore(t, srcStore, 100, 0, 2099)

	require.NoError(t, runResize(t, srcURL, dstURL, 0, 2000, 100, 1000))

	dstStore, err := dstore.NewDBinStore(dstURL)
	require.NoError(t, err)
	files := readStore(t, dstStore)
	require.Len(t, files, 2)
	assert.Len(t, files["0000000000"], 1000)
	assert.Len(t, files["0000001000"], 1000)
	assert.Equal(t, uint64(0), files["0000000000"][0])
	assert.Equal(t, uint64(1999), files["0000001000"][999])
}

func TestResizeMergedBlocksDownsize(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)

	// blocks 0..1999 in 1000-blocks files: the stream needs to read the stop
	// block (1000) from the source, so the file 0000001000 must exist
	fillStore(t, srcStore, 1000, 0, 1999)

	require.NoError(t, runResize(t, srcURL, dstURL, 0, 1000, 1000, 100))

	dstStore, err := dstore.NewDBinStore(dstURL)
	require.NoError(t, err)
	files := readStore(t, dstStore)
	require.Len(t, files, 10)
	assert.Len(t, files["0000000000"], 100)
	assert.Len(t, files["0000000900"], 100)
	assert.Equal(t, uint64(999), files["0000000900"][99])
}

func TestResizeMergedBlocksSameSize(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)

	// blocks 0..399 in 100-blocks files: the stream needs to read the stop
	// block (300) from the source, so the file 0000000300 must exist
	fillStore(t, srcStore, 100, 0, 399)

	require.NoError(t, runResize(t, srcURL, dstURL, 0, 300, 100, 100, "force=true"))

	dstStore, err := dstore.NewDBinStore(dstURL)
	require.NoError(t, err)
	files := readStore(t, dstStore)
	require.Len(t, files, 3)
	for _, name := range []string{"0000000000", "0000000100", "0000000200"} {
		assert.Len(t, files[name], 100, name)
	}
	assert.Equal(t, uint64(0), files["0000000000"][0])
	assert.Equal(t, uint64(100), files["0000000100"][0])
	assert.Equal(t, uint64(299), files["0000000200"][99])
}

// chainBlock is block num with parentNum as parent, the ids following testBlock's.
func chainBlock(num, parentNum uint64) *pbbstream.Block {
	blk := testBlock(num)
	blk.ParentNum = parentNum
	blk.ParentId = fmt.Sprintf("%08x", parentNum)
	return blk
}

// writeSourceFile writes blocks as they are to the merged-blocks file named after lowBlockNum.
func writeSourceFile(t *testing.T, store dstore.Store, lowBlockNum uint64, blocks ...*pbbstream.Block) {
	t.Helper()
	pr, pw := io.Pipe()
	go func() {
		writer, err := bstream.NewDBinBlockWriter(pw)
		if err == nil {
			for _, blk := range blocks {
				if err = writer.Write(blk); err != nil {
					break
				}
			}
		}
		pw.CloseWithError(err)
	}()
	require.NoError(t, store.WriteObject(context.Background(), fmt.Sprintf("%010d", lowBlockNum), pr))
}

// writeLegacySourceFile writes blocks to the merged-blocks file named after lowBlockNum with
// the content type that legacy files have in their header.
func writeLegacySourceFile(t *testing.T, store dstore.Store, lowBlockNum uint64, blocks ...*pbbstream.Block) {
	t.Helper()
	pr, pw := io.Pipe()
	go func() {
		writer := dbin.NewWriter(pw)
		err := writer.WriteHeader("ETH")
		for _, blk := range blocks {
			if err != nil {
				break
			}
			var data []byte
			if data, err = proto.Marshal(blk); err == nil {
				err = writer.WriteMessage(data)
			}
		}
		pw.CloseWithError(err)
	}()
	require.NoError(t, store.WriteObject(context.Background(), fmt.Sprintf("%010d", lowBlockNum), pr))
}

func runResizeFixingMisplacedBlocks(t *testing.T, srcURL, dstURL string, start, stop uint64) error {
	t.Helper()
	cmd := NewToolsResizeMergedBlocksCmd(&firecore.Chain[testChainBlock]{}, zap.NewNop())
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.Flags().Set("target-bundle-size", "1000"))
	require.NoError(t, cmd.Flags().Set("fix-misplaced-blocks", "true"))
	return cmd.RunE(cmd, []string{srcURL, dstURL, fmt.Sprintf("%d", start), fmt.Sprintf("%d", stop)})
}

// fillMisplacedBlocksStore writes the blocks 0..216, then a gap up to block 1350, like
// near-testnet around block 53461716: the files 300 to 1200 have no block of their own, the
// first ones carry block 216 and the blocks 1350, 1352, ..., 1368 are written one per file
// from 0000000300 to 0000001200. The file 0000001300 starts at block 1370.
func fillMisplacedBlocksStore(t *testing.T, store dstore.Store) {
	t.Helper()
	fillStore(t, store, 100, 0, 199)
	writeSourceFile(t, store, 200, blockRange(200, 216)...)

	previous := uint64(216)
	for i, low := uint64(0), uint64(300); low <= 1200; i, low = i+1, low+100 {
		num := 1350 + 2*i
		blocks := []*pbbstream.Block{chainBlock(num, previous)}
		if low <= 400 {
			blocks = append([]*pbbstream.Block{testBlock(216)}, blocks...)
		}
		writeSourceFile(t, store, low, blocks...)
		previous = num
	}

	writeSourceFile(t, store, 1300, append([]*pbbstream.Block{chainBlock(1370, previous)}, blockRange(1371, 1399)...)...)
	fillStore(t, store, 100, 1400, 2099)
}

func blockRange(from, to uint64) (out []*pbbstream.Block) {
	for i := from; i <= to; i++ {
		out = append(out, testBlock(i))
	}
	return out
}

func TestResizeMergedBlocksFixMisplacedBlocks(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)
	fillMisplacedBlocksStore(t, srcStore)

	require.ErrorContains(t, runResize(t, srcURL, dstURL, 0, 2000, 100, 1000), "beyond the configured bundle size")

	require.NoError(t, runResizeFixingMisplacedBlocks(t, srcURL, dstURL, 0, 2000))

	dstStore, err := dstore.NewDBinStore(dstURL)
	require.NoError(t, err)
	files := readStore(t, dstStore)
	require.Len(t, files, 2)

	var first []uint64
	for _, blk := range blockRange(0, 216) {
		first = append(first, blk.Number)
	}
	assert.Equal(t, first, files["0000000000"])

	second := []uint64{1350, 1352, 1354, 1356, 1358, 1360, 1362, 1364, 1366, 1368}
	for i := uint64(1370); i < 2000; i++ {
		second = append(second, i)
	}
	assert.Equal(t, second, files["0000001000"])
}

func TestResizeMergedBlocksFixMisplacedBlocksUnlinkedBlock(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)

	fillStore(t, srcStore, 100, 0, 149)
	writeSourceFile(t, srcStore, 100, append(blockRange(100, 149), chainBlock(150, 148))...)
	fillStore(t, srcStore, 100, 200, 1099)

	assert.ErrorContains(t, runResizeFixingMisplacedBlocks(t, srcURL, dstURL, 0, 1000), "does not link to the last block written")
}

func TestResizeMergedBlocksFixMisplacedBlocksLegacyFormat(t *testing.T) {
	t.Setenv("ACCEPT_LEGACY_BLOCK_FORMAT", "true")

	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)

	// Block 150 is skipped, legacy blocks have no payload and no parent number.
	legacy := func(blk *pbbstream.Block) *pbbstream.Block {
		blk.Payload = nil
		blk.ParentNum = 0
		blk.PayloadKind = pbbstream.Protocol_NEAR
		blk.PayloadBuffer = []byte{0x01}
		return blk
	}
	var blocks []*pbbstream.Block
	for _, blk := range append(append(blockRange(100, 149), chainBlock(151, 149)), blockRange(152, 199)...) {
		blocks = append(blocks, legacy(blk))
	}

	fillStore(t, srcStore, 100, 0, 99)
	writeLegacySourceFile(t, srcStore, 100, blocks...)
	fillStore(t, srcStore, 100, 200, 1099)

	require.NoError(t, runResizeFixingMisplacedBlocks(t, srcURL, dstURL, 0, 1000))

	dstStore, err := dstore.NewDBinStore(dstURL)
	require.NoError(t, err)
	reader, err := dstStore.OpenObject(context.Background(), "0000000000")
	require.NoError(t, err)
	defer reader.Close()
	blockReader, err := bstream.NewDBinBlockReader(reader)
	require.NoError(t, err)

	parentNums := map[uint64]uint64{}
	for {
		blk, err := blockReader.Read()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.NotNil(t, blk.Payload, "block %d", blk.Number)
		if blk.Number >= 100 && blk.Number < 200 {
			assert.Equal(t, []byte{0x01}, blk.Payload.Value, "block %d", blk.Number)
		}
		assert.Nil(t, blk.PayloadBuffer, "block %d", blk.Number)
		assert.Equal(t, pbbstream.Protocol_UNKNOWN, blk.PayloadKind, "block %d", blk.Number)
		parentNums[blk.Number] = blk.ParentNum
	}
	assert.Len(t, parentNums, 999)
	assert.Equal(t, uint64(148), parentNums[149])
	assert.Equal(t, uint64(149), parentNums[151])
	assert.Equal(t, uint64(151), parentNums[152])
}

func TestResizeMergedBlocksValidation(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	// non-multiple-of-100 target
	assert.Error(t, runResize(t, srcURL, dstURL, 0, 1000, 100, 250))
	// sizes not dividing evenly
	assert.Error(t, runResize(t, srcURL, dstURL, 0, 6000, 200, 300))
	// start not aligned on target boundary
	assert.Error(t, runResize(t, srcURL, dstURL, 500, 2000, 100, 1000))
	// stop not aligned on target boundary
	assert.Error(t, runResize(t, srcURL, dstURL, 0, 1500, 100, 1000))
	// stop below start
	assert.Error(t, runResize(t, srcURL, dstURL, 2000, 1000, 100, 1000))
}
