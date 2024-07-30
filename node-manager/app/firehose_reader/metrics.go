package firehose_reader

import "github.com/streamingfast/dmetrics"

var metrics = dmetrics.NewSet(dmetrics.PrefixNameWith("reader_node_firehose"))

func init() {
	metrics.Register()
}

var BlockReadCount = metrics.NewCounter("block_read_count", "The number of blocks read by the Firehose reader")
