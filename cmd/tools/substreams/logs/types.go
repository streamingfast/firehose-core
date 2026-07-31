package logs

import (
	"time"
)

// Connection status constants
type ConnectionStatus string

const (
	StatusActive ConnectionStatus = "active"
	StatusClosed ConnectionStatus = "closed"
	StatusError  ConnectionStatus = "error"
	StatusOrphan ConnectionStatus = "orphan"
)

// QueryOptions contains parameters for querying connection logs
//
// Either UserID or TraceID must be set. UserID matches all requests for an
// organization; TraceID matches a single request via Cloud Logging SEARCH().
type QueryOptions struct {
	UserID    string
	TraceID   string
	Namespace string
	StartTime time.Time
	EndTime   time.Time

	// AllMessages disables the "incoming request"/"request stats" message
	// restriction, returning every log entry matching the subject (trace ID or
	// user ID) instead. Used to display the raw logs of a single request.
	AllMessages bool

	// Limit caps the number of entries returned, keeping the newest ones. Zero
	// means no limit.
	Limit int
}

// ConnectionLog represents a single connection (may be partial if stats not yet received)
type ConnectionLog struct {
	// From incoming request
	TraceID          string
	UserID           string
	IPAddress        string
	OutputModule     string
	OutputModuleHash string
	StartBlock       int64
	StopBlock        uint64
	ProductionMode   bool
	Timestamp        time.Time

	// From resource labels (backend-specific, extracted by backend)
	Namespace   string
	ClusterName string
	PodName     string

	// From stats (nil if not yet received)
	Stats *ConnectionStats

	// IsOrphan indicates this is a stats-only record with no matching incoming request
	IsOrphan bool
}

// Status returns the connection status based on whether stats are present and if there's an error
func (c *ConnectionLog) Status() ConnectionStatus {
	if c.IsOrphan {
		return StatusOrphan
	}
	if c.Stats == nil {
		return StatusActive
	}
	if c.Stats.Error != "" && !IsNormalDisconnect(c.Stats.Error) {
		return StatusError
	}
	return StatusClosed
}

// isNormalDisconnect returns true if the error indicates a normal client disconnect
// rather than an actual error condition
func IsNormalDisconnect(err string) bool {
	return err == "context canceled"
}

// Duration returns the duration of the connection
// For active connections, returns time since start
// For closed connections, returns the actual duration
func (c *ConnectionLog) Duration() time.Duration {
	if c.Stats == nil {
		return time.Since(c.Timestamp)
	}
	return c.Stats.EndTimestamp.Sub(c.Timestamp)
}

// ConnectionStats contains statistics from the request stats log
type ConnectionStats struct {
	TotalBlocksProcessed uint64
	BlockRatePerSec      string
	TimeToFirstData      float64
	ResolvedStartBlock   uint64
	Error                string
	EndTimestamp         time.Time
	Duration             time.Duration // Request duration (from parallel_duration)
}

// CorrelationResult holds the result of correlating incoming requests with stats
type CorrelationResult struct {
	Connections   []*ConnectionLog
	MaxConcurrent int // Maximum number of connections active at the same time (orphans use range start as start time)
}
