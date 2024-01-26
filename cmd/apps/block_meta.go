package apps

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/cli"
	firecore "github.com/streamingfast/firehose-core"
	blockmeta "github.com/streamingfast/firehose-core/block-meta"
	appblockmeta "github.com/streamingfast/firehose-core/block-meta/app"
	"github.com/streamingfast/firehose-core/launcher"
	"github.com/streamingfast/logging"
	"go.uber.org/zap"
)

var appLogger, appTracer = logging.PackageLogger("block-meta", "block-meta")

func RegisterBlockMetaApp[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) {
	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:    "block-meta",
		Title: "Block metadata indexer",
		Description: cli.Dedent(`
			App that builds the block metadata index on disk.

			Today the indexing is performed on disk using simple file. The indexer is receiving a stream of
			final blocks and write them to the storage disk.

			Firehose is going to use this index on disk to serve the block metadata to the clients.
		`),
		RegisterFlags: func(cmd *cobra.Command) error {
			cmd.Flags().String("block-meta-grpc-listen-addr", firecore.BlockMetaServiceAddr, "Address to listen for gRPC & HTTP healthz check")
			cmd.Flags().Uint64("block-meta-start-block", 0, "Block number to start indexing at")
			cmd.Flags().Uint64("block-meta-stop-block", 0, "Block number to stop indexing at, leave as 0 for indexing forever")
			return nil
		},
		InitFunc: func(runtime *launcher.Runtime) error {
			return nil
		},
		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			mergedBlocksStoreURL, _, _, err := firecore.GetCommonStoresURLs(runtime.AbsDataDir)
			if err != nil {
				return nil, fmt.Errorf("get common store urls: %w", err)
			}

			blockMetaStoreURL, err := firecore.GetBlockMetaStoreURL(runtime.AbsDataDir)
			if err != nil {
				return nil, fmt.Errorf("get block meta store url: %w", err)
			}

			startBlockResolver := func(ctx context.Context) (uint64, error) {
				select {
				case <-ctx.Done():
					return 0, ctx.Err()
				default:
				}

				// If the start block is set in the config, use it
				if startBlock := viper.GetUint64("block-meta-start-block"); startBlock != 0 {
					return startBlock, nil
				}

				startBlockNum, err := blockmeta.GetStartBlock(ctx, blockMetaStoreURL, appLogger, appTracer)
				if err != nil {
					return 0, fmt.Errorf("block meta get start block: %w", err)
				}

				return startBlockNum, nil
			}

			app := appblockmeta.New(&appblockmeta.Config{
				StartBlockResolver:   startBlockResolver,
				EndBlock:             viper.GetUint64("block-meta-stop-block"),
				MergedBlocksStoreURL: mergedBlocksStoreURL,
				BlockMetaStoreURL:    blockMetaStoreURL,
				GRPCListenAddr:       viper.GetString("block-met-grpc-listen-addr"),
			}, appLogger, appTracer)

			return app, nil
		},
	})
}
