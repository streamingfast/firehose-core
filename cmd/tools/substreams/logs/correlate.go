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

	// Sort by timestamp (newest first)
	slices.SortFunc(connections, func(a, b *ConnectionLog) int {
		return -a.Timestamp.Compare(b.Timestamp)
	})

	return &CorrelationResult{
		Connections:   connections,
		OrphanedCount: orphanedCount,
	}
}
