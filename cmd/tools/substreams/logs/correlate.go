package logs

import (
	"slices"
	"time"
)

// CorrelateConnections matches incoming request logs with their corresponding stats logs
// by trace_id. Returns correlated connections and count of orphaned stats logs.
func CorrelateConnections(entries []LogEntry) *CorrelationResult {
	// Build map of trace_id -> ConnectionLog for incoming requests
	connectionsByTraceID := make(map[string]*ConnectionLog)
	var statsEntries []LogEntry

	// First pass: collect incoming requests and stats entries
	for _, entry := range entries {
		if entry.IsIncomingRequest() {
			ts, _ := time.Parse(time.RFC3339Nano, entry.Timestamp)
			connectionsByTraceID[entry.TraceID] = &ConnectionLog{
				TraceID:          entry.TraceID,
				UserID:           entry.UserID,
				IPAddress:        entry.IPAddress,
				OutputModule:     entry.OutputModule,
				OutputModuleHash: entry.OutputModuleHash,
				StartBlock:       entry.StartBlock,
				StopBlock:        entry.StopBlock,
				ProductionMode:   entry.ProductionMode,
				Timestamp:        ts,
				Namespace:        entry.Namespace,
				ClusterName:      entry.ClusterName,
				PodName:          entry.PodName,
			}
		} else if entry.IsRequestStats() {
			statsEntries = append(statsEntries, entry)
		}
	}

	// Second pass: match stats to incoming requests
	orphanedCount := 0
	for _, stats := range statsEntries {
		conn, found := connectionsByTraceID[stats.TraceID]
		if !found {
			// Orphaned stats log - no matching incoming request
			orphanedCount++
			continue
		}

		ts, _ := time.Parse(time.RFC3339Nano, stats.Timestamp)
		conn.Stats = &ConnectionStats{
			TotalBlocksProcessed: stats.TotalBlocksProcessed,
			BlockRatePerSec:      stats.BlockRatePerSec,
			TimeToFirstData:      stats.TimeToFirstData,
			ResolvedStartBlock:   stats.ResolvedStartBlock,
			Error:                stats.Error,
			EndTimestamp:         ts,
		}
	}

	// Collect all connections into a slice
	connections := make([]*ConnectionLog, 0, len(connectionsByTraceID))
	for _, conn := range connectionsByTraceID {
		connections = append(connections, conn)
	}

	// Sort by timestamp (oldest first, most recent at bottom)
	slices.SortFunc(connections, func(a, b *ConnectionLog) int {
		return a.Timestamp.Compare(b.Timestamp)
	})

	// Calculate max concurrent connections
	maxConcurrent := calculateMaxConcurrent(connections)

	return &CorrelationResult{
		Connections:   connections,
		OrphanedCount: orphanedCount,
		MaxConcurrent: maxConcurrent,
	}
}

// calculateMaxConcurrent finds the maximum number of connections active at the same time
func calculateMaxConcurrent(connections []*ConnectionLog) int {
	if len(connections) == 0 {
		return 0
	}

	// Create events: +1 for start, -1 for end
	type event struct {
		time  time.Time
		delta int // +1 for start, -1 for end
	}

	events := make([]event, 0, len(connections)*2)
	now := time.Now()

	for _, conn := range connections {
		events = append(events, event{time: conn.Timestamp, delta: 1})

		// Use end time from stats, or "now" for active connections
		endTime := now
		if conn.Stats != nil {
			endTime = conn.Stats.EndTimestamp
		}
		events = append(events, event{time: endTime, delta: -1})
	}

	// Sort events by time, with ends before starts at the same time
	// (so we don't double-count connections that end and start at the exact same moment)
	slices.SortFunc(events, func(a, b event) int {
		cmp := a.time.Compare(b.time)
		if cmp != 0 {
			return cmp
		}
		// At same time: ends (-1) come before starts (+1)
		return a.delta - b.delta
	})

	// Walk through events tracking current and max concurrent
	current, maxConcurrent := 0, 0
	for _, e := range events {
		current += e.delta
		if current > maxConcurrent {
			maxConcurrent = current
		}
	}

	return maxConcurrent
}
