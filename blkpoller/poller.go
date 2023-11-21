package blkpoller

import (
	"context"
	"fmt"
	"time"

	"github.com/streamingfast/derr"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	pbbstream "github.com/streamingfast/bstream/types/pb/sf/bstream/v1"

	"go.uber.org/zap"
)

type BlockFireFunc func(*pbbstream.Block)

type BlkPoller struct {
	blockFetcher         BlockFetcher
	blockFireFunc        BlockFireFunc
	fetchBlockRetryCount uint64
	forkDB               *forkable.ForkDB
	startBlockNumGate    uint64
	logger               *zap.Logger
}

func New(
	blockFetcher BlockFetcher,
	blockFire BlockFireFunc,
	logger *zap.Logger,
) *BlkPoller {
	return &BlkPoller{
		blockFetcher:         blockFetcher,
		blockFireFunc:        blockFire,
		fetchBlockRetryCount: 4,
		forkDB:               forkable.NewForkDB(forkable.ForkDBWithLogger(logger)),
		logger:               logger,
	}
}

func (p *BlkPoller) Run(ctx context.Context, startBlockNum uint64, finalizedBlockNum bstream.BlockRef) error {
	p.startBlockNumGate = startBlockNum
	resolveStartBlockNum := resolveStartBlock(startBlockNum, finalizedBlockNum.Num())
	p.logger.Info("starting poller",
		zap.Uint64("start_block_num", startBlockNum),
		zap.Stringer("finalized_block_num", finalizedBlockNum),
		zap.Uint64("resolved_start_block_num", resolveStartBlockNum),
	)

	startBlock, err := p.blockFetcher.Fetch(ctx, resolveStartBlockNum)
	if err != nil {

		return fmt.Errorf("unable to fetch start block %d: %w", resolveStartBlockNum, err)
	}

	return p.run(startBlock.AsRef())
}

func (p *BlkPoller) run(resolvedStartBlock bstream.BlockRef) (err error) {
	currentState := &state{state: ContinuousSegState, logger: p.logger}
	p.forkDB.InitLIB(resolvedStartBlock)
	blkIter := resolvedStartBlock.Num()
	intervalDuration := p.blockFetcher.PollingInterval()
	for {
		blkIter, err = p.processBlock(currentState, blkIter)
		if err != nil {
			return fmt.Errorf("unable to fetch  block %d: %w", blkIter, err)
		}
		time.Sleep(intervalDuration)
	}
}

func (p *BlkPoller) processBlock(currentState *state, blkNum uint64) (uint64, error) {
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
		p.fireCompleteSegment(blocks)

		// since the block is linkable to the current lib
		// we can safely set the new lib to the current block's Lib
		// the assumption here is that teh Lib the Block we received from the block fetcher ir ALWAYS CORRECT
		p.logger.Debug("setting lib", zap.Stringer("blk", blk.AsRef()), zap.Uint64("lib_num", blk.LibNum))
		p.forkDB.SetLIB(blk.AsRef(), "", blk.LibNum)
		p.forkDB.PurgeBeforeLIB(0)

		return nextBlkInSeg(blocks), nil
	}

	currentState.blkIsNotConnectedToLib()
	return prevBlkInSeg(blocks), nil
}

func (p *BlkPoller) fetchBlock(blkNum uint64) (blk *pbbstream.Block, err error) {
	var out *pbbstream.Block
	if err := derr.Retry(p.fetchBlockRetryCount, func(ctx context.Context) error {
		out, err = p.blockFetcher.Fetch(ctx, blkNum)
		if err != nil {
			return fmt.Errorf("unable to fetch  block %d: %w", blkNum, err)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to fetch block with retries %d: %w", blkNum, err)
	}
	return out, nil
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

func resolveStartBlock(startBlockNum, finalizedBlockNum uint64) uint64 {
	if finalizedBlockNum < startBlockNum {
		return finalizedBlockNum
	}
	return startBlockNum
}

type block struct {
	*pbbstream.Block
	fired bool
}

func newBlock(block2 *pbbstream.Block) *block {
	return &block{block2, false}
}

func (p *BlkPoller) fireCompleteSegment(blocks []*forkable.Block) {
	for _, blk := range blocks {
		if blk.BlockNum < p.startBlockNumGate {
			continue
		}
		p.tryFire(blk.Object.(*block))
	}
}

func (p *BlkPoller) tryFire(b *block) bool {
	if b.fired {
		return false
	}
	p.blockFireFunc(b.Block)
	p.logger.Debug("block fired", zap.Stringer("blk", b.Block.AsRef()))
	b.fired = true
	return true
}
