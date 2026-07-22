package node_manager

import (
	"os"
	"strconv"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"

	"github.com/streamingfast/dmetrics"
	coremetrics "github.com/streamingfast/firehose-core/metrics"
	"go.uber.org/atomic"
)

// metricsInitialLivenessMaxLatency is used to determine the initial max latency for liveness checks
// Can be overridden with METRICS_INITIAL_LIVENESS_MAX_LATENCY environment variable (in seconds)
var metricsInitialLivenessMaxLatency = getMetricsInitialLivenessMaxLatency()

func getMetricsInitialLivenessMaxLatency() time.Duration {
	if envVal := os.Getenv("METRICS_INITIAL_LIVENESS_MAX_LATENCY"); envVal != "" {
		if seconds, err := strconv.Atoi(envVal); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return time.Second * 5 // default value
}

type Readiness interface {
	IsReady() bool
}

type MetricsAndReadinessManager struct {
	headBlockChan          chan *pbbstream.Block
	headBlockTimeDrift     *dmetrics.HeadTimeDrift
	headBlockNumber        *dmetrics.HeadBlockNum
	headBlockRelativeDrift *dmetrics.HeadBlockRelativeTime
	finalizedBlockNumber   *coremetrics.FinalizedBlockNum
	appReadiness           *dmetrics.AppReadiness
	readinessProbe         *atomic.Bool

	// ReadinessMaxLatency is the max delta between head block time and
	// now before /healthz starts returning success
	readinessMaxLatency time.Duration

	lastSeenBlock *atomic.Pointer[pbbstream.Block]
}

// MetricsAndReadinessOption customizes a MetricsAndReadinessManager. It exists so that new
// metrics can be added without breaking chain repositories calling NewMetricsAndReadinessManager.
type MetricsAndReadinessOption func(*MetricsAndReadinessManager)

// WithFinalizedBlockNumberMetric reports the LIB number of each head block seen, so that
// head to finality drift can be computed from `head_block_number - finalized_block_number`.
func WithFinalizedBlockNumberMetric(finalizedBlockNumber *coremetrics.FinalizedBlockNum) MetricsAndReadinessOption {
	return func(m *MetricsAndReadinessManager) {
		m.finalizedBlockNumber = finalizedBlockNumber
	}
}

func NewMetricsAndReadinessManager(headBlockTimeDrift *dmetrics.HeadTimeDrift, headBlockNumber *dmetrics.HeadBlockNum, headBlockRelativeDrift *dmetrics.HeadBlockRelativeTime, appReadiness *dmetrics.AppReadiness, readinessMaxLatency time.Duration, options ...MetricsAndReadinessOption) *MetricsAndReadinessManager {
	manager := &MetricsAndReadinessManager{
		headBlockChan:          make(chan *pbbstream.Block, 1), // just for non-blocking, saving a few nanoseconds here
		readinessProbe:         atomic.NewBool(false),
		appReadiness:           appReadiness,
		headBlockTimeDrift:     headBlockTimeDrift,
		headBlockNumber:        headBlockNumber,
		headBlockRelativeDrift: headBlockRelativeDrift,
		readinessMaxLatency:    readinessMaxLatency,

		lastSeenBlock: atomic.NewPointer[pbbstream.Block](nil),
	}

	for _, option := range options {
		option(manager)
	}

	return manager
}

func (m *MetricsAndReadinessManager) setReadinessProbeOn() {
	m.readinessProbe.CompareAndSwap(false, true)
	m.appReadiness.SetReady()
}

func (m *MetricsAndReadinessManager) setReadinessProbeOff() {
	m.readinessProbe.CompareAndSwap(true, false)
	m.appReadiness.SetNotReady()
}

func (m *MetricsAndReadinessManager) IsReady() bool {
	return m.readinessProbe.Load()
}

func (m *MetricsAndReadinessManager) Launch() {
	var reachedChainHead bool
	for {
		select {
		case block := <-m.headBlockChan:
			m.lastSeenBlock.Store(block)

			// metrics
			if m.headBlockNumber != nil {
				m.headBlockNumber.SetUint64(block.Number)
			}

			// Set before the zero timestamp check below, a block without a timestamp still
			// carries a valid LIB number.
			if m.finalizedBlockNumber != nil {
				m.finalizedBlockNumber.SetUint64(block.LibNum)
			}

			bt := block.Time()
			if bt.IsZero() { // never act upon zero timestamps
				continue
			}
			if m.headBlockTimeDrift != nil {
				m.headBlockTimeDrift.SetBlockTime(bt)
			}

			if m.headBlockRelativeDrift != nil {
				if reachedChainHead {
					m.headBlockRelativeDrift.SetLastBlock(bt)
				} else {
					reachedChainHead = time.Since(bt) < metricsInitialLivenessMaxLatency
				}
			}

		case <-time.After(time.Second):
		}

		block := m.lastSeenBlock.Load()
		if block == nil {
			continue
		}

		// readiness
		if m.readinessMaxLatency == 0 || time.Since(block.Time()) < m.readinessMaxLatency {
			m.setReadinessProbeOn()
		} else {
			m.setReadinessProbeOff()
		}
	}
}

func (m *MetricsAndReadinessManager) UpdateHeadBlock(block *pbbstream.Block) error {
	m.headBlockChan <- block
	return nil
}

func (m *MetricsAndReadinessManager) GetHeadBlock() bstream.BlockRef {
	block := m.lastSeenBlock.Load()
	if block == nil {
		return bstream.BlockRefEmpty
	}

	return block.AsRef()
}
