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

package metrics

import (
	"sync/atomic"
	"time"

	"github.com/streamingfast/dmetrics"
	coremetrics "github.com/streamingfast/firehose-core/metrics"
)

var MetricSet = dmetrics.NewSet()

var HeadBlockTimeDrift = MetricSet.NewHeadTimeDrift("merger")
var HeadBlockNumber = MetricSet.NewHeadBlockNumber("merger")

// FinalizedBlockNumber always tracks HeadBlockNumber: the merger only ever bundles
// irreversible blocks, so its head is finalized by construction. Reporting the LIB
// number carried by those blocks would trail our own head and read as if the merger
// were lagging finality.
var FinalizedBlockNumber = coremetrics.NewFinalizedBlockNumber("merger")
var AppReadiness = MetricSet.NewAppReadiness("merger")

var headBlockTimeNanos atomic.Int64

// SetHeadBlockTimeForward updates HeadBlockTimeDrift, ignoring block times older than the
// one already reported. Two paths write it: the last block of the last merged bundle,
// read on startup as a reference point, and the live one-block files, read
// asynchronously as they are bundled. Without this guard the stale startup value (or
// merged blocks produced by another process) can land after a fresher one and pin the
// drift high until a brand new block shows up.
func SetHeadBlockTimeForward(t time.Time) {
	nanos := t.UnixNano()
	for {
		current := headBlockTimeNanos.Load()
		if nanos <= current {
			return
		}
		if headBlockTimeNanos.CompareAndSwap(current, nanos) {
			HeadBlockTimeDrift.SetBlockTime(t)
			return
		}
	}
}
