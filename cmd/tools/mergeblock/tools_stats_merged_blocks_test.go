package mergeblock

import (
	"encoding/json"
	"testing"
	"time"

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

func TestNewStatsReport(t *testing.T) {
	months := map[string]*mergedBlocksTally{
		"2025-11": {files: 1, blocks: 100, compressed: 1000, uncompressed: 2000},
		"2025-10": {files: 2, blocks: 200, compressed: 2000, uncompressed: 6000},
	}
	total := mergedBlocksTally{files: 3, blocks: 300, compressed: 3000, uncompressed: 8000}
	cfg := statsConfig{startBlock: 1000, stopBlock: 2000, chainName: "eth-mainnet"}

	report := newStatsReport(cfg, months, total, false, 1000, 1900, 4, 1500*time.Millisecond)

	assert.Equal(t, "eth-mainnet", report.ChainName)
	assert.Equal(t, uint64(1000), report.StartBlock)
	assert.Equal(t, uint64(2000), report.StopBlock)
	require.NotNil(t, report.FirstBlockSeen)
	assert.Equal(t, uint64(1000), *report.FirstBlockSeen)
	require.NotNil(t, report.LastBlockSeen)
	assert.Equal(t, uint64(1900), *report.LastBlockSeen)
	assert.Equal(t, uint64(4), report.UnannotatedFiles)
	assert.Equal(t, 1.5, report.ListedInSeconds)

	// Months come out sorted.
	require.Len(t, report.Months, 2)
	assert.Equal(t, "2025-10", report.Months[0].Month)
	assert.Equal(t, "2025-11", report.Months[1].Month)
	assert.Equal(t, statsBucket{
		Month:                     "2025-10",
		Files:                     2,
		Blocks:                    200,
		CompressedBytes:           2000,
		UncompressedBytes:         6000,
		CompressionRatio:          3,
		CompressedBytesPerBlock:   10,
		UncompressedBytesPerBlock: 30,
	}, report.Months[0])

	// The total carries no month.
	assert.Equal(t, "", report.Total.Month)
	assert.Equal(t, int64(3), report.Total.Files)

	out, err := json.Marshal(report)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"months":[{"month":"2025-10"`)
}

func TestNewStatsReportEmpty(t *testing.T) {
	report := newStatsReport(statsConfig{}, nil, mergedBlocksTally{}, true, 0, 0, 0, 0)

	assert.Nil(t, report.FirstBlockSeen)
	assert.Nil(t, report.LastBlockSeen)

	// An empty report still marshals to an empty months array, not null.
	out, err := json.Marshal(report)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"months":[]`)
	assert.Contains(t, string(out), `"first_block_seen":null`)
}

func TestDefaultChainName(t *testing.T) {
	assert.Equal(t, "eth-mainnet", defaultChainName("gs://mybucket/something/eth-mainnet/v1"))
	assert.Equal(t, "eth-mainnet", defaultChainName("gs://mybucket/something/eth-mainnet/v1/"))
	assert.Equal(t, "eth-mainnet", defaultChainName("gs://mybucket/eth-mainnet/v1?project=some-project"))
	// A one-element path is that element, the bucket is never the guess.
	assert.Equal(t, "eth-mainnet", defaultChainName("gs://mybucket/eth-mainnet"))
	assert.Equal(t, "eth-mainnet", defaultChainName("gs://mybucket/eth-mainnet/"))

	// A url with nothing but a bucket has no path to guess from.
	assert.Equal(t, "", defaultChainName("gs://mybucket"))
	assert.Equal(t, "", defaultChainName("gs://mybucket/"))
}
