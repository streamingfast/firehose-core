package mindreader

import (
	"github.com/streamingfast/dmetrics"
)

var testModeMetrics = dmetrics.NewSet(dmetrics.PrefixNameWith("reader_test_mode"))

func init() {
	testModeMetrics.Register()
}

// Counter metrics
var TestModeBlocksSeen = testModeMetrics.NewCounter("blocks_seen_total", "Total number of blocks seen in test mode (includes all blocks attempted)")
var TestModeBlocksReorg = testModeMetrics.NewCounter("blocks_reorg_total", "Total number of blocks skipped due to re-org (block ID mismatch)")
var TestModeBlocksFetchFailure = testModeMetrics.NewCounter("blocks_fetch_failure_total", "Total number of blocks that failed to fetch from production")
var TestModeBlocksCompared = testModeMetrics.NewCounter("blocks_compared_total", "Total number of blocks successfully compared (matched + mismatched)")
var TestModeBlocksComparedMatched = testModeMetrics.NewCounter("blocks_compared_matched_total", "Total number of blocks that matched production")
var TestModeBlocksComparedMismatched = testModeMetrics.NewCounter("blocks_compared_mismatched_total", "Total number of blocks that did not match production")

// Gauge metrics for percentages
var TestModeSuccessPercentage = testModeMetrics.NewGauge("success_percentage", "Percentage of compared blocks that matched production")
var TestModeFailurePercentage = testModeMetrics.NewGauge("failure_percentage", "Percentage of compared blocks that did not match production")
