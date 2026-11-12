package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/streamingfast/dmetrics"
)

var mergedBlocksLag = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Name: "merged_blocks_lag",
	Help: "Number of blocks between the last finalized block received live and the last block found in merged-blocks files, from the point of view of the app serving streams",
}, []string{"app"})

// MergedBlocksLag reports how many blocks the last finalized block of an app's live hub
// is ahead of the last block it found in the merged-blocks store. With a merger keeping
// up, the value moves between 1 and a little over one merged-blocks file; it keeps
// growing when the merger is stopped or too slow.
//
// A hub shared by several apps reports the same value for each of them.
type MergedBlocksLag struct {
	apps []string
}

var _ prometheus.Collector = (*MergedBlocksLag)(nil)

func NewMergedBlocksLag(apps ...string) *MergedBlocksLag {
	return &MergedBlocksLag{apps: apps}
}

func (m *MergedBlocksLag) SetUint64(blocks uint64) {
	for _, app := range m.apps {
		mergedBlocksLag.WithLabelValues(app).Set(float64(blocks))
	}
}

// Collect implements prometheus.Collector.
func (m *MergedBlocksLag) Collect(ch chan<- prometheus.Metric) {
	mergedBlocksLag.Collect(ch)
}

// Describe implements prometheus.Collector.
func (m *MergedBlocksLag) Describe(ch chan<- *prometheus.Desc) {
	mergedBlocksLag.Describe(ch)
}

func init() {
	dmetrics.PrometheusRegister(mergedBlocksLag)
}
