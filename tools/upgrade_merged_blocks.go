package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/streamingfast/logging"
	"go.uber.org/zap"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/stream"
	"github.com/streamingfast/dstore"
)

var GetMergedBlocksUpgrader = func(zlog *zap.Logger, tracer logging.Tracer, tweakFunc func(cmd *cobra.Command, block *bstream.Block) (*bstream.Block, error)) *cobra.Command {
	out := &cobra.Command{
		Use:   "upgrade-merged-blocks <source> <destination> <start> <stop>",
		Short: "from a merged-blocks source, rewrite blocks to a new merged-blocks destination, while applying all possible upgrades",
		Args:  cobra.ExactArgs(4),
		RunE:  getMergedBlockUpgrader(zlog, tracer, tweakFunc),
	}

	return out
}

func getMergedBlockUpgrader(zlog *zap.Logger, tracer logging.Tracer, tweakFunc func(cmd *cobra.Command, block *bstream.Block) (*bstream.Block, error)) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		source := args[0]
		sourceStore, err := dstore.NewDBinStore(source)
		if err != nil {
			return fmt.Errorf("reading source store: %w", err)
		}

		dest := args[1]
		destStore, err := dstore.NewStore(dest, "dbin.zst", "zstd", true)
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

		zlog.Info("starting block upgrader process", zap.Uint64("start", start), zap.Uint64("stop", stop), zap.String("source", source), zap.String("dest", dest))
		writer := &mergedBlocksWriter{
			cmd:           cmd,
			store:         destStore,
			lowBlockNum:   lowBoundary(start),
			stopBlockNum:  stop,
			writerFactory: bstream.GetBlockWriterFactory,
			tweakBlock:    tweakFunc,
			logger:        zlog,
		}
		stream := stream.New(nil, sourceStore, nil, int64(start), writer, stream.WithFinalBlocksOnly())

		err = stream.Run(context.Background())
		if errors.Is(err, io.EOF) {
			zlog.Info("Complete!")
			return nil
		}
		return err
	}
}
