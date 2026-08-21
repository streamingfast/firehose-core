package check

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	print2 "github.com/streamingfast/firehose-core/cmd/tools/print"
	"github.com/streamingfast/firehose-core/types"
	"go.uber.org/zap"
)

var numberRegex = regexp.MustCompile(`(\d{10})`)

type PrintDetails uint8

const (
	PrintNoDetails PrintDetails = iota
	PrintStats
	PrintFull
)

// MergedBlocksCheckOptions configures what `tools check merged-blocks` looks at. Reading
// the files is opt-in: a full-chain run would otherwise download the whole dataset.
type MergedBlocksCheckOptions struct {
	// PrintDetails prints each block as it is read. Implies ValidateBlocks.
	PrintDetails PrintDetails

	// ValidateBlocks reads every file and validates its content: bundle boundaries,
	// block ordering and chain linkability.
	ValidateBlocks bool
}

func (o MergedBlocksCheckOptions) readAllBlocks() bool {
	return o.ValidateBlocks || o.PrintDetails != PrintNoDetails
}

// Deprecated: use CheckMergedBlocksWithOptions, which can validate the content of the
// files without printing every block.
func CheckMergedBlocks[B firecore.Block](ctx context.Context, chain *firecore.Chain[B], logger *zap.Logger, storeURL string, fileBlockSize uint64, blockRange types.BlockRange, printDetails PrintDetails) error {
	return CheckMergedBlocksWithOptions(ctx, chain, logger, storeURL, fileBlockSize, blockRange, MergedBlocksCheckOptions{PrintDetails: printDetails})
}

func CheckMergedBlocksWithOptions[B firecore.Block](ctx context.Context, chain *firecore.Chain[B], logger *zap.Logger, storeURL string, fileBlockSize uint64, blockRange types.BlockRange, options MergedBlocksCheckOptions) error {
	readAllBlocks := options.readAllBlocks()
	fmt.Printf("Checking block holes on %s\n", storeURL)
	if readAllBlocks {
		fmt.Println("Content validation requested: all block files will be read, their blocks checked against the bundle boundaries of their file name and for continuity. This may take a while...")
	} else {
		fmt.Println("Only the file names are listed: a file holding blocks outside of its own bundle boundaries would not be seen. Pass --validate-blocks to read and validate the content of every file.")
	}

	var expected uint64
	var count int
	var highestBlockSeen uint64
	var brokenSegments []string
	lowestBlockSeen := firecore.MaxUint64

	holeFound := false
	expected = types.RoundToBundleStartBlock(uint64(blockRange.Start), fileBlockSize)
	currentStartBlk := uint64(blockRange.Start)

	blocksStore, err := dstore.NewDBinStore(storeURL)
	if err != nil {
		return err
	}

	walkPrefix := WalkBlockPrefix(blockRange, fileBlockSize)

	tfdb := &trackedForkDB{
		fdb: forkable.NewForkDB(),
	}

	logger.Debug("walking merged blocks", zap.Stringer("block_range", blockRange), zap.String("walk_prefix", walkPrefix))
	err = blocksStore.Walk(ctx, walkPrefix, func(filename string) error {
		match := numberRegex.FindStringSubmatch(filename)
		if match == nil {
			return nil
		}

		logger.Debug("received merged blocks", zap.String("filename", filename))

		count++
		baseNum, _ := strconv.ParseUint(match[1], 10, 32)
		if baseNum+uint64(fileBlockSize)-1 < uint64(blockRange.Start) {
			logger.Debug("base num lower then block range start, quitting", zap.Uint64("base_num", baseNum), zap.Int64("starting_at", blockRange.Start))
			return nil
		}

		if baseNum != expected {
			// There is no previous valid block range if we are at the ever first seen file
			if count > 1 {
				fmt.Printf("✅ Range %s\n", types.NewClosedRange(int64(currentStartBlk), uint64(types.RoundToBundleEndBlock(expected-fileBlockSize, fileBlockSize))))
			}

			// Otherwise, we do not follow last seen element (previous is `100 - 199` but we are `299 - 300`)
			missingRange := types.NewClosedRange(int64(expected), types.RoundToBundleEndBlock(baseNum-fileBlockSize, fileBlockSize))
			fmt.Printf("❌ Range %s (Missing, [%s])\n", missingRange, missingRange.ReprocRange())
			currentStartBlk = baseNum

			holeFound = true
		}
		expected = baseNum + fileBlockSize

		if readAllBlocks {
			lowestBlockSegment, highestBlockSegment, broken := validateBlockSegment(ctx, chain, blocksStore, filename, baseNum, fileBlockSize, blockRange, options.PrintDetails, tfdb)
			if lowestBlockSegment < lowestBlockSeen {
				lowestBlockSeen = lowestBlockSegment
			}
			if highestBlockSegment > highestBlockSeen {
				highestBlockSeen = highestBlockSegment
			}
			if broken {
				brokenSegments = append(brokenSegments, filename)
			}
		} else {
			if baseNum < lowestBlockSeen {
				lowestBlockSeen = baseNum
			}
			if baseNum+fileBlockSize > highestBlockSeen {
				highestBlockSeen = baseNum + fileBlockSize
			}
		}

		if count%10000 == 0 {
			fmt.Printf("✅ Range %s\n", types.NewClosedRange(int64(currentStartBlk), types.RoundToBundleEndBlock(baseNum, fileBlockSize)))
			currentStartBlk = baseNum + fileBlockSize
		}

		if blockRange.IsClosed() && types.RoundToBundleEndBlock(baseNum, fileBlockSize) >= *blockRange.Stop-1 {
			return dstore.StopIteration
		}

		return nil
	})

	if err != nil {
		return err
	}

	logger.Debug("checking incomplete range",
		zap.Stringer("range", blockRange),
		zap.Bool("range_unbounded", blockRange.IsOpen()),
		zap.Uint64("lowest_block_seen", lowestBlockSeen),
		zap.Uint64("highest_block_seen", highestBlockSeen),
	)
	if tfdb.lastLinkedBlock != nil && tfdb.lastLinkedBlock.Number < highestBlockSeen {
		fmt.Printf("🔶 Range %s has issues with forks, last linkable block number: %d\n", types.NewClosedRange(int64(currentStartBlk), uint64(highestBlockSeen)), tfdb.lastLinkedBlock.Number)
	} else {
		fmt.Printf("✅ Range %s\n", types.NewClosedRange(int64(currentStartBlk), uint64(highestBlockSeen)))
	}

	fmt.Println()
	fmt.Println("Summary:")

	if blockRange.IsClosed() &&
		(highestBlockSeen < uint64(*blockRange.Stop-1) ||
			(lowestBlockSeen > uint64(blockRange.Start) && lowestBlockSeen > bstream.GetProtocolFirstStreamableBlock)) {
		fmt.Printf("> 🔶 Incomplete range %s, started at block %s and stopped at block: %s\n", blockRange, types.PrettyBlockNum(lowestBlockSeen), types.PrettyBlockNum(highestBlockSeen))
	}

	if holeFound {
		fmt.Printf("> 🆘 Holes found!\n")
	} else {
		fmt.Printf("> 🆗 No hole found\n")
	}

	switch {
	case len(brokenSegments) > 0:
		fmt.Printf("> 🆘 %d merged-blocks file(s) are either unreadable or hold blocks beyond their own bundle boundaries: %s\n", len(brokenSegments), formatBrokenSegments(brokenSegments))
		fmt.Println(">    See the ❌ lines above for which is which. Any Firehose or Substreams request covering them fails. You can try `tools fix-bloated-merged-blocks` on the latter.")
	case readAllBlocks:
		fmt.Printf("> 🆗 All files readable, no block beyond its bundle boundaries\n")
	default:
		fmt.Printf("> 🔶 File content not validated, run again with --validate-blocks to catch corrupted bundles\n")
	}

	return nil
}

// formatBrokenSegments lists broken file names, capped so a widely broken store does not
// print thousands of them.
func formatBrokenSegments(segments []string) string {
	const maxListed = 10
	if len(segments) <= maxListed {
		return fmt.Sprintf("%v", segments)
	}

	return fmt.Sprintf("%v and %d more", segments[0:maxListed], len(segments)-maxListed)
}

type trackedForkDB struct {
	fdb                    *forkable.ForkDB
	firstUnlinkableBlock   *pbbstream.Block
	lastLinkedBlock        *pbbstream.Block
	unlinkableSegmentCount int
}

// maxOffendingBlocksListed caps the out-of-bundle block numbers listed per file: a file
// merged at the wrong bundle size holds hundreds of them.
const maxOffendingBlocksListed = 5

// segmentBoundaryValidator checks the block numbers of one merged-blocks file against
// the bundle its name claims: `0096336100` at size 100 may only hold [96336100, 96336200).
// A block at or above the upper boundary is what bstream's FileSource refuses to read at
// serving time ("beyond the configured bundle size"). Blocks below the base num are fine,
// the merger carries the previous bundle's last block over (merger/bundler.go).
type segmentBoundaryValidator struct {
	segment    string
	baseNum    uint64
	bundleSize uint64

	blocksBelowBase     uint64
	blocksAboveBoundary uint64
	blocksAboveListed   []uint64
	outOfOrderCount     uint64
	firstOutOfOrder     uint64

	hasPrevious bool
	previousNum uint64
}

func newSegmentBoundaryValidator(segment string, baseNum, bundleSize uint64) *segmentBoundaryValidator {
	return &segmentBoundaryValidator{segment: segment, baseNum: baseNum, bundleSize: bundleSize}
}

// add validates one block, in the order it was read from the file.
func (v *segmentBoundaryValidator) add(blockNum uint64) {
	if v.hasPrevious && blockNum < v.previousNum {
		v.outOfOrderCount++
		if v.outOfOrderCount == 1 {
			v.firstOutOfOrder = blockNum
		}
	}
	v.hasPrevious = true
	v.previousNum = blockNum

	switch {
	case blockNum < v.baseNum:
		// carried over by the merger, readers skip it
		v.blocksBelowBase++

	case blockNum >= v.baseNum+v.bundleSize:
		v.blocksAboveBoundary++
		if len(v.blocksAboveListed) < maxOffendingBlocksListed {
			v.blocksAboveListed = append(v.blocksAboveListed, blockNum)
		}
	}
}

// broken reports whether the file holds blocks no reader on this bundle size can stream.
func (v *segmentBoundaryValidator) broken() bool {
	return v.blocksAboveBoundary > 0
}

// bundleRange is the range of block numbers the file name allows.
func (v *segmentBoundaryValidator) bundleRange() types.BlockRange {
	return types.NewClosedRange(int64(v.baseNum), v.baseNum+v.bundleSize-1)
}

// printSummary prints at most one line per kind of issue, to stay readable on a store
// where every file is broken.
func (v *segmentBoundaryValidator) printSummary() {
	if v.blocksAboveBoundary > 0 {
		fmt.Printf("❌ Merged blocks file %s holds %d block(s) beyond its bundle boundary %s (%s), it will fail any read configured with a bundle size of %d\n",
			v.segment,
			v.blocksAboveBoundary,
			v.bundleRange(),
			v.formatOffendingBlocks(),
			v.bundleSize,
		)
	}

	if v.outOfOrderCount > 0 {
		fmt.Printf("🔶 Merged blocks file %s holds %d block(s) out of order, first one #%d\n", v.segment, v.outOfOrderCount, v.firstOutOfOrder)
	}
}

func (v *segmentBoundaryValidator) formatOffendingBlocks() string {
	out := ""
	for i, blockNum := range v.blocksAboveListed {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("#%d", blockNum)
	}

	if remaining := v.blocksAboveBoundary - uint64(len(v.blocksAboveListed)); remaining > 0 {
		out += fmt.Sprintf(" and %d more", remaining)
	}

	return out
}

func validateBlockSegment[B firecore.Block](
	ctx context.Context,
	chain *firecore.Chain[B],
	store dstore.Store,
	segment string,
	baseNum uint64,
	fileBlockSize uint64,
	blockRange types.BlockRange,
	printDetails PrintDetails,
	tfdb *trackedForkDB,
) (lowestBlockSeen, highestBlockSeen uint64, broken bool) {
	lowestBlockSeen = firecore.MaxUint64
	reader, err := store.OpenObject(ctx, segment)
	if err != nil {
		fmt.Printf("❌ Unable to read blocks segment %s: %s\n", segment, err)
		return lowestBlockSeen, highestBlockSeen, true
	}
	defer reader.Close()

	readerFactory, err := bstream.NewDBinBlockReader(reader)
	if err != nil {
		fmt.Printf("❌ Unable to read blocks segment %s: %s\n", segment, err)
		return lowestBlockSeen, highestBlockSeen, true
	}

	printer := print2.TextOutputPrinter{}

	validator := newSegmentBoundaryValidator(segment, baseNum, fileBlockSize)
	defer validator.printSummary()

	seenBlockCount := 0
	for {
		block, err := readerFactory.Read()
		if block != nil {
			// bundle boundaries belong to the file, not to the requested range: validate
			// before the range filtering below
			validator.add(block.Number)

			if block.Number < uint64(blockRange.Start) {
				continue
			}

			if blockRange.IsClosed() && block.Number > *blockRange.Stop {
				return lowestBlockSeen, highestBlockSeen, validator.broken()
			}

			if block.Number < lowestBlockSeen {
				lowestBlockSeen = block.Number
			}
			if block.Number > highestBlockSeen {
				highestBlockSeen = block.Number
			}

			if !tfdb.fdb.HasLIB() {
				tfdb.fdb.InitLIB(block.AsRef())
			}

			tfdb.fdb.AddLink(block.AsRef(), block.ParentId, nil)
			revSeg, _ := tfdb.fdb.ReversibleSegment(block.AsRef())
			if revSeg == nil {
				tfdb.unlinkableSegmentCount++
				if tfdb.firstUnlinkableBlock == nil {
					tfdb.firstUnlinkableBlock = block
				}

				// TODO: this print should be under a 'check forkable' flag?
				fmt.Printf("🔶 Block #%d is not linkable at this point\n", block.Number)

				if tfdb.unlinkableSegmentCount > 99 && tfdb.unlinkableSegmentCount%100 == 0 {
					// TODO: this print should be under a 'check forkable' flag?
					fmt.Printf("❌ Large gap of %d unlinkable blocks found in chain. Last linked block: %d, first Unlinkable block: %d. \n", tfdb.unlinkableSegmentCount, tfdb.lastLinkedBlock.Number, tfdb.firstUnlinkableBlock.Number)
				}
			} else {
				tfdb.lastLinkedBlock = block
				tfdb.unlinkableSegmentCount = 0
				tfdb.firstUnlinkableBlock = nil
				tfdb.fdb.SetLIB(block.AsRef(), block.LibNum)
				if tfdb.fdb.HasLIB() {
					tfdb.fdb.PurgeBeforeLIB(0)
				}
			}
			seenBlockCount++

			if printDetails == PrintStats {
				err := printer.PrintTo(block, os.Stdout)
				if err != nil {
					fmt.Printf("❌ Unable to print block %s: %s\n", block.AsRef(), err)
					continue
				}
			}

			if printDetails == PrintFull {
				printer, err := print2.GetOutputPrinter(globalToolsCheckCmd, chain.BlockFileDescriptor())
				if err != nil {
					fmt.Printf("❌ Unable to create output printer: %s\n", err)
					break
				}

				var b = chain.BlockFactory()

				if _, ok := b.(*pbbstream.Block); ok {
					//todo: implements when buf registry available ...
					panic("printing full block is not supported for pbbstream.Block")
				}

				if err := block.Payload.UnmarshalTo(b); err != nil {
					fmt.Printf("❌ Unable unmarshall block %s: %s\n", block.AsRef(), err)
					break
				}

				err = printer.PrintTo(b, os.Stdout)
				if err != nil {
					fmt.Printf("❌ Unable to print full block %s: %s\n", block.AsRef(), err)
					continue
				}
			}

			continue
		}

		if block == nil && errors.Is(err, io.EOF) {
			if seenBlockCount < expectedBlockCount(segment, fileBlockSize) {
				fmt.Printf("🔶 Segment %s contained only %d blocks (< %d), this can happen on some chains\n", segment, seenBlockCount, fileBlockSize)
			}

			return lowestBlockSeen, highestBlockSeen, validator.broken()
		}

		if err != nil {
			fmt.Printf("❌ Unable to read all blocks from segment %s after reading %d blocks: %s\n", segment, seenBlockCount, err)
			return lowestBlockSeen, highestBlockSeen, true
		}
	}

	// only reachable when the printing path above breaks out of the loop
	return lowestBlockSeen, highestBlockSeen, validator.broken()
}

func WalkBlockPrefix(blockRange types.BlockRange, fileBlockSize uint64) string {
	if blockRange.IsOpen() {
		return ""
	}

	startString := fmt.Sprintf("%010d", types.RoundToBundleStartBlock(uint64(blockRange.Start), fileBlockSize))
	endString := fmt.Sprintf("%010d", types.RoundToBundleEndBlock(*blockRange.Stop-1, fileBlockSize)+1)

	offset := 0
	for i := 0; i < len(startString); i++ {
		if startString[i] != endString[i] {
			return string(startString[0:i])
		}

		offset++
	}

	// At this point, the two strings are equal, to return the string
	return startString
}

func expectedBlockCount(segment string, fileBlockSize uint64) int {
	if segment == "0000000000" {
		return int(fileBlockSize - bstream.GetProtocolFirstStreamableBlock)
	}

	return int(fileBlockSize)
}
