package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/bstream/stream"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"go.uber.org/zap"
)

func NewToolsUpgradeMergedBlocksCmd[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade-merged-blocks <source> <destination> <range>",
		Short: "From a merged-blocks source, rewrite blocks to a new merged-blocks destination, while applying all possible upgrades",
		Args:  cobra.ExactArgs(4),
		RunE:  getMergedBlockUpgrader(chain.Tools.MergedBlockUpgrader, rootLog),
	}
}

func getMergedBlockUpgrader(tweakFunc func(block *pbbstream.Block) (*pbbstream.Block, error), rootLog *zap.Logger) func(cmd *cobra.Command, args []string) error {
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

		rootLog.Info("starting block upgrader process", zap.Uint64("start", start), zap.Uint64("stop", stop), zap.String("source", source), zap.String("dest", dest))
		writer := &mergedBlocksWriter{
			cmd:          cmd,
			store:        destStore,
			lowBlockNum:  lowBoundary(start),
			stopBlockNum: stop,
			tweakBlock:   tweakFunc,
		}
		stream := stream.New(nil, sourceStore, nil, int64(start), writer, stream.WithFinalBlocksOnly())

		err = stream.Run(context.Background())
		if errors.Is(err, io.EOF) {
			rootLog.Info("Complete!")
			return nil
		}
		return err
	}
}

type mergedBlocksWriter struct {
	store        dstore.Store
	lowBlockNum  uint64
	stopBlockNum uint64

	blocks []*pbbstream.Block
	logger *zap.Logger
	cmd    *cobra.Command

	tweakBlock func(*pbbstream.Block) (*pbbstream.Block, error)
}

func (w *mergedBlocksWriter) ProcessBlock(blk *pbbstream.Block, obj interface{}) error {
	if w.tweakBlock != nil {
		b, err := w.tweakBlock(blk)
		if err != nil {
			return fmt.Errorf("tweaking block: %w", err)
		}
		blk = b
	}

	if w.lowBlockNum == 0 && blk.Number > 99 { // initial block
		if blk.Number%100 != 0 && blk.Number != bstream.GetProtocolFirstStreamableBlock {
			return fmt.Errorf("received unexpected block %s (not a boundary, not the first streamable block %d)", blk, bstream.GetProtocolFirstStreamableBlock)
		}
		w.lowBlockNum = lowBoundary(blk.Number)
		w.logger.Debug("setting initial boundary to %d upon seeing block %s", zap.Uint64("low_boundary", w.lowBlockNum), zap.Stringer("blk", blk))
	}

	if blk.Number > w.lowBlockNum+99 {
		w.logger.Debug("bundling because we saw block %s from next bundle (%d was not seen, it must not exist on this chain)", zap.Stringer("blk", blk), zap.Uint64("last_bundle_block", w.lowBlockNum+99))
		if err := w.writeBundle(); err != nil {
			return err
		}
	}

	if w.stopBlockNum > 0 && blk.Number >= w.stopBlockNum {
		return io.EOF
	}

	w.blocks = append(w.blocks, blk)

	if blk.Number == w.lowBlockNum+99 {
		w.logger.Debug("bundling on last bundle block", zap.Uint64("last_bundle_block", w.lowBlockNum+99))
		if err := w.writeBundle(); err != nil {
			return err
		}
		return nil
	}

	return nil
}

func filename(num uint64) string {
	return fmt.Sprintf("%010d", num)
}

func (w *mergedBlocksWriter) writeBundle() error {
	file := filename(w.lowBlockNum)
	w.logger.Info("writing merged file to store (suffix: .dbin.zst)", zap.String("filename", file), zap.Uint64("lowBlockNum", w.lowBlockNum))

	if len(w.blocks) == 0 {
		return fmt.Errorf("no blocks to write to bundle")
	}

	pr, pw := io.Pipe()

	go func() {
		var err error
		defer func() {
			pw.CloseWithError(err)
		}()

		blockWriter, err := bstream.NewDBinBlockWriter(pw)
		if err != nil {
			return
		}

		for _, blk := range w.blocks {
			err = blockWriter.Write(blk)
			if err != nil {
				return
			}
		}
	}()

	err := w.store.WriteObject(context.Background(), file, pr)
	if err != nil {
		w.logger.Error("writing to store", zap.Error(err))
	}

	w.lowBlockNum += 100
	w.blocks = nil

	return err
}

func lowBoundary(i uint64) uint64 {
	return i - (i % 100)
}
