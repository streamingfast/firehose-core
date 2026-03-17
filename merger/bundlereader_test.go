package merger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"path"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/stretchr/testify/require"
)

func inMemoryOpener(_ context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(obf.MemoizeData)), nil
}

func TestStreamingBundleReader_ReadSimpleFiles(t *testing.T) {
	bundle := NewTestBundle()

	r, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], inMemoryOpener)
	require.NoError(t, err)

	all, err := ioutil.ReadAll(r)
	require.NoError(t, err)

	// Expected: DBIN header once, then each block's payload (without their individual headers)
	expected := append(testOneBlockHeader, 0x1, 0x2, 0x3, 0x4, 0x5, 0x6)
	require.Equal(t, expected, all)
}

func TestStreamingBundleReader_OpenerErrorOnHeader(t *testing.T) {
	bundle := NewTestBundle()

	opener := func(_ context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
		return nil, fmt.Errorf("storage unavailable")
	}

	_, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], opener)
	require.Error(t, err)
	require.Contains(t, err.Error(), "storage unavailable")
}

func TestStreamingBundleReader_OpenerErrorOnBlock(t *testing.T) {
	bundle := NewTestBundle()
	callCount := 0

	opener := func(_ context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
		callCount++
		if callCount == 1 {
			// succeed for anyOneBlockFile (header read)
			return io.NopCloser(bytes.NewReader(obf.MemoizeData)), nil
		}
		return nil, fmt.Errorf("block download failed")
	}

	r, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], opener)
	require.NoError(t, err)

	_, err = ioutil.ReadAll(r)
	require.Error(t, err)
	require.Contains(t, err.Error(), "block download failed")
}

func TestStreamingBundleReader_ContextCancellation(t *testing.T) {
	bundle := NewTestBundle()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r, err := NewStreamingBundleReader(ctx, testLogger, bundle, bundle[0], inMemoryOpener)
	require.NoError(t, err)

	_, err = ioutil.ReadAll(r)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func TestStreamingBundleReader_RealBlockFiles(t *testing.T) {
	bundle := []*bstream.OneBlockFile{
		NewTestOneBlockFileFromFile(t, "0000000001-20150730T152628.0-13406cb6-b1cb8fa3.dbin"),
		NewTestOneBlockFileFromFile(t, "0000000002-20150730T152657.0-044698c9-13406cb6.dbin"),
		NewTestOneBlockFileFromFile(t, "0000000003-20150730T152728.0-a88cf741-044698c9.dbin"),
	}

	r, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], inMemoryOpener)
	require.NoError(t, err)
	data, err := ioutil.ReadAll(r)
	require.NoError(t, err)
	require.NotEmpty(t, data)
}

func NewTestOneBlockFileFromFile(t *testing.T, fileName string) *bstream.OneBlockFile {
	t.Helper()
	data, err := ioutil.ReadFile(path.Join("test_data", fileName))
	require.NoError(t, err)
	time.Sleep(1 * time.Millisecond)
	return &bstream.OneBlockFile{
		CanonicalName: fileName,
		Filenames:     map[string]bool{fileName: true},
		ID:            "",
		Num:           0,
		PreviousID:    "",
		MemoizeData:   data,
	}
}

var testOneBlockHeader = []byte("dbin\x00tes\x00\x00")

func NewTestBundle() []*bstream.OneBlockFile {
	o1 := &bstream.OneBlockFile{
		CanonicalName: "o1",
		MemoizeData:   append(testOneBlockHeader, []byte{0x1, 0x2}...),
	}
	o2 := &bstream.OneBlockFile{
		CanonicalName: "o2",
		MemoizeData:   append(testOneBlockHeader, []byte{0x3, 0x4}...),
	}
	o3 := &bstream.OneBlockFile{
		CanonicalName: "o3",
		MemoizeData:   append(testOneBlockHeader, []byte{0x5, 0x6}...),
	}
	return []*bstream.OneBlockFile{o1, o2, o3}
}
