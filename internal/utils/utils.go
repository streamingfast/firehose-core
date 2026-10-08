package utils

import (
	"fmt"
	"os"
	"strconv"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dmetrics"
	"go.uber.org/zap"
)

func GetEnvForceFinalityAfterBlocks() *uint64 {
	if fin := os.Getenv("FORCE_FINALITY_AFTER_BLOCKS"); fin != "" {
		if fin64, err := strconv.ParseInt(fin, 10, 64); err == nil {
			finu64 := uint64(fin64)
			return &finu64
		}
	}
	return nil
}

// GetEnvMergerMaxUnlinkableBlocks reads MERGER_MAX_UNLINKABLE_BLOCKS, the operator override for
// the merger's maxUnlinkableBlocks circuit breaker (normally bundleSize*4 — see merger.run()).
// Deployments running many one-block-file writers (readers/relayers), or chains fast enough
// that bundleSize*4 blocks cover only a few seconds of real time, can exceed that default
// during an ordinary reorg or brief writer hiccup and fatally crash-loop the merger. Returns nil
// (caller keeps its own default) if unset or invalid.
func GetEnvMergerMaxUnlinkableBlocks() *int {
	if max := os.Getenv("MERGER_MAX_UNLINKABLE_BLOCKS"); max != "" {
		if max64, err := strconv.ParseInt(max, 10, 64); err == nil {
			maxInt := int(max64)
			return &maxInt
		}
	}
	return nil
}

// ClampLibNum enforces the invariant that a block's LIB number cannot exceed its own block
// number. A buggy chain client can emit the opposite (e.g. firehose-geyser-plugin's 2026-10-08
// incident, where a Solana LibNum was one past its block's Number): propagated as-is, it moves
// bstream's forkable LIB past head, which stalls the relayer forever without panicking or
// failing health checks. Called unconditionally on every decoded block, chain-agnostic, so a bad
// value is clamped and surfaced instead of silently wedging the pipeline or crashing the reader.
func ClampLibNum(blk *pbbstream.Block, logger *zap.Logger, clampedCount *dmetrics.Counter) {
	if blk.LibNum <= blk.Number {
		return
	}

	logger.Error("block has lib_num greater than its own block number, clamping lib_num to block number",
		zap.Uint64("block_num", blk.Number),
		zap.String("block_id", blk.Id),
		zap.Uint64("original_lib_num", blk.LibNum),
	)

	clampedCount.Inc()
	blk.LibNum = blk.Number
}

// TweakBlockFinality forces a block's LIB to never be more than maxDistanceToBlock behind its
// own block number. The console-reader path (node-manager/mindreader) runs [ClampLibNum] on
// every block before reaching here, so blk.LibNum > blk.Number is unreachable from that caller;
// it panics instead of silently misbehaving if some other caller skips that precondition.
func TweakBlockFinality(blk *pbbstream.Block, maxDistanceToBlock uint64) {
	if blk.LibNum > blk.Number {
		distance := blk.LibNum - blk.Number
		panic(fmt.Sprintf("libnum cannot be greater than block number (lib_num=%d, block_num=%d, distance=%d, max_distance=%d)", blk.LibNum, blk.Number, distance, maxDistanceToBlock))
	}
	if blk.Number < maxDistanceToBlock {
		return // prevent uin64 underflow at the beginning of the chain
	}
	if (blk.Number - blk.LibNum) >= maxDistanceToBlock {
		blk.LibNum = blk.Number - maxDistanceToBlock // force finality
	}
}
