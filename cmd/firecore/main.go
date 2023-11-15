package main

import (
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/nodemanager"
	pbbstream "github.com/streamingfast/pbgo/sf/bstream/v1"
)

func main() {
	firecore.Main(&firecore.Chain[*pbbstream.Block]{
		ShortName:            "near",
		LongName:             "NEAR",
		ExecutableName:       "near-firehose-indexer",
		FullyQualifiedModule: "github.com/streamingfast/firehose-near",
		Version:              version,

		Protocol:        "NEA",
		ProtocolVersion: 1,

		BlockFactory: func() firecore.Block { return new(pbbstream.Block) },

		ConsoleReaderFactory: nodemanager.NewConsoleReader,

		Tools: &firecore.ToolsConfig[*pbnear.Block]{
			BlockPrinter: printBlock,
		},
	})
}

// Version value, injected via go build `ldflags` at build time, **must** not be removed or inlined
var version = "dev"
