package blockpoller

import (
	"context"
	"fmt"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/derr"
	"github.com/streamingfast/firehose-core/internal/utils"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
)

type block struct {
	*pbbstream.Block
	fired bool
}

func newBlock(block2 *pbbstream.Block) *block {
	return &block{block2, false}
}

type BlockPoller struct {
	*shutter.Shutter
	startBlockNumGate        uint64
	fetchBlockRetryCount     uint64
	stateStorePath           string
	ignoreCursor             bool
	forceFinalityAfterBlocks *uint64

	blockFetcher BlockFetcher
	blockHandler BlockHandler
	forkDB       *forkable.ForkDB

	logger *zap.Logger
}

func New(
	blockFetcher BlockFetcher,
	blockHandler BlockHandler,
	opts ...Option,
) *BlockPoller {

	b := &BlockPoller{
		Shutter:                  shutter.New(),
		blockFetcher:             blockFetcher,
		blockHandler:             blockHandler,
		fetchBlockRetryCount:     4,
		logger:                   zap.NewNop(),
		forceFinalityAfterBlocks: utils.GetEnvForceFinalityAfterBlocks(),
	}

	for _, opt := range opts {
		opt(b)
	}

	return b
}

func (p *BlockPoller) Run(ctx context.Context, startBlockNum uint64) error {
	p.startBlockNumGate = startBlockNum
	p.logger.Info("starting poller",
		zap.Uint64("start_block_num", startBlockNum),
	)
	p.blockHandler.Init()
	startBlock, err := p.blockFetcher.Fetch(ctx, startBlockNum)
	if err != nil {
		return fmt.Errorf("unable to fetch start block %d: %w", startBlockNum, err)
	}

	return p.run(startBlock.AsRef())
}

func (p *BlockPoller) run(resolvedStartBlock bstream.BlockRef) (err error) {

	p.forkDB, resolvedStartBlock, err = initState(resolvedStartBlock, p.stateStorePath, p.ignoreCursor, p.logger)
	if err != nil {
		return fmt.Errorf("unable to initialize cursor: %w", err)
	}

	currentCursor := &cursor{state: ContinuousSegState, logger: p.logger}
	blkIter := resolvedStartBlock.Num()
	for {
		if p.IsTerminating() {
			p.logger.Info("block poller is terminating")
		}

		blkIter, err = p.processBlock(currentCursor, blkIter)
		if err != nil {
			return fmt.Errorf("unable to fetch  block %d: %w", blkIter, err)
		}

		if p.IsTerminating() {
			p.logger.Info("block poller is terminating")
		}
	}
}

func (p *BlockPoller) processBlock(currentState *cursor, blkNum uint64) (uint64, error) {
	if blkNum < p.forkDB.LIBNum() {
		panic(fmt.Errorf("unexpected error block %d is below the current LIB num %d. There should be no re-org above the current LIB num", blkNum, p.forkDB.LIBNum()))
	}

	// On the first run, we will fetch the blk for the `startBlockRef`, since we have a `Ref` it stands
	// to reason that we may already have the block. We could potentially optimize this
	blk, err := p.fetchBlock(blkNum)
	if err != nil {
		return 0, fmt.Errorf("unable to fetch  block %d: %w", blkNum, err)
	}

	seenBlk, seenParent := p.forkDB.AddLink(blk.AsRef(), blk.ParentId, newBlock(blk))

	currentState.addBlk(blk, seenBlk, seenParent)

	blkCompleteSegNum := currentState.getBlkSegmentNum()
	blocks, reachLib := p.forkDB.CompleteSegment(blkCompleteSegNum)
	p.logger.Debug("checked if block is complete segment",
		zap.Uint64("blk_num", blkCompleteSegNum.Num()),
		zap.Int("segment_len", len(blocks)),
		zap.Bool("reached_lib", reachLib),
	)

	if reachLib {
		currentState.blkIsConnectedToLib()
		err = p.fireCompleteSegment(blocks)
		if err != nil {
			return 0, fmt.Errorf("firing complete segment: %w", err)
		}

		// since the block is linkable to the current lib
		// we can safely set the new lib to the current block's Lib
		// the assumption here is that teh Lib the Block we received from the block fetcher ir ALWAYS CORRECT
		p.logger.Debug("setting lib", zap.Stringer("blk", blk.AsRef()), zap.Uint64("lib_num", blk.LibNum))
		p.forkDB.SetLIB(blk.AsRef(), blk.LibNum)
		p.forkDB.PurgeBeforeLIB(0)

		err := p.saveState(blocks)
		if err != nil {
			return 0, fmt.Errorf("saving state: %w", err)
		}

		return nextBlkInSeg(blocks), nil
	}

	currentState.blkIsNotConnectedToLib()
	return prevBlkInSeg(blocks), nil
}

func (p *BlockPoller) fetchBlock(blkNum uint64) (blk *pbbstream.Block, err error) {
	var out *pbbstream.Block
	err = derr.Retry(p.fetchBlockRetryCount, func(ctx context.Context) error {
		out, err = p.blockFetcher.Fetch(ctx, blkNum)
		if err != nil {
			return fmt.Errorf("unable to fetch  block %d: %w", blkNum, err)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to fetch block with retries %d: %w", blkNum, err)
	}

	if p.forceFinalityAfterBlocks != nil {
		utils.TweakBlockFinality(blk, *p.forceFinalityAfterBlocks)
	}

	return out, nil
}

func (p *BlockPoller) fireCompleteSegment(blocks []*forkable.Block) error {
	for _, blk := range blocks {
		b := blk.Object.(*block)
		if _, err := p.fire(b); err != nil {
			return fmt.Errorf("fireing block %d (%qs) %w", blk.BlockNum, blk.BlockID, err)
		}
	}
	return nil
}

func (p *BlockPoller) fire(blk *block) (bool, error) {
	if blk.fired {
		return false, nil
	}

	if blk.Number < p.startBlockNumGate {
		return false, nil
	}

	if err := p.blockHandler.Handle(blk.Block); err != nil {
		return false, err
	}

	blk.fired = true
	return true, nil
}

func nextBlkInSeg(blocks []*forkable.Block) uint64 {
	if len(blocks) == 0 {
		panic(fmt.Errorf("the blocks segments should never be empty"))
	}
	return blocks[len(blocks)-1].BlockNum + 1
}

func prevBlkInSeg(blocks []*forkable.Block) uint64 {
	if len(blocks) == 0 {
		panic(fmt.Errorf("the blocks segments should never be empty"))
	}
	return blocks[0].Object.(*block).ParentNum
}
