// Package metrics holds metrics that are shared by more than one Firehose app.
//
// Metrics defined here follow the `dmetrics` convention of a single package-level
// collector labelled by `app`, registered once at init time. Per-app instances are
// thin wrappers holding only the label value, which is what makes them safe when
// multiple apps run within the same process.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/streamingfast/dmetrics"
)

var finalizedBlockNumber = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "finalized_block_number",
	Help: "Block number of the last known finalized (irreversible) block",
}, []string{"app"})

// FinalizedBlockNum reports the last known finalized (irreversible) block number for a
// given app. Paired with `head_block_number`, it tells how far behind finality the head
// of that app is.
type FinalizedBlockNum struct {
	app string
}

var _ prometheus.Collector = (*FinalizedBlockNum)(nil)

func NewFinalizedBlockNumber(app string) *FinalizedBlockNum {
	return &FinalizedBlockNum{app: app}
}

func (f *FinalizedBlockNum) SetUint64(blockNum uint64) {
	finalizedBlockNumber.WithLabelValues(f.app).Set(float64(blockNum))
}

// Collect implements prometheus.Collector.
func (f *FinalizedBlockNum) Collect(ch chan<- prometheus.Metric) {
	finalizedBlockNumber.Collect(ch)
}

// Describe implements prometheus.Collector.
func (f *FinalizedBlockNum) Describe(ch chan<- *prometheus.Desc) {
	finalizedBlockNumber.Describe(ch)
}

func init() {
	dmetrics.PrometheusRegister(finalizedBlockNumber)
}
