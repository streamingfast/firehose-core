package firecore

import (
	"strconv"
	"time"

	"github.com/streamingfast/dstore"
)

// The custom object metadata carried by each merged-blocks file. The merger writes all three
// as it uploads a bundle, `firecore tools annotate-merged-blocks` backfills them on files
// written before that, and `firecore tools stats-merged-blocks` reports from them without
// downloading anything.
const (
	// MergedBlocksDataSizeMetadataKey holds the decimal number of bytes the file holds once
	// decompressed.
	MergedBlocksDataSizeMetadataKey = "datasize"

	// MergedBlocksItemCountMetadataKey holds the decimal number of blocks the file holds.
	MergedBlocksItemCountMetadataKey = "itemcount"

	// MergedBlocksTimestampMetadataKey holds the time of the file's first block, formatted as
	// MergedBlocksTimestampLayout in UTC.
	MergedBlocksTimestampMetadataKey = "timestamp"

	MergedBlocksTimestampLayout = "2006-01-02 15:04:05"
)

// MergedBlocksMetadata describes one merged-blocks file. The three entries are always written
// together: a reader that finds only some of them cannot tell whether they describe the same
// version of the file, so it must treat the file as unannotated.
func MergedBlocksMetadata(dataSize, blockCount int64, firstBlockTime time.Time) map[string]string {
	return map[string]string{
		MergedBlocksDataSizeMetadataKey:  strconv.FormatInt(dataSize, 10),
		MergedBlocksItemCountMetadataKey: strconv.FormatInt(blockCount, 10),
		MergedBlocksTimestampMetadataKey: firstBlockTime.UTC().Format(MergedBlocksTimestampLayout),
	}
}

// MergedBlocksMetadataSupported reports whether the store keeps custom metadata on an object
// that can be read back from a listing, which is what makes the annotation worth writing.
// Google Cloud Storage is the only backend this is used with.
func MergedBlocksMetadataSupported(store dstore.Store) bool {
	return store.BaseURL().Scheme == "gs"
}
