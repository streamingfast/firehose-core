package logs

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"time"

	"cloud.google.com/go/logging"
	"cloud.google.com/go/logging/logadmin"
	"go.uber.org/zap"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/structpb"
)

// GCPBackend implements LogBackend using Google Cloud Logging
type GCPBackend struct {
	client    *logadmin.Client
	projectID string
	logger    *zap.Logger
}

// NewGCPBackend creates a new GCP Cloud Logging backend
func NewGCPBackend(ctx context.Context, projectID string, logger *zap.Logger) (*GCPBackend, error) {
	client, err := logadmin.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("creating logadmin client: %w", err)
	}

	return &GCPBackend{
		client:    client,
		projectID: projectID,
		logger:    logger,
	}, nil
}

// QueryLogs queries Cloud Logging for connection-related log entries
func (b *GCPBackend) QueryLogs(ctx context.Context, opts QueryOptions) ([]LogEntry, error) {
	filter := b.buildFilter(opts)
	b.logger.Debug("querying GCP Cloud Logging",
		zap.String("project_id", b.projectID),
		zap.String("filter", filter),
	)

	var entries []LogEntry
	for entry, err := range b.iterateEntries(ctx, filter) {
		if err != nil {
			return nil, fmt.Errorf("iterating log entries: %w", err)
		}
		entries = append(entries, entry)
	}

	b.logger.Debug("query completed", zap.Int("entries_found", len(entries)))
	return entries, nil
}

// iterateEntries returns an iterator over log entries matching the filter
func (b *GCPBackend) iterateEntries(ctx context.Context, filter string) iter.Seq2[LogEntry, error] {
	return func(yield func(LogEntry, error) bool) {
		it := b.client.Entries(ctx, logadmin.Filter(filter), logadmin.NewestFirst())

		for {
			entry, err := it.Next()
			if err == iterator.Done {
				return
			}
			if err != nil {
				yield(LogEntry{}, err)
				return
			}

			logEntry := b.parseEntry(entry)
			if !yield(logEntry, nil) {
				return
			}
		}
	}
}

// buildFilter constructs the Cloud Logging filter string
//
// When TraceID is set, filters by SEARCH() across the entry payload. Otherwise
// filters by jsonPayload.user_id.
func (b *GCPBackend) buildFilter(opts QueryOptions) string {
	var subjectFilter string
	if opts.TraceID != "" {
		subjectFilter = fmt.Sprintf(`SEARCH("%s")`, escapeFilterValue(opts.TraceID))
	} else {
		subjectFilter = fmt.Sprintf(`jsonPayload.user_id="%s"`, escapeFilterValue(opts.UserID))
	}

	filter := fmt.Sprintf(`resource.type="k8s_container"
%s
(
  jsonPayload.message="incoming Substreams Blocks request" OR
  (jsonPayload.message="substreams request stats" AND jsonPayload.tier="tier1")
)
timestamp >= "%s"
timestamp <= "%s"`,
		subjectFilter,
		opts.StartTime.Format(time.RFC3339),
		opts.EndTime.Format(time.RFC3339),
	)

	// Add namespace filter if specified
	if opts.Namespace != "" {
		filter += fmt.Sprintf(`
resource.labels.namespace_name="%s"`, escapeFilterValue(opts.Namespace))
	}

	return filter
}

// escapeFilterValue escapes a string for use inside a double-quoted Cloud
// Logging filter literal. Only `\` and `"` carry special meaning inside a
// quoted string; control characters (newline, carriage return, tab) are
// stripped because they would terminate the literal and let an attacker append
// arbitrary filter clauses.
//
// See https://cloud.google.com/logging/docs/view/logging-query-language#string-comparison-operators
func escapeFilterValue(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", "",
		"\r", "",
		"\t", "",
	)
	return r.Replace(s)
}

// parseEntry extracts fields from a Cloud Logging entry into a LogEntry
func (b *GCPBackend) parseEntry(entry *logging.Entry) LogEntry {
	le := LogEntry{}

	// Extract jsonPayload fields
	// Cloud Logging returns Payload as *structpb.Struct for JSON logs
	var payload map[string]any
	switch p := entry.Payload.(type) {
	case *structpb.Struct:
		payload = p.AsMap()
	case map[string]any:
		payload = p
	default:
		b.logger.Debug("unknown payload type", zap.String("type", fmt.Sprintf("%T", entry.Payload)))
		return le
	}

	// Trace log the raw entry for debugging
	if b.logger.Core().Enabled(zap.DebugLevel) {
		var resourceLabels map[string]string
		if entry.Resource != nil {
			resourceLabels = entry.Resource.Labels
		}
		b.logger.Debug("raw log entry retrieved",
			zap.Any("payload", payload),
			zap.String("log_name", entry.LogName),
			zap.Time("timestamp", entry.Timestamp),
			zap.Any("resource_labels", resourceLabels),
		)
	}

	le.Message = getString(payload, "message")
	le.TraceID = getString(payload, "trace_id")
	le.SessionID = getString(payload, "session_id")
	le.UserID = getString(payload, "user_id")
	le.IPAddress = getString(payload, "ip_address")
	le.OutputModule = getString(payload, "output_module")
	if le.OutputModule == "" {
		// Stats log uses output_module_name instead of output_module
		le.OutputModule = getString(payload, "output_module_name")
	}
	le.OutputModuleHash = getString(payload, "output_module_hash")
	le.StartBlock = getInt64(payload, "start_block")
	le.StopBlock = getUint64(payload, "stop_block")
	le.Cursor = getString(payload, "cursor")
	le.ProductionMode = getBool(payload, "production_mode")
	le.FinalBlocksOnly = getBool(payload, "final_blocks_only")
	le.NoopMode = getBool(payload, "noop_mode")
	le.Timestamp = getString(payload, "timestamp")

	// Stats-specific fields
	le.Tier = getString(payload, "tier")
	le.TotalBlocksProcessed = getUint64(payload, "total_blocks_processed")
	le.BlockRatePerSec = getString(payload, "block_rate_per_sec")
	le.TimeToFirstData = getFloat64(payload, "time_to_first_data")
	le.ResolvedStartBlock = getUint64(payload, "resolved_start_block")
	le.Error = getString(payload, "error")
	le.Duration = getFloat64(payload, "parallel_duration")

	b.logger.Debug("parsed log entry",
		zap.String("message", le.Message),
		zap.String("trace_id", le.TraceID),
		zap.String("tier", le.Tier),
	)

	// Extract resource labels (GCP-specific envelope)
	if entry.Resource != nil && entry.Resource.Labels != nil {
		le.Namespace = entry.Resource.Labels["namespace_name"]
		le.ClusterName = entry.Resource.Labels["cluster_name"]
		le.PodName = entry.Resource.Labels["pod_name"]
	}

	return le
}

// Close releases resources held by the backend
func (b *GCPBackend) Close() error {
	return b.client.Close()
}

// Helper functions for safe type extraction from map[string]any

func getString(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func getInt64(m map[string]any, key string) int64 {
	switch n := m[key].(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func getUint64(m map[string]any, key string) uint64 {
	switch n := m[key].(type) {
	case float64:
		return uint64(n)
	case uint64:
		return n
	case int64:
		return uint64(n)
	case int:
		return uint64(n)
	default:
		return 0
	}
}

func getFloat64(m map[string]any, key string) float64 {
	if f, ok := m[key].(float64); ok {
		return f
	}
	return 0
}

func getBool(m map[string]any, key string) bool {
	if b, ok := m[key].(bool); ok {
		return b
	}
	return false
}
