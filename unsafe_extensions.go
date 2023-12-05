package firecore

import (
	"context"

	"github.com/streamingfast/dlauncher/launcher"
	"go.uber.org/zap"
)

// UnsafeResolveReaderNodeStartBlock is a function that resolved the reader node start block num, by default it simply
// returns the value of the 'reader-node-start-block-num'. However, the function may be overwritten in certain chains
// to perform a more complex resolution logic.
var UnsafeResolveReaderNodeStartBlock = func(ctx context.Context, startBlockNum uint64, firstStreamableBlock uint64, runtime *launcher.Runtime, rootLog *zap.Logger) (uint64, error) {
	return startBlockNum, nil
}
