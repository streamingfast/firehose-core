package blockpoller

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	pbbstream "github.com/streamingfast/bstream/types/pb/sf/bstream/v1"
	"github.com/streamingfast/derr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/anypb"
)

type BlockFireFunc func(b *block) error

type BlockPoller struct {
	blockTypeURL string
	// the block number at which
	startBlockNumGate    uint64
	blockFetcher         BlockFetcher
	fetchBlockRetryCount uint64

	forkDB        *forkable.ForkDB
	logger        *zap.Logger
	fireFunc      BlockFireFunc
	stopRequested bool
}

func New(
	blockType string,
	blockFetcher BlockFetcher,
	logger *zap.Logger,
) *BlockPoller {
	poller := &BlockPoller{
		blockTypeURL:         blockType,
		blockFetcher:         blockFetcher,
		fetchBlockRetryCount: 4,
		forkDB:               forkable.NewForkDB(forkable.ForkDBWithLogger(logger)),
		logger:               logger,
	}
	poller.fireFunc = poller.fire
	return poller
}

func (p *BlockPoller) Run(ctx context.Context, startBlockNum uint64, finalizedBlockNum bstream.BlockRef) error {
	p.startBlockNumGate = startBlockNum
	resolveStartBlockNum := resolveStartBlock(startBlockNum, finalizedBlockNum.Num())
	p.logger.Info("starting poller",
		zap.Uint64("start_block_num", startBlockNum),
		zap.Stringer("finalized_block_num", finalizedBlockNum),
		zap.Uint64("resolved_start_block_num", resolveStartBlockNum),
	)

	//initLine := "FIRE INIT 1.0 sf.ethereum.type.v2.Block"
	fmt.Println("FIRE INIT 1.0 ", p.blockTypeURL)

	startBlock, err := p.blockFetcher.Fetch(ctx, resolveStartBlockNum)
	if err != nil {

		return fmt.Errorf("unable to fetch start block %d: %w", resolveStartBlockNum, err)
	}

	return p.run(startBlock.AsRef())
}

func (p *BlockPoller) run(resolvedStartBlock bstream.BlockRef) (err error) {
	currentState := &state{state: ContinuousSegState, logger: p.logger}
	p.forkDB.InitLIB(resolvedStartBlock)
	blkIter := resolvedStartBlock.Num()
	intervalDuration := p.blockFetcher.PollingInterval()
	for {
		blkIter, err = p.processBlock(currentState, blkIter)
		if err != nil {
			return fmt.Errorf("unable to fetch  block %d: %w", blkIter, err)
		}
		if p.stopRequested {
			return nil
		}
		time.Sleep(intervalDuration)
	}
}

func (p *BlockPoller) Stop() {
	p.stopRequested = true
}

func (p *BlockPoller) processBlock(currentState *state, blkNum uint64) (uint64, error) {
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
		err = p.fireCompleteSegment(blocks, p.fireFunc)
		if err != nil {
			return 0, fmt.Errorf("firing complete segment: %w", err)
		}

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

func (p *BlockPoller) fireCompleteSegment(blocks []*forkable.Block, fireFunc BlockFireFunc) error {
	for _, blk := range blocks {
		if blk.BlockNum < p.startBlockNumGate {
			continue
		}

		b := blk.Object.(*block)
		if !b.fired {
			err := fireFunc(b)
			if err != nil {
				return fmt.Errorf("fireing block %d %q: %w", b.Block.Number, b.Block.Id, err)
			}
			b.fired = true
		}
	}
	return nil
}

func (p *BlockPoller) fire(b *block) error {

	//blockLine := "FIRE BLOCK 18571000 d2836a703a02f3ca2a13f05efe26fc48c6fa0db0d754a49e56b066d3b7d54659 18570999 55de88c909fa368ae1e93b6b8ffb3fbb12e64aefec1d4a1fcc27ae7633de2f81 18570800 1699992393935935000 Ci10eXBlLmdvb2dsZWFwaXMuY29tL3NmLmV0aGVyZXVtLnR5cGUudjIuQmxvY2sSJxIg0oNqcDoC88oqE/Be/ib8SMb6DbDXVKSeVrBm07fVRlkY+L3tCA=="
	anyBlock, err := anypb.New(b.Block)
	if err != nil {
		return fmt.Errorf("converting block to anypb: %w", err)
	}

	if anyBlock.TypeUrl != p.blockTypeURL {
		return fmt.Errorf("block type url %q does not match expected type %q", anyBlock.TypeUrl, p.blockTypeURL)
	}

	blockLine := fmt.Sprintf(
		"FIRE BLOCK %d %s %d %s %d %d %s",
		b.Block.Number,
		b.Block.Id,
		b.Block.ParentNum,
		b.Block.ParentId,
		b.Block.LibNum,
		b.Block.Timestamp.AsTime().UnixNano(),
		base64.StdEncoding.EncodeToString(anyBlock.Value),
	)

	fmt.Println(blockLine)

	return nil
}
