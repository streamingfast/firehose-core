package fix

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
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

func writeMergedFile(t *testing.T, store dstore.Store, name string, from, upTo uint64, corrupt bool) {
	t.Helper()

	var buf bytes.Buffer
	bw, err := bstream.NewDBinBlockWriter(&buf)
	require.NoError(t, err)
	for i := from; i <= upTo; i++ {
		require.NoError(t, bw.Write(testBlock(i)))
	}

	if corrupt {
		buf.Write([]byte{0x00, 0x00, 0x00, 0x10}) // dbin length prefix: 16 bytes
		buf.Write(bytes.Repeat([]byte{0xFF}, 16)) // garbage protobuf payload
	}
	require.NoError(t, store.WriteObject(context.Background(), name, bytes.NewReader(buf.Bytes())))
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

func runFixBloated(t *testing.T, srcURL, dstURL, blockRange string, bundleSize uint64) error {
	t.Helper()
	cmd := NewToolsFixBloatedMergedBlocks(&firecore.Chain[*pbbstream.Block]{}, zap.NewNop())
	cmd.SetContext(context.Background())
	cmd.Flags().Uint64("merged-blocks-bundle-size", bundleSize, "")
	return cmd.RunE(cmd, []string{srcURL, dstURL, blockRange})
}

func TestFixBloatedMergedBlocksCorruptFileErrors(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)
	// last block message is garbage: reading it fails with a non-EOF error
	writeMergedFile(t, srcStore, "0000000000", 0, 1, true)

	var runErr error
	require.NotPanics(t, func() {
		runErr = runFixBloated(t, srcURL, dstURL, "0:99", 100)
	})
	require.Error(t, runErr)
	require.Contains(t, runErr.Error(), "reading block")
}
