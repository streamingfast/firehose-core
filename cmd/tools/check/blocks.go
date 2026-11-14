package check

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
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

	// Parallel is how many merged-blocks files are read at the same time when their content
	// is read. Each file is downloaded, decoded and checked against its bundle boundaries
	// concurrently, but the results are linked into the chain and printed in file order, so
	// the output is the same as a sequential run and at most Parallel files are held in
	// memory. Zero or one reads the files one at a time. Not supported with PrintFull.
	Parallel int
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
	parallel := max(options.Parallel, 1)
	if parallel > 1 && options.PrintDetails == PrintFull {
		return fmt.Errorf("printing full blocks cannot run in parallel, use a parallelism of 1")
	}

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

	// apply links one file into the chain and prints everything about it, always in file
	// order and from a single goroutine: tfdb and the counters below are not shared
	apply := func(item *checkItem) {
		for _, line := range item.before {
			fmt.Print(line)
		}

		if item.content != nil {
			lowestBlockSegment, highestBlockSegment, broken := item.content.apply(tfdb)
			if lowestBlockSegment < lowestBlockSeen {
				lowestBlockSeen = lowestBlockSegment
			}
			if highestBlockSegment > highestBlockSeen {
				highestBlockSeen = highestBlockSegment
			}
			if broken {
				brokenSegments = append(brokenSegments, item.segment)
			}
		} else {
			if item.baseNum < lowestBlockSeen {
				lowestBlockSeen = item.baseNum
			}
			if item.baseNum+fileBlockSize > highestBlockSeen {
				highestBlockSeen = item.baseNum + fileBlockSize
			}
		}

		for _, line := range item.after {
			fmt.Print(line)
		}
	}

	// read downloads and checks one file, touching nothing shared, so files can be read concurrently
	read := func(ctx context.Context, item *checkItem) (*checkItem, error) {
		if readAllBlocks {
			item.content = readBlockSegment(ctx, chain, blocksStore, item.segment, item.baseNum, fileBlockSize, blockRange, options.PrintDetails)
		}
		return item, nil
	}

	dispatch, finish := sequentialCheck(ctx, read, apply)
	if parallel > 1 && readAllBlocks {
		dispatch, finish = parallelCheck(ctx, logger, parallel, read, apply)
	}

	logger.Debug("walking merged blocks", zap.Stringer("block_range", blockRange), zap.String("walk_prefix", walkPrefix), zap.Int("parallel", parallel))
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

		item := &checkItem{segment: filename, baseNum: baseNum}

		if baseNum != expected {
			// There is no previous valid block range if we are at the ever first seen file
			if count > 1 {
				item.before = append(item.before, fmt.Sprintf("✅ Range %s\n", types.NewClosedRange(int64(currentStartBlk), uint64(types.RoundToBundleEndBlock(expected-fileBlockSize, fileBlockSize)))))
			}

			// Otherwise, we do not follow last seen element (previous is `100 - 199` but we are `299 - 300`)
			missingRange := types.NewClosedRange(int64(expected), types.RoundToBundleEndBlock(baseNum-fileBlockSize, fileBlockSize))
			item.before = append(item.before, fmt.Sprintf("❌ Range %s (Missing, [%s])\n", missingRange, missingRange.ReprocRange()))
			currentStartBlk = baseNum

			holeFound = true
		}
		expected = baseNum + fileBlockSize

		if count%10000 == 0 {
			item.after = append(item.after, fmt.Sprintf("✅ Range %s\n", types.NewClosedRange(int64(currentStartBlk), types.RoundToBundleEndBlock(baseNum, fileBlockSize))))
			currentStartBlk = baseNum + fileBlockSize
		}

		dispatch(item)

		if blockRange.IsClosed() && types.RoundToBundleEndBlock(baseNum, fileBlockSize) >= *blockRange.Stop-1 {
			return dstore.StopIteration
		}

		return nil
	})

	// every file listed so far is applied before reporting, even when the walk failed
	if finishErr := finish(); err == nil {
		err = finishErr
	}
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

	// blocks are only linked when they are read, a run left unlinked at the end means
	// blocks are missing inside the files even if no file is
	unlinkableFound := tfdb.firstUnlinkableBlock != nil && tfdb.lastLinkedBlock != nil

	if holeFound {
		fmt.Printf("> 🆘 Holes found!\n")
	}
	if unlinkableFound {
		fmt.Printf("> 🆘 Unlinkable blocks found! %d block(s) from #%d to #%d do not link to block #%d, the last linkable one\n", tfdb.unlinkableSegmentCount, tfdb.firstUnlinkableBlock.Number, highestBlockSeen, tfdb.lastLinkedBlock.Number)
	}
	if !holeFound && !unlinkableFound {
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

// summary returns at most one line per kind of issue, to stay readable on a store where
// every file is broken.
func (v *segmentBoundaryValidator) summary() (lines []string) {
	if v.blocksAboveBoundary > 0 {
		lines = append(lines, fmt.Sprintf("❌ Merged blocks file %s holds %d block(s) beyond its bundle boundary %s (%s), it will fail any read configured with a bundle size of %d\n",
			v.segment,
			v.blocksAboveBoundary,
			v.bundleRange(),
			v.formatOffendingBlocks(),
			v.bundleSize,
		))
	}

	if v.outOfOrderCount > 0 {
		lines = append(lines, fmt.Sprintf("🔶 Merged blocks file %s holds %d block(s) out of order, first one #%d\n", v.segment, v.outOfOrderCount, v.firstOutOfOrder))
	}

	return lines
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
