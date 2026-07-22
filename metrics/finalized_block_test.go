package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFinalizedBlockNumberPerApp(t *testing.T) {
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(NewFinalizedBlockNumber("relayer")))

	NewFinalizedBlockNumber("relayer").SetUint64(100)
	NewFinalizedBlockNumber("merger").SetUint64(90)

	mfs, err := reg.Gather()
	require.NoError(t, err)
	require.Len(t, mfs, 1)
	assert.Equal(t, "finalized_block_number", mfs[0].GetName())

	values := make(map[string]float64)
	for _, m := range mfs[0].GetMetric() {
		var app string
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "app" {
				app = lp.GetValue()
			}
		}
		values[app] = m.GetGauge().GetValue()
	}

	assert.Equal(t, map[string]float64{"relayer": 100, "merger": 90}, values)
}
