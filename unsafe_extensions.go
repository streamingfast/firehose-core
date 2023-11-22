package firecore

import (
	"context"

	"github.com/spf13/cobra"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dlauncher/launcher"
	"go.uber.org/zap"
)

var UnsafePayloadKind pbbstream.Protocol = pbbstream.Protocol_UNKNOWN
var UnsafeJsonBytesEncoder = "hex"

// UnsafeResolveReaderNodeStartBlock is a function that resolved the reader node start block num, by default it simply
// returns the value of the 'reader-node-start-block-num'. However, the function may be overwritten in certain chains
// to perform a more complex resolution logic.
var UnsafeResolveReaderNodeStartBlock = func(ctx context.Context, command *cobra.Command, runtime *launcher.Runtime, rootLog *zap.Logger) (uint64, error) {
	return sflags.MustGetUint64(command, "reader-node-start-block-num"), nil
}
