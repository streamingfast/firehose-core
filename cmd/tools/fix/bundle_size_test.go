package fix

import (
	"context"
	"path/filepath"
	"testing"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Both fix tools must honor --merged-blocks-bundle-size when writing: the
// writer used to be built without BundleSize, silently falling back to
// 100-blocks bundles regardless of the flag.

func TestFixBloatedMergedBlocksHonorsBundleSize(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)
	writeMergedFile(t, srcStore, "0000000000", 0, 999, false)
	writeMergedFile(t, srcStore, "0000001000", 1000, 1999, false)

	require.NoError(t, runFixBloated(t, srcURL, dstURL, "0:1999", 1000))

	dstStore, err := dstore.NewDBinStore(dstURL)
	require.NoError(t, err)
	files := readStore(t, dstStore)
	require.Contains(t, files, "0000000000")
	require.Contains(t, files, "0000001000")
	require.Len(t, files, 2, "writer must produce 1000-blocks bundles, not default-100 ones")
	assert.Len(t, files["0000000000"], 1000)
	assert.Len(t, files["0000001000"], 1000)
}

func TestLegacy2BlockAnyHonorsBundleSize(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)
	writeMergedFile(t, srcStore, "0000000000", 0, 999, false)

	cmd := NewLegacy2BlockAby(&firecore.Chain[*pbbstream.Block]{}, zap.NewNop())
	cmd.SetContext(context.Background())
	cmd.Flags().Uint64("merged-blocks-bundle-size", 1000, "")
	require.NoError(t, cmd.RunE(cmd, []string{srcURL, dstURL, "0:999"}))

	dstStore, err := dstore.NewDBinStore(dstURL)
	require.NoError(t, err)
	files := readStore(t, dstStore)
	require.Len(t, files, 1, "writer must produce 1000-blocks bundles, not default-100 ones")
	require.Contains(t, files, "0000000000")
	assert.Len(t, files["0000000000"], 1000)
}
