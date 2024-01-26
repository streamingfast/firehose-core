package blockmeta

import (
	"context"
	"fmt"

	"github.com/streamingfast/logging"
	"go.uber.org/zap"
)

func GetStartBlock(ctx context.Context, blockMetaStoreURL string, logger *zap.Logger, tracer logging.Tracer) (uint64, error) {
	store, err := NewStore(blockMetaStoreURL, logger, tracer)
	if err != nil {
		return 0, fmt.Errorf("unable to create block meta store: %w", err)
	}

	startBlock, err := store.GetLastWrittenBlockNum(ctx)
	if err != nil {
		return 0, fmt.Errorf("unable to get start block from store: %w", err)
	}

	return startBlock, nil
}
