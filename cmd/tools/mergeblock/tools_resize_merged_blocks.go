package mergeblock

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
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"go.uber.org/zap"
)

func NewToolsResizeMergedBlocksCmd[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resize-merged-blocks <source> <destination> <start> <stop>",
		Short: "Rewrites a merged-blocks store to a new store with a different number of blocks per file (ex: 100 -> 1000) or different compression settings",
		Long: cli.Dedent(`
			Reads merged-blocks files of --source-bundle-size blocks from <source> and rewrites
			them as merged-blocks files of --target-bundle-size blocks into <destination>.

			<start> must be aligned on a --target-bundle-size boundary (or be the first streamable
			block of the chain) and <stop> is exclusive and must be aligned on a --target-bundle-size
			boundary, so only complete bundles are ever written. The source store must contain
			the <stop> block: the process completes upon reading it.

			With --fix-misplaced-blocks, the source files are read in order without checking which
			blocks they hold, and every block is written to the file of its own number. Use it on a
			store where a file holds blocks outside of its range (ex: blocks written to the empty
			files of a gap in block numbers). In that mode:

			  - a block at or below the last block written is skipped (blocks carried into the
			    empty files of a gap, duplicates);
			  - a block above the last block written must have it as parent, the process fails
			    otherwise. A store holding forked blocks cannot be read in that mode;
			  - each block gets the last block written as parent number;
			  - the files before the one of <start> are read too while they hold blocks at or
			    above <start>.

			Files in the legacy block format (no 'payload') are read when the
			ACCEPT_LEGACY_BLOCK_FORMAT environment variable is set. The deprecated 'payload_kind'
			and 'payload_buffer' fields are cleared from every block written.

			Example: rewrite 100-blocks files into 1000-blocks files:

			  firecore tools resize-merged-blocks ./merged-100 ./merged-1000 0 5000 --target-bundle-size=1000

			When --source-bundle-size and --target-bundle-size are equal, files are rewritten as-is,
			which is useful to change their compression with the 'compression_config' query parameter
			of <destination>. This asks for confirmation unless --force is passed.

			Example: recompress 100-blocks files with zstd 'best', keeping the bundle size:

			  firecore tools resize-merged-blocks ./merged "./merged-best?compression_config=best" 0 5000 --target-bundle-size=100 --force
		`),
		Args: cobra.ExactArgs(4),
		RunE: createResizeMergedBlocksE(rootLog),
	}

	cmd.Flags().Uint64("source-bundle-size", 100, "Number of blocks per merged-blocks file in the source store")
	cmd.Flags().Uint64("target-bundle-size", 0, "Number of blocks per merged-blocks file to write to the destination store (required)")
	cmd.Flags().Uint64("first-streamable-block", 0, "First streamable block of the chain, used to allow a <start> below the first target boundary")
	cmd.Flags().Bool("force", false, "Skip the confirmation prompt when --source-bundle-size and --target-bundle-size are equal")
	cmd.Flags().Bool("fix-misplaced-blocks", false, "Write each block to the file of its own number whatever source file holds it, skip blocks at or below the last block written and fail on a block that does not link to it")

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
		if sourceSize == targetSize && !sflags.MustGetBool(cmd, "force") {
			message := fmt.Sprintf("source-bundle-size and target-bundle-size are both %d, files will be rewritten as-is (ex: to change compression). Continue?", sourceSize)
			confirmed, answered := cli.PromptConfirm(message)
			if !answered {
				return fmt.Errorf("source-bundle-size and target-bundle-size are both %d, pass --force to rewrite files as-is when not running in a terminal", sourceSize)
			}
			if !confirmed {
				rootLog.Info("skipped by user")
				return nil
			}
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
			TweakBlock:   clearLegacyPayloadFields,
		}

		if sflags.MustGetBool(cmd, "fix-misplaced-blocks") {
			return resizeFixingMisplacedBlocks(cmd.Context(), sourceStore, writer, start, sourceSize, rootLog)
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

// clearLegacyPayloadFields clears the deprecated payload_kind and payload_buffer fields,
// the block data is in payload.
func clearLegacyPayloadFields(blk *pbbstream.Block) (*pbbstream.Block, error) {
	if len(blk.PayloadBuffer) > 0 && (blk.Payload == nil || len(blk.Payload.Value) == 0) {
		return nil, fmt.Errorf("block %s has its data in payload_buffer only", blk.AsRef())
	}
	blk.PayloadKind = pbbstream.Protocol_UNKNOWN
	blk.PayloadBuffer = nil
	return blk, nil
}

// resizeFixingMisplacedBlocks reads the source files in order and hands their blocks to the
// writer, which writes each one to the file of its own number. Blocks at or below the last
// block handed are skipped, and a block above it must have it as parent. It returns once the
// writer has seen the stop block.
func resizeFixingMisplacedBlocks(ctx context.Context, sourceStore dstore.Store, writer *firecore.MergedBlocksWriter, start, sourceSize uint64, logger *zap.Logger) error {
	var last *pbbstream.Block
	var movedCount, skippedCount uint64

	firstFileLowBlockNum, err := firstSourceFileHolding(ctx, sourceStore, start, sourceSize)
	if err != nil {
		return err
	}
	if firstFileLowBlockNum != firecore.LowBoundaryFor(start, sourceSize) {
		logger.Info("reading source files before the start block, they hold blocks at or above it", zap.Uint64("first_file", firstFileLowBlockNum))
	}

	err = sourceStore.WalkFrom(ctx, "", fmt.Sprintf("%010d", firstFileLowBlockNum), func(filename string) error {
		fileLowBlockNum, err := strconv.ParseUint(filename, 10, 64)
		if err != nil {
			return fmt.Errorf("parsing merged-blocks file name %q: %w", filename, err)
		}

		reader, err := sourceStore.OpenObject(ctx, filename)
		if err != nil {
			return fmt.Errorf("opening merged-blocks file %s: %w", filename, err)
		}
		defer reader.Close()

		blockReader, err := bstream.NewDBinBlockReader(reader)
		if err != nil {
			return fmt.Errorf("reading merged-blocks file %s: %w", filename, err)
		}

		for {
			blk, err := blockReader.Read()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("reading block from merged-blocks file %s: %w", filename, err)
			}

			if blk.Number < start {
				continue
			}

			if last != nil {
				if blk.Number <= last.Number {
					logger.Debug("skipping block at or below the last block written", zap.String("filename", filename), zap.Stringer("block", blk.AsRef()), zap.Stringer("last_block", last.AsRef()))
					skippedCount++
					continue
				}

				if blk.ParentId != last.Id {
					return fmt.Errorf("block %s in merged-blocks file %s does not link to the last block written %s: its parent is #%d (%s)", blk.AsRef(), filename, last.AsRef(), blk.ParentNum, blk.ParentId)
				}

				// Blocks in the legacy format are read with a parent number of their number
				// minus one, which is wrong across skipped heights.
				blk.ParentNum = last.Number
			}

			if firecore.LowBoundaryFor(blk.Number, sourceSize) != fileLowBlockNum {
				logger.Info("moving misplaced block to the file of its number", zap.String("filename", filename), zap.Stringer("block", blk.AsRef()))
				movedCount++
			}

			if err := writer.ProcessBlock(blk, nil); err != nil {
				return err
			}
			last = blk
		}
	})

	if errors.Is(err, io.EOF) {
		logger.Info("complete", zap.Uint64("misplaced_blocks_moved", movedCount), zap.Uint64("blocks_skipped", skippedCount))
		return nil
	}
	if err != nil {
		return err
	}

	return fmt.Errorf("source store ended before the stop block %d", writer.StopBlockNum)
}

// firstSourceFileHolding returns the lowest block number of the first source file to read for
// blocks at or above start. Starting from the file of start, it goes back one file at a time
// while the previous file holds a block at or above start, as the files of a gap in block
// numbers can.
func firstSourceFileHolding(ctx context.Context, sourceStore dstore.Store, start, sourceSize uint64) (uint64, error) {
	low := firecore.LowBoundaryFor(start, sourceSize)
	for low >= sourceSize {
		previous := low - sourceSize
		filename := fmt.Sprintf("%010d", previous)

		exists, err := sourceStore.FileExists(ctx, filename)
		if err != nil {
			return 0, fmt.Errorf("checking merged-blocks file %s: %w", filename, err)
		}
		if !exists {
			return low, nil
		}

		highest, err := highestBlockNumIn(ctx, sourceStore, filename)
		if err != nil {
			return 0, err
		}
		if highest < start {
			return low, nil
		}
		low = previous
	}
	return low, nil
}

func highestBlockNumIn(ctx context.Context, sourceStore dstore.Store, filename string) (uint64, error) {
	reader, err := sourceStore.OpenObject(ctx, filename)
	if err != nil {
		return 0, fmt.Errorf("opening merged-blocks file %s: %w", filename, err)
	}
	defer reader.Close()

	blockReader, err := bstream.NewDBinBlockReader(reader)
	if err != nil {
		return 0, fmt.Errorf("reading merged-blocks file %s: %w", filename, err)
	}

	var highest uint64
	for {
		blk, err := blockReader.Read()
		if err == io.EOF {
			return highest, nil
		}
		if err != nil {
			return 0, fmt.Errorf("reading block from merged-blocks file %s: %w", filename, err)
		}
		highest = max(highest, blk.Number)
	}
}
