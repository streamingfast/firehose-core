package blkpoller

import (
	"context"
	"time"

	pbbstream "github.com/streamingfast/bstream/types/pb/sf/bstream/v1"
)

type BlockFetcher interface {
	PollingInterval() time.Duration
	Fetch(ctx context.Context, blkNum uint64) (*pbbstream.Block, error)
}
