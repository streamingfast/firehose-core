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
		name        string
		size        int64
		lowBlockNum uint64
		metadata    map[string]string
		expect      annotatedFile
		expectOK    bool
	}{
		{
			name:        "complete",
			size:        1000,
			lowBlockNum: 10123000,
			metadata:    map[string]string{dataSizeMetadataKey: "4096", itemCountMetadataKey: "100", timestampMetadataKey: "2025-10-12 10:23:12"},
			expect: annotatedFile{
				month: "2025-10",
				tally: mergedBlocksTally{files: 1, blocks: 100, compressed: 1000, uncompressed: 4096, firstBlock: 10123000},
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
			object := mergedBlocksObject{attrs: &storage.ObjectAttrs{Size: test.size, Metadata: test.metadata}, lowBlockNum: test.lowBlockNum}

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
	tally.add(mergedBlocksTally{files: 1, blocks: 100, compressed: 1000, uncompressed: 4000, firstBlock: 200})
	tally.add(mergedBlocksTally{files: 1, blocks: 100, compressed: 1000, uncompressed: 2000, firstBlock: 100})

	// The bucket takes the lowest first-block of the files added to it, whatever the order.
	assert.Equal(t, mergedBlocksTally{files: 2, blocks: 200, compressed: 2000, uncompressed: 6000, firstBlock: 100}, tally)
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

func annotatedObject(lowBlockNum uint64, size int64, timestamp string) mergedBlocksObject {
	return mergedBlocksObject{
		attrs: &storage.ObjectAttrs{Size: size, Metadata: map[string]string{
			dataSizeMetadataKey:  "4000",
			itemCountMetadataKey: "100",
			timestampMetadataKey: timestamp,
		}},
		lowBlockNum: lowBlockNum,
	}
}

func unannotatedObject(lowBlockNum uint64, size int64) mergedBlocksObject {
	return mergedBlocksObject{attrs: &storage.ObjectAttrs{Size: size}, lowBlockNum: lowBlockNum}
}

func TestStatsAccumulator(t *testing.T) {
	acc := newStatsAccumulator()

	// Unannotated files ahead of the annotated ones, the shape of a store annotated
	// only from some point on.
	assert.False(t, acc.add(unannotatedObject(0, 300)))
	assert.False(t, acc.add(unannotatedObject(100, 500)))
	assert.True(t, acc.add(annotatedObject(200, 1000, "2025-10-12 10:23:12")))
	assert.True(t, acc.add(annotatedObject(300, 2000, "2025-11-01 00:00:00")))

	assert.Equal(t, uint64(4), acc.seen)
	// The first block seen is the first file listed, annotated or not.
	assert.Equal(t, uint64(0), acc.firstBlock)
	assert.Equal(t, uint64(300), acc.lastBlock)

	// Only the file count, compressed size and first block of unannotated files are known.
	assert.Equal(t, mergedBlocksTally{files: 2, compressed: 800, firstBlock: 0}, acc.unannotated)

	assert.Equal(t, mergedBlocksTally{files: 2, blocks: 200, compressed: 3000, uncompressed: 8000, firstBlock: 200}, acc.annotated)
	require.Len(t, acc.months, 2)
	assert.Equal(t, int64(1000), acc.months["2025-10"].compressed)
	assert.Equal(t, int64(2000), acc.months["2025-11"].compressed)

	// Annotated files hold 100 blocks per file and inflate 8000/3000 times, so the two
	// unannotated files are taken to hold 200 blocks and 800*8000/3000 bytes.
	assert.Equal(t, mergedBlocksTally{files: 2, blocks: 200, compressed: 800, uncompressed: 2133, firstBlock: 0}, acc.estimatedUnannotated())
	assert.Equal(t, mergedBlocksTally{files: 4, blocks: 400, compressed: 3800, uncompressed: 10133, firstBlock: 0}, acc.total())
}

func TestStatsAccumulatorNothingAnnotated(t *testing.T) {
	acc := newStatsAccumulator()
	acc.add(unannotatedObject(100, 300))
	acc.add(unannotatedObject(200, 500))

	// Nothing to estimate from: blocks and uncompressed size stay 0, the rest is exact.
	assert.Equal(t, mergedBlocksTally{files: 2, compressed: 800, firstBlock: 100}, acc.estimatedUnannotated())
	assert.Equal(t, mergedBlocksTally{files: 2, compressed: 800, firstBlock: 100}, acc.total())
}

func TestStatsAccumulatorAllAnnotated(t *testing.T) {
	acc := newStatsAccumulator()
	acc.add(annotatedObject(100, 1000, "2025-10-12 10:23:12"))

	assert.Equal(t, mergedBlocksTally{}, acc.estimatedUnannotated())
	assert.Equal(t, acc.annotated, acc.total())
}

func TestNewStatsReport(t *testing.T) {
	acc := &statsAccumulator{
		months: map[string]*mergedBlocksTally{
			"2025-11": {files: 1, blocks: 100, compressed: 1000, uncompressed: 2000, firstBlock: 1800},
			"2025-10": {files: 2, blocks: 200, compressed: 2000, uncompressed: 6000, firstBlock: 1000},
		},
		annotated:   mergedBlocksTally{files: 3, blocks: 300, compressed: 3000, uncompressed: 8000, firstBlock: 1100},
		unannotated: mergedBlocksTally{files: 3, compressed: 1500, firstBlock: 1000},
		seen:        6,
		firstBlock:  1000,
		lastBlock:   1900,
	}
	cfg := statsConfig{startBlock: 1000, stopBlock: 2000, chainName: "eth-mainnet"}

	report := newStatsReport(cfg, acc, 1500*time.Millisecond)

	assert.Equal(t, "eth-mainnet", report.ChainName)
	assert.Equal(t, uint64(1000), report.StartBlock)
	assert.Equal(t, uint64(2000), report.StopBlock)
	require.NotNil(t, report.FirstBlockSeen)
	assert.Equal(t, uint64(1000), *report.FirstBlockSeen)
	require.NotNil(t, report.LastBlockSeen)
	assert.Equal(t, uint64(1900), *report.LastBlockSeen)
	assert.Equal(t, uint64(3), report.UnannotatedFiles)
	assert.Equal(t, 1.5, report.ListedInSeconds)

	// Months come out sorted.
	require.Len(t, report.Months, 2)
	assert.Equal(t, "2025-10", report.Months[0].Month)
	assert.Equal(t, "2025-11", report.Months[1].Month)
	assert.Equal(t, statsBucket{
		Month:                     "2025-10",
		FirstBlock:                1000,
		Files:                     2,
		Blocks:                    200,
		CompressedBytes:           2000,
		UncompressedBytes:         6000,
		CompressionRatio:          3,
		CompressedBytesPerBlock:   10,
		UncompressedBytesPerBlock: 30,
	}, report.Months[0])

	// The unannotated files are estimated at 100 blocks per file and 8000/3000 compression.
	assert.Equal(t, statsBucket{
		FirstBlock:                1000,
		Files:                     3,
		Blocks:                    300,
		CompressedBytes:           1500,
		UncompressedBytes:         4000,
		CompressionRatio:          4000.0 / 1500.0,
		CompressedBytesPerBlock:   5,
		UncompressedBytesPerBlock: 4000.0 / 300.0,
	}, report.Unannotated)

	// The total carries no month and includes the estimate.
	assert.Equal(t, "", report.Total.Month)
	assert.Equal(t, uint64(1000), report.Total.FirstBlock)
	assert.Equal(t, int64(6), report.Total.Files)
	assert.Equal(t, int64(600), report.Total.Blocks)
	assert.Equal(t, int64(4500), report.Total.CompressedBytes)
	assert.Equal(t, int64(12000), report.Total.UncompressedBytes)

	out, err := json.Marshal(report)
	require.NoError(t, err)
	assert.Contains(t, string(out), `"months":[{"month":"2025-10"`)
	assert.Contains(t, string(out), `"unannotated":{"first_block":1000,"files":3,"blocks":300`)
	assert.Contains(t, string(out), `"unannotated_files":3`)
}

func TestNewStatsReportEmpty(t *testing.T) {
	report := newStatsReport(statsConfig{}, newStatsAccumulator(), 0)

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
