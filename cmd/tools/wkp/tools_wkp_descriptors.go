package wkp

import (
	"fmt"
	"io"
	"maps"
	"os"
	"slices"

	"github.com/spf13/cobra"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/proto/wkp"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

func toolsWKPDescriptorsRunner[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) firecore.CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		outputPath := "well-known-descriptors.binpb"
		if len(args) > 0 {
			outputPath = args[0]
		}

		descriptors := collectSortedDescriptors()

		fds := &descriptorpb.FileDescriptorSet{File: descriptors}

		b, err := proto.Marshal(fds)
		if err != nil {
			return fmt.Errorf("marshaling FileDescriptorSet: %w", err)
		}

		var w io.Writer
		if outputPath == "-" {
			w = os.Stdout
		} else {
			f, err := os.Create(outputPath)
			if err != nil {
				return fmt.Errorf("creating output file %q: %w", outputPath, err)
			}
			defer f.Close()
			w = f
		}

		if _, err := w.Write(b); err != nil {
			return fmt.Errorf("writing output: %w", err)
		}

		if outputPath != "-" {
			logger.Info("exported well-known descriptors",
				zap.String("path", outputPath),
				zap.Int("file_count", len(descriptors)),
				zap.Int("bytes", len(b)),
			)
		}

		return nil
	}
}

// collectSortedDescriptors returns all well-known proto file descriptors in a
// deterministic topological order: dependencies always precede dependants, and
// siblings are ordered alphabetically by file name.
func collectSortedDescriptors() []*descriptorpb.FileDescriptorProto {
	all := wkp.WellKnownProtos()

	fileMap := make(map[string]*descriptorpb.FileDescriptorProto, len(all))
	for _, d := range all {
		fileMap[d.GetName()] = d
	}

	// Visit in sorted name order so that the topological traversal is deterministic.
	sortedNames := slices.Sorted(maps.Keys(fileMap))

	visited := make(map[string]bool, len(sortedNames))
	result := make([]*descriptorpb.FileDescriptorProto, 0, len(sortedNames))

	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true

		d := fileMap[name]
		deps := slices.Clone(d.Dependency)
		slices.Sort(deps)
		for _, dep := range deps {
			if _, exists := fileMap[dep]; exists {
				visit(dep)
			}
		}

		result = append(result, d)
	}

	for _, name := range sortedNames {
		visit(name)
	}

	return result
}
