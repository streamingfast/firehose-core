package info

import (
	"context"
	"fmt"
	"strconv"
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

	blockType      string
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
	blockType string,
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
		blockType:      blockType,
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

// detectBundleSizeMismatch checks the merged-blocks store for a bundle-size
// misconfiguration in either direction, both of which silently skip blocks and
// stall the stream on unlinkable blocks far from the root cause:
//
//   - Configured smaller than the files (e.g. reading a 200-block store with the
//     default 100): the merged-blocks consumer would look for 0000000100 which
//     does not exist.
//   - Configured bigger than the files (e.g. reading a 100-block store with 1000):
//     the merged-blocks consumer jumps over the blocks between the sparse
//     boundaries.
//
// Merged-blocks stores have no holes in normal operation (an empty bundle is
// still written as an empty file), so detection is done from the listing alone:
// the gap between the first two file boundaries is the actual bundle size and
// must equal the configured one. A file read is only needed for the degenerate
// single-file store, where there is no second boundary to measure against
// (merged-file writes are atomic, so that lone file is a *completed* bundle whose
// highest block reveals the size).
//
//   - gap == configured: aligned.
//   - gap < configured: files are smaller than configured; the merged-blocks
//     consumer jumps over blocks. Set the flag down to the gap.
//   - gap > configured: files are bigger than configured; the merged-blocks
//     consumer looks for boundaries that do not exist. Set the flag up to the gap.
//
// The merged-blocks consumer does not hard-fail on a mismatch on its own (it only
// warns about holes and unlinkable blocks), which is why this proactive check
// exists. Returns a nil error when nothing is off or the store is empty/unreadable.
func detectBundleSizeMismatch(ctx context.Context, mergedBlocksStore dstore.Store, firstStreamableBlock uint64) error {
	configured := bstream.DefaultMergedBlocksBundleSize
	if configured == 0 {
		return nil
	}
	lowBoundary := firstStreamableBlock - (firstStreamableBlock % configured)
	startFilename := fmt.Sprintf("%010d", lowBoundary)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var boundaries []uint64
	walkErr := mergedBlocksStore.WalkFrom(ctx, "", startFilename, func(filename string) error {
		num, err := strconv.ParseUint(filename, 10, 64)
		if err != nil {
			return nil // skip non merged-blocks files (indexes, etc.)
		}
		boundaries = append(boundaries, num)
		if len(boundaries) >= 2 {
			return dstore.StopIteration
		}
		return nil
	})
	if walkErr != nil && walkErr != dstore.StopIteration {
		return nil // listing failed; don't block startup on a best-effort check
	}
	if len(boundaries) == 0 {
		return nil // empty store: nothing to infer
	}

	first := boundaries[0]
	firstFile := fmt.Sprintf("%010d", first)

	if len(boundaries) >= 2 {
		gap := boundaries[1] - boundaries[0]
		if gap == configured {
			return nil // aligned
		}
		return fmt.Errorf("merged-blocks bundle size mismatch: configured %d but the store's files are %d blocks apart (%010d then %010d) -- reading it silently skips blocks; set --common-merged-blocks-bundle-size=%d to match the store",
			configured, gap, boundaries[0], boundaries[1], gap)
	}

	// Single completed bundle: no gap to measure, so read its content.
	maxBlock, overflow, ok := scanMergedFile(ctx, mergedBlocksStore, firstFile, first+configured)
	if !ok {
		return nil
	}
	if overflow { // file reaches into the next configured window -> bigger than configured
		return fmt.Errorf("merged-blocks bundle size mismatch: configured %d but the only bundle file %s contains block %d (at or beyond boundary %d) -- the store uses a larger bundle size, so reading it skips blocks; increase --common-merged-blocks-bundle-size to match the store",
			configured, firstFile, maxBlock, first+configured)
	}
	end := maxBlock + 1
	if end < configured && end%100 == 0 { // file closed on a smaller bundle boundary
		return fmt.Errorf("merged-blocks bundle size mismatch: configured %d but the only bundle file %s ends on block %d (a %d-block boundary) -- the store uses a smaller bundle size, which silently skips blocks; set --common-merged-blocks-bundle-size=%d to match the store",
			configured, firstFile, maxBlock, end, end)
	}
	return nil
}

// scanMergedFile reads a merged-blocks file and returns the highest block number
// it contains. It stops early, returning overflow=true, as soon as it sees a
// block whose number is >= overflowAt (pass 0 to disable early exit and read the
// whole file). Merged-file writes are atomic, so a present file is a complete
// bundle. Returns ok=false if the file cannot be read.
func scanMergedFile(ctx context.Context, store dstore.Store, filename string, overflowAt uint64) (highest uint64, overflow bool, ok bool) {
	reader, err := store.OpenObject(ctx, filename)
	if err != nil {
		return 0, false, false
	}
	defer reader.Close()

	blockReader, err := bstream.NewDBinBlockReader(reader)
	if err != nil {
		return 0, false, false
	}

	seen := false
	for {
		block, err := blockReader.Read()
		if block != nil {
			if block.Number > highest {
				highest = block.Number
			}
			seen = true
			if overflowAt != 0 && block.Number >= overflowAt {
				return highest, true, true
			}
		}
		if err != nil {
			break
		}
	}
	return highest, false, seen
}

// init tries to fetch the first streamable block from the different sources and fills the response with it
// returns an error if it is incomplete
// it can be called only once
func (s *InfoServer) init(ctx context.Context, fhub *hub.ForkableHub, mergedBlocksStore dstore.Store, oneBlockStore dstore.Store, logger *zap.Logger) error {
	if err := detectBundleSizeMismatch(ctx, mergedBlocksStore, s.response.FirstStreamableBlockNum); err != nil {
		if s.validate {
			return fmt.Errorf("%w -- use --ignore-advertise-validation to skip these checks", err)
		}
		logger.Warn("merged-blocks bundle size check", zap.Error(err))
	}

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

// GetBlockType returns the block type for the InfoServer, e.g. sf.ethereum.type.v2.Block, which
// is usually inferred from the networks registry and picked from the `advertised-chain-name` flag.
//
// It can be the empty string if the chain name was not passed or it was not a known chain (yet).
func (s *InfoServer) GetBlockType() string {
	return s.blockType
}
