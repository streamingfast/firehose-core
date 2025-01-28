package blockpoller

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/derr"
	"github.com/streamingfast/dhammer"
	"github.com/streamingfast/firehose-core/internal/utils"
	"github.com/streamingfast/firehose-core/rpc"
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

type BlockPoller[C any] struct {
	*shutter.Shutter
	startBlockNumGate        uint64
	fetchBlockRetryCount     uint64
	stateStorePath           string
	ignoreCursor             bool
	forceFinalityAfterBlocks *uint64

	blockFetcher BlockFetcher[C]
	blockHandler BlockHandler
	clients      *rpc.Clients[C]

	forkDB *forkable.ForkDB

	logger *zap.Logger

	optimisticallyPolledBlocks map[uint64]*BlockItem

	fetching                       bool
	optimisticallyPolledBlocksLock sync.Mutex
}

func New[C any](
	blockFetcher BlockFetcher[C],
	blockHandler BlockHandler,
	clients *rpc.Clients[C],
	opts ...Option[C],
) *BlockPoller[C] {

	b := &BlockPoller[C]{
		Shutter:                  shutter.New(),
		blockFetcher:             blockFetcher,
		blockHandler:             blockHandler,
		clients:                  clients,
		fetchBlockRetryCount:     math.MaxUint64,
		logger:                   zap.NewNop(),
		forceFinalityAfterBlocks: utils.GetEnvForceFinalityAfterBlocks(),
	}

	for _, opt := range opts {
		opt(b)
	}

	return b
}

func (p *BlockPoller[C]) Run(firstStreamableBlockNum uint64, stopBlock *uint64, blockFetchBatchSize int) error {
	p.startBlockNumGate = firstStreamableBlockNum
	p.logger.Info("starting poller",
		zap.Uint64("first_streamable_block", firstStreamableBlockNum),
		zap.Uint64("block_fetch_batch_size", uint64(blockFetchBatchSize)),
	)
	p.blockHandler.Init()

	forkDB, resolvedStartBlock, err := p.initState(firstStreamableBlockNum, p.stateStorePath, p.ignoreCursor, p.logger)
	if err != nil {
		return fmt.Errorf("unable to initialize cursor: %w", err)
	}
	p.forkDB = forkDB

	resolveStopBlock := uint64(math.MaxUint64)
	if stopBlock != nil {
		resolveStopBlock = *stopBlock
	}

	return p.run(resolvedStartBlock, resolveStopBlock, blockFetchBatchSize)
}

func (p *BlockPoller[C]) run(resolvedStartBlock bstream.BlockRef, stopBlock uint64, blockFetchBatchSize int) (err error) {
	currentCursor := &cursor{state: ContinuousSegState, logger: p.logger}
	blockToFetch := resolvedStartBlock.Num()
	var hashToFetch *string
	for {

		if blockToFetch >= stopBlock {
			p.logger.Info("stop block reach", zap.Uint64("stop_block", stopBlock))
			return nil
		}

		if p.IsTerminating() {
			p.logger.Info("block poller is terminating")
		}

		p.logger.Info("about to fetch block", zap.Uint64("block_to_fetch", blockToFetch))
		var fetchedBlock *pbbstream.Block
		if hashToFetch != nil {
			fetchedBlock, err = p.fetchBlockWithHash(blockToFetch, *hashToFetch)
		} else {

			for {
				fetchedBlockItem, err := p.requestBlock(blockToFetch, blockFetchBatchSize)
				if err != nil {
					return err
				}
				if !fetchedBlockItem.skipped {
					fetchedBlock = fetchedBlockItem.block
					break
				}

				p.logger.Info("block was skipped", zap.Uint64("block_num", fetchedBlockItem.blockNumber))
				blockToFetch++
			}
		}

		if err != nil {
			return fmt.Errorf("unable to fetch  block %d: %w", blockToFetch, err)
		}

		blockToFetch, hashToFetch, err = p.processBlock(currentCursor, fetchedBlock)
		if err != nil {
			return fmt.Errorf("unable to fetch  block %d: %w", blockToFetch, err)
		}

		if p.IsTerminating() {
			p.logger.Info("block poller is terminating")
		}
	}
}

func (p *BlockPoller[C]) processBlock(currentState *cursor, block *pbbstream.Block) (uint64, *string, error) {
	p.logger.Info("processing block", zap.Stringer("block", block.AsRef()), zap.Uint64("lib_num", block.LibNum))
	if block.Number < p.forkDB.LIBNum() {
		panic(fmt.Errorf("unexpected error block %d is below the current LIB num %d. There should be no re-org above the current LIB num", block.Number, p.forkDB.LIBNum()))
	}

	// On the first run, we will fetch the blk for the `startBlockRef`, since we have a `Ref` it stands
	// to reason that we may already have the block. We could potentially optimize this

	seenBlk, seenParent := p.forkDB.AddLink(block.AsRef(), block.ParentId, newBlock(block))

	currentState.addBlk(block, seenBlk, seenParent)

	blkCompleteSegNum := currentState.getBlkSegmentNum()
	completeSegment, reachLib := p.forkDB.CompleteSegment(blkCompleteSegNum)
	p.logger.Debug("checked if block is complete segment",
		zap.Uint64("blk_num", blkCompleteSegNum.Num()),
		zap.Int("segment_len", len(completeSegment)),
		zap.Bool("reached_lib", reachLib),
	)

	if reachLib {
		currentState.blkIsConnectedToLib()
		err := p.fireCompleteSegment(completeSegment)
		if err != nil {
			return 0, nil, fmt.Errorf("firing complete segment: %w", err)
		}

		// since the block is linkable to the current lib
		// we can safely set the new lib to the current block's Lib
		// the assumption here is that teh Lib the Block we received from the block fetcher ir ALWAYS CORRECT
		p.logger.Debug("setting lib", zap.Stringer("blk", block.AsRef()), zap.Uint64("lib_num", block.LibNum))
		p.forkDB.SetLIB(block.AsRef(), block.LibNum)
		p.forkDB.PurgeBeforeLIB(0)

		err = p.saveState(completeSegment)
		if err != nil {
			return 0, nil, fmt.Errorf("saving state: %w", err)
		}

		nextBlockNum := nextBlkInSeg(completeSegment)
		return nextBlockNum, nil, nil
	}

	currentState.blkIsNotConnectedToLib()

	prevBlockNum, prevBlockHash := prevBlockInSegment(completeSegment)
	return prevBlockNum, prevBlockHash, nil
}

type BlockItem struct {
	blockNumber uint64
	block       *pbbstream.Block
	skipped     bool
}

func (p *BlockPoller[C]) loadNextBlocks(requestedBlock uint64, numberOfBlockToFetch int) error {
	p.optimisticallyPolledBlocks = map[uint64]*BlockItem{}
	p.fetching = true

	nailer := dhammer.NewNailer(10, func(ctx context.Context, blockToFetch uint64) (*BlockItem, error) {
		var blockItem *BlockItem
		err := derr.Retry(p.fetchBlockRetryCount, func(ctx context.Context) error {

			bi, err := rpc.WithClients(p.clients, func(ctx context.Context, client C) (*BlockItem, error) {
				b, skipped, err := p.blockFetcher.Fetch(ctx, client, blockToFetch)
				if err != nil {
					return nil, fmt.Errorf("fetching block %d: %w", blockToFetch, err)
				}

				if skipped {
					return &BlockItem{
						blockNumber: blockToFetch,
						block:       nil,
						skipped:     true,
					}, nil
				}

				return &BlockItem{
					blockNumber: blockToFetch,
					block:       b,
					skipped:     false,
				}, nil
			})

			if err != nil {
				return fmt.Errorf("fetching block %d with retry : %w", blockToFetch, err)
			}
			blockItem = bi

			return nil

		})

		if err != nil {
			return nil, fmt.Errorf("failed to fetch block with retries %d: %w", blockToFetch, err)
		}

		return blockItem, err
	})

	ctx := context.Background()
	nailer.Start(ctx)

	done := make(chan interface{}, 1)
	go func() {
		for blockItem := range nailer.Out {
			p.optimisticallyPolledBlocksLock.Lock()
			p.optimisticallyPolledBlocks[blockItem.blockNumber] = blockItem
			p.optimisticallyPolledBlocksLock.Unlock()
		}
		close(done)
	}()

	didTriggerFetch := false
	for i := 0; i < numberOfBlockToFetch; i++ {
		b := requestedBlock + uint64(i)

		//only fetch block if it is available on chain
		if p.blockFetcher.IsBlockAvailable(b) {
			p.logger.Info("optimistically fetching block", zap.Uint64("block_num", b))
			didTriggerFetch = true
			nailer.Push(ctx, b)
		} else {
			//if this block is not available, we can assume that the next blocks are not available as well
			break
		}
	}

	if !didTriggerFetch {
		//if we did not trigger any fetch, we fetch the requested block
		// Fetcher should return the block when available (this will be a blocking call until the block is available)
		nailer.Push(ctx, requestedBlock)
	}

	nailer.Close()

	<-done

	p.fetching = false

	if nailer.Err() != nil {
		return fmt.Errorf("failed optimistically fetch blocks starting at %d: %w", requestedBlock, nailer.Err())
	}

	return nil
}

func (p *BlockPoller[C]) requestBlock(blockNumber uint64, numberOfBlockToFetch int) (*BlockItem, error) {
	p.logger.Info("requesting block", zap.Uint64("block_num", blockNumber))

	lastLog := time.Time{}
	for {
		if p.IsTerminating() {
			return nil, fmt.Errorf("block poller is terminating")
		}

		p.optimisticallyPolledBlocksLock.Lock()
		blockItem, found := p.optimisticallyPolledBlocks[blockNumber]
		p.optimisticallyPolledBlocksLock.Unlock()
		if !found {
			if !p.fetching {
				go func() {
					err := p.loadNextBlocks(blockNumber, numberOfBlockToFetch)
					if err != nil {
						p.Shutdown(err)
						return
					}
				}()
			}
			if time.Since(lastLog) > 2*time.Second {
				p.logger.Debug("waiting for block to be fetched", zap.Uint64("block_num", blockNumber))
				lastLog = time.Now()
			}

			time.Sleep(20 * time.Millisecond)
			continue
		}

		p.logger.Info("block was optimistically polled", zap.Uint64("block_num", blockNumber))
		return blockItem, nil
	}

}

type FetchResponse struct {
	Block   *pbbstream.Block
	Skipped bool
}

func (p *BlockPoller[C]) fetchBlockWithHash(blkNum uint64, hash string) (*pbbstream.Block, error) {
	p.logger.Info("fetching block with hash", zap.Uint64("block_num", blkNum), zap.String("hash", hash))
	_ = hash //todo: hash will be used to fetch block from  cache

	p.optimisticallyPolledBlocks = map[uint64]*BlockItem{}

	var out *pbbstream.Block
	var skipped bool

	err := derr.Retry(p.fetchBlockRetryCount, func(ctx context.Context) error {
		br, err := rpc.WithClients(p.clients, func(ctx context.Context, client C) (br *FetchResponse, err error) {
			b, skipped, err := p.blockFetcher.Fetch(ctx, client, blkNum)
			if err != nil {
				return nil, fmt.Errorf("fetching block  block %d: %w", blkNum, err)
			}
			return &FetchResponse{
				Block:   b,
				Skipped: skipped,
			}, nil
		})

		if err != nil {
			return fmt.Errorf("fetching block with retry %d: %w", blkNum, err)
		}

		out = br.Block
		skipped = br.Skipped
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to fetch block with retries %d: %w", blkNum, err)
	}

	if skipped {
		return nil, fmt.Errorf("block %d was skipped and should not have been requested", blkNum)
	}

	if p.forceFinalityAfterBlocks != nil {
		utils.TweakBlockFinality(out, *p.forceFinalityAfterBlocks)
	}

	return out, nil
}

func (p *BlockPoller[C]) fireCompleteSegment(blocks []*forkable.Block) error {
	for _, blk := range blocks {
		b := blk.Object.(*block)
		if _, err := p.fire(b); err != nil {
			return fmt.Errorf("fireing block %d (%qs) %w", blk.BlockNum, blk.BlockID, err)
		}
	}
	return nil
}

func (p *BlockPoller[C]) fire(blk *block) (bool, error) {
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

func prevBlockInSegment(blocks []*forkable.Block) (uint64, *string) {
	if len(blocks) == 0 {
		panic(fmt.Errorf("the blocks segments should never be empty"))
	}
	blockObject := blocks[0].Object.(*block)
	return blockObject.ParentNum, &blockObject.ParentId
}
