package mergeblock

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// writeCorruptMergedFile writes a merged-blocks file whose second block
// message is garbage, so reading it fails with a non-EOF error.
func writeCorruptMergedFile(t *testing.T, store dstore.Store, name string) {
	t.Helper()

	var buf bytes.Buffer
	bw, err := bstream.NewDBinBlockWriter(&buf)
	require.NoError(t, err)
	require.NoError(t, bw.Write(testBlock(0)))

	buf.Write([]byte{0x00, 0x00, 0x00, 0x10}) // dbin length prefix: 16 bytes
	buf.Write(bytes.Repeat([]byte{0xFF}, 16)) // garbage protobuf payload

	require.NoError(t, store.WriteObject(context.Background(), name, bytes.NewReader(buf.Bytes())))
}

func TestUnmergeBlocksCorruptFileErrors(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)
	writeCorruptMergedFile(t, srcStore, "0000000000")

	cmd := NewToolsUnmergeBlocksCmd(&firecore.Chain[testChainBlock]{}, zap.NewNop())
	cmd.SetContext(context.Background())
	cmd.Flags().Uint64("merged-blocks-bundle-size", 100, "")

	require.NotPanics(t, func() {
		err = cmd.RunE(cmd, []string{srcURL, dstURL, "0:10"})
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading block")
}
