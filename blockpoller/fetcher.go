package blockpoller

import (
	"context"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
)

type BlockFetcher[C any] interface {
	IsBlockAvailable(requestedSlot uint64) bool
	Fetch(ctx context.Context, client C, blkNum uint64) (b *pbbstream.Block, skipped bool, err error)
}

// HeadBlockNumberFetcher is optionally implemented by a BlockFetcher. When it is, the
// poller warns on startup if the block it starts from is above the chain's head block.
type HeadBlockNumberFetcher[C any] interface {
	FetchHeadBlockNumber(ctx context.Context, client C) (uint64, error)
}
