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

	blocks []*pbbstream.Block
	Logger *zap.Logger
	Cmd    *cobra.Command

	TweakBlock func(*pbbstream.Block) (*pbbstream.Block, error)
}

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

	if w.LowBlockNum == 0 && blk.Number >= bundleSize { // initial block
		if blk.Number%bundleSize != 0 && blk.Number != bstream.GetProtocolFirstStreamableBlock {
			return fmt.Errorf("received unexpected block %s (not a boundary, not the first streamable block %d)", blk, bstream.GetProtocolFirstStreamableBlock)
		}
		w.LowBlockNum = LowBoundaryFor(blk.Number, bundleSize)
		w.Logger.Debug("setting initial boundary to %d upon seeing block %s", zap.Uint64("low_boundary", w.LowBlockNum), zap.Uint64("blk_num", blk.Number))
	}

	if blk.Number > w.LowBlockNum+bundleSize-1 {
		w.Logger.Debug("bundling because we saw block %s from next bundle (%d was not seen, it must not exist on this chain)", zap.Uint64("blk_num", blk.Number), zap.Uint64("last_bundle_block", w.LowBlockNum+bundleSize-1))
		if err := w.WriteBundle(); err != nil {
			return err
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
