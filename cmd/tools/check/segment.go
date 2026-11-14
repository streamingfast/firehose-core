package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dhammer"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	print2 "github.com/streamingfast/firehose-core/cmd/tools/print"
	"github.com/streamingfast/firehose-core/types"
	"go.uber.org/zap"
)

// checkItem is one merged-blocks file listed by the walk: the hole and progress lines
// printed around it and, once read, its content (nil when content is not read).
type checkItem struct {
	segment string
	baseNum uint64
	before  []string
	after   []string
	content *segmentContent
}

// segmentContent is what reading one merged-blocks file found, gathered without touching
// the shared fork database so that files can be read concurrently: its blocks in file order
// with the details printed for each, and the lines printed after them.
type segmentContent struct {
	blocks           []segmentBlock
	trailer          []string
	lowestBlockSeen  uint64
	highestBlockSeen uint64
	broken           bool
}

type segmentBlock struct {
	// block holds only what linking needs, the payload is dropped once details are rendered
	block   *pbbstream.Block
	details string
}

// sequentialCheck reads and applies each file as soon as the walk lists it.
func sequentialCheck(ctx context.Context, read dhammer.NailerFunc[*checkItem, *checkItem], apply func(*checkItem)) (dispatch func(*checkItem), finish func() error) {
	dispatch = func(item *checkItem) {
		item, _ = read(ctx, item)
		apply(item)
	}

	return dispatch, func() error { return nil }
}

// parallelCheck reads up to `parallel` files at the same time. The nailer hands results
// back in the order files were listed, and a single goroutine applies them, so linking and
// output are the same as sequentialCheck. dispatch blocks while `parallel` files are in
// flight, which bounds memory.
func parallelCheck(ctx context.Context, logger *zap.Logger, parallel int, read dhammer.NailerFunc[*checkItem, *checkItem], apply func(*checkItem)) (dispatch func(*checkItem), finish func() error) {
	nailer := dhammer.NewNailer(parallel, read, dhammer.NailerLogger(logger))
	nailer.Start(ctx)

	applied := make(chan struct{})
	go func() {
		defer close(applied)
		for item := range nailer.Out {
			apply(item)
		}
	}()

	dispatch = func(item *checkItem) {
		nailer.Push(ctx, item)
	}

	finish = func() error {
		nailer.Close()
		<-applied

		if err := nailer.Err(); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return ctx.Err()
	}

	return dispatch, finish
}

// apply links the blocks into the chain in file order and prints everything about the file.
func (c *segmentContent) apply(tfdb *trackedForkDB) (lowestBlockSeen, highestBlockSeen uint64, broken bool) {
	for _, entry := range c.blocks {
		block := entry.block

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

		fmt.Print(entry.details)
	}

	for _, line := range c.trailer {
		fmt.Print(line)
	}

	return c.lowestBlockSeen, c.highestBlockSeen, c.broken
}

// readBlockSegment reads one merged-blocks file and checks it against the bundle its name
// claims. It doesn't link blocks (see segmentContent.apply) so it is safe to run concurrently.
func readBlockSegment[B firecore.Block](
	ctx context.Context,
	chain *firecore.Chain[B],
	store dstore.Store,
	segment string,
	baseNum uint64,
	fileBlockSize uint64,
	blockRange types.BlockRange,
	printDetails PrintDetails,
) *segmentContent {
	content := &segmentContent{lowestBlockSeen: firecore.MaxUint64}

	reader, err := store.OpenObject(ctx, segment)
	if err != nil {
		content.trailer = append(content.trailer, fmt.Sprintf("❌ Unable to read blocks segment %s: %s\n", segment, err))
		content.broken = true
		return content
	}
	defer reader.Close()

	readerFactory, err := bstream.NewDBinBlockReader(reader)
	if err != nil {
		content.trailer = append(content.trailer, fmt.Sprintf("❌ Unable to read blocks segment %s: %s\n", segment, err))
		content.broken = true
		return content
	}

	validator := newSegmentBoundaryValidator(segment, baseNum, fileBlockSize)
	// the boundary summary comes last, after the end-of-file messages
	defer func() {
		content.trailer = append(content.trailer, validator.summary()...)
	}()

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
				content.broken = validator.broken()
				return content
			}

			if block.Number < content.lowestBlockSeen {
				content.lowestBlockSeen = block.Number
			}
			if block.Number > content.highestBlockSeen {
				content.highestBlockSeen = block.Number
			}

			details, stop := renderBlockDetails(chain, block, printDetails)
			content.blocks = append(content.blocks, segmentBlock{block: linkOnly(block), details: details})
			seenBlockCount++

			if stop {
				// only the full printing path stops early, like it used to break out of the loop
				content.broken = validator.broken()
				return content
			}

			continue
		}

		if errors.Is(err, io.EOF) {
			if seenBlockCount < expectedBlockCount(segment, fileBlockSize) {
				content.trailer = append(content.trailer, fmt.Sprintf("🔶 Segment %s contained only %d blocks (< %d), this can happen on some chains\n", segment, seenBlockCount, fileBlockSize))
			}

			content.broken = validator.broken()
			return content
		}

		if err != nil {
			content.trailer = append(content.trailer, fmt.Sprintf("❌ Unable to read all blocks from segment %s after reading %d blocks: %s\n", segment, seenBlockCount, err))
			content.broken = true
			return content
		}
	}
}

// renderBlockDetails returns what --print-stats or --print-full prints for a block, and
// whether reading the file should stop (an error that makes printing the rest pointless).
func renderBlockDetails[B firecore.Block](chain *firecore.Chain[B], block *pbbstream.Block, printDetails PrintDetails) (details string, stop bool) {
	var buf bytes.Buffer

	switch printDetails {
	case PrintStats:
		printer := print2.TextOutputPrinter{}
		if err := printer.PrintTo(block, &buf); err != nil {
			buf.WriteString(fmt.Sprintf("❌ Unable to print block %s: %s\n", block.AsRef(), err))
		}

	case PrintFull:
		printer, err := print2.GetOutputPrinter(globalToolsCheckCmd, chain.BlockFileDescriptor())
		if err != nil {
			return fmt.Sprintf("❌ Unable to create output printer: %s\n", err), true
		}

		var b = chain.BlockFactory()

		if _, ok := b.(*pbbstream.Block); ok {
			//todo: implements when buf registry available ...
			panic("printing full block is not supported for pbbstream.Block")
		}

		if err := block.Payload.UnmarshalTo(b); err != nil {
			return fmt.Sprintf("❌ Unable unmarshall block %s: %s\n", block.AsRef(), err), true
		}

		if err := printer.PrintTo(b, &buf); err != nil {
			buf.WriteString(fmt.Sprintf("❌ Unable to print full block %s: %s\n", block.AsRef(), err))
		}
	}

	return buf.String(), false
}

// linkOnly keeps what linking a block needs, so files waiting to be applied hold no payload.
func linkOnly(block *pbbstream.Block) *pbbstream.Block {
	return &pbbstream.Block{
		Id:        block.Id,
		Number:    block.Number,
		ParentId:  block.ParentId,
		ParentNum: block.ParentNum,
		LibNum:    block.LibNum,
		Timestamp: block.Timestamp,
	}
}
