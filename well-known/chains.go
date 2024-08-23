package wellknown

import (
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
)

type WellKnownProtocol struct {
	Name          string
	BlockType     string
	BufBuildURL   string
	BytesEncoding pbfirehose.InfoResponse_BlockIdEncoding
	KnownChains   []*Chain
}

type Chain struct {
	Name    string
	Aliases []string
	// Genesis block here is actually the "lowest possible" first streamable block through firehose blocks.
	// In most cases, it matches the "genesis block" of the chain.
	GenesisBlockID     string
	GenesisBlockNumber uint64
}

type WellKnownProtocolList []WellKnownProtocol

var WellKnownProtocols = WellKnownProtocolList([]WellKnownProtocol{
	{
		Name:          "ethereum",
		BlockType:     "type.googleapis.com/sf.ethereum.type.v2.Block",
		BufBuildURL:   "buf.build/streamingfast/firehose-ethereum",
		BytesEncoding: pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX,
		KnownChains: []*Chain{
			{
				Name:               "mainnet",
				Aliases:            []string{"ethereum"},
				GenesisBlockID:     "d4e56740f876aef8c010b86a40d5f56745a118d0906a34e69aec8c0db1cb8fa3",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "sepolia",
				Aliases:            []string{},
				GenesisBlockID:     "25a5cc106eea7138acab33231d7160d69cb777ee0c2c553fcddf5138993e6dd9",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "holesky",
				Aliases:            []string{},
				GenesisBlockID:     "b5f7f912443c940f21fd611f12828d75b534364ed9e95ca4e307729a4661bde4",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "matic",
				Aliases:            []string{"polygon"},
				GenesisBlockID:     "a9c28ce2141b56c474f1dc504bee9b01eb1bd7d1a507580d5519d4437a97de1b",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "bsc",
				Aliases:            []string{"bnb", "bsc-mainnet"},
				GenesisBlockID:     "0d21840abff46b96c84b2ac9e10e4f5cdaeb5693cb665db62a2f3b02d2d57b5b",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "optimism",
				Aliases:            []string{},
				GenesisBlockID:     "7ca38a1916c42007829c55e69d3e9a73265554b586a499015373241b8a3fa48b",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "optimism-sepolia",
				Aliases:            []string{},
				GenesisBlockID:     "102de6ffb001480cc9b8b548fd05c34cd4f46ae4aa91759393db90ea0409887d",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "chapel",
				Aliases:            []string{"bsc-chapel", "bsc-testnet"},
				GenesisBlockID:     "6d3c66c5357ec91d5c43af47e234a939b22557cbb552dc45bebbceeed90fbe34",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "arbitrum-one",
				Aliases:            []string{"arb-one", "arbitrum"},
				GenesisBlockID:     "7ee576b35482195fc49205cec9af72ce14f003b9ae69f6ba0faef4514be8b442",
				GenesisBlockNumber: 0,
			},
			// We do not auto-discover avalanche because the genesis block ID is the same as their testnet fuji and we can't differentiate them
			//{
			//	Name:               "avalanche",
			//	Aliases:            []string{"avax"},
			//	GenesisBlockID:     "31ced5b9beb7f8782b014660da0cb18cc409f121f408186886e1ca3e8eeca96b",
			//	GenesisBlockNumber: 0,
			//},
		},
	},
	{
		Name:          "near",
		BlockType:     "type.googleapis.com/sf.near.type.v1.Block",
		BufBuildURL:   "buf.build/streamingfast/firehose-near",
		BytesEncoding: pbfirehose.InfoResponse_BLOCK_ID_ENCODING_BASE58,
		KnownChains: []*Chain{
			{
				Name:               "near-mainnet",
				Aliases:            []string{"near"},
				GenesisBlockID:     "CFAAJTVsw5y4GmMKNmuTNybxFJtapKcrarsTh5TPUyQf",
				GenesisBlockNumber: 9820214,
			},
			{
				Name:               "near-testnet",
				Aliases:            []string{},
				GenesisBlockID:     "fQURSjwQKZn8F98ayQjpndh85msJBu12FBkUY1gc5WA",
				GenesisBlockNumber: 42376923,
			},
		},
	},
	{
		Name:          "solana",
		BlockType:     "type.googleapis.com/sf.solana.type.v1.Block",
		BufBuildURL:   "buf.build/streamingfast/firehose-solana",
		BytesEncoding: pbfirehose.InfoResponse_BLOCK_ID_ENCODING_BASE58,
		KnownChains: []*Chain{
			{
				Name:               "solana-mainnet-beta",
				Aliases:            []string{"solana", "solana-mainnet"},
				GenesisBlockID:     "4sGjMW1sUnHzSxGspuhpqLDx6wiyjNtZAMdL4VZHirAn",
				GenesisBlockNumber: 0,
			},
		},
	},
	{
		Name:          "bitcoin",
		BlockType:     "type.googleapis.com/sf.bitcoin.type.v1.Block",
		BufBuildURL:   "buf.build/streamingfast/firehose-bitcoin",
		BytesEncoding: pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX,
		KnownChains: []*Chain{
			{
				Name:               "btc",
				Aliases:            []string{"bitcoin"},
				GenesisBlockID:     "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f",
				GenesisBlockNumber: 0,
			},
		},
	},
	{
		Name:          "antelope",
		BlockType:     "type.googleapis.com/sf.antelope.type.v1.Block",
		BufBuildURL:   "buf.build/pinax/firehose-antelope",
		BytesEncoding: pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX,
		KnownChains: []*Chain{
			{
				Name:               "eos",
				Aliases:            []string{"eos-mainnet"},
				GenesisBlockID:     "0000000267f3e2284b482f3afc2e724be1d6cbc1804532ec62d4e7af47c30693",
				GenesisBlockNumber: 2, // even though the genesis block is 1, it is never available through firehose/substreams
			},
			{
				Name:               "kylin",
				Aliases:            []string{},
				GenesisBlockID:     "00000002a1ec7ae214b9e43a904b6c010fb1260c9e8a12e5967bdbe451152a07",
				GenesisBlockNumber: 2, // even though the genesis block is 1, it is never available through firehose/substreams
			},
			{
				Name:               "jungle4",
				Aliases:            []string{},
				GenesisBlockID:     "00000002d61d836f51657f886a5bc55b18a731f7eace6565784328fbd051fc90",
				GenesisBlockNumber: 2, // even though the genesis block is 1, it is never available through firehose/substreams
			},
		},
	},
	{
		Name:          "arweave",
		BlockType:     "type.googleapis.com/sf.arweave.type.v1.Block",
		BufBuildURL:   "buf.build/pinax/firehose-arweave",
		BytesEncoding: pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX, // even though the usual encoding is base64url, firehose blocks are written with the hex-encoded version
		KnownChains: []*Chain{
			{
				Name:               "arweave",
				Aliases:            []string{},
				GenesisBlockID:     "ef0214ecaa252020230a5325719dfc2d9cec86123bc46926dad0c2251ed6be17b7112528dbe678fb2d31d6e6a0951244",
				GenesisBlockNumber: 0,
			},
		},
	},
	{
		Name:          "beacon",
		BlockType:     "type.googleapis.com/sf.beacon.type.v1.Block",
		BufBuildURL:   "buf.build/pinax/firehose-beacon",
		BytesEncoding: pbfirehose.InfoResponse_BLOCK_ID_ENCODING_0X_HEX,
		KnownChains: []*Chain{
			{
				Name:               "mainnet-cl",
				Aliases:            []string{},
				GenesisBlockID:     "0x4d611d5b93fdab69013a7f0a2f961caca0c853f87cfe9595fe50038163079360",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "sepolia-cl",
				Aliases:            []string{},
				GenesisBlockID:     "0xfb9b64fe445f76696407e1e3cc390371edff147bf712db86db6197d4b31ede43",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "holesky-cl",
				Aliases:            []string{},
				GenesisBlockID:     "0xab09edd9380f8451c3ff5c809821174a36dce606fea8b5ea35ea936915dbf889",
				GenesisBlockNumber: 0,
			},
		},
	},
	{
		Name:          "starknet",
		BlockType:     "type.googleapis.com/sf.starknet.type.v1.Block",
		BufBuildURL:   "buf.build/streamingfast/firehose-starknet",
		BytesEncoding: pbfirehose.InfoResponse_BLOCK_ID_ENCODING_0X_HEX,
		KnownChains: []*Chain{
			{
				Name:               "starknet-mainnet",
				Aliases:            []string{},
				GenesisBlockID:     "0x47c3637b57c2b079b93c61539950c17e868a28f46cdef28f88521067f21e943",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "starknet-testnet",
				Aliases:            []string{},
				GenesisBlockID:     "0x5c627d4aeb51280058bed93c7889bce78114d63baad1be0f0aeb32496d5f19c",
				GenesisBlockNumber: 0,
			},
		},
	},
	{
		Name:          "cosmos",
		BlockType:     "type.googleapis.com/sf.cosmos.type.v2.Block",
		BufBuildURL:   "buf.build/streamingfast/firehose-cosmos",
		BytesEncoding: pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX,
		KnownChains: []*Chain{
			{
				Name:               "injective-mainnet",
				Aliases:            []string{},
				GenesisBlockID:     "24c9714291a999b952859ee02ec9b233394fe743b07ea3578d432a4a2707b6af",
				GenesisBlockNumber: 1,
			},
			{
				Name:               "injective-testnet",
				Aliases:            []string{},
				GenesisBlockID:     "a9effb99c7bc3ba8c18a487ffffd800c137bc2b2f47f73c350f3ca27077044a1",
				GenesisBlockNumber: 37368800, // Not the real genesis block, but the other blocks are lost on the testnet
			},
		},
	},
	{
		Name:          "gear",
		BlockType:     "type.googleapis.com/sf.gear.type.v1.Block",
		BufBuildURL:   "buf.build/streamingfast/firehose-gear",
		BytesEncoding: pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX,
		KnownChains: []*Chain{
			{
				Name:               "vara-mainnet",
				Aliases:            []string{},
				GenesisBlockID:     "fe1b4c55fd4d668101126434206571a7838a8b6b93a6d1b95d607e78e6c53763",
				GenesisBlockNumber: 0,
			},
			{
				Name:               "vara-testnet",
				Aliases:            []string{},
				GenesisBlockID:     "525639f713f397dcf839bd022cd821f367ebcf179de7b9253531f8adbe5436d6",
				GenesisBlockNumber: 0,
			},
		},
	},
})

func (p WellKnownProtocolList) ChainByGenesisBlock(blockNum uint64, blockID string) *Chain {
	for _, protocol := range p {
		for _, chain := range protocol.KnownChains {
			if chain.GenesisBlockNumber == blockNum && chain.GenesisBlockID == blockID {
				return chain
			}
		}
	}
	return nil
}

func (p WellKnownProtocolList) ChainByName(name string) *Chain {
	for _, protocol := range p {
		for _, chain := range protocol.KnownChains {
			if chain.Name == name {
				return chain
			}
			for _, alias := range chain.Aliases {
				if alias == name {
					return chain
				}
			}
		}
	}
	return nil
}
