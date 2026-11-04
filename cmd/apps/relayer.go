package apps

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/launcher"
	"github.com/streamingfast/firehose-core/relayer"
	relayerapp "github.com/streamingfast/firehose-core/relayer/app/relayer"
	"go.uber.org/zap"
)

func RegisterRelayerApp(rootLog *zap.Logger) {
	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          "relayer",
		Title:       "Relayer",
		Description: "Serves blocks as a stream, with a buffer",
		RegisterFlags: func(cmd *cobra.Command) error {
			cmd.Flags().String("relayer-grpc-listen-addr", firecore.RelayerServingAddr, "Address to listen for incoming gRPC requests")
			cmd.Flags().String("relayer-http-healthz-addr", firecore.RelayerHTTPHealthzAddr, "Address to listen on for the HTTP /healthz endpoint. Set to an empty string to disable. Returns 200 when ready, 503 otherwise.")
			cmd.Flags().StringSlice("relayer-source", []string{firecore.ReaderNodeGRPCAddr}, strings.TrimSpace(`
List of live sources (reader nodes) to connect to for live block feeds (repeat flag as needed).

Each address supports:
  - Environment variable interpolation using the syntax ${ENV_VAR_NAME}, e.g. ":${READER_PORT}"
  - An optional secret key appended as a query parameter: "<addr>?secret=<key>"
  - An optional reconnection interval appended as a query parameter: "<addr>?retry_interval=<duration>".
    It is the minimum time between two connection attempts to that source, and must be at least 5s.
    Sources are checked every 5s, so the interval is rounded up to the next 5s increment
    (e.g. 12s behaves as 15s). Without it, the source is retried every 5s.

Examples:
  :10010
  :10010?secret=mysecret
  :10010?retry_interval=120s
  ${READER_HOST}:10010?secret=${READER_SECRET}&retry_interval=2m
`))
			cmd.Flags().Duration("relayer-max-source-latency", 999999*time.Hour, "Max latency tolerated to connect to a source. A performance optimization for when you have redundant sources and some may not have caught up")
			return nil
		},
		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			sfDataDir := runtime.AbsDataDir

			_, oneBlocksStoreURL, _, err := firecore.GetCommonStoresURLs(sfDataDir)
			if err != nil {
				return nil, err
			}

			rawSources := viper.GetStringSlice("relayer-source")
			sources := make([]relayer.SourceAddr, 0, len(rawSources))
			for _, raw := range rawSources {
				source, err := relayer.ParseSourceAddress(raw)
				if err != nil {
					return nil, fmt.Errorf("parsing relayer sources: %w", err)
				}
				sources = append(sources, source)
			}

			return relayerapp.New(&relayerapp.Config{
				Sources:               sources,
				OneBlocksURL:          oneBlocksStoreURL,
				GRPCListenAddr:        viper.GetString("relayer-grpc-listen-addr"),
				HTTPHealthzListenAddr: viper.GetString("relayer-http-healthz-addr"),
				MaxSourceLatency:      viper.GetDuration("relayer-max-source-latency"),
				IsPendingShutdown:     runtime.IsPendingShutdown,
			}), nil
		},
	})
}
