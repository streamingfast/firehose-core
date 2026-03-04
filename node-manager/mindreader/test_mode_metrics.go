package mindreader

import (
	"github.com/streamingfast/dmetrics"
)

var testModeMetrics = dmetrics.NewSet(dmetrics.PrefixNameWith("reader_test_mode"))

func init() {
	testModeMetrics.Register()
}

// Counter metrics
var TestModeBlocksCompared = testModeMetrics.NewCounter("blocks_compared_total", "Total number of blocks compared in test mode")
var TestModeBlocksMatched = testModeMetrics.NewCounter("blocks_matched_total", "Total number of blocks that matched production")
var TestModeBlocksMismatched = testModeMetrics.NewCounter("blocks_mismatched_total", "Total number of blocks that did not match production")

// Gauge metrics for percentages
var TestModeSuccessPercentage = testModeMetrics.NewGauge("success_percentage", "Percentage of blocks that matched production")
var TestModeFailurePercentage = testModeMetrics.NewGauge("failure_percentage", "Percentage of blocks that did not match production")
