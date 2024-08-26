package info

import (
	"fmt"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	wellknown "github.com/streamingfast/firehose-core/well-known"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
)

var DefaultInfoResponseFiller = func(firstStreamableBlock *pbbstream.Block, resp *pbfirehose.InfoResponse, validate bool) error {
	resp.FirstStreamableBlockId = firstStreamableBlock.Id

	for _, protocol := range wellknown.WellKnownProtocols {
		if protocol.BlockType == firstStreamableBlock.Payload.TypeUrl {
			resp.BlockIdEncoding = protocol.BytesEncoding
			break
		}
	}

	if !validate {
		if resp.ChainName == "" {
			// still try to fill the chain name if it is not given
			if chain := wellknown.WellKnownProtocols.ChainByGenesisBlock(firstStreamableBlock.Number, firstStreamableBlock.Id); chain != nil {
				resp.ChainName = chain.Name
				resp.ChainNameAliases = chain.Aliases
			}
		}
		return nil
	}

	if resp.ChainName != "" {
		if chain := wellknown.WellKnownProtocols.ChainByName(resp.ChainName); chain != nil {
			if firstStreamableBlock.Number == chain.GenesisBlockNumber && chain.GenesisBlockID != firstStreamableBlock.Id { // we don't check if the firstStreamableBlock is something other than our well-known genesis block
				return fmt.Errorf("chain name defined in flag: %q inconsistent with the genesis block ID %q (expected: %q)", resp.ChainName, ox(firstStreamableBlock.Id), ox(chain.GenesisBlockID))
			}
			resp.ChainName = chain.Name // ensure we use the canonical name if the user provided one of the aliases
			resp.ChainNameAliases = chain.Aliases
		} else if chain := wellknown.WellKnownProtocols.ChainByGenesisBlock(firstStreamableBlock.Number, firstStreamableBlock.Id); chain != nil {
			return fmt.Errorf("chain name defined in flag: %q inconsistent with the one discovered from genesis block %q", resp.ChainName, chain.Name)
		}
	} else {
		if chain := wellknown.WellKnownProtocols.ChainByGenesisBlock(firstStreamableBlock.Number, firstStreamableBlock.Id); chain != nil {
			resp.ChainName = chain.Name
			resp.ChainNameAliases = chain.Aliases
		}
	}

	// Extra validation for ethereum blocks
	if firstStreamableBlock.Payload.TypeUrl == "type.googleapis.com/sf.ethereum.type.v2.Block" {
		var seenDetailLevel bool
		for _, feature := range resp.BlockFeatures {
			if feature == "base" || feature == "extended" || feature == "hybrid" {
				seenDetailLevel = true
				break
			}
		}
		if !seenDetailLevel {
			return fmt.Errorf("ethereum blocks are used without setting detail level in 'advertise-block-features': expected one of 'base', 'extended' or 'hybrid' (or use 'firehose-ethereum' binary instead to serve this chain and get automatic detection/validation)")
		}
	}

	return nil
}

func ox(s string) string {
	return "0x" + s
}
