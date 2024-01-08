package main

import (
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	firecore "github.com/streamingfast/firehose-core"
	fhCMD "github.com/streamingfast/firehose-core/cmd"
)

func main() {
	firecore.UnsafeRunningFromFirecore = true

	fhCMD.Main(&firecore.Chain[*pbbstream.Block]{
		ShortName:            "core",      //used to compose the binary name
		LongName:             "CORE",      //only used to compose cmd title and description
		ExecutableName:       "fire-core", //only used to set default value of reader-node-path, we should not provide a default value anymore ...
		FullyQualifiedModule: "github.com/streamingfast/firehose-core",
		Version:              version,
		BlockFactory:         func() firecore.Block { return new(pbbstream.Block) },
		ConsoleReaderFactory: firecore.NewConsoleReader,
		Tools:                &firecore.ToolsConfig[*pbbstream.Block]{},
	})
}

// Version value, injected via go build `ldflags` at build time, **must** not be removed or inlined
var version = "dev"
