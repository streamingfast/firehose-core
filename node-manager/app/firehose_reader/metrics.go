package firehose_reader

import "github.com/streamingfast/dmetrics"

var metrics = dmetrics.NewSet(dmetrics.PrefixNameWith("reader_node_firehose"))

func init() {
	metrics.Register()
}

const HeadDriftServiceName = "reader_node_firehose"

var BlockWriteCount = metrics.NewCounter("block_write_count", "The number of blocks written by the Firehose reader to one-block store")
var HeadBlockTimeDrift = metrics.NewHeadTimeDrift(HeadDriftServiceName)
var HeadBlockNumber = metrics.NewHeadBlockNumber(HeadDriftServiceName)
var AppReadiness = metrics.NewAppReadiness(HeadDriftServiceName)
