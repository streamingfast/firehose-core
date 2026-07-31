package logs

import (
	"context"
	"time"
)

// LogEntry represents a raw log entry from any backend
// The backend extracts jsonPayload fields into this struct
type LogEntry struct {
	// Common fields from jsonPayload
	Message          string
	TraceID          string
	SessionID        string
	UserID           string
	IPAddress        string
	OutputModule     string
	OutputModuleHash string
	StartBlock       int64
	StopBlock        uint64
	Cursor           string
	ProductionMode   bool
	FinalBlocksOnly  bool
	NoopMode         bool
	Timestamp        string

	// EntryTime is the timestamp of the log entry itself as recorded by the
	// backend, used when the payload carries no `timestamp` field
	EntryTime time.Time

	// Stats-specific fields (only present for "substreams request stats")
	Tier                 string
	TotalBlocksProcessed uint64
	BlockRatePerSec      string
	TimeToFirstData      float64
	ResolvedStartBlock   uint64
	Error                string
	Duration             float64 // Request duration in seconds

	// Resource labels (backend extracts these from envelope)
	Namespace   string
	ClusterName string
	PodName     string

	// Severity of the entry as reported by the backend ("INFO", "ERROR", ...),
	// empty when the backend does not report one
	Severity string

	// Fields holds every jsonPayload field of the entry, used to render raw
	// logs. Nil for entries whose payload was not a JSON object.
	Fields map[string]any
}

// IsIncomingRequest returns true if this is an incoming request log
func (e *LogEntry) IsIncomingRequest() bool {
	return e.Message == "incoming Substreams Blocks request"
}

// IsRequestStats returns true if this is a request stats log (tier1 only)
func (e *LogEntry) IsRequestStats() bool {
	return e.Message == "substreams request stats" && e.Tier == "tier1"
}

// Backend abstracts the log querying mechanism
type Backend interface {
	// QueryLogs returns all log entries matching the criteria
	// Returns both incoming requests and stats logs for correlation
	QueryLogs(ctx context.Context, opts QueryOptions) ([]LogEntry, error)

	// Close releases any resources held by the backend
	Close() error
}
