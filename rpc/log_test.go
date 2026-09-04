package rpc

import (
	"github.com/streamingfast/logging"
)

var zlogTest, _ = logging.PackageLogger("rpc", "github.com/streamingfast/firehose-core/rpc.test")

func init() {
	logging.InstantiateLoggers()
}
