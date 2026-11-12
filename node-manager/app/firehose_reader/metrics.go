package firehose_reader

import (
	"sync"

	"github.com/streamingfast/dmetrics"
	coremetrics "github.com/streamingfast/firehose-core/metrics"
)

var metrics = dmetrics.NewSet(dmetrics.PrefixNameWith("reader_node_firehose"))

const HeadDriftServiceName = "reader_node_firehose"

var BlockWriteCount = metrics.NewCounter("block_write_count", "The number of blocks written by the Firehose reader to one-block store")
var HeadBlockTimeDrift = metrics.NewHeadTimeDrift(HeadDriftServiceName)
var HeadBlockRelativeTime = metrics.NewHeadBlockRelativeTime(HeadDriftServiceName)
var HeadBlockNumber = metrics.NewHeadBlockNumber(HeadDriftServiceName)
var FinalizedBlockNumber = coremetrics.NewFinalizedBlockNumber(HeadDriftServiceName)

// AppReadiness is created by registerMetrics: creating it exports 'ready' for this app at 0,
// which must not happen in processes that don't run 'reader-node-firehose'.
var AppReadiness *dmetrics.AppReadiness

var registerMetricsOnce sync.Once

// registerMetrics exports this app's metrics. It is called when the app is created, so a
// process exports them only when it runs 'reader-node-firehose'.
func registerMetrics() {
	registerMetricsOnce.Do(func() {
		metrics.Register()
		AppReadiness = metrics.NewAppReadiness(HeadDriftServiceName)
	})
}
