package substreams

import (
	"bytes"
	"testing"
	"time"

	"github.com/streamingfast/firehose-core/cmd/tools/substreams/logs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewToolsLogsConnectionCmd(t *testing.T) {
	cmd := NewToolsLogsConnectionCmd(zlogTest)

	assert.Equal(t, "connection <trace-id> [<date-range>]", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)
	assert.NotEmpty(t, cmd.Example)

	flags := cmd.Flags()

	stateStoreFlag := flags.Lookup("state-store")
	require.NotNil(t, stateStoreFlag, "state-store flag should exist")

	gcpProjectFlag := flags.Lookup("gcp-project")
	require.NotNil(t, gcpProjectFlag, "gcp-project flag should exist")

	logsFlag := flags.Lookup("logs")
	require.NotNil(t, logsFlag, "logs flag should exist")
	assert.Equal(t, "false", logsFlag.DefValue)

	logsLimitFlag := flags.Lookup("logs-limit")
	require.NotNil(t, logsLimitFlag, "logs-limit flag should exist")
	assert.Equal(t, "500", logsLimitFlag.DefValue)
}

func TestRequestLogWindow(t *testing.T) {
	startTime := time.Date(2026, 2, 18, 0, 0, 0, 0, time.UTC)
	endTime := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	at := func(hour int) string {
		return time.Date(2026, 2, 18, hour, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	}

	t.Run("narrows to padded request lifetime", func(t *testing.T) {
		start, end := requestLogWindow(
			&logs.LogEntry{Timestamp: at(5)},
			&logs.LogEntry{Timestamp: at(6)},
			startTime, endTime,
		)

		assert.Equal(t, time.Date(2026, 2, 18, 4, 59, 0, 0, time.UTC), start.UTC())
		assert.Equal(t, time.Date(2026, 2, 18, 6, 1, 0, 0, time.UTC), end.UTC())
	})

	t.Run("keeps queried end when request is still running", func(t *testing.T) {
		start, end := requestLogWindow(&logs.LogEntry{Timestamp: at(5)}, nil, startTime, endTime)

		assert.Equal(t, time.Date(2026, 2, 18, 4, 59, 0, 0, time.UTC), start.UTC())
		assert.Equal(t, endTime, end.UTC())
	})

	t.Run("falls back on queried range", func(t *testing.T) {
		start, end := requestLogWindow(nil, nil, startTime, endTime)

		assert.Equal(t, startTime, start.UTC())
		assert.Equal(t, endTime, end.UTC())
	})

	t.Run("falls back on queried bounds when timestamps are unparseable", func(t *testing.T) {
		start, end := requestLogWindow(
			&logs.LogEntry{Timestamp: "nope"},
			&logs.LogEntry{Timestamp: "nope"},
			startTime, endTime,
		)

		assert.Equal(t, startTime, start.UTC())
		assert.Equal(t, endTime, end.UTC())
	})
}

func TestConnectionCommandHelp(t *testing.T) {
	cmd := NewToolsLogsConnectionCmd(zlogTest)

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})

	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "connection <trace-id>")
	assert.Contains(t, output, "--state-store")
	assert.Contains(t, output, "--gcp-project")
}

func TestConnectionCommandRegistered(t *testing.T) {
	logsCmd := NewToolsLogsCmd(zlogTest)

	connectionCmd, _, err := logsCmd.Find([]string{"connection"})
	require.NoError(t, err)
	assert.Equal(t, "connection <trace-id> [<date-range>]", connectionCmd.Use)
}

func TestConnectionCommandValidation(t *testing.T) {
	t.Run("missing trace-id argument", func(t *testing.T) {
		cmd := NewToolsLogsConnectionCmd(zlogTest)
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs([]string{})

		err := cmd.Execute()
		require.Error(t, err)
	})

	t.Run("missing gcp-project flag", func(t *testing.T) {
		cmd := NewToolsLogsConnectionCmd(zlogTest)
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs([]string{"abc123"})

		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gcp-project")
	})
}

func TestPickConnectionEntries(t *testing.T) {
	ts := func(min int) string {
		return time.Date(2026, 1, 1, 12, min, 0, 0, time.UTC).Format(time.RFC3339Nano)
	}

	t.Run("only request", func(t *testing.T) {
		entries := []logs.LogEntry{
			{Message: "incoming Substreams Blocks request", TraceID: "t1", Timestamp: ts(5)},
		}
		req, stats, count := pickConnectionEntries(entries)
		require.NotNil(t, req)
		assert.Equal(t, "t1", req.TraceID)
		assert.Nil(t, stats)
		assert.Equal(t, 1, count)
	})

	t.Run("only stats", func(t *testing.T) {
		entries := []logs.LogEntry{
			{Message: "substreams request stats", Tier: "tier1", TraceID: "t2", Timestamp: ts(10)},
		}
		req, stats, count := pickConnectionEntries(entries)
		assert.Nil(t, req)
		require.NotNil(t, stats)
		assert.Equal(t, "t2", stats.TraceID)
		assert.Equal(t, 0, count)
	})

	t.Run("request and stats together", func(t *testing.T) {
		entries := []logs.LogEntry{
			{Message: "substreams request stats", Tier: "tier1", TraceID: "t3", Timestamp: ts(15)},
			{Message: "incoming Substreams Blocks request", TraceID: "t3", Timestamp: ts(5)},
		}
		req, stats, count := pickConnectionEntries(entries)
		require.NotNil(t, req)
		require.NotNil(t, stats)
		assert.Equal(t, 1, count)
	})

	t.Run("multiple incoming requests picks oldest", func(t *testing.T) {
		entries := []logs.LogEntry{
			{Message: "incoming Substreams Blocks request", TraceID: "t4", Timestamp: ts(20)},
			{Message: "incoming Substreams Blocks request", TraceID: "t4", Timestamp: ts(5)},
			{Message: "incoming Substreams Blocks request", TraceID: "t4", Timestamp: ts(15)},
		}
		req, _, count := pickConnectionEntries(entries)
		require.NotNil(t, req)
		assert.Equal(t, ts(5), req.Timestamp)
		assert.Equal(t, 3, count)
	})

	t.Run("multiple stats picks newest", func(t *testing.T) {
		entries := []logs.LogEntry{
			{Message: "substreams request stats", Tier: "tier1", TraceID: "t5", Timestamp: ts(5)},
			{Message: "substreams request stats", Tier: "tier1", TraceID: "t5", Timestamp: ts(20)},
			{Message: "substreams request stats", Tier: "tier1", TraceID: "t5", Timestamp: ts(10)},
		}
		_, stats, _ := pickConnectionEntries(entries)
		require.NotNil(t, stats)
		assert.Equal(t, ts(20), stats.Timestamp)
	})

	t.Run("mixed RFC3339 and RFC3339Nano timestamps order correctly", func(t *testing.T) {
		// At the same wall-clock second, the RFC3339 form ("...:04Z") sorts
		// AFTER the RFC3339Nano form ("...:04.5Z") lexicographically because
		// '.' (0x2E) < 'Z' (0x5A). The parsed-time comparison must treat
		// :04Z as earlier than :04.5Z.
		early := "2026-02-18T05:20:04Z"
		late := "2026-02-18T05:20:04.500Z"
		entries := []logs.LogEntry{
			{Message: "incoming Substreams Blocks request", TraceID: "t6", Timestamp: late},
			{Message: "incoming Substreams Blocks request", TraceID: "t6", Timestamp: early},
		}
		req, _, _ := pickConnectionEntries(entries)
		require.NotNil(t, req)
		assert.Equal(t, early, req.Timestamp)
	})

	t.Run("empty entries", func(t *testing.T) {
		req, stats, count := pickConnectionEntries(nil)
		assert.Nil(t, req)
		assert.Nil(t, stats)
		assert.Equal(t, 0, count)
	})
}

func TestParseLogTimestamp(t *testing.T) {
	t.Run("RFC3339Nano", func(t *testing.T) {
		ts, ok := parseLogTimestamp("2026-02-18T05:20:04.46216987Z")
		assert.True(t, ok)
		assert.Equal(t, 2026, ts.Year())
	})

	t.Run("RFC3339", func(t *testing.T) {
		ts, ok := parseLogTimestamp("2026-02-18T05:20:04Z")
		assert.True(t, ok)
		assert.Equal(t, 2026, ts.Year())
	})

	t.Run("empty", func(t *testing.T) {
		_, ok := parseLogTimestamp("")
		assert.False(t, ok)
	})

	t.Run("invalid", func(t *testing.T) {
		_, ok := parseLogTimestamp("not-a-date")
		assert.False(t, ok)
	})
}
