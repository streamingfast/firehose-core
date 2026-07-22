package apps

import (
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/bstream/transform"
	"github.com/streamingfast/dauth"
	discoveryservice "github.com/streamingfast/dgrpc/server/discovery-service"
	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/dsession"
	_ "github.com/streamingfast/dsession/local"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/firehose/app/firehose"
	"github.com/streamingfast/firehose-core/firehose/server"
	"github.com/streamingfast/firehose-core/launcher"
	coremetrics "github.com/streamingfast/firehose-core/metrics"
	"github.com/streamingfast/logging"
	"go.uber.org/zap"
)

var metricset = dmetrics.NewSet()
var headBlockNumMetric = metricset.NewHeadBlockNumber("firehose")
var headTimeDriftmetric = metricset.NewHeadTimeDrift("firehose")
var finalizedBlockNumMetric = coremetrics.NewFinalizedBlockNumber("firehose")

func RegisterFirehoseApp[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) {
	appLogger, appTracer := logging.PackageLogger("firehose", "firehose")

	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          "firehose",
		Title:       "Block Firehose",
		Description: "Provides on-demand filtered blocks, depends on common-merged-blocks-store-url and common-live-blocks-addr",
		RegisterFlags: func(cmd *cobra.Command) error {
			cmd.Flags().String("firehose-grpc-listen-addr", firecore.FirehoseGRPCServingAddr, "Address on which the firehose will listen")
			cmd.Flags().String("firehose-discovery-service-url", "", "Url to configure the gRPC discovery service") //traffic-director://xds?vpc_network=vpc-global&use_xds_reds=true
			cmd.Flags().Int("firehose-rate-limit-bucket-size", -1, "Rate limit bucket size (default: no rate limit)")
			cmd.Flags().Duration("firehose-rate-limit-bucket-fill-rate", 10*time.Second, "Rate limit bucket refill rate (default: 10s)")
			cmd.Flags().Bool("firehose-enforce-compression", true, "Reject any request that does not accept gzip or zstd encoding in their GRPC/Connect header")
			cmd.Flags().Bool("firehose-discard-partial-blocks", false, "Drop partial (flash) blocks coming from the live source before they reach the forkable hub; use to disable flash/partial block processing in the firehose app")

			return nil
		},

		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			authPlugin := viper.GetString("common-auth-plugin")
			appLogger.Info("auth plugin instantiation", zap.String("plugin_kind", authPluginScheme(authPlugin)))

			authenticator, err := dauth.New(authPlugin, appLogger)
			if err != nil {
				return nil, fmt.Errorf("unable to initialize auth plugin: %w", err)
			}
			mergedBlocksStoreURL, oneBlocksStoreURL, forkedBlocksStoreURL, err := firecore.GetCommonStoresURLs(runtime.AbsDataDir)
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

			indexStore, possibleIndexSizes, err := firecore.GetIndexStore(runtime.AbsDataDir)
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

			var serverOptions []server.Option

			if viper.GetBool("firehose-enforce-compression") {
				serverOptions = append(serverOptions, server.WithEnforceCompression(true))
			}

			sessionPlugin := viper.GetString("common-session-plugin")
			sessionPool, err := dsession.New(sessionPlugin, appLogger)
			if err != nil {
				return nil, fmt.Errorf("unable to create session pool: %w", err)
			}
			serverOptions = append(serverOptions, server.WithSessionPool(sessionPool))

			limiterSize := viper.GetInt("firehose-rate-limit-bucket-size")
			limiterRefillRate := viper.GetDuration("firehose-rate-limit-bucket-fill-rate")
			if limiterSize > 0 {
				serverOptions = append(serverOptions, server.WithLeakyBucketLimiter(limiterSize, limiterRefillRate))
			}

			return firehose.New(appLogger, appTracer, &firehose.Config{
				MergedBlocksStoreURL:    mergedBlocksStoreURL,
				OneBlocksStoreURL:       oneBlocksStoreURL,
				ForkedBlocksStoreURL:    forkedBlocksStoreURL,
				BlockStreamAddr:         viper.GetString("common-live-blocks-addr"),
				DiscardPartialBlocks:    viper.GetBool("firehose-discard-partial-blocks"),
				GRPCListenAddr:          viper.GetString("firehose-grpc-listen-addr"),
				GRPCShutdownGracePeriod: 1 * time.Second,
				ServiceDiscoveryURL:     serviceDiscoveryURL,
				ServerOptions:           serverOptions,
			}, &firehose.Modules{
				Authenticator:              authenticator,
				SessionPool:                sessionPool,
				HeadTimeDriftMetric:        headTimeDriftmetric,
				HeadBlockNumberMetric:      headBlockNumMetric,
				FinalizedBlockNumberMetric: finalizedBlockNumMetric,
				TransformRegistry:          registry,
				CheckPendingShutdown:       runtime.IsPendingShutdown,
				InfoServer:                 runtime.InfoServer,
			}), nil
		},
	})
}
