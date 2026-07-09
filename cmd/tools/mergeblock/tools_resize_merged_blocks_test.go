package mergeblock

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
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

func runResize(t *testing.T, srcURL, dstURL string, start, stop uint64, sourceSize, targetSize uint64) error {
	t.Helper()
	cmd := NewToolsResizeMergedBlocksCmd(&firecore.Chain[testChainBlock]{}, zap.NewNop())
	cmd.SetContext(context.Background())
	require.NoError(t, cmd.Flags().Set("source-bundle-size", fmt.Sprintf("%d", sourceSize)))
	require.NoError(t, cmd.Flags().Set("target-bundle-size", fmt.Sprintf("%d", targetSize)))
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

func TestResizeMergedBlocksValidation(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	// same size
	assert.Error(t, runResize(t, srcURL, dstURL, 0, 1000, 100, 100))
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
