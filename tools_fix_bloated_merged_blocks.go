package firecore

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/tools"
	pbbstream "github.com/streamingfast/pbgo/sf/bstream/v1"
	"go.uber.org/zap"
)

func newToolsFixBloatedMergedBlocks[B Block](chain *Chain[B], zlog *zap.Logger) *cobra.Command {
	return &cobra.Command{
		Use:   "fix-bloated-merged-blocks <src_merged_blocks_store> <dest_one_blocks_store> [<block_range>]",
		Short: "Fixes 'corrupted' merged-blocks that contain extraneous or duplicate blocks. Some older versions of the merger may have produce such bloated merged-blocks. All merged-blocks files in given range will be rewritten, regardless of if they were corrupted.",
		Args:  cobra.ExactArgs(3),
		RunE:  runFixBloatedMergedBlocksE(zlog),
	}
}

func runFixBloatedMergedBlocksE(zlog *zap.Logger) CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		srcStore, err := dstore.NewDBinStore(args[0])
		if err != nil {
			return fmt.Errorf("unable to create source store: %w", err)
		}

		destStore, err := dstore.NewDBinStore(args[1])
		if err != nil {
			return fmt.Errorf("unable to create destination store: %w", err)
		}

		blockRange, err := tools.GetBlockRangeFromArg(args[2])
		if err != nil {
			return fmt.Errorf("parsing block range: %w", err)
		}

		err = srcStore.Walk(ctx, WalkBlockPrefix(blockRange, 100), func(filename string) error {
			zlog.Debug("checking merged block file", zap.String("filename", filename))

			startBlock := mustParseUint64(filename)

			if startBlock > uint64(blockRange.GetStopBlockOr(MaxUint64)) {
				zlog.Debug("skipping merged block file", zap.String("reason", "past stop block"), zap.String("filename", filename))
				return dstore.StopIteration
			}

			if startBlock+100 < uint64(blockRange.Start) {
				zlog.Debug("skipping merged block file", zap.String("reason", "before start block"), zap.String("filename", filename))
				return nil
			}

			rc, err := srcStore.OpenObject(ctx, filename)
			if err != nil {
				return fmt.Errorf("failed to open %s: %w", filename, err)
			}
			defer rc.Close()

			br, err := bstream.NewDBinBlockReader(rc)
			if err != nil {
				return fmt.Errorf("creating block reader: %w", err)
			}

			mergeWriter := &mergedBlocksWriter{
				store:      destStore,
				tweakBlock: func(b *pbbstream.Block) (*pbbstream.Block, error) { return b, nil },
				logger:     zlog,
			}

			seen := make(map[string]bool)

			var lastBlockID string
			var lastBlockNum uint64

			// iterate through the blocks in the file
			for {
				block, err := br.Read()
				if err == io.EOF {
					break
				}

				if block.Number < uint64(startBlock) {
					continue
				}

				if block.Number > uint64(blockRange.GetStopBlockOr(MaxUint64)) {
					break
				}

				if seen[block.Id] {
					zlog.Info("skipping seen block (source merged-blocks had duplicates, skipping)", zap.String("id", block.Id), zap.Uint64("num", block.Number))
					continue
				}

				if lastBlockID != "" && block.ParentId != lastBlockID {
					return fmt.Errorf("got an invalid sequence of blocks: block %q has previousId %s, previous block %d had ID %q, this endpoint is serving blocks out of order", block.String(), block.ParentId, lastBlockNum, lastBlockID)
				}
				lastBlockID = block.Id
				lastBlockNum = block.Number

				seen[block.Id] = true

				if err := mergeWriter.ProcessBlock(block, nil); err != nil {
					return fmt.Errorf("write to blockwriter: %w", err)
				}
			}

			return nil
		})

		if err == io.EOF {
			return nil
		}

		if err != nil {
			return err
		}

		return nil
	}
}
