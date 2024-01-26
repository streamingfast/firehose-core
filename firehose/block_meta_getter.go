package firehose

import (
	"context"
	"errors"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/hub"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/derr"
	"github.com/streamingfast/dmetering"
	"github.com/streamingfast/dstore"
	blockmeta "github.com/streamingfast/firehose-core/block-meta"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type BlockMetaGetter struct {
	blockMetaStore    *blockmeta.Store
	forkedBlocksStore dstore.Store
	hub               *hub.ForkableHub
}

func NewBlockMetaGetter(
	blockMetaStore *blockmeta.Store,
	forkedBlocksStore dstore.Store,
	hub *hub.ForkableHub,
) *BlockMetaGetter {
	return &BlockMetaGetter{
		blockMetaStore:    blockMetaStore,
		forkedBlocksStore: forkedBlocksStore,
		hub:               hub,
	}
}

func (g *BlockMetaGetter) GetByNum(
	ctx context.Context,
	num uint64,
	logger *zap.Logger,
) (out *pbbstream.BlockMeta, err error) {
	reqLogger := logger.With(
		zap.Uint64("by_number", num),
	)

	// check for block in live segment: Hub
	if g.hub != nil && num > g.hub.LowestBlockNum() {
		if blk := g.hub.GetBlock(num, ""); blk != nil {
			reqLogger.Info("block meta request", zap.String("source", "hub"), zap.Bool("found", true))
			return blk.ToBlocKMeta(), nil
		}
		reqLogger.Info("block meta request", zap.String("source", "hub"), zap.Bool("found", false))
		return nil, status.Error(codes.NotFound, "live block not found in hub")
	}

	// FIXME: What about metering? It's not really block here but there is a cost to call block
	// meta, so should we meter it somehow?

	err = derr.RetryContext(ctx, 3, func(ctx context.Context) error {
		// All errors are retried, even if the block is not found, because the block meta store
		// could have been delayed a bit, we give it a chance to catch up.
		out, err = g.blockMetaStore.GetBlockMetaByNumber(ctx, num)
		return err
	})
	if out != nil {
		return out, nil
	}

	if !errors.Is(err, blockmeta.ErrBlockNotFound) {
		reqLogger.Debug("block meta request not found", zap.Error(err))
		return nil, status.Error(codes.Internal, "block meta not found in store")
	}

	// Check for block in forkedBlocksStore
	if g.forkedBlocksStore != nil {
		forkedBlocksStore := g.forkedBlocksStore
		if clonable, ok := forkedBlocksStore.(dstore.Clonable); ok {
			var err error
			forkedBlocksStore, err = clonable.Clone(ctx)
			if err != nil {
				return nil, err
			}
			forkedBlocksStore.SetMeter(dmetering.GetBytesMeter(ctx))
		}

		if blk, _ := bstream.FetchBlockMetaFromOneBlockStore(ctx, num, "", forkedBlocksStore); blk != nil {
			reqLogger.Info("block meta request", zap.String("source", "forked_blocks"), zap.Bool("found", true))
			return blk, nil
		}
	}

	reqLogger.Info("block meta request", zap.Bool("found", false), zap.Error(err))
	return nil, status.Error(codes.NotFound, "block not found")
}

func (g *BlockMetaGetter) GetByHash(
	ctx context.Context,
	id string,
	logger *zap.Logger,
) (out *pbbstream.BlockMeta, err error) {
	reqLogger := logger.With(
		zap.String("by_hash", id),
	)

	if blk := g.hub.GetBlockByHash(id); blk != nil {
		reqLogger.Info("block meta request", zap.String("source", "hub"), zap.Bool("found", true))
		return blk.ToBlocKMeta(), nil
	}

	// FIXME: What about metering? It's not really block here but there is a cost to call block
	// meta, so should we meter it somehow?

	err = derr.RetryContext(ctx, 3, func(ctx context.Context) error {
		// All errors are retried, even if the block is not found, because the block meta store
		// could have been delayed a bit, we give it a chance to catch up.
		out, err = g.blockMetaStore.GetBlockMetaByHash(ctx, id)
		return err
	})
	if out != nil {
		return out, nil
	}

	if !errors.Is(err, blockmeta.ErrBlockNotFound) {
		reqLogger.Debug("block meta request not found", zap.Error(err))
		return nil, status.Error(codes.Internal, "block meta not found in store")
	}

	// Check for block in forkedBlocksStore
	if g.forkedBlocksStore != nil {
		forkedBlocksStore := g.forkedBlocksStore
		if clonable, ok := forkedBlocksStore.(dstore.Clonable); ok {
			var err error
			forkedBlocksStore, err = clonable.Clone(ctx)
			if err != nil {
				return nil, err
			}
			forkedBlocksStore.SetMeter(dmetering.GetBytesMeter(ctx))
		}

		if blk, _ := bstream.FetchBlockMetaByHashFromOneBlockStore(ctx, id, forkedBlocksStore); blk != nil {
			reqLogger.Info("block meta request", zap.String("source", "forked_blocks"), zap.Bool("found", true))
			return blk, nil
		}
	}

	reqLogger.Info("block meta request", zap.Bool("found", false), zap.Error(err))
	return nil, status.Error(codes.NotFound, "block not found")
}
