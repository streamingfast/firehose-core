package mindreader

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/streamingfast/quic-block-transport/blockinfo"
	quicblockserver "github.com/streamingfast/quic-block-transport/quic"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"go.uber.org/zap"
)

// Compile-time check that BlockSourceAdapter implements quic.BlockSource.
var _ quicblockserver.S2CompressedBlockSource = (*BlockSourceAdapter)(nil)

// BlockSourceAdapter adapts a channel of pbbstream.Block into the
// quic block server BlockSource interface so the quic block server
// can stream blocks produced by the mindreader.
type BlockSourceAdapter struct {
	blocks chan *pbbstream.Block

	closeOnce sync.Once
	done      chan struct{}
	logger    *zap.Logger
}

func NewBlockSourceAdapter(capacity int, logger *zap.Logger) *BlockSourceAdapter {
	return &BlockSourceAdapter{
		blocks: make(chan *pbbstream.Block, capacity),
		done:   make(chan struct{}),
		logger: logger.Named("block_source_adapter"),
	}
}

// Push sends a block to the adapter. It is non-blocking if the channel
// has capacity; otherwise it blocks until a consumer calls Next.
func (a *BlockSourceAdapter) Push(block *pbbstream.Block) {
	select {
	case a.blocks <- block:
	case <-a.done:
	}
}

// Close signals that no more blocks will be pushed.
func (a *BlockSourceAdapter) Close() {
	a.closeOnce.Do(func() {
		close(a.done)
		close(a.blocks)
	})
}

// Next implements server.BlockSource. It returns the next block's info
// and a reader for its payload. Returns io.EOF when the adapter is closed
// and all buffered blocks have been consumed.
func (a *BlockSourceAdapter) Next(ctx context.Context) (*blockinfo.BlockInfo, *quicblockserver.S2CompressedData, error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case block, ok := <-a.blocks:
		if !ok {
			return nil, nil, io.EOF
		}

		var payload []byte
		if block.Payload != nil {
			payload = block.Payload.Value
		} else {
			payload = block.PayloadBuffer
		}

		compressedData, err := quicblockserver.CompressData(&quicblockserver.UncompressedData{Bytes: payload})
		if err != nil {
			return nil, nil, fmt.Errorf("compressing block: %w", err)
		}

		info := &blockinfo.BlockInfo{
			Number:      block.Number,
			ID:          block.Id,
			ParentID:    block.ParentId,
			ParentNum:   block.ParentNum,
			LibNum:      block.LibNum,
			Timestamp:   block.Timestamp.AsTime(),
			PayloadSize: uint64(len(payload)),
		}

		a.logger.Debug("next returning", zap.Uint64("block_number", info.Number), zap.Uint64("payload_size", info.PayloadSize))

		return info, compressedData, nil
	}
}
