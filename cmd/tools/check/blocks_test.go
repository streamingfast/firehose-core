package check

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestSegmentBoundaryValidator_HealthyBundle(t *testing.T) {
	v := newSegmentBoundaryValidator("0096336100", 96336100, 100)

	v.add(96336099) // carried over from the previous bundle, not an issue
	for i := uint64(96336100); i < 96336200; i++ {
		v.add(i)
	}

	assert.False(t, v.broken())
	assert.Equal(t, uint64(1), v.blocksBelowBase)
	assert.Equal(t, uint64(0), v.blocksAboveBoundary)
	assert.Equal(t, uint64(0), v.outOfOrderCount)
}

func TestSegmentBoundaryValidator_BlockBeyondBoundary(t *testing.T) {
	v := newSegmentBoundaryValidator("0096336100", 96336100, 100)

	for i := uint64(96336100); i < 96336200; i++ {
		v.add(i)
	}
	// what substreams-tier2 reported: a later bundle's block sitting in 0096336100
	v.add(96336220)

	assert.True(t, v.broken())
	assert.Equal(t, uint64(1), v.blocksAboveBoundary)
	assert.Equal(t, []uint64{96336220}, v.blocksAboveListed)
	assert.Equal(t, "#96336220", v.formatOffendingBlocks())
}

func TestSegmentBoundaryValidator_UpperBoundaryIsExclusive(t *testing.T) {
	v := newSegmentBoundaryValidator("0000000100", 100, 100)

	v.add(199)
	assert.False(t, v.broken(), "#199 is the last block of bundle [100, 200)")

	v.add(200)
	assert.True(t, v.broken(), "#200 belongs to the next bundle")
}

func TestSegmentBoundaryValidator_BiggerBundleSize(t *testing.T) {
	v := newSegmentBoundaryValidator("0000001000", 1000, 1000)

	v.add(1999)
	assert.False(t, v.broken())

	v.add(2000)
	assert.True(t, v.broken())
}

func TestSegmentBoundaryValidator_OutOfOrder(t *testing.T) {
	v := newSegmentBoundaryValidator("0000000100", 100, 100)

	v.add(100)
	v.add(102)
	v.add(101)
	v.add(101) // equal numbers are legitimate, a block can be repeated

	assert.Equal(t, uint64(1), v.outOfOrderCount)
	assert.False(t, v.broken(), "an ordering issue alone does not make the file unreadable")
}

func TestSegmentBoundaryValidator_FormatOffendingBlocksIsCapped(t *testing.T) {
	v := newSegmentBoundaryValidator("0000000100", 100, 100)

	// a file merged at a bigger bundle size holds the next bundles too
	for i := uint64(100); i < 300; i++ {
		v.add(i)
	}

	assert.True(t, v.broken())
	assert.Equal(t, uint64(100), v.blocksAboveBoundary)
	assert.Equal(t, "#200, #201, #202, #203, #204 and 95 more", v.formatOffendingBlocks())
}

func TestCheckMergedBlocks_ValidateBlocksCatchesBloatedBundle(t *testing.T) {
	tmp := t.TempDir()
	storeURL := "file://" + filepath.Join(tmp, "merged-blocks")

	store, err := dstore.NewDBinStore(storeURL)
	require.NoError(t, err)

	// 0000000200 holds a block of a later bundle: no hole in the names, unreadable file
	writeCheckMergedFile(t, store, "0000000100", 100, 199, nil)
	writeCheckMergedFile(t, store, "0000000200", 200, 299, []uint64{320})
	writeCheckMergedFile(t, store, "0000000300", 300, 399, nil)

	holesOnly := runCheckMergedBlocks(t, storeURL, 100, types.NewClosedRange(100, 400), MergedBlocksCheckOptions{})
	assert.Contains(t, holesOnly, "🆗 No hole found")
	assert.Contains(t, holesOnly, "--validate-blocks", "the summary must say the content was not looked at")
	assert.NotContains(t, holesOnly, "0000000200 holds")

	validated := runCheckMergedBlocks(t, storeURL, 100, types.NewClosedRange(100, 400), MergedBlocksCheckOptions{ValidateBlocks: true})
	assert.Contains(t, validated, "🆗 No hole found")
	assert.Contains(t, validated, "Merged blocks file 0000000200 holds 1 block(s) beyond its bundle boundary [#200, #299] (#320)")
	assert.Contains(t, validated, "1 merged-blocks file(s) are either unreadable or hold blocks beyond their own bundle boundaries")
	assert.Contains(t, validated, "[0000000200]")
}

func TestCheckMergedBlocks_ValidateBlocksOnHealthyStore(t *testing.T) {
	tmp := t.TempDir()
	storeURL := "file://" + filepath.Join(tmp, "merged-blocks")

	store, err := dstore.NewDBinStore(storeURL)
	require.NoError(t, err)

	writeCheckMergedFile(t, store, "0000000100", 100, 199, nil)
	// starting at 199 is what the merger does, it must not be reported
	writeCheckMergedFile(t, store, "0000000200", 199, 299, nil)

	out := runCheckMergedBlocks(t, storeURL, 100, types.NewClosedRange(100, 300), MergedBlocksCheckOptions{ValidateBlocks: true})
	assert.Contains(t, out, "🆗 No hole found")
	assert.Contains(t, out, "🆗 All files readable, no block beyond its bundle boundaries")
	assert.NotContains(t, out, "beyond its bundle boundary")
}

// runCheckMergedBlocks runs the checker on a local store and returns its stdout.
func runCheckMergedBlocks(t *testing.T, storeURL string, bundleSize uint64, blockRange types.BlockRange, options MergedBlocksCheckOptions) string {
	t.Helper()

	stdout := os.Stdout
	read, write, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = write

	captured := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, read)
		captured <- buf.String()
	}()

	runErr := CheckMergedBlocksWithOptions(context.Background(), &firecore.Chain[*pbbstream.Block]{}, zap.NewNop(), storeURL, bundleSize, blockRange, options)

	require.NoError(t, write.Close())
	os.Stdout = stdout
	require.NoError(t, runErr)

	return <-captured
}

// writeCheckMergedFile writes blocks [from, upTo] plus any extraneous ones, which is how
// a bloated bundle looks.
func writeCheckMergedFile(t *testing.T, store dstore.Store, name string, from, upTo uint64, extraneous []uint64) {
	t.Helper()

	var buf bytes.Buffer
	writer, err := bstream.NewDBinBlockWriter(&buf)
	require.NoError(t, err)

	for i := from; i <= upTo; i++ {
		require.NoError(t, writer.Write(newCheckTestBlock(i)))
	}
	for _, num := range extraneous {
		require.NoError(t, writer.Write(newCheckTestBlock(num)))
	}

	require.NoError(t, store.WriteObject(context.Background(), name, bytes.NewReader(buf.Bytes())))
}

func newCheckTestBlock(num uint64) *pbbstream.Block {
	return &pbbstream.Block{
		Number:   num,
		Id:       fmt.Sprintf("%08x", num),
		ParentId: fmt.Sprintf("%08x", num-1),
		LibNum:   num - 1,
		Payload:  &anypb.Any{TypeUrl: "type.googleapis.com/sf.test.Block"},
	}
}
