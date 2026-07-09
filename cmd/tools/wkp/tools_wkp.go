package wkp

import (
	"github.com/spf13/cobra"
	. "github.com/streamingfast/cli"
	firecore "github.com/streamingfast/firehose-core"
	"go.uber.org/zap"
)

func NewToolsWKPCmd[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) *cobra.Command {
	return ToCobraCmd(Group(
		"wkp",
		"Well-known protocols related tools",

		Command(
			toolsWKPDescriptorsRunner(chain, logger),
			"descriptors [output-file]",
			"Export all well-known blockchain protobuf descriptors as a self-contained FileDescriptorSet",
			Description(`
				Exports every well-known blockchain protobuf descriptor registered in firehose-core
				as a serialized google.protobuf.FileDescriptorSet written to <output-file>
				(defaults to "well-known-descriptors.binpb"). Use "-" to write to stdout.

				The exported set is self-contained: all transitive imports, including google/protobuf/*
				well-known types, are included. No external resolution is needed to build a descriptor
				registry from this file.

				The output is deterministic: given the same embedded descriptors, re-running the
				command produces byte-identical output, enabling "is it up to date?" CI checks via a
				regenerate-and-diff workflow.

				source_code_info (proto field/message comments) is included when the embedded
				descriptors were generated with the BSR HTTP descriptor endpoint using ?source_info=true
				(see proto/generator/generator.go). Re-run the generator to embed updated descriptors.
			`),
		),
	))
}
