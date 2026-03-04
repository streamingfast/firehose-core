package apps

import (
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/launcher"
	"github.com/streamingfast/firehose-core/relayer-quic/app/relayer"
	"go.uber.org/zap"
)

func RegisterRelayerQuicApp(rootLog *zap.Logger) {
	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          "relayer-quic",
		Title:       "Relayer QUIC",
		Description: "Serves blocks as a stream, with a buffer, using QUIC transport",
		RegisterFlags: func(cmd *cobra.Command) error {
			cmd.Flags().String("relayer-quic-grpc-listen-addr", firecore.RelayerQuicServingAddr, "Address to listen for incoming gRPC requests")
			cmd.Flags().StringSlice("relayer-quic-source", []string{"localhost" + firecore.ReaderNodeQuicCAddr}, "List of live sources (reader(s)) to connect to for live block feeds (repeat flag as needed)")
			cmd.Flags().Int("relayer-quic-source-request-burst", 0, "Burst size for source requests")
			cmd.Flags().Duration("relayer-quic-max-source-latency", 999999*time.Hour, "Max latency tolerated to connect to a source. A performance optimization for when you have redundant sources and some may not have caught up")
			return nil
		},
		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			sfDataDir := runtime.AbsDataDir

			_, oneBlocksStoreURL, _, err := firecore.GetCommonStoresURLs(sfDataDir)
			if err != nil {
				return nil, err
			}

			sourcesAddr := viper.GetStringSlice("relayer-quic-source")

			return relayer.New(&relayer.Config{
				SourcesAddr:        sourcesAddr,
				OneBlocksURL:       oneBlocksStoreURL,
				QuicListenAddr:     viper.GetString("relayer-quic-grpc-listen-addr"),
				SourceRequestBurst: viper.GetInt("relayer-quic-source-request-burst"),
				MaxSourceLatency:   viper.GetDuration("relayer-quic-max-source-latency"),
			}), nil
		},
	})
}
