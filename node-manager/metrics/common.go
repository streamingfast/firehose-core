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

// The reader metrics below are split by the feature that sets them, and each set is registered
// by the code that builds that feature. A reader therefore exports the metrics of what it runs,
// whichever reader app it is, and nothing for features it doesn't run (no gauge stuck at 0).
// Registering a set more than once is a no-op.
var lineReaderMetricset = dmetrics.NewSet()
var blockReaderMetricset = dmetrics.NewSet()
var oneBlockUploadMetricset = dmetrics.NewSet()

// MaxReadBlockSize tracks the largest single line (block) in bytes read out of the node
// process so far. Compare it against LineBufferSize to know how often the line buffer has to
// grow past its normal size.
var MaxReadBlockSize = lineReaderMetricset.NewGauge("reader_node_max_read_block_size_bytes", "Largest single line (block) in bytes read out of the node process so far")

// LineBufferSize reports the configured 'reader-node-line-buffer-size', the normal size in
// bytes of the buffer reading lines (blocks) out of the node process.
var LineBufferSize = lineReaderMetricset.NewGauge("reader_node_line_buffer_size_bytes", "Configured normal size in bytes of the buffer reading a single line (block) out of the node process ('reader-node-line-buffer-size')")

// InvalidLibNumClampedCount counts blocks whose decoded lib_num was greater than their own
// block number (invalid) and had to be clamped down to the block number.
var InvalidLibNumClampedCount = blockReaderMetricset.NewCounter("reader_node_invalid_libnum_clamped_count", "Number of blocks whose decoded lib_num was greater than their own block number and got clamped to the block number")

// OneBlockUploadPendingFiles and OneBlockUploadOldestPendingSeconds report, at each upload
// pass, the one-block files still waiting in the reader's local staging directory and how long
// the oldest one has waited. While the one-block store is slow or down they keep growing, which
// nothing else shows: the reader keeps reading blocks and serving them to its relayers.
var OneBlockUploadPendingFiles = oneBlockUploadMetricset.NewGauge("reader_node_one_block_upload_pending_files", "Number of one-block files waiting in the reader's local staging directory to be uploaded, at the start of the last upload pass")
var OneBlockUploadOldestPendingSeconds = oneBlockUploadMetricset.NewGauge("reader_node_one_block_upload_oldest_pending_seconds", "How long the oldest one-block file waiting in the reader's local staging directory has waited, at the start of the last upload pass")

// RegisterLineReaderMetrics registers the metrics of a reader reading blocks as lines out of a
// node process or standard input, and reports the configured normal line buffer size.
func RegisterLineReaderMetrics(lineBufferSize uint64) {
	lineReaderMetricset.Register()
	LineBufferSize.SetUint64(lineBufferSize)
}

// RegisterBlockReaderMetrics registers the metrics of a reader decoding blocks (the mindreader).
func RegisterBlockReaderMetrics() {
	blockReaderMetricset.Register()
}

// RegisterOneBlockUploadMetrics registers the metrics of a reader staging one-block files
// locally and uploading them to the one-block store.
func RegisterOneBlockUploadMetrics() {
	oneBlockUploadMetricset.Register()
}

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
