package firecore

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	"go.uber.org/zap"
)

type MergedBlocksWriter struct {
	Store        dstore.Store
	LowBlockNum  uint64
	StopBlockNum uint64

	// BundleSize is the number of blocks per merged-blocks file. When 0,
	// bstream.DefaultMergedBlocksBundleSize applies.
	BundleSize uint64

	blocks         []*pbbstream.Block
	lastBlock      *pbbstream.Block // last block of the previously written bundle, carried into empty gap bundles
	processedCount uint64
	Logger         *zap.Logger
	Cmd            *cobra.Command

	TweakBlock func(*pbbstream.Block) (*pbbstream.Block, error)
}

// progressLogInterval is how often ProcessBlock emits a progress log, so a long
// run (e.g. resize-merged-blocks over a big range) does not look stuck between
// bundle writes.
const progressLogInterval = 100_000

func (w *MergedBlocksWriter) bundleSize() uint64 {
	if w.BundleSize != 0 {
		return w.BundleSize
	}
	return bstream.DefaultMergedBlocksBundleSize
}

func (w *MergedBlocksWriter) ProcessBlock(blk *pbbstream.Block, obj interface{}) error {
	if w.TweakBlock != nil {
		b, err := w.TweakBlock(blk)
		if err != nil {
			return fmt.Errorf("tweaking block: %w", err)
		}
		blk = b
	}

	bundleSize := w.bundleSize()

	w.processedCount++
	if w.processedCount%progressLogInterval == 0 {
		w.Logger.Info("processing blocks",
			zap.Uint64("processed_count", w.processedCount),
			zap.Uint64("current_block", blk.Number),
			zap.Uint64("current_bundle_low_block", w.LowBlockNum),
		)
	}

	if w.LowBlockNum == 0 && len(w.blocks) == 0 && blk.Number >= bundleSize { // initial block
		if blk.Number%bundleSize != 0 && blk.Number != bstream.GetProtocolFirstStreamableBlock {
			return fmt.Errorf("received unexpected block %s (not a boundary, not the first streamable block %d)", blk, bstream.GetProtocolFirstStreamableBlock)
		}
		w.LowBlockNum = LowBoundaryFor(blk.Number, bundleSize)
		w.Logger.Debug("setting initial boundary to %d upon seeing block %s", zap.Uint64("low_boundary", w.LowBlockNum), zap.Uint64("blk_num", blk.Number))
	}

	if blk.Number > w.LowBlockNum+bundleSize-1 {
		w.Logger.Debug("bundling because we saw block %s from next bundle (%d was not seen, it must not exist on this chain)", zap.Uint64("blk_num", blk.Number), zap.Uint64("last_bundle_block", w.LowBlockNum+bundleSize-1))
		if len(w.blocks) > 0 {
			if err := w.WriteBundle(); err != nil {
				return err
			}
		}

		// A gap can span more than one bundle (skipped blocks on some chains).
		// Merged-blocks files must stay contiguous by boundary: filesource walks
		// boundaries one bundle at a time and stalls (or errors) on a missing
		// file, so we cannot just jump the boundary. Instead, mirror the
		// production merger (merger/bundler.go) and write one file per skipped
		// boundary carrying the previous bundle's last block. That block is below
		// the file's base num, so filesource skips it while still seeing a
		// non-empty, readable file at every boundary.
		for blk.Number > w.LowBlockNum+bundleSize-1 {
			if w.lastBlock == nil {
				// Gap before any bundle was written (nothing to carry): jump the
				// boundary. In practice unreachable, the initial-boundary logic
				// above already snaps LowBlockNum onto the first block's window.
				w.LowBlockNum = LowBoundaryFor(blk.Number, bundleSize)
				break
			}
			w.Logger.Debug("writing empty gap bundle, those blocks must not exist on this chain", zap.Uint64("low_block_num", w.LowBlockNum), zap.Uint64("carry_block", w.lastBlock.Number))
			w.blocks = []*pbbstream.Block{w.lastBlock}
			if err := w.WriteBundle(); err != nil {
				return err
			}
		}
	}

	if w.StopBlockNum > 0 && blk.Number >= w.StopBlockNum {
		return io.EOF
	}

	w.blocks = append(w.blocks, blk)

	if blk.Number == w.LowBlockNum+bundleSize-1 {
		w.Logger.Debug("bundling on last bundle block", zap.Uint64("last_bundle_block", w.LowBlockNum+bundleSize-1))
		if err := w.WriteBundle(); err != nil {
			return err
		}
		return nil
	}

	return nil
}

// Flush writes any pending blocks as a (possibly partial) bundle. Unlike
// WriteBundle, it is a no-op when no blocks are pending.
func (w *MergedBlocksWriter) Flush() error {
	if len(w.blocks) == 0 {
		return nil
	}
	return w.WriteBundle()
}

func (w *MergedBlocksWriter) WriteBundle() error {
	file := filename(w.LowBlockNum)
	w.Logger.Info("writing merged file to store (suffix: .dbin.zst)", zap.String("filename", file), zap.Uint64("lowBlockNum", w.LowBlockNum))

	if len(w.blocks) == 0 {
		return fmt.Errorf("no blocks to write to bundle")
	}

	pr, pw := io.Pipe()
	done := make(chan struct{})

	go func() {
		var err error
		defer close(done)
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

	err := w.Store.WriteObject(context.Background(), file, pr)
	if err != nil {
		w.Logger.Error("writing to store", zap.Error(err))
	}

	// If the store returned without consuming the reader (e.g. an S3 store
	// skipping an already-existing object), the goroutine above would block
	// forever on its next write to the pipe, pinning the bundle's blocks in
	// memory. Closing the read end unblocks it; we then wait for it to
	// finish so the captured slice is released before we return.
	pr.Close()
	<-done

	w.LowBlockNum += w.bundleSize()
	w.lastBlock = w.blocks[len(w.blocks)-1]
	w.blocks = nil

	return err
}
func filename(num uint64) string {
	return fmt.Sprintf("%010d", num)
}

// LowBoundary returns the base block number of the merged-blocks file that
// contains block i, using bstream.DefaultMergedBlocksBundleSize.
func LowBoundary(i uint64) uint64 {
	return LowBoundaryFor(i, bstream.DefaultMergedBlocksBundleSize)
}

// LowBoundaryFor returns the base block number of the merged-blocks file that
// contains block i for a given bundle size.
func LowBoundaryFor(i uint64, bundleSize uint64) uint64 {
	return i - (i % bundleSize)
}

// ValidateMergedBlocksBundleSize returns an error unless size is a positive
// multiple of 100. Keeping every bundle boundary on a 100-block boundary means
// legacy 100-blocks files can always be converted to a bigger size by pure
// concatenation, and block index sizes (100, 1000, 10000, ...) stay aligned.
func ValidateMergedBlocksBundleSize(size uint64) error {
	if size == 0 || size%100 != 0 {
		return fmt.Errorf("invalid merged-blocks bundle size %d: must be a positive multiple of 100", size)
	}
	return nil
}

// GetMergedBlocksBundleSizeFlag reads and validates the shared
// "merged-blocks-bundle-size" tools flag. Every tool reading that flag should go
// through this so an invalid value (0 or not a multiple of 100) is rejected
// up-front instead of panicking on a modulo/divide deeper in the process.
func GetMergedBlocksBundleSizeFlag(cmd *cobra.Command) (uint64, error) {
	size, err := cmd.Flags().GetUint64("merged-blocks-bundle-size")
	if err != nil {
		return 0, err
	}
	if err := ValidateMergedBlocksBundleSize(size); err != nil {
		return 0, err
	}
	return size, nil
}
