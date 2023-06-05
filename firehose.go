package firecore

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	dauthAuthenticator "github.com/streamingfast/dauth/authenticator"
	discoveryservice "github.com/streamingfast/dgrpc/server/discovery-service"
	"github.com/streamingfast/dlauncher/launcher"
	"github.com/streamingfast/dmetering"
	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/dstore"
	firehoseApp "github.com/streamingfast/firehose/app/firehose"
	"github.com/streamingfast/logging"
	substreamsClient "github.com/streamingfast/substreams/client"
	substreamsService "github.com/streamingfast/substreams/service"
)

var metricset = dmetrics.NewSet()
var headBlockNumMetric = metricset.NewHeadBlockNumber("firehose")
var headTimeDriftmetric = metricset.NewHeadTimeDrift("firehose")

func registerFirehoseApp[B Block](chain *Chain[B]) {
	appLogger, _ := logging.PackageLogger("firehose", chain.LoggerPackageID("firehose"))

	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          "firehose",
		Title:       "Block Firehose",
		Description: "Provides on-demand filtered blocks, depends on common-merged-blocks-store-url and common-live-blocks-addr",
		RegisterFlags: func(cmd *cobra.Command) error {
			cmd.Flags().String("firehose-grpc-listen-addr", FirehoseGRPCServingAddr, "Address on which the firehose will listen")
			cmd.Flags().String("firehose-discovery-service-url", "", "Url to configure the gRPC discovery service") //traffic-director://xds?vpc_network=vpc-global&use_xds_reds=true

			cmd.Flags().Bool("substreams-enabled", false, "Whether to enable substreams")
			cmd.Flags().Bool("substreams-partial-mode-enabled", false, "Whether to enable partial stores generation support on this instance (usually for internal deployments only)")
			cmd.Flags().Bool("substreams-request-stats-enabled", false, "Enables stats per request, like block rate. Should only be enabled in debugging instance not in production")
			cmd.Flags().String("substreams-state-store-url", "{data-dir}/localdata", "where substreams state data are stored")
			cmd.Flags().Uint64("substreams-cache-save-interval", uint64(1_000), "Interval in blocks at which to save store snapshots and output caches")
			cmd.Flags().Uint64("substreams-max-fuel-per-block-module", uint64(5_000_000_000_000), "Hard limit for the number of instructions within the execution of a single wasmtime module for a single block")
			cmd.Flags().Int("substreams-parallel-subrequest-limit", 4, "Number of parallel subrequests substream can make to synchronize its stores")
			cmd.Flags().String("substreams-client-endpoint", "", "Firehose endpoint for substreams client, if left empty, will default to this current local firehose.")
			cmd.Flags().String("substreams-client-jwt", "", "JWT for substreams client authentication")
			cmd.Flags().Bool("substreams-client-insecure", false, "Substreams client in insecure mode")
			cmd.Flags().Bool("substreams-client-plaintext", true, "Substreams client in plaintext mode")
			cmd.Flags().Uint64("substreams-sub-request-parallel-jobs", 5, "Substreams subrequest parallel jobs for the scheduler")
			cmd.Flags().Uint64("substreams-sub-request-block-range-size", 10000, "Substreams subrequest block range size value for the scheduler")
			return nil
		},

		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			sfDataDir := runtime.AbsDataDir

			authenticator, err := dauthAuthenticator.New(viper.GetString("common-auth-plugin"))
			if err != nil {
				return nil, fmt.Errorf("unable to initialize dauth: %w", err)
			}

			metering, err := dmetering.New(viper.GetString("common-metering-plugin"))
			if err != nil {
				return nil, fmt.Errorf("unable to initialize dmetering: %w", err)
			}
			dmetering.SetDefaultMeter(metering)

			mergedBlocksStoreURL, oneBlocksStoreURL, forkedBlocksStoreURL, err := GetCommonStoresURLs(runtime.AbsDataDir)
			if err != nil {
				return nil, err
			}

			rawServiceDiscoveryURL := viper.GetString("firehose-discovery-service-url")
			var serviceDiscoveryURL *url.URL
			if rawServiceDiscoveryURL != "" {
				serviceDiscoveryURL, err = url.Parse(rawServiceDiscoveryURL)
				if err != nil {
					return nil, fmt.Errorf("unable to parse discovery service url: %w", err)
				}
				err = discoveryservice.Bootstrap(serviceDiscoveryURL)
				if err != nil {
					return nil, fmt.Errorf("unable to bootstrap discovery service: %w", err)
				}
			}

			var registerServiceExt firehoseApp.RegisterServiceExtensionFunc
			if viper.GetBool("substreams-enabled") {
				stateStore, err := dstore.NewStore(MustReplaceDataDir(sfDataDir, viper.GetString("substreams-state-store-url")), "", "", true)
				if err != nil {
					return nil, fmt.Errorf("setting up state store for data: %w", err)
				}

				opts := []substreamsService.Option{
					substreamsService.WithCacheSaveInterval(viper.GetUint64("substreams-cache-save-interval")),
					substreamsService.WithMaxWasmFuelPerBlockModule(viper.GetUint64("substreams-max-fuel-per-block-module")),
				}

				if viper.GetBool("substreams-partial-mode-enabled") {
					opts = append(opts, substreamsService.WithPartialMode())
				}

				clientEndpoint := viper.GetString("substreams-client-endpoint")
				if clientEndpoint == "" {
					clientEndpoint = viper.GetString("firehose-grpc-listen-addr")
				}

				clientConfig := substreamsClient.NewSubstreamsClientConfig(
					clientEndpoint,
					os.ExpandEnv(viper.GetString("substreams-client-jwt")),
					viper.GetBool("substreams-client-insecure"),
					viper.GetBool("substreams-client-plaintext"),
				)

				sss, err := substreamsService.New(
					stateStore,
					"sf.acme.type.v1",
					viper.GetUint64("substreams-sub-request-parallel-jobs"),
					viper.GetUint64("substreams-sub-request-block-range-size"),
					clientConfig,
					opts...,
				)
				if err != nil {
					return nil, fmt.Errorf("create substreams service: %w", err)
				}

				registerServiceExt = sss.Register
			}

			return firehoseApp.New(appLogger, &firehoseApp.Config{
				MergedBlocksStoreURL:    mergedBlocksStoreURL,
				OneBlocksStoreURL:       oneBlocksStoreURL,
				ForkedBlocksStoreURL:    forkedBlocksStoreURL,
				BlockStreamAddr:         viper.GetString("common-live-blocks-addr"),
				GRPCListenAddr:          viper.GetString("firehose-grpc-listen-addr"),
				GRPCShutdownGracePeriod: 1 * time.Second,
				ServiceDiscoveryURL:     serviceDiscoveryURL,
			}, &firehoseApp.Modules{
				Authenticator:            authenticator,
				HeadTimeDriftMetric:      headTimeDriftmetric,
				HeadBlockNumberMetric:    headBlockNumMetric,
				RegisterServiceExtension: registerServiceExt,
			}), nil
		},
	})
}
