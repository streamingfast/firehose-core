package metrics

import "github.com/streamingfast/dmetrics"

var MetricSet = dmetrics.NewSet()

var HeadBlockTimeDrift = MetricSet.NewHeadTimeDrift("block-meta")
var HeadBlockNumber = MetricSet.NewHeadBlockNumber("block-meta")
var AppReadiness = MetricSet.NewAppReadiness("block-meta")
