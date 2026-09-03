package substreams

import (
	"bytes"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/streamingfast/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var zlogTest, _ = logging.PackageLogger("logs-test", "github.com/streamingfast/firehose-core/cmd/tools/substreams.test")

func init() {
	logging.InstantiateLoggers()
}

func TestNewToolsLogsCmd(t *testing.T) {
	cmd := NewToolsLogsCmd(zlogTest)

	assert.Equal(t, "logs", cmd.Use)
	assert.NotEmpty(t, cmd.Short)

	// Should have connections subcommand
	connectionsCmd, _, err := cmd.Find([]string{"connections"})
	require.NoError(t, err)
	assert.Equal(t, "connections <user_id>", connectionsCmd.Use)
}

func TestNewToolsLogsConnectionsCmd(t *testing.T) {
	cmd := NewToolsLogsConnectionsCmd(zlogTest)

	assert.Equal(t, "connections <user_id>", cmd.Use)
	assert.NotEmpty(t, cmd.Short)
	assert.NotEmpty(t, cmd.Long)
	assert.NotEmpty(t, cmd.Example)

	// Check flags are registered
	flags := cmd.Flags()

	backendFlag := flags.Lookup("backend")
	require.NotNil(t, backendFlag, "backend flag should exist")
	assert.Equal(t, "gcp", backendFlag.DefValue)

	sinceFlag := flags.Lookup("since")
	require.NotNil(t, sinceFlag, "since flag should exist")

	namespaceFlag := flags.Lookup("k8s-namespace")
	require.NotNil(t, namespaceFlag, "k8s-namespace flag should exist")
	assert.Equal(t, "n", namespaceFlag.Shorthand)

	gcpProjectFlag := flags.Lookup("gcp-project")
	require.NotNil(t, gcpProjectFlag, "gcp-project flag should exist")
}

func TestConnectionsCommandHelp(t *testing.T) {
	cmd := NewToolsLogsConnectionsCmd(zlogTest)

	// Capture help output
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})

	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "connections <user_id>")
	assert.Contains(t, output, "--backend")
	assert.Contains(t, output, "--since")
	assert.Contains(t, output, "--k8s-namespace")
	assert.Contains(t, output, "--gcp-project")
}

func TestConnectionsCommandValidation(t *testing.T) {
	t.Run("missing user_id argument", func(t *testing.T) {
		cmd := NewToolsLogsConnectionsCmd(zlogTest)
		cmd.SetArgs([]string{})

		err := cmd.Execute()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "accepts 1 arg(s)")
	})

	t.Run("invalid since value", func(t *testing.T) {
		cmd := NewToolsLogsConnectionsCmd(zlogTest)
		// Silence usage/errors for cleaner test output
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs([]string{"sfinfra", "--since", "not-a-duration", "--gcp-project", "test"})

		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parsing time range")
	})

	t.Run("missing gcp-project for gcp backend", func(t *testing.T) {
		cmd := NewToolsLogsConnectionsCmd(zlogTest)
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs([]string{"sfinfra", "--since", "1h"})

		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "gcp-project is required")
	})
}

func TestLogsCommandStructure(t *testing.T) {
	logsCmd := NewToolsLogsCmd(zlogTest)

	// Create a root command to simulate the real command tree
	rootCmd := &cobra.Command{Use: "firecore"}
	toolsCmd := &cobra.Command{Use: "tools"}
	substreamsCmd := &cobra.Command{Use: "substreams"}

	rootCmd.AddCommand(toolsCmd)
	toolsCmd.AddCommand(substreamsCmd)
	substreamsCmd.AddCommand(logsCmd)

	// Test command path
	connectionsCmd, _, err := rootCmd.Find([]string{"tools", "substreams", "logs", "connections"})
	require.NoError(t, err)
	assert.Equal(t, "connections <user_id>", connectionsCmd.Use)
}

func TestParseTimeRange(t *testing.T) {
	now := time.Now()

	t.Run("default lookback", func(t *testing.T) {
		start, end, err := parseTimeRange("", time.Hour)
		require.NoError(t, err)
		assert.WithinDuration(t, now, end, time.Second)
		assert.WithinDuration(t, now.Add(-time.Hour), start, time.Second)
	})

	t.Run("default lookback, different value", func(t *testing.T) {
		start, end, err := parseTimeRange("", 7*24*time.Hour)
		require.NoError(t, err)
		assert.WithinDuration(t, now, end, time.Second)
		assert.WithinDuration(t, now.Add(-7*24*time.Hour), start, time.Second)
	})

	t.Run("since 30m", func(t *testing.T) {
		start, end, err := parseTimeRange("30m", time.Hour)
		require.NoError(t, err)
		assert.WithinDuration(t, now, end, time.Second)
		assert.WithinDuration(t, now.Add(-30*time.Minute), start, time.Second)
	})

	t.Run("since 2d", func(t *testing.T) {
		start, end, err := parseTimeRange("2d", time.Hour)
		require.NoError(t, err)
		assert.WithinDuration(t, now, end, time.Second)
		assert.WithinDuration(t, now.Add(-48*time.Hour), start, time.Second)
	})

	t.Run("since 1w", func(t *testing.T) {
		start, end, err := parseTimeRange("1w", time.Hour)
		require.NoError(t, err)
		assert.WithinDuration(t, now, end, time.Second)
		assert.WithinDuration(t, now.Add(-7*24*time.Hour), start, time.Second)
	})

	t.Run("invalid since", func(t *testing.T) {
		_, _, err := parseTimeRange("not-a-duration", time.Hour)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unrecognized date-range format")
	})

	t.Run("since full range RFC3339", func(t *testing.T) {
		start, end, err := parseTimeRange("2024-01-15T10:00:00Z/2024-01-15T12:00:00Z", time.Hour)
		require.NoError(t, err)
		expectedStart, _ := time.Parse(time.RFC3339, "2024-01-15T10:00:00Z")
		expectedEnd, _ := time.Parse(time.RFC3339, "2024-01-15T12:00:00Z")
		assert.Equal(t, expectedStart, start)
		assert.Equal(t, expectedEnd, end)
	})

	t.Run("since colon-separated range", func(t *testing.T) {
		start, end, err := parseTimeRange("2024-01-15T10:00:00Z:2024-01-15T12:00:00Z", time.Hour)
		require.NoError(t, err)
		expectedStart, _ := time.Parse(time.RFC3339, "2024-01-15T10:00:00Z")
		expectedEnd, _ := time.Parse(time.RFC3339, "2024-01-15T12:00:00Z")
		assert.Equal(t, expectedStart, start)
		assert.Equal(t, expectedEnd, end)
	})

	t.Run("since single timestamp", func(t *testing.T) {
		start, end, err := parseTimeRange("2024-01-15T10:00:00Z", time.Hour)
		require.NoError(t, err)
		expectedStart, _ := time.Parse(time.RFC3339, "2024-01-15T10:00:00Z")
		assert.Equal(t, expectedStart, start)
		assert.WithinDuration(t, time.Now(), end, time.Second)
	})

	t.Run("since range end before start fails", func(t *testing.T) {
		_, _, err := parseTimeRange("2024-01-15T12:00:00Z/2024-01-15T10:00:00Z", time.Hour)
		require.Error(t, err)
	})
}

func TestParseDateTime(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"RFC3339", "2024-01-15T10:00:00Z", "2024-01-15T10:00:00Z", false},
		{"RFC3339Nano", "2024-01-15T10:00:00.123456789Z", "2024-01-15T10:00:00.123456789Z", false},
		{"simple date", "2024-01-15", "2024-01-15T00:00:00Z", false},
		{"date with time no tz", "2024-01-15T10:00:00", "2024-01-15T10:00:00Z", false},
		{"invalid", "not-a-date", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDateTime(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			// For date-only input, it becomes midnight UTC
			if tt.input == "2024-01-15" {
				expected, _ := time.Parse("2006-01-02", "2024-01-15")
				assert.Equal(t, expected, got)
			} else if tt.input == "2024-01-15T10:00:00" {
				expected, _ := time.Parse("2006-01-02T15:04:05", "2024-01-15T10:00:00")
				assert.Equal(t, expected, got)
			}
		})
	}
}
