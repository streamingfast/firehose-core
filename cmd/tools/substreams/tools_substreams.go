package substreams

import (
	"github.com/spf13/cobra"
	firecore "github.com/streamingfast/firehose-core"
	"go.uber.org/zap"
)

func NewToolsSubstreamsCmd[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "substreams",
		Short: "Substreams-related tools for operators",
	}

	cmd.AddCommand(NewToolsStoreSizeCmd(logger))
	cmd.AddCommand(NewToolsLogsCmd(logger))

	return cmd
}
