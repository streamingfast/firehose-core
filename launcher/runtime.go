package launcher

import "github.com/streamingfast/firehose-core/firehose/info"

type Runtime struct {
	AbsDataDir string
	InfoServer *info.InfoServer

	// IsPendingShutdown is a function that is going to return true as soon as the initial SIGINT signal is
	// received which can be used to turn a healthz monitor as unhealthy so that a load balancer can
	// remove the node from the pool and has 'common-system-shutdown-signal-delay' to do it.
	IsPendingShutdown func() bool
}
