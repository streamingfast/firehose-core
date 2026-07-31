package substreams

import (
	"strings"
	"testing"
	"time"

	"github.com/streamingfast/firehose-core/cmd/tools/substreams/logs"
	"github.com/stretchr/testify/assert"
)

func TestRawLogTime(t *testing.T) {
	entryTime := time.Date(2026, 2, 18, 5, 0, 0, 0, time.UTC)

	t.Run("payload timestamp wins", func(t *testing.T) {
		got := rawLogTime(logs.LogEntry{Timestamp: "2026-02-18T06:30:00.250Z", EntryTime: entryTime})

		assert.Equal(t, time.Date(2026, 2, 18, 6, 30, 0, 250000000, time.UTC), got.UTC())
	})

	t.Run("falls back on entry time", func(t *testing.T) {
		assert.Equal(t, entryTime, rawLogTime(logs.LogEntry{EntryTime: entryTime}))
	})

	t.Run("falls back on entry time when payload timestamp is unparseable", func(t *testing.T) {
		assert.Equal(t, entryTime, rawLogTime(logs.LogEntry{Timestamp: "not-a-date", EntryTime: entryTime}))
	})
}

func TestFormatSeverity(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"info", "INFO", "INFO"},
		{"error", "ERROR", "ERROR"},
		{"warning", "WARNING", "WARNING"},
		{"emergency", "EMERGENCY", "EMERGENCY"},
		{"empty becomes dash", "", "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatSeverity(tt.in))
		})
	}
}

func TestFormatLogger(t *testing.T) {
	t.Run("renders logger between parenthesis with trailing space", func(t *testing.T) {
		assert.Equal(t, "(substreams-tier1.tier1-grpc) ", formatLogger(map[string]any{"logger": "substreams-tier1.tier1-grpc"}))
	})

	t.Run("no logger field", func(t *testing.T) {
		assert.Empty(t, formatLogger(map[string]any{"tier": "tier1"}))
	})

	t.Run("logger is not a string", func(t *testing.T) {
		assert.Empty(t, formatLogger(map[string]any{"logger": float64(1)}))
	})
}

func TestFormatRawLogValue(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"string", "tier1", "tier1"},
		{"string with space is left as-is", "context canceled", "context canceled"},
		{"string with quotes is not escaped", `{"rpc:eth_call": {"count": 2}}`, `{"rpc:eth_call": {"count": 2}}`},
		{"integral float has no decimals", float64(12), "12"},
		{"fractional float", 1.25, "1.25"},
		{"bool", true, "true"},
		{"nil", nil, "null"},
		{"nested value is json encoded", map[string]any{"a": float64(1)}, `{"a":1}`},
		{"long value is not truncated", strings.Repeat("a", 200), strings.Repeat("a", 200)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatRawLogValue(tt.in))
		})
	}
}

func TestRawLogFields(t *testing.T) {
	t.Run("no fields", func(t *testing.T) {
		assert.Empty(t, rawLogFields(nil))
	})

	t.Run("sorted, skipping rendered and internal fields", func(t *testing.T) {
		got := rawLogFields(map[string]any{
			"message":                             "substreams request stats",
			"timestamp":                           "2026-02-18T05:00:00Z",
			"severity":                            "INFO",
			"level":                               "info",
			"logger":                              "substreams-tier1.tier1-grpc",
			"trace_id":                            "abc123",
			"logging.googleapis.com/sourceConfig": "noise",
			"tier":                                "tier1",
			"module":                              "map_out",
		})

		assert.Equal(t, []rawLogField{
			{Key: "module", Value: "map_out"},
			{Key: "tier", Value: "tier1"},
		}, got)
	})
}

func TestIsDurationKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"duration", "duration", true},
		{"prefixed duration", "parallel_duration", true},
		{"module exec duration", "module_exec_duration", true},
		{"time to first data", "time_to_first_data", true},
		{"embedded time", "client_read_average_time_last_5_minutes", true},
		{"elapsed", "elapsed_since_start", true},
		{"latency", "read_latency", true},
		{"explicit milliseconds are not seconds", "processing_time_ms", false},
		{"explicit nanoseconds are not seconds", "exec_duration_ns", false},
		{"unrelated key", "processed_blocks", false},
		{"timestamp-ish key", "last_sent_block_time", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isDurationKey(tt.in))
		})
	}
}

func TestFormatRawLogFieldValue(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value any
		want  string
	}{
		{"seconds become a duration", "duration", 3042.668484665, "50m42s"},
		{"sub-second duration", "parallel_duration", 0.25, "250ms"},
		{"sub-millisecond duration keeps its unit", "client_read_average_time_last_5_minutes", 0.000037998, "37.998µs"},
		{"zero duration", "time_to_first_data", float64(0), "0s"},
		{"epoch-looking value is left alone", "elapsed_time_since_epoch", 1.7e9, "1700000000"},
		{"non-numeric duration is left alone", "duration", "a while", "a while"},
		{"non-duration key is left alone", "processed_blocks", float64(237), "237"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatRawLogFieldValue(tt.key, tt.value))
		})
	}
}
