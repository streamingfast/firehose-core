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
