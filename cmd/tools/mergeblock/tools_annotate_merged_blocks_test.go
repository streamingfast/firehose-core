package mergeblock

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dbin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// writeMergedBlocksFile builds a dbin stream of blocks whose payloads are payloadSize bytes
// each, one second apart starting at firstBlockTime.
func writeMergedBlocksFile(t *testing.T, blockCount int, payloadSize int, firstBlockTime time.Time) []byte {
	t.Helper()

	var out bytes.Buffer
	writer := dbin.NewWriter(&out)
	require.NoError(t, writer.WriteHeader("type.googleapis.com/sf.test.type.v1.Block"))

	for i := range blockCount {
		block := &pbbstream.Block{
			Number:    uint64(100 + i),
			Id:        "00000000000000000000000000000000000000000000000000000000000000ff",
			ParentId:  "00000000000000000000000000000000000000000000000000000000000000fe",
			ParentNum: uint64(99 + i),
			Timestamp: timestamppb.New(firstBlockTime.Add(time.Duration(i) * time.Second)),
			Payload:   &anypb.Any{TypeUrl: "type.googleapis.com/sf.test.type.v1.Block", Value: bytes.Repeat([]byte("p"), payloadSize)},
		}
		message, err := proto.Marshal(block)
		require.NoError(t, err)
		require.NoError(t, writer.WriteMessage(message))
	}

	return out.Bytes()
}

func TestScanMergedBlocksFile(t *testing.T) {
	firstBlockTime := time.Date(2025, 10, 12, 10, 23, 12, 0, time.UTC)

	tests := []struct {
		name        string
		blockCount  int
		payloadSize int
	}{
		{name: "single small block", blockCount: 1, payloadSize: 16},
		{name: "many small blocks", blockCount: 100, payloadSize: 128},
		// Blocks far larger than the scratch buffer must still be walked without being held.
		{name: "blocks larger than the scratch buffer", blockCount: 3, payloadSize: 3 * scanScratchBufferSize},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := writeMergedBlocksFile(t, test.blockCount, test.payloadSize, firstBlockTime)

			scratch := make([]byte, scanScratchBufferSize)
			scan, err := scanMergedBlocksFile(bytes.NewReader(file), scratch)
			require.NoError(t, err)

			assert.Equal(t, int64(len(file)), scan.dataSize)
			assert.Equal(t, int64(test.blockCount), scan.blockCount)
			assert.Equal(t, firstBlockTime, scan.firstBlockTime)
		})
	}
}

// The scratch buffer is reused across files, so a scan must not depend on what the last one
// left in it.
func TestScanMergedBlocksFileReusesScratch(t *testing.T) {
	scratch := make([]byte, scanScratchBufferSize)

	for i, blockTime := range []time.Time{
		time.Date(2025, 10, 12, 10, 23, 12, 0, time.UTC),
		time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
	} {
		file := writeMergedBlocksFile(t, 5+i, 64, blockTime)

		scan, err := scanMergedBlocksFile(bytes.NewReader(file), scratch)
		require.NoError(t, err)
		assert.Equal(t, int64(len(file)), scan.dataSize)
		assert.Equal(t, int64(5+i), scan.blockCount)
		assert.Equal(t, blockTime, scan.firstBlockTime)
	}
}

func TestScanMergedBlocksFileErrors(t *testing.T) {
	scratch := make([]byte, scanScratchBufferSize)
	firstBlockTime := time.Date(2025, 10, 12, 10, 23, 12, 0, time.UTC)

	t.Run("not a dbin file", func(t *testing.T) {
		_, err := scanMergedBlocksFile(bytes.NewReader([]byte("not a dbin file at all")), scratch)
		require.ErrorContains(t, err, "reading dbin header")
	})

	t.Run("header without a block", func(t *testing.T) {
		var out bytes.Buffer
		writer := dbin.NewWriter(&out)
		require.NoError(t, writer.WriteHeader("type.googleapis.com/sf.test.type.v1.Block"))

		_, err := scanMergedBlocksFile(bytes.NewReader(out.Bytes()), scratch)
		require.ErrorContains(t, err, "file holds no block")
	})

	t.Run("truncated block", func(t *testing.T) {
		file := writeMergedBlocksFile(t, 4, 1024, firstBlockTime)

		_, err := scanMergedBlocksFile(bytes.NewReader(file[:len(file)-512]), scratch)
		require.ErrorContains(t, err, "reading block")
	})

	t.Run("length announcing more than is there", func(t *testing.T) {
		file := writeMergedBlocksFile(t, 1, 64, firstBlockTime)
		// Overstate the length of a second message that is not there at all.
		file = binary.BigEndian.AppendUint32(file, 4096)

		_, err := scanMergedBlocksFile(bytes.NewReader(file), scratch)
		require.ErrorContains(t, err, "reading block 1")
	})
}

func TestIsAnnotated(t *testing.T) {
	complete := map[string]string{
		dataSizeMetadataKey:  "1024",
		itemCountMetadataKey: "100",
		timestampMetadataKey: "2025-10-12 10:23:12",
	}
	assert.True(t, isAnnotated(complete))

	for _, missing := range []string{dataSizeMetadataKey, itemCountMetadataKey, timestampMetadataKey} {
		partial := map[string]string{}
		for key, value := range complete {
			if key != missing {
				partial[key] = value
			}
		}
		assert.False(t, isAnnotated(partial), "should not be annotated without %q", missing)
	}

	assert.False(t, isAnnotated(nil))
}

func TestDiscard(t *testing.T) {
	scratch := make([]byte, 8)
	source := bytes.NewReader(bytes.Repeat([]byte("x"), 100))

	require.NoError(t, discard(source, 60, scratch))
	assert.Equal(t, 40, source.Len())

	// Asking for more than remains reports the short stream rather than succeeding, whether the
	// stream runs out on a chunk boundary (io.EOF) or inside one (io.ErrUnexpectedEOF).
	err := discard(source, 60, scratch)
	require.Error(t, err)
	assert.True(t, errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF), "got %v", err)
}
