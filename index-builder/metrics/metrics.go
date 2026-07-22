package metrics

import (
	"github.com/streamingfast/dmetrics"
	coremetrics "github.com/streamingfast/firehose-core/metrics"
)

var MetricSet = dmetrics.NewSet()

var HeadBlockTimeDrift = MetricSet.NewHeadTimeDrift("block-indexer")
var HeadBlockNumber = MetricSet.NewHeadBlockNumber("block-indexer")

// FinalizedBlockNumber always tracks HeadBlockNumber: the index builder streams with
// `FinalBlocksOnly`, so its head is finalized by construction. Reporting the LIB number
// carried by those blocks would trail our own head and read as if the index builder
// were lagging finality.
var FinalizedBlockNumber = coremetrics.NewFinalizedBlockNumber("block-indexer")
var AppReadiness = MetricSet.NewAppReadiness("block-indexer")
