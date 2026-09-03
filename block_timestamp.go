package firecore

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// BlockTimestampPeekSize is how many bytes of a serialized pbbstream.Block are enough to reach
// its timestamp. The fields before it are number (varint, ~2 bytes), id and parent_id (strings,
// ~34 bytes each), so the timestamp always lands well within this, long before the payload.
// Reading only this much keeps a block of any size out of memory.
const BlockTimestampPeekSize = 512

// ExtractBlockTimestamp scans the head of a serialized pbbstream.Block field by field, returning
// as soon as it reaches field 4, the timestamp. The bytes after it, the chain-specific payload
// included, never need to be read.
func ExtractBlockTimestamp(data []byte) (time.Time, error) {
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
	return time.Time{}, fmt.Errorf("timestamp field not found in first %d bytes of block", BlockTimestampPeekSize)
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
