// Copyright 2019 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package relayer

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/firehose-core/relayer/metrics"
	"go.uber.org/zap"
)

// stuckDetectionPollInterval is how often the stuck detector re-checks head progress.
// It is independent of relayer-stuck-detection-threshold, which only controls how long
// a frozen head is tolerated before it is considered stuck.
const stuckDetectionPollInterval = 10 * time.Second

// wrapLiveSourceFactoryForStuckDetection wraps factory so that every block reaching its
// handler (i.e. a block that already cleared its source's realtime gate and the
// multiplexed source merge) also records its number in sourceMaxBlockNum. That number is
// the live-ingestion side of the stuck detector: it keeps advancing from sources alone,
// independently of whatever the hub/forkable does with the block afterward.
func wrapLiveSourceFactoryForStuckDetection(factory bstream.SourceFactory, sourceMaxBlockNum *atomic.Uint64) bstream.SourceFactory {
	return bstream.SourceFactory(func(h bstream.Handler) bstream.Source {
		tracked := bstream.HandlerFunc(func(blk *pbbstream.Block, obj any) error {
			raiseUint64(sourceMaxBlockNum, blk.Number)
			return h.ProcessBlock(blk, obj)
		})
		return factory(tracked)
	})
}

// raiseUint64 sets v to candidate if candidate is greater than v's current value.
func raiseUint64(v *atomic.Uint64, candidate uint64) {
	for {
		current := v.Load()
		if candidate <= current {
			return
		}
		if v.CompareAndSwap(current, candidate) {
			return
		}
	}
}

// stuckDetectorState is the pure decision logic behind the stuck detector, kept separate
// from the polling loop so it can be unit tested without a ticker or real metrics.
//
// The detector declares the relayer stuck when its emitted head has not advanced at all
// for threshold, while a source has, at that point, already delivered a block numbered
// past that head. A head that keeps advancing, even slowly (e.g. a reconnect replaying a
// backlog from the one-block store), continuously resets the clock and never trips.
type stuckDetectorState struct {
	initialized bool
	lastHeadNum uint64
	frozenSince time.Time
}

func (s stuckDetectorState) observe(now time.Time, headNum, sourceMaxBlockNum uint64, threshold time.Duration) (tripped bool, next stuckDetectorState) {
	if !s.initialized || headNum != s.lastHeadNum {
		return false, stuckDetectorState{initialized: true, lastHeadNum: headNum}
	}

	if s.frozenSince.IsZero() {
		s.frozenSince = now
		return false, s
	}

	if sourceMaxBlockNum <= headNum {
		return false, s
	}

	return now.Sub(s.frozenSince) >= threshold, s
}

// watchForStuckRelayer periodically checks whether the relayer's emitted head has frozen
// while a source keeps delivering blocks past it (see stuckDetectorState), and fatally
// shuts the relayer down for a restart if so. It mirrors the existing
// "consecutive unlinkable blocks" guard in bstream's hub package, which covers the
// complementary case: this detector catches a hub/forkable wedge that Linkable() alone
// does not see, because the blocks involved keep linking fine, they are just never
// turned into emitted output.
//
// Disabled when threshold <= 0. Must be started only once the hub is ready, so it never
// mistakes startup/catch-up bootstrapping for being stuck.
func (r *Relayer) watchForStuckRelayer(threshold time.Duration) {
	if threshold <= 0 {
		return
	}

	ticker := time.NewTicker(stuckDetectionPollInterval)
	defer ticker.Stop()

	var state stuckDetectorState
	for {
		select {
		case <-r.Terminating():
			return
		case <-ticker.C:
			headNum, ok := dmetrics.NewValuesFromMetric(metrics.HeadBlockNumber).Uints("app")["relayer"]
			if !ok {
				continue
			}

			sourceMaxBlockNum := r.sourceMaxBlockNum.Load()

			var tripped bool
			tripped, state = state.observe(time.Now(), headNum, sourceMaxBlockNum, threshold)
			if !tripped {
				continue
			}

			zlog.Error("relayer appears stuck: head has not advanced while a source has newer blocks, shutting down for restart",
				zap.Uint64("head_block_num", headNum),
				zap.Uint64("source_block_num", sourceMaxBlockNum),
				zap.Duration("threshold", threshold),
			)
			r.Shutdown(fmt.Errorf("relayer stuck: head frozen at block %d for over %s while a source has delivered block %d, restart required", headNum, threshold, sourceMaxBlockNum))
			return
		}
	}
}
