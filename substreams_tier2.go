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

package firecore

import (
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	discoveryservice "github.com/streamingfast/dgrpc/server/discovery-service"
	"github.com/streamingfast/dlauncher/launcher"
	"github.com/streamingfast/logging"
	app "github.com/streamingfast/substreams/app"
)

var ss2HeadBlockNumMetric = metricset.NewHeadBlockNumber("substreams-tier2")
var ss2HeadTimeDriftmetric = metricset.NewHeadTimeDrift("substreams-tier2")

func registerSubstreamsTier2App[B Block](chain *Chain[B]) {
	appLogger, _ := logging.PackageLogger("substreams-tier2", "github.com/streamingfast/firehose-ethereum/substreams-tier2")

	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          "substreams-tier2",
		Title:       "Substreams tier2 server",
		Description: "Provides a substreams grpc endpoint",
		RegisterFlags: func(cmd *cobra.Command) error {
			cmd.Flags().String("substreams-tier2-grpc-listen-addr", SubstreamsTier2GRPCServingAddr, "Address on which the substreams tier2 will listen. Default is plain-text, appending a '*' to the end to jkkkj")
			cmd.Flags().String("substreams-tier2-discovery-service-url", "", "URL to advertise presence to the grpc discovery service") //traffic-director://xds?vpc_network=vpc-global&use_xds_reds=true

			// all substreams
			registerCommonSubstreamsFlags(cmd)
			return nil
		},

		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			mergedBlocksStoreURL, _, _, err := GetCommonStoresURLs(runtime.AbsDataDir)
			if err != nil {
				return nil, err
			}

			sfDataDir := runtime.AbsDataDir

			rawServiceDiscoveryURL := viper.GetString("substreams-tier2-discovery-service-url")
			grpcListenAddr := viper.GetString("substreams-tier2-grpc-listen-addr")

			stateStoreURL := MustReplaceDataDir(sfDataDir, viper.GetString("substreams-state-store-url"))
			stateStoreDefaultTag := viper.GetString("substreams-state-store-default-tag")

			stateBundleSize := viper.GetUint64("substreams-state-bundle-size")

			tracing := os.Getenv("SUBSTREAMS_TRACING") == "modules_exec"

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

			wasmExtensions, pipelineOptioner, err := getSubstreamsExtensions(chain)
			if err != nil {
				return nil, fmt.Errorf("substreams extensions: %w", err)
			}

			return app.NewTier2(appLogger,
				&app.Tier2Config{
					MergedBlocksStoreURL: mergedBlocksStoreURL,

					StateStoreURL:        stateStoreURL,
					StateStoreDefaultTag: stateStoreDefaultTag,
					StateBundleSize:      stateBundleSize,
					BlockType:            getSubstreamsBlockMessageType(chain),

					WASMExtensions:  wasmExtensions,
					PipelineOptions: pipelineOptioner,

					Tracing: tracing,

					GRPCListenAddr:      grpcListenAddr,
					ServiceDiscoveryURL: serviceDiscoveryURL,
				}), nil
		},
	})
}
