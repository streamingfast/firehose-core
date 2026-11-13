package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestMergedBlocksLag(t *testing.T) {
	NewMergedBlocksLag("firehose").SetUint64(12)
	assert.Equal(t, 12.0, testutil.ToFloat64(mergedBlocksLag.WithLabelValues("firehose")))

	// a hub shared by two apps reports the same value for each of them
	NewMergedBlocksLag("firehose", "substreams-tier1").SetUint64(450)
	assert.Equal(t, 450.0, testutil.ToFloat64(mergedBlocksLag.WithLabelValues("firehose")))
	assert.Equal(t, 450.0, testutil.ToFloat64(mergedBlocksLag.WithLabelValues("substreams-tier1")))
}
