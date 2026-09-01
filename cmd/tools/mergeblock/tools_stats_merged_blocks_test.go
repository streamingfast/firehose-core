package mergeblock

import (
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadAnnotation(t *testing.T) {
	tests := []struct {
		name     string
		size     int64
		metadata map[string]string
		expect   annotatedFile
		expectOK bool
	}{
		{
			name:     "complete",
			size:     1000,
			metadata: map[string]string{dataSizeMetadataKey: "4096", itemCountMetadataKey: "100", timestampMetadataKey: "2025-10-12 10:23:12"},
			expect: annotatedFile{
				month: "2025-10",
				tally: mergedBlocksTally{files: 1, blocks: 100, compressed: 1000, uncompressed: 4096},
			},
			expectOK: true,
		},
		{name: "no metadata at all", size: 1000},
		{
			name:     "missing item count",
			size:     1000,
			metadata: map[string]string{dataSizeMetadataKey: "4096", timestampMetadataKey: "2025-10-12 10:23:12"},
		},
		{
			name:     "timestamp in another format",
			size:     1000,
			metadata: map[string]string{dataSizeMetadataKey: "4096", itemCountMetadataKey: "100", timestampMetadataKey: "2025-10-12T10:23:12Z"},
		},
		{
			name:     "data size is not a number",
			size:     1000,
			metadata: map[string]string{dataSizeMetadataKey: "4 KiB", itemCountMetadataKey: "100", timestampMetadataKey: "2025-10-12 10:23:12"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			object := mergedBlocksObject{attrs: &storage.ObjectAttrs{Size: test.size, Metadata: test.metadata}}

			file, ok := readAnnotation(object)
			require.Equal(t, test.expectOK, ok)
			if test.expectOK {
				assert.Equal(t, test.expect, file)
			}
		})
	}
}

func TestMergedBlocksTally(t *testing.T) {
	var tally mergedBlocksTally
	tally.add(mergedBlocksTally{files: 1, blocks: 100, compressed: 1000, uncompressed: 4000})
	tally.add(mergedBlocksTally{files: 1, blocks: 100, compressed: 1000, uncompressed: 2000})

	assert.Equal(t, mergedBlocksTally{files: 2, blocks: 200, compressed: 2000, uncompressed: 6000}, tally)
	assert.Equal(t, 3.0, tally.compressionRatio())
	assert.Equal(t, 30.0, tally.uncompressedPerBlock())
	assert.Equal(t, 10.0, tally.compressedPerBlock())

	// An empty tally divides by nothing.
	var empty mergedBlocksTally
	assert.Equal(t, 0.0, empty.compressionRatio())
	assert.Equal(t, 0.0, empty.uncompressedPerBlock())
	assert.Equal(t, 0.0, empty.compressedPerBlock())
}

func TestDescribeBlockRange(t *testing.T) {
	assert.Equal(t, "whole store", describeBlockRange(0, 0))
	assert.Equal(t, "1,000,000 and up", describeBlockRange(1000000, 0))
	assert.Equal(t, "1,000,000 to 2,000,000 (exclusive)", describeBlockRange(1000000, 2000000))
}
