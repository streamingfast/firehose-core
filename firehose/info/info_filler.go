package info

import (
	"fmt"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
)

var DefaultInfoResponseFiller = func(block *pbbstream.Block, resp *pbfirehose.InfoResponse) error {
	resp.FirstStreamableBlockId = block.Id

	switch block.Payload.TypeUrl {
	case "type.googleapis.com/sf.antelope.type.v1.Block":
		return fillInfoResponseForAntelope(block, resp)

	case "type.googleapis.com/sf.ethereum.type.v2.Block":
		return fillInfoResponseForEthereum(block, resp)

	case "type.googleapis.com/sf.cosmos.type.v1.Block":
		return fillInfoResponseForCosmos(block, resp)

	case "type.googleapis.com/sf.solana.type.v1.Block":
		return fillInfoResponseForSolana(block, resp)
	}

	return nil
}

// this is a simple helper, a full implementation would live in github.com/streamingfast/firehose-ethereum
func fillInfoResponseForEthereum(block *pbbstream.Block, resp *pbfirehose.InfoResponse) error {
	resp.BlockIdEncoding = pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX
	var seenBlockType bool
	for _, feature := range resp.BlockFeatures {
		if feature == "extended" || feature == "base" || feature == "hybrid" {
			seenBlockType = true
			break
		}
	}
	if !seenBlockType {
		return fmt.Errorf("invalid block features, missing 'base', 'extended' or 'hybrid'")
	}
	return nil
}

// this is a simple helper, a full implementation would live in github.com/pinax-network/firehose-antelope
func fillInfoResponseForAntelope(block *pbbstream.Block, resp *pbfirehose.InfoResponse) error {
	resp.BlockIdEncoding = pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX
	return nil
}

func fillInfoResponseForCosmos(block *pbbstream.Block, resp *pbfirehose.InfoResponse) error {
	resp.BlockIdEncoding = pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX
	return nil
}

func fillInfoResponseForSolana(block *pbbstream.Block, resp *pbfirehose.InfoResponse) error {
	resp.BlockIdEncoding = pbfirehose.InfoResponse_BLOCK_ID_ENCODING_BASE58
	return nil
}
