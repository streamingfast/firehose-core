package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/streamingfast/dmetrics"
	"github.com/stretchr/testify/require"
)

// readyValues returns the `ready` gauge per app, as exported.
func readyValues(t *testing.T) map[string]float64 {
	t.Helper()

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	values := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "ready" {
			continue
		}
		for _, m := range family.GetMetric() {
			for _, label := range m.GetLabel() {
				if label.GetName() == "app" {
					values[label.GetValue()] = m.GetGauge().GetValue()
				}
			}
		}
	}
	return values
}

func TestAppReadiness_ExportedOnlyOnceInitialized(t *testing.T) {
	readiness := NewAppReadiness(dmetrics.NewSet(), "test-app-not-running")
	_, exported := readyValues(t)["test-app-not-running"]
	require.False(t, exported, "an app that never starts must not export 'ready'")

	readiness.Init()
	value, exported := readyValues(t)["test-app-not-running"]
	require.True(t, exported)
	require.Equal(t, float64(0), value)

	readiness.SetReady()
	require.Equal(t, float64(1), readyValues(t)["test-app-not-running"])

	// Init again must not reset a ready app to 0.
	readiness.Init()
	require.Equal(t, float64(1), readyValues(t)["test-app-not-running"])
}

func TestAppReadiness_SetReadyWithoutInit(t *testing.T) {
	readiness := NewAppReadiness(dmetrics.NewSet(), "test-app-set-ready")
	readiness.SetReady()
	require.Equal(t, float64(1), readyValues(t)["test-app-set-ready"])
}
