package node_manager

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dmetrics"
	coremetrics "github.com/streamingfast/firehose-core/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testAppReadiness avoids a nil dereference in the manager's readiness loop, which
// kicks in one second after the first block is seen.
func testAppReadiness(app string) *dmetrics.AppReadiness {
	return dmetrics.NewSet().NewAppReadiness(app)
}

func finalizedBlockNumberFor(t *testing.T, reg *prometheus.Registry, app string) (float64, bool) {
	t.Helper()

	mfs, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range mfs {
		if mf.GetName() != "finalized_block_number" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "app" && lp.GetValue() == app {
					return m.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

func TestMetricsAndReadinessManagerReportsFinalizedBlockNumber(t *testing.T) {
	const app = "test-finalized-reader"

	finalizedBlockNumber := coremetrics.NewFinalizedBlockNumber(app)
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(finalizedBlockNumber))

	manager := NewMetricsAndReadinessManager(nil, nil, nil, testAppReadiness(app), 0, WithFinalizedBlockNumberMetric(finalizedBlockNumber))
	go manager.Launch()

	require.NoError(t, manager.UpdateHeadBlock(&pbbstream.Block{Number: 100, LibNum: 90}))

	require.Eventually(t, func() bool {
		value, found := finalizedBlockNumberFor(t, reg, app)
		return found && value == 90
	}, 2*time.Second, 10*time.Millisecond)
}

func TestMetricsAndReadinessManagerWithoutFinalizedBlockNumberMetric(t *testing.T) {
	const app = "test-finalized-reader-disabled"

	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(coremetrics.NewFinalizedBlockNumber(app)))

	manager := NewMetricsAndReadinessManager(nil, nil, nil, testAppReadiness(app), 0)
	go manager.Launch()

	require.NoError(t, manager.UpdateHeadBlock(&pbbstream.Block{Number: 100, LibNum: 90}))

	time.Sleep(100 * time.Millisecond)
	_, found := finalizedBlockNumberFor(t, reg, app)
	assert.False(t, found, "no finalized block number must be reported when the option is not given")
}
