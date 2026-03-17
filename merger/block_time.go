package merger

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/streamingfast/bstream"
	"google.golang.org/protobuf/encoding/protowire"
)

// readBlockTimestamp opens a one-block-file and extracts the block timestamp
// by reading at most 512 bytes past the DBIN header.
func readBlockTimestamp(ctx context.Context, obf *bstream.OneBlockFile, opener func(context.Context, *bstream.OneBlockFile) (io.ReadCloser, error)) (time.Time, error) {
	r, err := opener(ctx, obf)
	if err != nil {
		return time.Time{}, err
	}
	if r == nil {
		return time.Time{}, fmt.Errorf("opener returned nil reader for block %d", obf.Num)
	}
	defer r.Close()

	// NewDBinBlockReader reads the DBIN header without buffering, leaving r
	// positioned at the start of the first length-prefixed protobuf message.
	if _, err := bstream.NewDBinBlockReader(r); err != nil {
		return time.Time{}, fmt.Errorf("reading dbin header: %w", err)
	}

	// DBIN message format: [4-byte big-endian uint32 length][protobuf bytes]
	var lengthBuf [4]byte
	if _, err := io.ReadFull(r, lengthBuf[:]); err != nil {
		return time.Time{}, fmt.Errorf("reading block message length: %w", err)
	}
	msgLen := int(binary.BigEndian.Uint32(lengthBuf[:]))

	// Read only enough bytes to reach field 4 (timestamp). Fields 1-3 are
	// number (varint ~2B), id (string ~34B) and parent_id (string ~34B), so
	// 512 bytes is more than enough to reach the timestamp before field 11 (payload).
	limit := msgLen
	if limit > 512 {
		limit = 512
	}
	buf := make([]byte, limit)
	if _, err := io.ReadFull(r, buf); err != nil {
		return time.Time{}, fmt.Errorf("reading block message bytes: %w", err)
	}

	return extractTimestampFromProto(buf)
}

// extractTimestampFromProto scans a partial pbbstream.Block protobuf payload
// field by field, returning as soon as field 4 (google.protobuf.Timestamp) is found.
func extractTimestampFromProto(data []byte) (time.Time, error) {
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return time.Time{}, protowire.ParseError(n)
		}
		data = data[n:]

		if num == 4 && typ == protowire.BytesType { // field 4 = timestamp (embedded message)
			tsBytes, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return time.Time{}, protowire.ParseError(n)
			}
			return decodeProtoTimestamp(tsBytes)
		}

		n = protowire.ConsumeFieldValue(num, typ, data)
		if n < 0 {
			return time.Time{}, protowire.ParseError(n)
		}
		data = data[n:]
	}
	return time.Time{}, fmt.Errorf("timestamp field not found in first 512 bytes of block")
}

// decodeProtoTimestamp decodes a serialized google.protobuf.Timestamp (seconds=1, nanos=2).
func decodeProtoTimestamp(data []byte) (time.Time, error) {
	var seconds int64
	var nanos int32
	for len(data) > 0 {
		num, typ, n := protowire.ConsumeTag(data)
		if n < 0 {
			return time.Time{}, protowire.ParseError(n)
		}
		data = data[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return time.Time{}, protowire.ParseError(n)
			}
			seconds = int64(v)
			data = data[n:]
		case num == 2 && typ == protowire.VarintType:
			v, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return time.Time{}, protowire.ParseError(n)
			}
			nanos = int32(v)
			data = data[n:]
		default:
			n = protowire.ConsumeFieldValue(num, typ, data)
			if n < 0 {
				return time.Time{}, protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return time.Unix(seconds, int64(nanos)).UTC(), nil
}
