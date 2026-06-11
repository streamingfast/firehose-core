package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/streamingfast/dmetrics"
)

// SourceHeadBlockTimeDrift tracks, per source, the time elapsed since the
// last block received from that source. The drift is computed at scrape
// time, so it keeps growing while a source is silent.
var SourceHeadBlockTimeDrift = newSourceHeadTimeDrift("relayer")

type sourceHeadTimeDrift struct {
	desc *prometheus.Desc

	mu             sync.Mutex
	lastBlockTimes map[string]time.Time
}

func newSourceHeadTimeDrift(app string) *sourceHeadTimeDrift {
	return &sourceHeadTimeDrift{
		desc: prometheus.NewDesc(
			"source_head_block_time_drift",
			"Number of seconds away from real-time, by source",
			[]string{"source"},
			prometheus.Labels{"app": app},
		),
		lastBlockTimes: make(map[string]time.Time),
	}
}

func (s *sourceHeadTimeDrift) SetBlockTime(source string, blockTime time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastBlockTimes[source] = blockTime
}

func (s *sourceHeadTimeDrift) Describe(ch chan<- *prometheus.Desc) {
	ch <- s.desc
}

func (s *sourceHeadTimeDrift) Collect(ch chan<- prometheus.Metric) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for source, blockTime := range s.lastBlockTimes {
		ch <- prometheus.MustNewConstMetric(s.desc, prometheus.GaugeValue, time.Since(blockTime).Seconds(), source)
	}
}

func init() {
	dmetrics.PrometheusRegister(SourceHeadBlockTimeDrift)
}
