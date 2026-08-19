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
	"github.com/streamingfast/dauth"
	discoveryservice "github.com/streamingfast/dgrpc/server/discovery-service"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/launcher"
	"github.com/streamingfast/logging"
	"github.com/streamingfast/substreams/app"
	"github.com/streamingfast/substreams/wasm"
	"go.uber.org/zap"
)

var ss2HeadBlockNumMetric = metricset.NewHeadBlockNumber("substreams-tier2")
var ss2HeadTimeDriftmetric = metricset.NewHeadTimeDrift("substreams-tier2")

func RegisterSubstreamsTier2App[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) {
	appLogger, _ := logging.PackageLogger("substreams-tier2", "github.com/streamingfast/firehose-core/firehose-ethereum/substreams-tier2")

	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          "substreams-tier2",
		Title:       "Substreams tier2 server",
		Description: "Provides a substreams grpc endpoint",
		RegisterFlags: func(cmd *cobra.Command) error {
			cmd.Flags().String("substreams-tier2-grpc-listen-addr", firecore.SubstreamsTier2GRPCServingAddr, "Address on which the substreams tier2 will listen. Default is plain-text, appending a '*' to the end to make it listen in snake-oil (insecure) TLS")
			cmd.Flags().String("substreams-tier2-authenticator", "trust://", "Authenticator to use for tier2 requests. Can be 'trust://' or 'secret://<key>'. Supports environment variable interpolation with ${ENV_VAR_NAME} syntax, e.g. 'secret://${TIER2_SECRET}'.")
			cmd.Flags().String("substreams-tier2-discovery-service-url", "", "URL to advertise presence to the grpc discovery service") //traffic-director://xds?vpc_network=vpc-global&use_xds_reds=true
			cmd.Flags().Uint64("substreams-tier2-max-concurrent-requests", 0, "Maximum number of concurrent requests allowed on the server. When the tier2 service hits this limit, it will set itself as 'Not Ready' until requests are processed. Default 0 (no limit)")
			cmd.Flags().Duration("substreams-tier2-segment-execution-timeout", 4*time.Hour, "Absolute backstop on a segment execution: maximum duration a segment can take, even while it keeps making progress, before being forcefully stopped with a DeadlineExceeded error")
			cmd.Flags().Duration("substreams-tier2-segment-stall-timeout", 10*time.Minute, "Maximum duration a segment can go without processing a single block before being forcefully stopped with a DeadlineExceeded error. Keep it well above --substreams-block-execution-timeout, which already bounds a single block")
			// all substreams
			registerCommonSubstreamsFlags(cmd)
			return nil
		},

		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			rawServiceDiscoveryURL := viper.GetString("substreams-tier2-discovery-service-url")
			grpcListenAddr := viper.GetString("substreams-tier2-grpc-listen-addr")

			maximumConcurrentRequests := viper.GetUint64("substreams-tier2-max-concurrent-requests")
			executionTimeout := viper.GetDuration("substreams-block-execution-timeout")
			segmentExecutionTimeout := viper.GetDuration("substreams-tier2-segment-execution-timeout")
			segmentStallTimeout := viper.GetDuration("substreams-tier2-segment-stall-timeout")

			tracing := os.Getenv("SUBSTREAMS_TRACING") == "modules_exec"

			var serviceDiscoveryURL *url.URL
			if rawServiceDiscoveryURL != "" {
				var err error
				svcURL, err := url.Parse(rawServiceDiscoveryURL)
				if err != nil {
					return nil, fmt.Errorf("unable to parse discovery service url: %w", err)
				}
				err = discoveryservice.Bootstrap(svcURL)
				if err != nil {
					return nil, fmt.Errorf("unable to bootstrap discovery service: %w", err)
				}
				serviceDiscoveryURL = svcURL
			}

			var wasmExtensions wasm.WASMExtensioner
			if chain.RegisterSubstreamsExtensions != nil {
				exts, err := chain.RegisterSubstreamsExtensions()
				if err != nil {
					return nil, fmt.Errorf("substreams extensions: %w", err)
				}
				wasmExtensions = exts
			}

			tmpDir, err := firecore.GetTmpDir(runtime.AbsDataDir)
			if err != nil {
				return nil, fmt.Errorf("getting temporary directory: %w", err)
			}

			authString := os.Expand(viper.GetString("substreams-tier2-authenticator"), os.Getenv)
			auth, err := dauth.New(authString, appLogger)
			if err != nil {
				return nil, fmt.Errorf("creating authenticator: %w", err)
			}

			config := app.NewDefaultTier2Config()
			config.Tracing = tracing
			config.GRPCListenAddr = grpcListenAddr
			config.ServiceDiscoveryURL = serviceDiscoveryURL
			config.TmpDir = tmpDir
			config.WASMExtensions = wasmExtensions
			config.BlockExecutionTimeout = executionTimeout
			config.SegmentExecutionTimeout = segmentExecutionTimeout
			config.SegmentStallTimeout = segmentStallTimeout
			config.MaximumConcurrentRequests = maximumConcurrentRequests
			config.StoresScratchSpace = firecore.MustReplaceDataDir(runtime.AbsDataDir, viper.GetString("substreams-stores-scratch-space"))
			config.StoresBackend = viper.GetString("substreams-stores-backend")

			return app.NewTier2(appLogger,
				config,
				&app.Tier2Modules{
					CheckPendingShutDown: runtime.IsPendingShutdown,
					Authenticator:        auth,
				}), nil
		},
	})
}
