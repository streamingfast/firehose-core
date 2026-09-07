package utils

import (
	"fmt"
	"os"
	"strconv"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
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
