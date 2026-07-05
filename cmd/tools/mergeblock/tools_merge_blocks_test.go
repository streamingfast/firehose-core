package mergeblock

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func writeOneBlockFile(t *testing.T, store dstore.Store, block *pbbstream.Block) {
	t.Helper()

	pr, pw := io.Pipe()
	go func() {
		var err error
		defer func() {
			pw.CloseWithError(err)
		}()

		bw, err := bstream.NewDBinBlockWriter(pw)
		if err != nil {
			return
		}
		err = bw.Write(block)
	}()

	filename := bstream.BlockFileNameWithSuffix(block, "extracted")
	require.NoError(t, store.WriteObject(context.Background(), filename, pr))
}

func runMergeBlocks(t *testing.T, srcURL, dstURL string, lowBoundary, bundleSize uint64) error {
	t.Helper()
	cmd := NewToolsMergeBlocksCmd(&firecore.Chain[testChainBlock]{}, zap.NewNop())
	cmd.SetContext(context.Background())
	cmd.Flags().Uint64("merged-blocks-bundle-size", bundleSize, "")
	return cmd.RunE(cmd, []string{srcURL, dstURL, fmt.Sprintf("%d", lowBoundary)})
}

func TestMergeBlocksStopsAtBundleBoundary(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)

	// blocks 0..100: block 100 belongs to the next bundle and must NOT
	// produce a bogus one-block merged file at 0000000100
	for i := uint64(0); i <= 100; i++ {
		writeOneBlockFile(t, srcStore, testBlock(i))
	}

	require.NoError(t, runMergeBlocks(t, srcURL, dstURL, 0, 100))

	dstStore, err := dstore.NewDBinStore(dstURL)
	require.NoError(t, err)
	files := readStore(t, dstStore)
	require.Len(t, files, 1)
	require.Contains(t, files, "0000000000")
	require.Len(t, files["0000000000"], 100)
	assert.Equal(t, uint64(0), files["0000000000"][0])
	assert.Equal(t, uint64(99), files["0000000000"][99])
}

func TestMergeBlocksFlushesPartialBundle(t *testing.T) {
	tmp := t.TempDir()
	srcURL := "file://" + filepath.Join(tmp, "src")
	dstURL := "file://" + filepath.Join(tmp, "dst")

	srcStore, err := dstore.NewDBinStore(srcURL)
	require.NoError(t, err)

	// fewer blocks than a full bundle: the walk completes normally and the
	// pending blocks are flushed as a partial bundle
	for i := uint64(0); i <= 49; i++ {
		writeOneBlockFile(t, srcStore, testBlock(i))
	}

	require.NoError(t, runMergeBlocks(t, srcURL, dstURL, 0, 100))

	dstStore, err := dstore.NewDBinStore(dstURL)
	require.NoError(t, err)
	files := readStore(t, dstStore)
	require.Len(t, files, 1)
	require.Len(t, files["0000000000"], 50)
}
