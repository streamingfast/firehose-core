package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func gatherDrift(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)

	out := make(map[string]float64)
	for _, mf := range mfs {
		if mf.GetName() != "source_head_block_time_drift" {
			continue
		}
		for _, m := range mf.GetMetric() {
			var source string
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "source" {
					source = lp.GetValue()
				}
			}
			out[source] = m.GetGauge().GetValue()
		}
	}
	return out
}

func TestSourceHeadTimeDriftGrowsWhileSilent(t *testing.T) {
	drift := newSourceHeadTimeDrift("relayer")
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(drift))

	drift.SetBlockTime("reader-1", time.Now().Add(-2*time.Second))
	drift.SetBlockTime("reader-2", time.Now())

	first := gatherDrift(t, reg)
	require.Contains(t, first, "reader-1")
	require.Contains(t, first, "reader-2")
	assert.InDelta(t, 2.0, first["reader-1"], 0.5)

	wait := 200 * time.Millisecond
	time.Sleep(wait)

	// no SetBlockTime in between: drift must grow by the elapsed time
	second := gatherDrift(t, reg)
	assert.InDelta(t, first["reader-1"]+wait.Seconds(), second["reader-1"], 0.1)
	assert.InDelta(t, first["reader-2"]+wait.Seconds(), second["reader-2"], 0.1)

	// a new block resets the drift for that source only
	drift.SetBlockTime("reader-1", time.Now())
	third := gatherDrift(t, reg)
	assert.Less(t, third["reader-1"], 0.1)
	assert.Greater(t, third["reader-2"], wait.Seconds())
}

func TestSourceHeadTimeDriftLabels(t *testing.T) {
	drift := newSourceHeadTimeDrift("relayer")
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(drift))

	drift.SetBlockTime("reader-1", time.Now())

	mfs, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, mfs, 1)
	require.Len(t, mfs[0].GetMetric(), 1)

	labels := make(map[string]string)
	for _, lp := range mfs[0].GetMetric()[0].GetLabel() {
		labels[lp.GetName()] = lp.GetValue()
	}
	assert.Equal(t, "relayer", labels["app"])
	assert.Equal(t, "reader-1", labels["source"])
}
