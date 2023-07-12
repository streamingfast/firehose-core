package firecore

import (
	"fmt"
	"sync"

	"github.com/spf13/cobra"
	"github.com/streamingfast/substreams/pipeline"
	"github.com/streamingfast/substreams/wasm"
	"google.golang.org/protobuf/proto"
)

var registerSSOnce sync.Once

func registerCommonSubstreamsFlags(cmd *cobra.Command) {
	registerSSOnce.Do(func() {
		cmd.Flags().Uint64("substreams-state-bundle-size", uint64(1_000), "Interval in blocks at which to save store snapshots and output caches")
		cmd.Flags().String("substreams-state-store-url", "{sf-data-dir}/localdata", "where substreams state data are stored")
		cmd.Flags().StringArray("substreams-rpc-endpoints", nil, "Remote endpoints to contact to satisfy Substreams 'eth_call's")
		cmd.Flags().String("substreams-rpc-cache-store-url", "{sf-data-dir}/rpc-cache", "where rpc cache will be store call responses")
		cmd.Flags().Uint64("substreams-rpc-cache-chunk-size", uint64(1_000), "RPC cache chunk size in block")
	})
}

func getSubstreamsBlockMessageType[B Block](chain *Chain[B]) string {
	return string(proto.MessageName(chain.BlockFactory()))
}

func getSubstreamsExtensions[B Block](chain *Chain[B]) ([]wasm.WASMExtensioner, []pipeline.PipelineOptioner, error) {
	var wasmExtensions []wasm.WASMExtensioner
	var pipelineOptions []pipeline.PipelineOptioner

	if chain.RegisterSubstreamsExtensions != nil {
		extensions, err := chain.RegisterSubstreamsExtensions(chain)
		if err != nil {
			return nil, nil, fmt.Errorf("register substreams extensions failed: %w", err)
		}

		for _, extension := range extensions {
			wasmExtensions = append(wasmExtensions, extension.WASMExtensioner)
			pipelineOptions = append(pipelineOptions, extension.PipelineOptioner)
		}
	}

	return wasmExtensions, pipelineOptions, nil
}

type SubstreamsExtension struct {
	PipelineOptioner pipeline.PipelineOptioner
	WASMExtensioner  wasm.WASMExtensioner
}
