package merger

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/streamingfast/bstream"
	firecore "github.com/streamingfast/firehose-core"
)

// readBlockTimestamp opens a one-block-file and extracts the block timestamp
// by reading only as far as the timestamp field of its first block.
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

	limit := msgLen
	if limit > firecore.BlockTimestampPeekSize {
		limit = firecore.BlockTimestampPeekSize
	}
	buf := make([]byte, limit)
	if _, err := io.ReadFull(r, buf); err != nil {
		return time.Time{}, fmt.Errorf("reading block message bytes: %w", err)
	}

	return firecore.ExtractBlockTimestamp(buf)
}
