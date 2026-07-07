package logs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCorrelateConnections(t *testing.T) {
	now := time.Now()
	queryStartTime := now.Add(-1 * time.Hour)
	ts1 := now.Add(-10 * time.Minute).Format(time.RFC3339Nano)
	ts2 := now.Add(-5 * time.Minute).Format(time.RFC3339Nano)
	ts3 := now.Add(-2 * time.Minute).Format(time.RFC3339Nano)

	tests := []struct {
		name          string
		entries       []LogEntry
		wantConnCount int
		wantActive    int
		wantClosed    int
		wantError     int
		wantOrphan    int
	}{
		{
			name:          "empty entries",
			entries:       []LogEntry{},
			wantConnCount: 0,
		},
		{
			name: "single active connection",
			entries: []LogEntry{
				{
					Message:          "incoming Substreams Blocks request",
					TraceID:          "trace1",
					UserID:           "sfinfra",
					IPAddress:        "1.2.3.4",
					OutputModule:     "map_blocks",
					OutputModuleHash: "abc123",
					Timestamp:        ts1,
				},
			},
			wantConnCount: 1,
			wantActive:    1,
		},
		{
			name: "single closed connection",
			entries: []LogEntry{
				{
					Message:          "incoming Substreams Blocks request",
					TraceID:          "trace1",
					UserID:           "sfinfra",
					IPAddress:        "1.2.3.4",
					OutputModule:     "map_blocks",
					OutputModuleHash: "abc123",
					Timestamp:        ts1,
				},
				{
					Message:              "substreams request stats",
					Tier:                 "tier1",
					TraceID:              "trace1",
					TotalBlocksProcessed: 100,
					BlockRatePerSec:      "50.0",
					Timestamp:            ts2,
				},
			},
			wantConnCount: 1,
			wantClosed:    1,
		},
		{
			name: "connection with context canceled is closed not error",
			entries: []LogEntry{
				{
					Message:          "incoming Substreams Blocks request",
					TraceID:          "trace1",
					UserID:           "sfinfra",
					IPAddress:        "1.2.3.4",
					OutputModule:     "map_blocks",
					OutputModuleHash: "abc123",
					Timestamp:        ts1,
				},
				{
					Message:              "substreams request stats",
					Tier:                 "tier1",
					TraceID:              "trace1",
					TotalBlocksProcessed: 10,
					Error:                "context canceled",
					Timestamp:            ts2,
				},
			},
			wantConnCount: 1,
			wantClosed:    1, // context canceled = normal disconnect
		},
		{
			name: "connection with real error",
			entries: []LogEntry{
				{
					Message:          "incoming Substreams Blocks request",
					TraceID:          "trace1",
					UserID:           "sfinfra",
					IPAddress:        "1.2.3.4",
					OutputModule:     "map_blocks",
					OutputModuleHash: "abc123",
					Timestamp:        ts1,
				},
				{
					Message:              "substreams request stats",
					Tier:                 "tier1",
					TraceID:              "trace1",
					TotalBlocksProcessed: 10,
					Error:                "deadline exceeded",
					Timestamp:            ts2,
				},
			},
			wantConnCount: 1,
			wantError:     1,
		},
		{
			name: "orphaned stats log",
			entries: []LogEntry{
				{
					Message:              "substreams request stats",
					Tier:                 "tier1",
					TraceID:              "orphan_trace",
					TotalBlocksProcessed: 50,
					Timestamp:            ts1,
				},
			},
			wantConnCount: 1, // Orphans are now included in connections
			wantOrphan:    1,
		},
		{
			name: "mixed connections and orphans",
			entries: []LogEntry{
				{
					Message:   "incoming Substreams Blocks request",
					TraceID:   "trace1",
					UserID:    "sfinfra",
					Timestamp: ts1,
				},
				{
					Message:   "incoming Substreams Blocks request",
					TraceID:   "trace2",
					UserID:    "sfinfra",
					Timestamp: ts2,
				},
				{
					Message:              "substreams request stats",
					Tier:                 "tier1",
					TraceID:              "trace1",
					TotalBlocksProcessed: 100,
					Timestamp:            ts3,
				},
				{
					Message:              "substreams request stats",
					Tier:                 "tier1",
					TraceID:              "orphan_trace",
					TotalBlocksProcessed: 50,
					Timestamp:            ts2,
				},
			},
			wantConnCount: 3, // 2 normal + 1 orphan
			wantActive:    1,
			wantClosed:    1,
			wantOrphan:    1,
		},
		{
			name: "tier2 stats should be ignored",
			entries: []LogEntry{
				{
					Message:   "incoming Substreams Blocks request",
					TraceID:   "trace1",
					UserID:    "sfinfra",
					Timestamp: ts1,
				},
				{
					Message:              "substreams request stats",
					Tier:                 "tier2",
					TraceID:              "trace1",
					TotalBlocksProcessed: 100,
					Timestamp:            ts2,
				},
			},
			wantConnCount: 1,
			wantActive:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CorrelateConnections(tt.entries, queryStartTime)

			assert.Equal(t, tt.wantConnCount, len(result.Connections), "connection count mismatch")

			// Count statuses
			active, closed, errored, orphan := 0, 0, 0, 0
			for _, conn := range result.Connections {
				switch conn.Status() {
				case StatusActive:
					active++
				case StatusClosed:
					closed++
				case StatusError:
					errored++
				case StatusOrphan:
					orphan++
				}
			}

			assert.Equal(t, tt.wantActive, active, "active count mismatch")
			assert.Equal(t, tt.wantClosed, closed, "closed count mismatch")
			assert.Equal(t, tt.wantError, errored, "error count mismatch")
			assert.Equal(t, tt.wantOrphan, orphan, "orphan count mismatch")
		})
	}
}

func TestConnectionLogStatus(t *testing.T) {
	tests := []struct {
		name string
		conn *ConnectionLog
		want ConnectionStatus
	}{
		{
			name: "no stats means active",
			conn: &ConnectionLog{},
			want: StatusActive,
		},
		{
			name: "stats without error means closed",
			conn: &ConnectionLog{
				Stats: &ConnectionStats{
					TotalBlocksProcessed: 100,
				},
			},
			want: StatusClosed,
		},
		{
			name: "stats with context canceled means closed (normal disconnect)",
			conn: &ConnectionLog{
				Stats: &ConnectionStats{
					Error: "context canceled",
				},
			},
			want: StatusClosed,
		},
		{
			name: "stats with real error means error",
			conn: &ConnectionLog{
				Stats: &ConnectionStats{
					Error: "deadline exceeded",
				},
			},
			want: StatusError,
		},
		{
			name: "orphan connection returns orphan status",
			conn: &ConnectionLog{
				IsOrphan: true,
				Stats: &ConnectionStats{
					TotalBlocksProcessed: 100,
				},
			},
			want: StatusOrphan,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.conn.Status()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLogEntryIsIncomingRequest(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{"incoming request", "incoming Substreams Blocks request", true},
		{"stats", "substreams request stats", false},
		{"other", "some other message", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := LogEntry{Message: tt.message}
			assert.Equal(t, tt.want, entry.IsIncomingRequest())
		})
	}
}

func TestLogEntryIsRequestStats(t *testing.T) {
	tests := []struct {
		name    string
		message string
		tier    string
		want    bool
	}{
		{"tier1 stats", "substreams request stats", "tier1", true},
		{"tier2 stats", "substreams request stats", "tier2", false},
		{"no tier", "substreams request stats", "", false},
		{"incoming request", "incoming Substreams Blocks request", "tier1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := LogEntry{Message: tt.message, Tier: tt.tier}
			assert.Equal(t, tt.want, entry.IsRequestStats())
		})
	}
}

func TestConnectionDuration(t *testing.T) {
	now := time.Now()

	t.Run("active connection returns time since start", func(t *testing.T) {
		conn := &ConnectionLog{
			Timestamp: now.Add(-5 * time.Minute),
		}
		duration := conn.Duration()
		require.True(t, duration >= 5*time.Minute, "duration should be at least 5 minutes")
	})

	t.Run("closed connection returns actual duration", func(t *testing.T) {
		startTime := now.Add(-10 * time.Minute)
		endTime := now.Add(-5 * time.Minute)
		conn := &ConnectionLog{
			Timestamp: startTime,
			Stats: &ConnectionStats{
				EndTimestamp: endTime,
			},
		}
		duration := conn.Duration()
		assert.Equal(t, 5*time.Minute, duration)
	})
}

func TestCalculateMaxConcurrent(t *testing.T) {
	now := time.Now()

	t.Run("empty connections", func(t *testing.T) {
		result := calculateMaxConcurrent(nil)
		assert.Equal(t, 0, result)
	})

	t.Run("single connection", func(t *testing.T) {
		conns := []*ConnectionLog{
			{
				Timestamp: now.Add(-10 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-5 * time.Minute)},
			},
		}
		result := calculateMaxConcurrent(conns)
		assert.Equal(t, 1, result)
	})

	t.Run("non-overlapping connections", func(t *testing.T) {
		conns := []*ConnectionLog{
			{
				Timestamp: now.Add(-10 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-8 * time.Minute)},
			},
			{
				Timestamp: now.Add(-6 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-4 * time.Minute)},
			},
		}
		result := calculateMaxConcurrent(conns)
		assert.Equal(t, 1, result)
	})

	t.Run("fully overlapping connections", func(t *testing.T) {
		// All 3 connections active at the same time
		conns := []*ConnectionLog{
			{
				Timestamp: now.Add(-10 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-2 * time.Minute)},
			},
			{
				Timestamp: now.Add(-9 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-3 * time.Minute)},
			},
			{
				Timestamp: now.Add(-8 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-4 * time.Minute)},
			},
		}
		result := calculateMaxConcurrent(conns)
		assert.Equal(t, 3, result)
	})

	t.Run("partial overlap", func(t *testing.T) {
		// Timeline:
		// conn1: |--------|
		// conn2:     |--------|
		// conn3:         |--------|
		// Max concurrent = 2 (when conn1+conn2 or conn2+conn3 overlap)
		conns := []*ConnectionLog{
			{
				Timestamp: now.Add(-10 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-6 * time.Minute)},
			},
			{
				Timestamp: now.Add(-8 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-4 * time.Minute)},
			},
			{
				Timestamp: now.Add(-5 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-1 * time.Minute)},
			},
		}
		result := calculateMaxConcurrent(conns)
		assert.Equal(t, 2, result)
	})

	t.Run("active connections count as ongoing", func(t *testing.T) {
		// Two active connections (no stats/end time) should both count
		conns := []*ConnectionLog{
			{Timestamp: now.Add(-10 * time.Minute)},
			{Timestamp: now.Add(-5 * time.Minute)},
		}
		result := calculateMaxConcurrent(conns)
		assert.Equal(t, 2, result)
	})

	t.Run("orphan connections use their timestamp as start time", func(t *testing.T) {
		// Orphan connection with Timestamp set to range start (by CorrelateConnections)
		// This orphan overlaps with the regular connection
		rangeStart := now.Add(-1 * time.Hour)
		conns := []*ConnectionLog{
			{
				Timestamp: now.Add(-10 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-5 * time.Minute)},
			},
			{
				Timestamp: rangeStart, // Orphan uses range start as timestamp
				IsOrphan:  true,
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-8 * time.Minute)},
			},
		}
		result := calculateMaxConcurrent(conns)
		// Both connections overlap: orphan runs from rangeStart to -8min, regular from -10min to -5min
		assert.Equal(t, 2, result)
	})

	t.Run("orphan connections non-overlapping", func(t *testing.T) {
		// Orphan that ends before the regular connection starts
		rangeStart := now.Add(-1 * time.Hour)
		conns := []*ConnectionLog{
			{
				Timestamp: now.Add(-10 * time.Minute),
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-5 * time.Minute)},
			},
			{
				Timestamp: rangeStart, // Orphan uses range start as timestamp
				IsOrphan:  true,
				Stats:     &ConnectionStats{EndTimestamp: now.Add(-15 * time.Minute)},
			},
		}
		result := calculateMaxConcurrent(conns)
		// Orphan ends at -15min, regular starts at -10min - no overlap
		assert.Equal(t, 1, result)
	})
}
