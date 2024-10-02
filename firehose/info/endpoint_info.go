package info

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/hub"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type InfoServer struct {
	sync.Mutex

	validate       bool
	responseFiller func(block *pbbstream.Block, resp *pbfirehose.InfoResponse, validate bool) error
	response       *pbfirehose.InfoResponse
	ready          chan struct{}
	initDone       bool
	initError      error
	logger         *zap.Logger
}

func (s *InfoServer) Info(ctx context.Context, request *pbfirehose.InfoRequest) (*pbfirehose.InfoResponse, error) {
	select {
	case <-s.ready:
		return s.response, nil
	default:
		return nil, fmt.Errorf("info server not ready")
	}
}

func NewInfoServer(
	chainName string,
	chainNameAliases []string,
	blockIDEncoding pbfirehose.InfoResponse_BlockIdEncoding,
	blockFeatures []string,
	firstStreamableBlock uint64,
	validate bool,
	responseFiller func(block *pbbstream.Block, resp *pbfirehose.InfoResponse, validate bool) error,
	logger *zap.Logger,
) *InfoServer {

	resp := &pbfirehose.InfoResponse{
		ChainName:               chainName,
		ChainNameAliases:        chainNameAliases,
		BlockIdEncoding:         blockIDEncoding,
		BlockFeatures:           blockFeatures,
		FirstStreamableBlockNum: firstStreamableBlock,
	}

	return &InfoServer{
		responseFiller: responseFiller,
		response:       resp,
		validate:       validate,
		ready:          make(chan struct{}),
		logger:         logger,
	}
}

func validateInfoResponse(resp *pbfirehose.InfoResponse) error {
	switch {
	case resp.ChainName == "":
		return fmt.Errorf("chain name is not set")
	case resp.BlockIdEncoding == pbfirehose.InfoResponse_BLOCK_ID_ENCODING_UNSET:
		return fmt.Errorf("block id encoding is not set")
	case resp.FirstStreamableBlockId == "":
		return fmt.Errorf("first streamable block id is not set")
	}

	return nil
}

// multiple apps (firehose, substreams...) can initialize the same server, we only need one
func (s *InfoServer) Init(ctx context.Context, fhub *hub.ForkableHub, mergedBlocksStore dstore.Store, oneBlockStore dstore.Store, logger *zap.Logger) error {
	s.Lock()
	defer func() {
		s.initDone = true
		s.Unlock()
	}()

	if s.initDone {
		return s.initError
	}

	if err := s.init(ctx, fhub, mergedBlocksStore, oneBlockStore, logger); err != nil {
		s.initError = err
		return err
	}

	return nil
}

func (s *InfoServer) getBlockFromMergedBlocksStore(ctx context.Context, blockNum uint64, mergedBlocksStore dstore.Store) *pbbstream.Block {
	for {
		if ctx.Err() != nil {
			return nil
		}

		block, err := bstream.FetchBlockFromMergedBlocksStore(ctx, blockNum, mergedBlocksStore)
		if err != nil {
			time.Sleep(time.Millisecond * 500)
			continue
		}
		return block
	}
}

func (s *InfoServer) getBlockFromForkableHub(ctx context.Context, blockNum uint64, forkableHub *hub.ForkableHub) *pbbstream.Block {
	for {
		if ctx.Err() != nil {
			return nil
		}

		block := forkableHub.GetBlock(s.response.FirstStreamableBlockNum, "")
		if block == nil {
			time.Sleep(time.Millisecond * 500)
			continue
		}
		return block
	}

}

func (s *InfoServer) getBlockFromOneBlockStore(ctx context.Context, blockNum uint64, oneBlockStore dstore.Store) *pbbstream.Block {
	for {
		if ctx.Err() != nil {
			return nil
		}

		block, err := bstream.FetchBlockFromOneBlockStore(ctx, blockNum, "", oneBlockStore)
		if err != nil {
			time.Sleep(time.Millisecond * 500)
			continue
		}
		return block
	}
}

// init tries to fetch the first streamable block from the different sources and fills the response with it
// returns an error if it is incomplete
// it can be called only once
func (s *InfoServer) init(ctx context.Context, fhub *hub.ForkableHub, mergedBlocksStore dstore.Store, oneBlockStore dstore.Store, logger *zap.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	// cancel is later and depends on s.validate

	ch := make(chan *pbbstream.Block)

	if fhub != nil {
		go func() {
			select {
			case ch <- s.getBlockFromForkableHub(ctx, s.response.FirstStreamableBlockNum, fhub):
			case <-ctx.Done():
			}
		}()
	}

	go func() {
		select {
		case ch <- s.getBlockFromMergedBlocksStore(ctx, s.response.FirstStreamableBlockNum, mergedBlocksStore):
		case <-ctx.Done():
		}
	}()

	go func() {
		select {
		case ch <- s.getBlockFromOneBlockStore(ctx, s.response.FirstStreamableBlockNum, oneBlockStore):
		case <-ctx.Done():
		}
	}()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				loglevel := zapcore.WarnLevel
				if !s.validate {
					loglevel = zapcore.DebugLevel
				}
				logger.Log(loglevel, "waiting to read the first_streamable_block before starting firehose/substreams endpoints",
					zap.Uint64("first_streamable_block", s.response.FirstStreamableBlockNum),
					zap.Stringer("merged_blocks_store", mergedBlocksStore.BaseURL()),
					zap.Stringer("one_block_store", oneBlockStore.BaseURL()),
				)
			}
		}
	}()

	if !s.validate {
		// in this case we don't wait for an answer, but we still try to fill the response
		go func() {
			defer cancel()
			select {
			case blk := <-ch:
				if err := s.responseFiller(blk, s.response, s.validate); err != nil {
					logger.Warn("unable to fill and validate info response", zap.Error(err))
				}
			case <-ctx.Done():
			}
			if err := validateInfoResponse(s.response); err != nil {
				logger.Warn("info response", zap.Error(err))
			}
			close(s.ready)
			cancel()
		}()

		return nil
	}
	defer cancel()

	select {
	case blk := <-ch:
		if err := s.responseFiller(blk, s.response, s.validate); err != nil {
			return fmt.Errorf("%w -- use --ignore-advertise-validation to skip these checks", err)
		}
	case <-ctx.Done():
		return fmt.Errorf("%w: no block found for first streamable block %d in the stores or on live source -- use --ignore-advertise-validation to skip these checks", ctx.Err(), s.response.FirstStreamableBlockNum)
	}

	if err := validateInfoResponse(s.response); err != nil {
		return err
	}

	close(s.ready)
	return nil
}
