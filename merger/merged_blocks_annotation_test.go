package merger

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// gsMockStore is a mock store that presents itself as Google Cloud Storage, the only backend
// the merger annotates merged-blocks files on.
type gsMockStore struct {
	*dstore.MockStore
}

func (s *gsMockStore) BaseURL() *url.URL {
	return &url.URL{Scheme: "gs", Host: "example-bucket", Path: "/merged"}
}

func newGSMockStore(writeFunc func(base string, f io.Reader) error) *gsMockStore {
	return &gsMockStore{MockStore: dstore.NewMockStore(writeFunc)}
}

// timedBlockReader returns a one-block file holding a block of the given number and time, with a
// payload far larger than the head the timestamp is read from.
func timedBlockReader(t *testing.T, number uint64, blockTime time.Time) io.ReadCloser {
	t.Helper()

	out := new(bytes.Buffer)
	writer, err := bstream.NewDBinBlockWriter(out)
	require.NoError(t, err)

	payload, err := anypb.New(&test.Block{Number: number})
	require.NoError(t, err)
	payload.Value = bytes.Repeat([]byte("p"), 4096)

	require.NoError(t, writer.Write(&pbbstream.Block{
		Number:    number,
		Id:        "00000000000000000000000000000000000000000000000000000000000000ff",
		ParentId:  "00000000000000000000000000000000000000000000000000000000000000fe",
		ParentNum: number - 1,
		Timestamp: timestamppb.New(blockTime),
		Payload:   payload,
	}))
	return io.NopCloser(out)
}

func TestMergerIO_AnnotatesMergedBlocksOnGS(t *testing.T) {
	blockTime := time.Date(2025, 10, 12, 10, 23, 12, 0, time.UTC)

	oneBlockStore := dstore.NewMockStore(nil)
	oneBlockStore.OpenObjectFunc = func(_ context.Context, name string) (io.ReadCloser, error) {
		return timedBlockReader(t, 100, blockTime), nil
	}

	var written int64
	mergedBlocksStore := newGSMockStore(func(base string, f io.Reader) error {
		size, err := io.Copy(io.Discard, f)
		written = size
		return err
	})

	var annotated string
	var metadata map[string]string
	mergedBlocksStore.SetMetadataFunc = func(_ context.Context, base string, m map[string]string) error {
		annotated, metadata = base, m
		return nil
	}

	mio := NewDStoreIO(testLogger, oneBlockStore, mergedBlocksStore, nil, 0, 0, 100, 0)
	require.NoError(t, mio.MergeAndStore(context.Background(), 100, []*bstream.OneBlockFile{block100(), block101()}))

	require.NotNil(t, metadata, "the merged-blocks file should have been annotated")
	assert.Equal(t, "0000000100", annotated)

	assert.Equal(t, "2", metadata[firecore.MergedBlocksItemCountMetadataKey])
	assert.Equal(t, "2025-10-12 10:23:12", metadata[firecore.MergedBlocksTimestampMetadataKey])
	// The size recorded is the bundle as written, before the store compresses it.
	assert.Equal(t, humanInt(written), metadata[firecore.MergedBlocksDataSizeMetadataKey])
}

// Every other backend keeps no metadata a listing can read back, so nothing is written there.
func TestMergerIO_DoesNotAnnotateOffGS(t *testing.T) {
	oneBlockStore := dstore.NewMockStore(nil)
	oneBlockStore.OpenObjectFunc = func(_ context.Context, name string) (io.ReadCloser, error) {
		return timedBlockReader(t, 100, time.Now()), nil
	}

	mergedBlocksStore := dstore.NewMockStore(func(base string, f io.Reader) error {
		_, err := io.Copy(io.Discard, f)
		return err
	})

	var annotations int
	mergedBlocksStore.SetMetadataFunc = func(context.Context, string, map[string]string) error {
		annotations++
		return nil
	}

	mio := NewDStoreIO(testLogger, oneBlockStore, mergedBlocksStore, nil, 0, 0, 100, 0)
	require.NoError(t, mio.MergeAndStore(context.Background(), 100, []*bstream.OneBlockFile{block100(), block101()}))

	assert.Zero(t, annotations)
}

// A merge whose annotation cannot be written still succeeds: the bundle is there and correct,
// only its description is missing, and the annotate tool fills that back in.
func TestMergerIO_AnnotationFailureDoesNotFailMerge(t *testing.T) {
	oneBlockStore := dstore.NewMockStore(nil)
	oneBlockStore.OpenObjectFunc = func(_ context.Context, name string) (io.ReadCloser, error) {
		return timedBlockReader(t, 100, time.Now()), nil
	}

	mergedBlocksStore := newGSMockStore(func(base string, f io.Reader) error {
		_, err := io.Copy(io.Discard, f)
		return err
	})
	mergedBlocksStore.SetMetadataFunc = func(context.Context, string, map[string]string) error {
		return assert.AnError
	}

	mio := NewDStoreIO(testLogger, oneBlockStore, mergedBlocksStore, nil, 0, 0, 100, 0)
	require.NoError(t, mio.MergeAndStore(context.Background(), 100, []*bstream.OneBlockFile{block100(), block101()}))
}

func humanInt(value int64) string {
	return firecore.MergedBlocksMetadata(value, 0, time.Time{})[firecore.MergedBlocksDataSizeMetadataKey]
}
