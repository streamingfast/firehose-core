package logs

import (
	"github.com/streamingfast/logging"
)

var zlogTest, _ = logging.PackageLogger("logs-test", "github.com/streamingfast/firehose-core/cmd/tools/substreams/logs.test")

func init() {
	logging.InstantiateLoggers()
}
