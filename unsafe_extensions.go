package firecore

import (
	"context"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dlauncher/launcher"
	"go.uber.org/zap"
)

var UnsafePayloadKind pbbstream.Protocol = pbbstream.Protocol_UNKNOWN
var UnsafeJsonBytesEncoder = "hex"

// UnsafeResolveReaderNodeStartBlock is a function that resolved the reader node start block num, by default it simply
// returns the value of the 'reader-node-start-block-num'. However, the function may be overwritten in certain chains
// to perform a more complex resolution logic.
var UnsafeResolveReaderNodeStartBlock = func(ctx context.Context, startBlockNum uint64, firstStreamableBlock uint64, runtime *launcher.Runtime, rootLog *zap.Logger) (uint64, error) {
	return startBlockNum, nil
}
