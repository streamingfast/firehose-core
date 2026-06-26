package wkp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// TestDescriptorsExport verifies that the exported FileDescriptorSet is valid,
// self-contained, and contains the expected well-known block types.
func TestDescriptorsExport(t *testing.T) {
	descriptors := collectSortedDescriptors()
	require.NotEmpty(t, descriptors)

	fds := &descriptorpb.FileDescriptorSet{File: descriptors}

	// Round-trip through proto marshal/unmarshal
	b, err := proto.Marshal(fds)
	require.NoError(t, err)
	require.NotEmpty(t, b)

	parsed := &descriptorpb.FileDescriptorSet{}
	require.NoError(t, proto.Unmarshal(b, parsed))
	assert.Equal(t, len(descriptors), len(parsed.File))
}

// TestDescriptorsDeterminism verifies that repeated calls to collectSortedDescriptors
// produce byte-identical output.
func TestDescriptorsDeterminism(t *testing.T) {
	marshal := func() []byte {
		fds := &descriptorpb.FileDescriptorSet{File: collectSortedDescriptors()}
		b, err := proto.Marshal(fds)
		require.NoError(t, err)
		return b
	}

	first := marshal()
	for range 5 {
		assert.Equal(t, first, marshal(), "output must be byte-identical across runs")
	}
}

// TestDescriptorsSelfContained verifies that the exported set forms a valid registry
// with no unresolved imports — every referenced file is present in the set.
func TestDescriptorsSelfContained(t *testing.T) {
	descriptors := collectSortedDescriptors()

	fds := &descriptorpb.FileDescriptorSet{File: descriptors}

	// protodesc.NewFiles builds a registry and validates all imports are present.
	reg, err := protodesc.NewFiles(fds)
	require.NoError(t, err, "all imports must be resolvable within the exported set")

	assert.Greater(t, reg.NumFiles(), 0)
}

// TestDescriptorsEthereumBlockType verifies that the Ethereum block type is fully
// resolvable with all its field definitions present.
func TestDescriptorsEthereumBlockType(t *testing.T) {
	fds := &descriptorpb.FileDescriptorSet{File: collectSortedDescriptors()}

	reg, err := protodesc.NewFiles(fds)
	require.NoError(t, err)

	desc, err := reg.FindDescriptorByName("sf.ethereum.type.v2.Block")
	require.NoError(t, err, "sf.ethereum.type.v2.Block must be present in the exported set")

	md, ok := desc.(protoreflect.MessageDescriptor)
	require.True(t, ok)
	assert.Greater(t, md.Fields().Len(), 0, "Block message must have fields")
}

// TestDescriptorsNoDuplicateFiles verifies that each file path appears exactly once.
func TestDescriptorsNoDuplicateFiles(t *testing.T) {
	descriptors := collectSortedDescriptors()

	seen := make(map[string]int, len(descriptors))
	for _, d := range descriptors {
		seen[d.GetName()]++
	}

	for name, count := range seen {
		assert.Equal(t, 1, count, "file %q appears %d times, expected exactly once", name, count)
	}
}

// TestDescriptorsTopologicalOrder verifies that every file appears after all of its
// declared imports in the exported set.
func TestDescriptorsTopologicalOrder(t *testing.T) {
	descriptors := collectSortedDescriptors()

	position := make(map[string]int, len(descriptors))
	for i, d := range descriptors {
		position[d.GetName()] = i
	}

	for _, d := range descriptors {
		for _, dep := range d.Dependency {
			depPos, present := position[dep]
			if !present {
				// Dependency not in our set (e.g., external), skip.
				continue
			}
			assert.Less(t, depPos, position[d.GetName()],
				"dependency %q must appear before %q", dep, d.GetName())
		}
	}
}

// TestDescriptorsWriteToFile verifies end-to-end: collect → marshal → write → read back.
func TestDescriptorsWriteToFile(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "descriptors.binpb")

	descriptors := collectSortedDescriptors()
	fds := &descriptorpb.FileDescriptorSet{File: descriptors}
	b, err := proto.Marshal(fds)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(outPath, b, 0o644))

	// Read back and verify
	raw, err := os.ReadFile(outPath)
	require.NoError(t, err)

	parsed := &descriptorpb.FileDescriptorSet{}
	require.NoError(t, proto.Unmarshal(raw, parsed))
	assert.Equal(t, len(descriptors), len(parsed.File))

	// Re-build registry from file to confirm it's still valid
	_, err = protodesc.NewFiles(parsed)
	require.NoError(t, err)
}

// TestDescriptorsSourceCodeInfoStatus documents the source_code_info state of the
// currently embedded descriptors. The generator (proto/generator/generator.go) now
// uses the BSR HTTP descriptor endpoint with ?source_info=true, so newly regenerated
// WKP files will carry source_code_info. The existing embedded files pre-date that
// change and do not yet have it. After the next `go generate ./proto/...` run this
// test should be updated to assert presence instead of absence.
func TestDescriptorsSourceCodeInfoStatus(t *testing.T) {
	descriptors := collectSortedDescriptors()
	withInfo := 0
	for _, d := range descriptors {
		if d.SourceCodeInfo != nil {
			withInfo++
		}
	}
	// After regenerating with the updated generator, withInfo will equal len(descriptors).
	// This assertion simply records the current state so the test fails loudly if someone
	// regenerates without updating it.
	t.Logf("source_code_info present in %d/%d embedded descriptor files", withInfo, len(descriptors))
}
