// Copyright 2021 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package apps

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/dauth"
	discoveryservice "github.com/streamingfast/dgrpc/server/discovery-service"
	"github.com/streamingfast/dsession"
	_ "github.com/streamingfast/dsession/local"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/launcher"
	"github.com/streamingfast/logging"
	app "github.com/streamingfast/substreams/app"
	"github.com/streamingfast/substreams/wasm"
	"go.uber.org/zap"
)

var ss1HeadBlockNumMetric = metricset.NewHeadBlockNumber("substreams-tier1")
var ss1HeadTimeDriftmetric = metricset.NewHeadTimeDrift("substreams-tier1")

func RegisterSubstreamsTier1App[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) {
	appLogger, _ := logging.PackageLogger("substreams-tier1", "github.com/streamingfast/firehose-core/firehose-ethereum/substreams-tier1")

	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          "substreams-tier1",
		Title:       "Substreams tier1 server",
		Description: "Provides a substreams grpc endpoint",
		RegisterFlags: func(cmd *cobra.Command) error {
			cmd.Flags().String("substreams-tier1-grpc-listen-addr", firecore.SubstreamsTier1GRPCServingAddr, "Address on which the Substreams tier1 will listen, listen by default in plain text, appending a '*' to the end of the address make it listen in snake-oil (inscure) TLS")
			cmd.Flags().String("substreams-tier1-subrequests-endpoint", firecore.SubstreamsTier2GRPCServingAddr, "Address on which the Substreans tier1 can reach the tier2")
			cmd.Flags().String("substreams-tier1-subrequests-secret-key", "", "Secret key to use for backend authentication with the tier2. Supports environment variable interpolation with ${ENV_VAR_NAME} syntax.")
			cmd.Flags().String("substreams-tier1-discovery-service-url", "", "URL to configure the grpc discovery service, used for communication with tier2") //traffic-director://xds?vpc_network=vpc-global&use_xds_reds=true
			cmd.Flags().Bool("substreams-tier1-subrequests-insecure", false, "Connect to tier2 without checking certificate validity")
			cmd.Flags().Bool("substreams-tier1-subrequests-plaintext", true, "Connect to tier2 without client in plaintext mode")
			cmd.Flags().Bool("substreams-tier1-enforce-compression", true, "Reject any request that does not accept gzip or zstd encoding in their GRPC/Connect header")
			cmd.Flags().Int("substreams-tier1-max-subrequests", 4, "default number of parallel subrequests that the tier1 makes to the tier2 per request")
			cmd.Flags().String("substreams-tier1-block-type", "", "Block type to use for the substreams tier1 (Ex: sf.ethereum.type.v2.Block)")
			cmd.Flags().Int("substreams-tier1-active-requests-soft-limit", 0, cli.FlagDescription(`
				The number of client active requests that a tier1 accepts before starting to be report itself as 'unready' within the health
				check endpoint. A limit of 0 or less means no limit.

				This is useful to load balance active requests more easily across a pool of tier1 instance. When the instance reaches the soft
				limit, it will start to be unready from the load balancer standpoint. The load balancer in return will remove it from the list
				of available instances, and new connections will be routed to remaining clients, spreading the load.
			`))
			cmd.Flags().Int("substreams-tier1-active-requests-hard-limit", 0, cli.FlagDescription(`
				The maximum number of client active requests that a tier1 accepts before rejecting incoming gRPC requests with 'Unavailable' code
				and setting itself as unready. A limit of 0 or less means no limit.

				This is useful to prevent the tier1 from being overwhelmed by too many requests, most client auto-reconnects on 'Unavailable' code
				so they should end up on another tier1 instance, assuming you have proper auto-scaling of the number of instances available.
			`))
			cmd.Flags().String("substreams-tier1-quicksave-store", "", "If enabled, substreams will use this store to put 'quicksave' data when shutting down while running requests with 'stores'. Use this flag with a non-zero --common-system-shutdown-signal-delay")
			cmd.Flags().String("substreams-tier1-global-worker-pool-address", "", "Address of the global worker pool to use for the substreams tier1. (disabled if empty)")
			cmd.Flags().String("substreams-tier1-global-request-pool-address", "", "Address of the global worker pool to use for the substreams tier1. (disabled if empty)")
			cmd.Flags().Duration("substreams-tier1-global-worker-pool-keep-alive-delay", 25*time.Second, "Delay between two keep alive call to the global worker pool. Default is 25s")
			cmd.Flags().Duration("substreams-tier1-global-request-pool-keep-alive-delay", 25*time.Second, "Delay between two keep alive call to the global worker pool for request. Default is 25s")
			cmd.Flags().Uint64("substreams-tier1-default-max-request-per-user", 3, "default max request per user, this will be use of the global worker pool is not reachable. Default is 5")
			cmd.Flags().Uint64("substreams-tier1-default-minimal-request-life-time-second", 180, "default minimal request request life time, this will be use of the global worker pool is not reachable.")
			cmd.Flags().String("substreams-tier1-foundational-stores-config-path", "", "default path for foundational stores endpoint configuration file")
			cmd.Flags().String("substreams-tier1-hosted-store-registry-address", "", "gRPC address of the control-plane registry service used to resolve hosted foundational stores (legacy/current stores continue to use the JSON config path)")
			cmd.Flags().Uint64("substreams-tier1-output-buffer-size", 100, "max number of messages bundled into BlockScopedDatas (for clients using v4)")
			cmd.Flags().Uint64("substreams-tier1-store-size-limit", 0, "Override the default store size limit (in bytes) for tier2 stores. 0 keeps the default of 1GiB. Forwarded to tier2 on each subrequest.")
			// all substreams
			registerCommonSubstreamsFlags(cmd)
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

			sfDataDir := runtime.AbsDataDir

			rawServiceDiscoveryURL := viper.GetString("substreams-tier1-discovery-service-url")

			blockType := runtime.InfoServer.GetBlockType()
			if fromFlag := viper.GetString("substreams-tier1-block-type"); fromFlag != "" {
				blockType = fromFlag
			}

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

			var wasmExtensions wasm.WASMExtensioner
			if chain.RegisterSubstreamsExtensions != nil {
				exts, err := chain.RegisterSubstreamsExtensions()
				if err != nil {
					return nil, fmt.Errorf("substreams extensions: %w", err)
				}
				wasmExtensions = exts
			}

			tmpDir, err := firecore.GetTmpDir(sfDataDir)
			if err != nil {
				return nil, fmt.Errorf("getting temporary directory: %w", err)
			}

			config := app.NewDefaultTier1Config()
			config.MeteringConfig = GetCommonMeteringPluginValue()
			config.EnforceCompression = viper.GetBool("substreams-tier1-enforce-compression")
			config.MergedBlocksStoreURL = mergedBlocksStoreURL
			config.OneBlocksStoreURL = oneBlocksStoreURL
			config.ForkedBlocksStoreURL = forkedBlocksStoreURL
			config.BlockStreamAddr = viper.GetString("common-live-blocks-addr")
			config.TmpDir = tmpDir
			config.StateStoreURL = firecore.MustReplaceDataDir(sfDataDir, viper.GetString("substreams-state-store-url"))
			config.StateStoreDefaultTag = viper.GetString("substreams-state-store-default-tag")
			config.StateBundleSize = viper.GetUint64("substreams-state-bundle-size")
			config.MergedBlocksBundleSize = viper.GetUint64("common-merged-blocks-bundle-size")
			config.MaxSubrequests = viper.GetUint64("substreams-tier1-max-subrequests")
			config.SubrequestsEndpoint = viper.GetString("substreams-tier1-subrequests-endpoint")
			config.ActiveRequestsSoftLimit = viper.GetInt("substreams-tier1-active-requests-soft-limit")
			config.ActiveRequestsHardLimit = viper.GetInt("substreams-tier1-active-requests-hard-limit")
			config.SubrequestsInsecure = viper.GetBool("substreams-tier1-subrequests-insecure")
			config.SubrequestsPlaintext = viper.GetBool("substreams-tier1-subrequests-plaintext")
			config.SubrequestsSecret = os.Expand(viper.GetString("substreams-tier1-subrequests-secret-key"), os.Getenv)
			config.BlockType = blockType
			config.WASMExtensions = wasmExtensions
			config.BlockExecutionTimeout = viper.GetDuration("substreams-block-execution-timeout")
			config.Tracing = os.Getenv("SUBSTREAMS_TRACING") == "modules_exec"
			config.GRPCListenAddr = viper.GetString("substreams-tier1-grpc-listen-addr")
			config.GRPCShutdownGracePeriod = time.Second
			config.ServiceDiscoveryURL = serviceDiscoveryURL
			config.QuickSaveStoreURL = viper.GetString("substreams-tier1-quicksave-store")
			config.FoundationalStoresConfigPath = viper.GetString("substreams-tier1-foundational-stores-config-path")
			config.HostedStoreRegistryAddress = viper.GetString("substreams-tier1-hosted-store-registry-address")
			config.OutputBufferSize = viper.GetUint64("substreams-tier1-output-buffer-size")
			config.StoresScratchSpace = firecore.MustReplaceDataDir(sfDataDir, viper.GetString("substreams-stores-scratch-space"))
			config.StoresBackend = viper.GetString("substreams-stores-backend")
			config.StoreSizeLimit = viper.GetUint64("substreams-tier1-store-size-limit")

			sessionPlugin := viper.GetString("common-session-plugin")
			sessionPool, err := dsession.New(sessionPlugin, appLogger)
			if err != nil {
				return nil, fmt.Errorf("unable to create session pool: %w", err)
			}

			return app.NewTier1(appLogger,
				config, &app.Tier1Modules{
					Authenticator:         authenticator,
					HeadTimeDriftMetric:   ss1HeadTimeDriftmetric,
					HeadBlockNumberMetric: ss1HeadBlockNumMetric,
					CheckPendingShutDown:  runtime.IsPendingShutdown,
					InfoServer:            runtime.InfoServer,
					SessionPool:           sessionPool,
				}), nil
		},
	})
}
