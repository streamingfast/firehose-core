package tools

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	"github.com/streamingfast/dstore"
	"go.uber.org/zap"
)

// FlagMergedBlocks will write a list of base-block-numbers to a store, for merged-blocks-files that are broken or missing
// broken merged-blocks-files are the ones that contain "empty" blocks (no ID) or unlinkable blocks
// there could be false positives on unlinkable blocks, though
// output files are like this: 0000123100.broken  0000123500.missing
func FlagMergedBlocks(
	ctx context.Context,
	logger *zap.Logger,
	sourceStoreURL string,
	destStoreURL string,
	fileBlockSize uint32,
	blockRange BlockRange,
) error {
	if !blockRange.IsResolved() {
		return fmt.Errorf("check merged blocks can only work with fully resolved range, got %s", blockRange)
	}

	expected := uint64(RoundToBundleStartBlock(uint32(blockRange.Start), fileBlockSize))
	fileBlockSize64 := uint64(fileBlockSize)

	blocksStore, err := dstore.NewDBinStore(sourceStoreURL)
	if err != nil {
		return err
	}
	destStore, err := dstore.NewSimpleStore(destStoreURL)
	if err != nil {
		return err
	}

	var firstFilename = fmt.Sprintf("%010d", RoundToBundleStartBlock(uint32(blockRange.Start), fileBlockSize))

	logger.Debug("walking merged blocks", zap.Stringer("block_range", blockRange), zap.String("first_filename", firstFilename))
	err = blocksStore.WalkFrom(ctx, "", firstFilename, func(filename string) error {
		if strings.HasSuffix(filename, ".tmp") {
			logger.Debug("skipping unknown tmp file", zap.String("filename", filename))
			return nil
		}
		match := numberRegex.FindStringSubmatch(filename)
		if match == nil {
			logger.Debug("skipping unknown file", zap.String("filename", filename))
			return nil
		}

		logger.Debug("received merged blocks", zap.String("filename", filename))

		// should not happen with firstFilename, but leaving in case
		baseNum, _ := strconv.ParseUint(match[1], 10, 32)
		if baseNum+uint64(fileBlockSize)-1 < uint64(blockRange.Start) {
			logger.Debug("base num lower than block range start, quitting", zap.Uint64("base_num", baseNum), zap.Int64("starting_at", blockRange.Start))
			return nil
		}

		if baseNum < uint64(expected) {
			return fmt.Errorf("unhandled error: found base number %d below expected %d", baseNum, expected)
		}
		for expected < baseNum {
			outputFile := fmt.Sprintf("%010d.missing", expected)
			logger.Info("found missing file, writing to store", zap.String("output_file", outputFile))
			destStore.WriteObject(ctx, outputFile, strings.NewReader(""))
			expected += fileBlockSize64
		}

		broken, err := checkMergedBlockFileBroken(ctx, blocksStore, filename)
		if broken {
			outputFile := fmt.Sprintf("%010d.broken", baseNum)
			logger.Info("found broken file, writing to store", zap.String("output_file", outputFile))
			destStore.WriteObject(ctx, outputFile, strings.NewReader(""))
		}
		if err != nil {
			return err
		}

		if !blockRange.IsClosed() && RoundToBundleEndBlock(uint32(baseNum), fileBlockSize) >= uint32(*blockRange.Stop-1) {
			return errStopWalk
		}
		expected = baseNum + fileBlockSize64

		return nil
	})
	if err != nil && err != errStopWalk {
		return err
	}
	return nil
}

func checkMergedBlockFileBroken(
	ctx context.Context,
	store dstore.Store,
	filename string,
) (broken bool, err error) {

	tfdb := &trackedForkDB{
		fdb: forkable.NewForkDB(),
	}

	reader, err := store.OpenObject(ctx, filename)
	if err != nil {
		return true, err
	}
	defer reader.Close()

	readerFactory, err := bstream.GetBlockReaderFactory.New(reader)
	if err != nil {
		return true, err
	}

	for {
		var block *bstream.Block
		block, err = readerFactory.Read()

		if block == nil {
			if err == io.EOF {
				err = nil
			}
			return
		}
		if err != nil {
			return
		}

		if block.Id == "" {
			broken = true
			return
		}

		if !tfdb.fdb.HasLIB() {
			tfdb.fdb.AddLink(block.AsRef(), block.PreviousID(), nil)
			tfdb.fdb.InitLIB(block)
			continue
		}

		tfdb.fdb.AddLink(block.AsRef(), block.PreviousID(), nil)
		revSeg, _ := tfdb.fdb.ReversibleSegment(block.AsRef())
		if revSeg == nil {
			broken = true
			return
		}
	}

}
