package info

import (
	"fmt"
	"strings"

	registry "github.com/pinax-network/graph-networks-libs/packages/golang/lib"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"github.com/streamingfast/substreams/networks"
)

var DefaultInfoResponseFiller = func(firstStreamableBlock *pbbstream.Block, resp *pbfirehose.InfoResponse, validate bool) error {
	resp.FirstStreamableBlockId = firstStreamableBlock.Id

	networksWithFirehose := networks.GetFirehoseRegistry()

	shortTypeURL := strings.TrimPrefix(firstStreamableBlock.Payload.TypeUrl, "type.googleapis.com/")
	for _, protocol := range networksWithFirehose {
		if protocol.Firehose.BlockType == shortTypeURL {
			resp.BlockIdEncoding = BlockIdEncodingForNetwork(protocol)
			break
		}
	}

	if !validate {
		if resp.ChainName == "" {
			// still try to fill the chain name if it is not given
			if chain := networksWithFirehose.FindByGenesisBlock(firstStreamableBlock.Number, firstStreamableBlock.Id); chain != nil {
				resp.ChainName = chain.ShortName
				resp.ChainNameAliases = chain.Aliases
			}
		}
		return nil
	}

	if resp.ChainName != "" {
		if chain := networksWithFirehose.Find(resp.ChainName); chain != nil {
			if firstStreamableBlock.Number == uint64(chain.Genesis.Height) && chain.Genesis.Hash != firstStreamableBlock.Id { // we don't check if the firstStreamableBlock is something other than our well-known genesis block
				return fmt.Errorf("chain name defined in flag: %q inconsistent with the genesis block ID %q (expected: %q)", resp.ChainName, ox(firstStreamableBlock.Id), ox(chain.Genesis.Hash))
			}
			resp.ChainName = chain.ShortName // ensure we use the canonical name if the user provided one of the aliases
			resp.ChainNameAliases = chain.Aliases
		} else if chain := networksWithFirehose.FindByGenesisBlock(firstStreamableBlock.Number, firstStreamableBlock.Id); chain != nil {
			return fmt.Errorf("chain name defined in flag: %q inconsistent with the one discovered from genesis block %q", resp.ChainName, chain.ShortName)
		}
	} else {
		if chain := networksWithFirehose.FindByGenesisBlock(firstStreamableBlock.Number, firstStreamableBlock.Id); chain != nil {
			resp.ChainName = chain.ShortName
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

// BlockIdEncodingForNetwork returns the InfoResponse_BlockIdEncoding for a given network based on its Firehose.BytesEncoding.
func BlockIdEncodingForNetwork(network *registry.Network) pbfirehose.InfoResponse_BlockIdEncoding {
	if network == nil || network.Firehose == nil {
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_UNSET
	}
	switch network.Firehose.BytesEncoding {
	case "hex":
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX
	case "0xhex":
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_0X_HEX
	case "base58":
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_BASE58
	case "base64":
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_BASE64
	case "base64url":
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_BASE64URL
	default:
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_UNSET
	}
}

func ox(s string) string {
	return "0x" + s
}
