package logs

import (
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// NewToolsLogsCmd returns the parent "logs" command for Substreams logs analysis tools
func NewToolsLogsCmd(logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Substreams logs analysis tools",
		Long: `Tools for analyzing Substreams logs from various backends.

Note: Currently only GCP Cloud Logging backend is supported.`,
	}

	cmd.AddCommand(NewToolsLogsConnectionsCmd(logger))

	return cmd
}
