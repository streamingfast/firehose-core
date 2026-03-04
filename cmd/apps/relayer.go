package apps

import (
	"fmt"
	"net/url"
	"os"
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
			cmd.Flags().StringSlice("relayer-source", []string{firecore.ReaderNodeGRPCAddr}, strings.TrimSpace(`
List of live sources (reader nodes) to connect to for live block feeds (repeat flag as needed).

Each address supports:
  - Environment variable interpolation using the syntax ${ENV_VAR_NAME}, e.g. ":${READER_PORT}"
  - An optional secret key appended as a query parameter: "<addr>?secret=<key>"

Examples:
  :10010
  :10010?secret=mysecret
  ${READER_HOST}:10010?secret=${READER_SECRET}
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
			sources, err := parseSourceAddresses(rawSources)
			if err != nil {
				return nil, fmt.Errorf("parsing relayer sources: %w", err)
			}

			return relayerapp.New(&relayerapp.Config{
				Sources:          sources,
				OneBlocksURL:     oneBlocksStoreURL,
				GRPCListenAddr:   viper.GetString("relayer-grpc-listen-addr"),
				MaxSourceLatency: viper.GetDuration("relayer-max-source-latency"),
			}), nil
		},
	})
}

// parseSourceAddresses processes a slice of raw source address strings, performing:
//  1. Environment variable interpolation: ${ENV_VAR_NAME} is replaced with os.Getenv("ENV_VAR_NAME").
//  2. Secret key extraction: if the address contains "?secret=<key>", the key is stripped from the
//     URL and stored separately in SourceAddr.SecretKey.
//
// Example inputs:
//
//	":10010"                          -> SourceAddr{URL: ":10010"}
//	":10010?secret=abc"               -> SourceAddr{URL: ":10010", SecretKey: "abc"}
//	"${HOST}:10010?secret=${SECRET}"  -> SourceAddr{URL: "<HOST>:10010", SecretKey: "<SECRET>"}
func parseSourceAddresses(raw []string) ([]relayer.SourceAddr, error) {
	out := make([]relayer.SourceAddr, 0, len(raw))
	for _, entry := range raw {
		// 1. Expand environment variables.
		expanded := os.Expand(entry, os.Getenv)

		// 2. Split on '?' to separate the address from the query string.
		//    We do not use net/url.Parse directly because the address portion
		//    (e.g. "dns:///host:port" or ":10010") is not a valid URL on its own.
		addr, query, hasQuery := strings.Cut(expanded, "?")
		if addr == "" {
			return nil, fmt.Errorf("empty address in source %q (after env expansion: %q)", entry, expanded)
		}

		sa := relayer.SourceAddr{URL: addr}

		if hasQuery {
			// Parse only the query portion so we can extract "secret".
			vals, err := url.ParseQuery(query)
			if err != nil {
				return nil, fmt.Errorf("invalid query string in source %q: %w", expanded, err)
			}
			sa.SecretKey = vals.Get("secret")
		}

		out = append(out, sa)
	}
	return out, nil
}
