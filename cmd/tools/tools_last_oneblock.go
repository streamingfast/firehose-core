package tools

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dstore"
	"go.uber.org/zap"
)

func NewToolsLastOneBlockCmd(logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "last-oneblock <oneblocks-store>",
		Short: "Print the block number of the most recent one-block file in a store",
		Long: `Walks the given one-blocks store and prints the highest block number found among its
one-block files, as a bare number on stdout. Exits non-zero when the store cannot be
listed, holds no one-block file, or a filename does not parse as one.`,
		Example: `  firecore tools last-oneblock gs://my-bucket/eth-mainnet/v1-oneblock`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			store, err := dstore.NewDBinStore(args[0])
			if err != nil {
				return fmt.Errorf("creating store for %q: %w", args[0], err)
			}

			found := false
			var last uint64
			err = store.Walk(cmd.Context(), "", func(filename string) error {
				file, err := bstream.NewOneBlockFile(filename)
				if err != nil {
					return fmt.Errorf("parsing one-block filename %q: %w", filename, err)
				}
				if !found || file.Num > last {
					last = file.Num
				}
				found = true
				return nil
			})
			if err != nil {
				return fmt.Errorf("walking store %q: %w", args[0], err)
			}
			if !found {
				return errors.New("no one-block file found")
			}

			fmt.Println(last)
			return nil
		},
	}

	return cmd
}
