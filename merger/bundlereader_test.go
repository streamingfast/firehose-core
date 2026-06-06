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
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func inMemoryOpener(_ context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(obf.MemoizeData)), nil
}

func TestStreamingBundleReader_ReadSimpleFiles(t *testing.T) {
	for _, validate := range []bool{false, true} {
		t.Run(fmt.Sprintf("validate_%t", validate), func(t *testing.T) {
			bundle := NewTestBundle(t)

			r, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], inMemoryOpener, validate)
			require.NoError(t, err)

			all, err := ioutil.ReadAll(r)
			require.NoError(t, err)

			// Expected: DBIN header once, then each block's bytes (without their individual headers)
			headerLen := testDBinHeaderLen(t, bundle[0].MemoizeData)
			expected := append([]byte{}, bundle[0].MemoizeData...)
			expected = append(expected, bundle[1].MemoizeData[headerLen:]...)
			expected = append(expected, bundle[2].MemoizeData[headerLen:]...)
			require.Equal(t, expected, all)
		})
	}
}

func TestStreamingBundleReader_OpenerErrorOnHeader(t *testing.T) {
	bundle := NewTestBundle(t)

	opener := func(_ context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
		return nil, fmt.Errorf("storage unavailable")
	}

	_, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], opener, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "storage unavailable")
}

func TestStreamingBundleReader_OpenerErrorOnBlock(t *testing.T) {
	bundle := NewTestBundle(t)
	callCount := 0

	opener := func(_ context.Context, obf *bstream.OneBlockFile) (io.ReadCloser, error) {
		callCount++
		if callCount == 1 {
			// succeed for anyOneBlockFile (header read)
			return io.NopCloser(bytes.NewReader(obf.MemoizeData)), nil
		}
		return nil, fmt.Errorf("block download failed")
	}

	r, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], opener, false)
	require.NoError(t, err)

	_, err = ioutil.ReadAll(r)
	require.Error(t, err)
	require.Contains(t, err.Error(), "block download failed")
}

func TestStreamingBundleReader_ContextCancellation(t *testing.T) {
	bundle := NewTestBundle(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r, err := NewStreamingBundleReader(ctx, testLogger, bundle, bundle[0], inMemoryOpener, false)
	require.NoError(t, err)

	_, err = ioutil.ReadAll(r)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

func TestStreamingBundleReader_CorruptPayload(t *testing.T) {
	bundle := NewTestBundle(t)

	// 0xff starts a field with wire type 7, which does not exist: this payload is
	// invalid protobuf wire-format while the block envelope around it stays valid,
	// mirroring a corrupted block produced by a faulty reader node
	bundle[1] = newTestOneBlockFile(t, "o2", 2, []byte{0xff, 0xff, 0xff})

	r, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], inMemoryOpener, true)
	require.NoError(t, err)

	_, err = ioutil.ReadAll(r)
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to merge corrupted one_block_file o2")
	require.Contains(t, err.Error(), "block #2")
	require.Contains(t, err.Error(), "invalid wire-format")
}

func TestStreamingBundleReader_CorruptPayloadPassesWithoutValidation(t *testing.T) {
	// documents the default behavior: without validation, the merger streams
	// one-block files as-is, corrupted or not
	bundle := NewTestBundle(t)
	bundle[1] = newTestOneBlockFile(t, "o2", 2, []byte{0xff, 0xff, 0xff})

	r, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], inMemoryOpener, false)
	require.NoError(t, err)

	_, err = ioutil.ReadAll(r)
	require.NoError(t, err)
}

func TestStreamingBundleReader_EmptyPayload(t *testing.T) {
	bundle := NewTestBundle(t)
	bundle[2] = newTestOneBlockFile(t, "o3", 3, nil)

	r, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], inMemoryOpener, true)
	require.NoError(t, err)

	_, err = ioutil.ReadAll(r)
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to merge corrupted one_block_file o3")
	require.Contains(t, err.Error(), "empty payload")
}

func TestStreamingBundleReader_TruncatedFile(t *testing.T) {
	bundle := NewTestBundle(t)

	truncated := newTestOneBlockFile(t, "o2", 2, []byte{0x08, 0x02})
	truncated.MemoizeData = truncated.MemoizeData[:len(truncated.MemoizeData)-3]
	bundle[1] = truncated

	r, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], inMemoryOpener, true)
	require.NoError(t, err)

	_, err = ioutil.ReadAll(r)
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to merge corrupted one_block_file o2")
}

func TestStreamingBundleReader_RealBlockFiles(t *testing.T) {
	for _, validate := range []bool{false, true} {
		t.Run(fmt.Sprintf("validate_%t", validate), func(t *testing.T) {
			bundle := []*bstream.OneBlockFile{
				NewTestOneBlockFileFromFile(t, "0000000001-20150730T152628.0-13406cb6-b1cb8fa3.dbin"),
				NewTestOneBlockFileFromFile(t, "0000000002-20150730T152657.0-044698c9-13406cb6.dbin"),
				NewTestOneBlockFileFromFile(t, "0000000003-20150730T152728.0-a88cf741-044698c9.dbin"),
			}

			r, err := NewStreamingBundleReader(context.Background(), testLogger, bundle, bundle[0], inMemoryOpener, validate)
			require.NoError(t, err)
			data, err := ioutil.ReadAll(r)
			require.NoError(t, err)
			require.NotEmpty(t, data)
		})
	}
}

func TestValidateOneBlockFile(t *testing.T) {
	valid := newTestOneBlockFile(t, "o1", 1, []byte{0x08, 0x01})
	require.NoError(t, validateOneBlockFile(valid.MemoizeData))

	corrupted := newTestOneBlockFile(t, "o1", 1, []byte{0xff, 0xff, 0xff})
	err := validateOneBlockFile(corrupted.MemoizeData)
	require.Error(t, err)
	require.Contains(t, err.Error(), "payload")

	garbage := append([]byte{}, valid.MemoizeData...)
	garbage = append(garbage, 0x42)
	err = validateOneBlockFile(garbage)
	require.Error(t, err)
	require.Contains(t, err.Error(), "trailing data")
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

// newTestOneBlockFile builds a valid one-block-file: a DBIN-framed pbbstream.Block whose
// payload contains the given protobuf wire-format bytes
func newTestOneBlockFile(t *testing.T, name string, num uint64, payload []byte) *bstream.OneBlockFile {
	t.Helper()

	parentNum := uint64(0)
	if num > 0 {
		parentNum = num - 1
	}

	buf := bytes.NewBuffer(nil)
	writer, err := bstream.NewDBinBlockWriter(buf)
	require.NoError(t, err)
	require.NoError(t, writer.Write(&pbbstream.Block{
		Id:        fmt.Sprintf("%08x", num),
		Number:    num,
		ParentId:  fmt.Sprintf("%08x", parentNum),
		ParentNum: parentNum,
		LibNum:    parentNum,
		Timestamp: timestamppb.New(time.Date(2015, 7, 30, 15, 26, 28, 0, time.UTC)),
		Payload: &anypb.Any{
			TypeUrl: "type.googleapis.com/sf.test.type.v1.Block",
			Value:   payload,
		},
	}))

	return &bstream.OneBlockFile{
		CanonicalName: name,
		Filenames:     map[string]bool{name: true},
		Num:           num,
		MemoizeData:   buf.Bytes(),
	}
}

func testDBinHeaderLen(t *testing.T, data []byte) int {
	t.Helper()
	reader, err := bstream.NewDBinBlockReader(bytes.NewReader(data))
	require.NoError(t, err)
	return len(reader.Header.RawBytes)
}

func NewTestBundle(t *testing.T) []*bstream.OneBlockFile {
	t.Helper()
	return []*bstream.OneBlockFile{
		newTestOneBlockFile(t, "o1", 1, []byte{0x08, 0x01}),
		newTestOneBlockFile(t, "o2", 2, []byte{0x08, 0x02}),
		newTestOneBlockFile(t, "o3", 3, []byte{0x08, 0x03}),
	}
}
