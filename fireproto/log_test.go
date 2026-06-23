package fireproto_test

import "github.com/streamingfast/logging"

var zlogTest, tracerTest = logging.PackageLogger("fireproto_test", "github.com/streamingfast/firehose-core/fireproto/test")

func init() {
	logging.InstantiateLoggers()
}
