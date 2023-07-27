package firecore

import (
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/bstream/transform"
	"github.com/streamingfast/dauth"
	discoveryservice "github.com/streamingfast/dgrpc/server/discovery-service"
	"github.com/streamingfast/dlauncher/launcher"
	"github.com/streamingfast/dmetrics"
	firehoseApp "github.com/streamingfast/firehose/app/firehose"
	firehoseServer "github.com/streamingfast/firehose/server"
	"github.com/streamingfast/logging"
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
			cmd.Flags().Int("firehose-rate-limit-bucket-size", -1, "Rate limit bucket size (default: no rate limit)")
			cmd.Flags().Duration("firehose-rate-limit-bucket-fill-rate", 10*time.Second, "Rate limit bucket refill rate (default: 10s)")

			return nil
		},

		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			authenticator, err := dauth.New(viper.GetString("common-auth-plugin"), appLogger)
			if err != nil {
				return nil, fmt.Errorf("unable to initialize authenticator: %w", err)
			}

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

			indexStore, possibleIndexSizes, err := GetIndexStore(runtime.AbsDataDir)
			if err != nil {
				return nil, fmt.Errorf("unable to initialize indexes: %w", err)
			}

			registry := transform.NewRegistry()
			for _, factory := range chain.BlockTransformerFactories {
				transformer, err := factory(indexStore, possibleIndexSizes)
				if err != nil {
					return nil, fmt.Errorf("unable to create transformer: %w", err)
				}

				registry.Register(transformer)
			}

			var serverOptions []firehoseServer.Option

			limiterSize := viper.GetInt("firehose-rate-limit-bucket-size")
			limiterRefillRate := viper.GetDuration("firehose-rate-limit-bucket-fill-rate")
			if limiterSize > 0 {
				serverOptions = append(serverOptions, firehoseServer.WithLeakyBucketLimiter(limiterSize, limiterRefillRate))
			}

			return firehoseApp.New(appLogger, &firehoseApp.Config{
				MergedBlocksStoreURL:    mergedBlocksStoreURL,
				OneBlocksStoreURL:       oneBlocksStoreURL,
				ForkedBlocksStoreURL:    forkedBlocksStoreURL,
				BlockStreamAddr:         viper.GetString("common-live-blocks-addr"),
				GRPCListenAddr:          viper.GetString("firehose-grpc-listen-addr"),
				GRPCShutdownGracePeriod: 1 * time.Second,
				ServiceDiscoveryURL:     serviceDiscoveryURL,
				ServerOptions:           serverOptions,
			}, &firehoseApp.Modules{
				Authenticator:         authenticator,
				HeadTimeDriftMetric:   headTimeDriftmetric,
				HeadBlockNumberMetric: headBlockNumMetric,
				TransformRegistry:     registry,
			}), nil
		},
	})
}
