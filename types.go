package firecore

import "github.com/spf13/cobra"

type CommandExecutor func(cmd *cobra.Command, args []string) (err error)
