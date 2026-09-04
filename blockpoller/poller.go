package blockpoller

import (
	"context"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
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

// optimisticPollInterval is how often `requestBlock` re-checks the optimistic cache
// while it waits for a block to be fetched.
const optimisticPollInterval = 20 * time.Millisecond

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
	delayBetweenFetch        time.Duration
	stateStorePath           string
	ignoreCursor             bool
	forceFinalityAfterBlocks *uint64

	blockFetcher BlockFetcher[C]
	blockHandler BlockHandler
	clients      *rpc.Clients[C]

	forkDB *forkable.ForkDB

	logger *zap.Logger

	optimisticallyPolledBlocks map[uint64]*BlockItem
	// optimisticEpoch is bumped every time the optimistic cache is dropped. A batch
	// fetch captures it when it starts and discards its results if it changed in the
	// meantime, so blocks fetched from a chain state we have since abandoned (a
	// reorg) can never land back in the cache.
	optimisticEpoch uint64

	// fetching is `true` while a `loadNextBlocks` call is in flight. It doubles as
	// the single-flight guard: only the goroutine that manages to swap it from
	// `false` to `true` is allowed to start a batch.
	fetching                       atomic.Bool
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
	p.resetOptimisticallyPolledBlocks()

	if blockFetchBatchSize < 1 {
		blockFetchBatchSize = 1
	}

	p.logger.Info("starting poller",
		zap.Uint64("first_streamable_block", firstStreamableBlockNum),
		zap.Uint64("block_fetch_batch_size", uint64(blockFetchBatchSize)),
	)
	if p.delayBetweenFetch != 0 && blockFetchBatchSize > 1 {
		p.logger.Warn("delayBetweenFetch is set, but blockFetchBatchSize is greater than 1: delayBetweenFetch will not be respected by the parallel fetching mechanism")
	}

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

	lastFetch := time.Time{}
	for {

		if blockToFetch >= stopBlock {
			p.logger.Info("stop block reach", zap.Uint64("stop_block", stopBlock))
			return nil
		}

		if p.IsTerminating() {
			p.logger.Info("block poller is terminating")
			return nil
		}

		delay := time.Duration(0)
		if p.delayBetweenFetch > 0 {
			since := time.Since(lastFetch)
			if since < p.delayBetweenFetch {
				delay = p.delayBetweenFetch - since
			}
		}

		p.logger.Info("about to fetch block", zap.Uint64("block_to_fetch", blockToFetch), zap.Duration("delay", delay), zap.Bool("keep", false))
		if delay != 0 {
			time.Sleep(delay)
		}
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
		lastFetch = time.Now()

		blockToFetch, hashToFetch, err = p.processBlock(currentCursor, fetchedBlock)
		if err != nil {
			return fmt.Errorf("unable to fetch  block %d: %w", blockToFetch, err)
		}

	}
}

func (p *BlockPoller[C]) processBlock(currentState *cursor, block *pbbstream.Block) (uint64, *string, error) {
	p.logger.Info("processing block", zap.Stringer("block", block.AsRef()), zap.Uint64("lib_num", block.LibNum), zap.Bool("keep", false))
	if block.Number < p.forkDB.LIBNum() {
		panic(fmt.Errorf("unexpected error block %d is below the current LIB num %d. There should be no re-org above the current LIB num", block.Number, p.forkDB.LIBNum()))
	}

	// On the first run, we will fetch the blk for the `startBlockRef`, since we have a `Ref` it stands
	// to reason that we may already have the block. We could potentially optimize this
	p.logger.Debug("adding link", zap.String("block_ref", block.AsRef().String()), zap.String("parent_id", block.ParentId))
	seenBlk, seenParent := p.forkDB.AddLink(block.AsRef(), block.ParentId, newBlock(block))
	p.logger.Debug("added link", zap.String("block_ref", block.AsRef().String()), zap.Bool("seen_block", seenBlk), zap.Bool("seen_parent", seenParent), zap.Bool("exist", p.forkDB.Exists(block.Id)))

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

// triggerLoadNextBlocks starts a background batch fetch, unless one is already in
// flight. The flag is claimed synchronously, before the goroutine is spawned, so
// two callers racing here cannot both start a batch (which would fetch the same
// blocks twice, from two different clients).
//
// When `speculative` is set, nothing is waiting on the batch: the run loop has not
// reached those blocks yet. A failure there is expected (the block may simply not
// be produced yet) and is ignored — the blocks get fetched on demand if the run
// loop ever gets to them. On the demand path a failure is fatal, otherwise
// `requestBlock` would wait for a block that nobody is fetching anymore.
func (p *BlockPoller[C]) triggerLoadNextBlocks(requestedBlock uint64, numberOfBlockToFetch int, speculative bool) {
	if !p.fetching.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer p.fetching.Store(false)

		if err := p.loadNextBlocks(requestedBlock, numberOfBlockToFetch); err != nil {
			if speculative {
				p.logger.Debug("optimistic block fetch failed, ignoring",
					zap.Uint64("block_num", requestedBlock), zap.Error(err))
				return
			}

			p.Shutdown(err)
		}
	}()
}

// resetOptimisticallyPolledBlocks drops every optimistically fetched block and
// invalidates the batches currently in flight.
func (p *BlockPoller[C]) resetOptimisticallyPolledBlocks() {
	p.optimisticallyPolledBlocksLock.Lock()
	defer p.optimisticallyPolledBlocksLock.Unlock()

	p.optimisticallyPolledBlocks = map[uint64]*BlockItem{}
	p.optimisticEpoch++
}

func (p *BlockPoller[C]) loadNextBlocks(requestedBlock uint64, numberOfBlockToFetch int) error {
	p.optimisticallyPolledBlocksLock.Lock()
	epoch := p.optimisticEpoch
	p.optimisticallyPolledBlocksLock.Unlock()

	nailer := dhammer.NewNailer(numberOfBlockToFetch, func(ctx context.Context, blockToFetch uint64) (*BlockItem, error) {
		var blockItem *BlockItem
		err := derr.Retry(p.fetchBlockRetryCount, func(ctx context.Context) error {
			clients := p.clients.DuplicateAndStartAt(int(blockToFetch % uint64(numberOfBlockToFetch)))
			bi, err := rpc.WithClients(clients, func(ctx context.Context, client C) (*BlockItem, error) {
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
			if p.optimisticEpoch == epoch {
				p.optimisticallyPolledBlocks[blockItem.blockNumber] = blockItem
			}
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

	if nailer.Err() != nil {
		return fmt.Errorf("failed optimistically fetch blocks starting at %d: %w", requestedBlock, nailer.Err())
	}

	return nil
}

func (p *BlockPoller[C]) requestBlock(blockNumber uint64, numberOfBlockToFetch int) (*BlockItem, error) {
	p.logger.Info("requesting block", zap.Uint64("block_num", blockNumber), zap.Bool("keep", false))

	lastLog := time.Time{}
	for {
		if p.IsTerminating() {
			return nil, fmt.Errorf("block poller is terminating")
		}

		p.optimisticallyPolledBlocksLock.Lock()
		blockItem, found := p.optimisticallyPolledBlocks[blockNumber]
		p.optimisticallyPolledBlocksLock.Unlock()
		if !found {
			p.triggerLoadNextBlocks(blockNumber, numberOfBlockToFetch, false)

			if time.Since(lastLog) > 2*time.Second {
				p.logger.Debug("waiting for block to be fetched", zap.Uint64("block_num", blockNumber))
				lastLog = time.Now()
			}

			time.Sleep(optimisticPollInterval)
			continue
		} else if numberOfBlockToFetch > 1 && !p.fetching.Load() {
			// Optimistically anticipate the next iterations.
			//
			// This only applies to batched polling. With a batch size of 1 the poller is
			// explicitly configured to fetch one block at a time, and that is also the
			// only mode where `delayBetweenFetch` is honoured, so reading ahead there
			// would both defeat the setting and waste RPC quota on a block the run loop
			// may never ask for.
			highestPolled := blockNumber

			p.optimisticallyPolledBlocksLock.Lock()
			for key := range p.optimisticallyPolledBlocks {
				if key > highestPolled {
					highestPolled = key
				}
				// Cleanup old blocks
				if key < blockNumber {
					delete(p.optimisticallyPolledBlocks, key)
				}
			}
			p.optimisticallyPolledBlocksLock.Unlock()

			if highestPolled < blockNumber+uint64(numberOfBlockToFetch) {
				p.logger.Info("anticipating future block polls", zap.Uint64("block_num", blockNumber), zap.Uint64("max", highestPolled))
				p.triggerLoadNextBlocks(highestPolled+1, numberOfBlockToFetch, true)
			}
		}

		p.logger.Info("block was optimistically polled", zap.Uint64("block_num", blockNumber), zap.Bool("keep", false))
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

	p.resetOptimisticallyPolledBlocks()

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
