package logs

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	dateRangeFlag := flags.Lookup("date-range")
	require.NotNil(t, dateRangeFlag, "date-range flag should exist")

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
	assert.Contains(t, output, "--date-range")
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

	t.Run("mutually exclusive since and date-range", func(t *testing.T) {
		cmd := NewToolsLogsConnectionsCmd(zlogTest)
		// Silence usage/errors for cleaner test output
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs([]string{"sfinfra", "--since", "1h", "--date-range", "2024-01-15T10:00:00Z", "--gcp-project", "test"})

		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
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
