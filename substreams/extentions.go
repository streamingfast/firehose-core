package substreams

import (
	"github.com/streamingfast/substreams/pipeline"
	"github.com/streamingfast/substreams/wasm"
)

type Extension struct {
	PipelineOptioner pipeline.PipelineOptioner
	WASMExtensioner  wasm.WASMExtensioner
}
