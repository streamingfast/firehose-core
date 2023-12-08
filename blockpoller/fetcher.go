package blockpoller

import (
	"context"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
)

type BlockFetcher interface {
	Fetch(ctx context.Context, blkNum uint64) (*pbbstream.Block, error)
}
