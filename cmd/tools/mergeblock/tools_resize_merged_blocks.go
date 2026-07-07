package mergeblock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/stream"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"go.uber.org/zap"
)

func NewToolsResizeMergedBlocksCmd[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resize-merged-blocks <source> <destination> <start> <stop>",
		Short: "Rewrites a merged-blocks store to a new store with a different number of blocks per file (ex: 100 -> 1000)",
		Long: cli.Dedent(`
			Reads merged-blocks files of --source-bundle-size blocks from <source> and rewrites
			them as merged-blocks files of --target-bundle-size blocks into <destination>.

			<start> must be aligned on a --target-bundle-size boundary (or be the first streamable
			block of the chain) and <stop> is exclusive and must be aligned on a --target-bundle-size
			boundary, so only complete bundles are ever written. The source store must contain
			the <stop> block: the process completes upon reading it.

			Example: rewrite 100-blocks files into 1000-blocks files:

			  firecore tools resize-merged-blocks ./merged-100 ./merged-1000 0 5000 --target-bundle-size=1000
		`),
		Args: cobra.ExactArgs(4),
		RunE: createResizeMergedBlocksE(rootLog),
	}

	cmd.Flags().Uint64("source-bundle-size", 100, "Number of blocks per merged-blocks file in the source store")
	cmd.Flags().Uint64("target-bundle-size", 0, "Number of blocks per merged-blocks file to write to the destination store (required)")
	cmd.Flags().Uint64("first-streamable-block", 0, "First streamable block of the chain, used to allow a <start> below the first target boundary")

	return cmd
}

func createResizeMergedBlocksE(rootLog *zap.Logger) firecore.CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		sourceStore, err := dstore.NewDBinStore(args[0])
		if err != nil {
			return fmt.Errorf("reading source store: %w", err)
		}

		destStore, err := dstore.NewStore(args[1], "dbin.zst", "zstd", true)
		if err != nil {
			return fmt.Errorf("reading destination store: %w", err)
		}

		start, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing start block num: %w", err)
		}
		stop, err := strconv.ParseUint(args[3], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing stop block num: %w", err)
		}

		sourceSize := sflags.MustGetUint64(cmd, "source-bundle-size")
		targetSize := sflags.MustGetUint64(cmd, "target-bundle-size")
		firstStreamableBlock := sflags.MustGetUint64(cmd, "first-streamable-block")
		bstream.GetProtocolFirstStreamableBlock = firstStreamableBlock

		if err := firecore.ValidateMergedBlocksBundleSize(sourceSize); err != nil {
			return fmt.Errorf("invalid source-bundle-size: %w", err)
		}
		if err := firecore.ValidateMergedBlocksBundleSize(targetSize); err != nil {
			return fmt.Errorf("invalid target-bundle-size: %w", err)
		}
		if sourceSize == targetSize {
			return fmt.Errorf("source-bundle-size and target-bundle-size are both %d, nothing to do", sourceSize)
		}
		if targetSize%sourceSize != 0 && sourceSize%targetSize != 0 {
			return fmt.Errorf("bundle sizes must divide evenly (source %d, target %d)", sourceSize, targetSize)
		}
		if start%targetSize != 0 && start != firstStreamableBlock {
			return fmt.Errorf("start block %d must be aligned on a target-bundle-size boundary (multiple of %d) or be the first streamable block (%d)", start, targetSize, firstStreamableBlock)
		}
		if stop%targetSize != 0 {
			return fmt.Errorf("stop block %d must be aligned on a target-bundle-size boundary (multiple of %d)", stop, targetSize)
		}
		if stop <= start {
			return fmt.Errorf("stop block %d must be above start block %d", stop, start)
		}

		if exists, err := destStore.FileExists(cmd.Context(), fmt.Sprintf("%010d", firecore.LowBoundaryFor(start, targetSize))); err == nil && exists {
			rootLog.Warn("destination store already contains the first bundle to write, existing files may be overwritten")
		}

		rootLog.Info("starting merged-blocks resize process",
			zap.Uint64("start", start),
			zap.Uint64("stop", stop),
			zap.Uint64("source_bundle_size", sourceSize),
			zap.Uint64("target_bundle_size", targetSize),
			zap.String("source", args[0]),
			zap.String("dest", args[1]),
		)

		writer := &firecore.MergedBlocksWriter{
			Cmd:          cmd,
			Store:        destStore,
			LowBlockNum:  firecore.LowBoundaryFor(start, targetSize),
			BundleSize:   targetSize,
			StopBlockNum: stop,
			Logger:       rootLog,
		}

		sourceStream := stream.New(nil, sourceStore, nil, int64(start), writer,
			stream.WithFinalBlocksOnly(),
			stream.WithMergedBlocksBundleSize(sourceSize),
		)

		err = sourceStream.Run(context.Background())
		if errors.Is(err, io.EOF) {
			rootLog.Info("complete")
			return nil
		}
		return err
	}
}
