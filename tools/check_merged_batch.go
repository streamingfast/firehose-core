package tools

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dstore"
)

type blockRef struct {
	hash string
	num  uint64
}

func (b *blockRef) reset() {
	b.hash = ""
	b.num = 0
}

func (b *blockRef) set(hash string, num uint64) {
	b.hash = hash
	b.num = num
}

func (b *blockRef) isUnset() bool {
	return b.hash == "" && b.num == 0
}

// CheckMergedBlocksBatch will write a list of base-block-numbers to a store, for merged-blocks-files that are broken or missing
// broken merged-blocks-files are the ones that contain "empty" blocks (no ID) or unlinkable blocks
// there could be false positives on unlinkable blocks, though
// output files are like this: 0000123100.broken  0000123500.missing
func CheckMergedBlocksBatch(
	ctx context.Context,
	sourceStoreURL string,
	destStoreURL string,
	fileBlockSize uint64,
	blockRange BlockRange,
) error {
	if !blockRange.IsResolved() {
		return fmt.Errorf("check merged blocks can only work with fully resolved range, got %s", blockRange)
	}

	expected := RoundToBundleStartBlock(uint64(blockRange.Start), fileBlockSize)
	fileBlockSize64 := uint64(fileBlockSize)

	blocksStore, err := dstore.NewDBinStore(sourceStoreURL)
	if err != nil {
		return err
	}
	destStore, err := dstore.NewSimpleStore(destStoreURL)
	if err != nil {
		return err
	}

	var firstFilename = fmt.Sprintf("%010d", RoundToBundleStartBlock(uint64(blockRange.Start), fileBlockSize))

	lastSeenBlock := &blockRef{}

	err = blocksStore.WalkFrom(ctx, "", firstFilename, func(filename string) error {
		if strings.HasSuffix(filename, ".tmp") {
			return nil
		}
		match := numberRegex.FindStringSubmatch(filename)
		if match == nil {
			return nil
		}

		// should not happen with firstFilename, but leaving in case
		baseNum, _ := strconv.ParseUint(match[1], 10, 32)
		if baseNum+uint64(fileBlockSize)-1 < uint64(blockRange.Start) {
			return nil
		}

		if baseNum < uint64(expected) {
			return fmt.Errorf("unhandled error: found base number %d below expected %d", baseNum, expected)
		}
		for expected < baseNum {
			outputFile := fmt.Sprintf("%010d.missing", expected)
			fmt.Printf("found missing file %s, writing to store\n", outputFile)
			destStore.WriteObject(ctx, outputFile, strings.NewReader(""))
			expected += fileBlockSize64
		}

		broken, err := checkMergedBlockFileBroken(ctx, blocksStore, filename, lastSeenBlock)
		if broken {
			brokenSince := RoundToBundleStartBlock(uint64(lastSeenBlock.num+1), 100)
			for i := brokenSince; i <= baseNum; i += fileBlockSize64 {
				outputFile := fmt.Sprintf("%010d.broken", i)
				fmt.Printf("found broken file %s, writing to store\n", outputFile)
				destStore.WriteObject(ctx, outputFile, strings.NewReader(""))
			}
			lastSeenBlock.reset()
		}

		if err != nil {
			return err
		}

		if blockRange.IsClosed() && RoundToBundleEndBlock(baseNum, fileBlockSize) >= *blockRange.Stop-1 {
			return dstore.StopIteration
		}
		expected = baseNum + fileBlockSize64

		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

var printCounter = 0

func checkMergedBlockFileBroken(
	ctx context.Context,
	store dstore.Store,
	filename string,
	lastSeenBlock *blockRef,
) (broken bool, err error) {
	if printCounter%100 == 0 {
		fmt.Println("checking", filename, "... (printing 1/100)")
	}
	printCounter++

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

		if lastSeenBlock.isUnset() {
			fakePreviousNum := block.Number
			if fakePreviousNum != 0 {
				fakePreviousNum -= 1
			}
			lastSeenBlock.set(block.PreviousId, fakePreviousNum)
		}
		if block.PreviousId != lastSeenBlock.hash {
			broken = true
			return
		}
		lastSeenBlock.set(block.Id, block.Number)
	}
}
