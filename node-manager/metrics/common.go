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
	"github.com/streamingfast/dmetrics"
)

var Metricset = dmetrics.NewSet()

// MaxReadBlockSize tracks the largest single line (block) in bytes read out of the node
// process so far. Compare it against LineBufferSize to know how often the line buffer has to
// grow past its normal size.
var MaxReadBlockSize = Metricset.NewGauge("reader_node_max_read_block_size_bytes", "Largest single line (block) in bytes read out of the node process so far")

// LineBufferSize reports the configured 'reader-node-line-buffer-size', the normal size in
// bytes of the buffer reading lines (blocks) out of the node process.
var LineBufferSize = Metricset.NewGauge("reader_node_line_buffer_size_bytes", "Configured normal size in bytes of the buffer reading a single line (block) out of the node process ('reader-node-line-buffer-size')")

func NewHeadBlockTimeDrift(serviceName string) *dmetrics.HeadTimeDrift {
	return Metricset.NewHeadTimeDrift(serviceName)
}

func NewHeadBlockNumber(serviceName string) *dmetrics.HeadBlockNum {
	return Metricset.NewHeadBlockNumber(serviceName)
}

func NewHeadBlockRelativeTime(serviceName string) *dmetrics.HeadBlockRelativeTime {
	return Metricset.NewHeadBlockRelativeTime(serviceName)
}

func NewAppReadiness(serviceName string) *dmetrics.AppReadiness {
	return Metricset.NewAppReadiness(serviceName)
}
