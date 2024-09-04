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
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/launcher"
	nodeManager "github.com/streamingfast/firehose-core/node-manager"
	"github.com/streamingfast/firehose-core/node-manager/app/firehose_reader"
	"github.com/streamingfast/firehose-core/node-manager/metrics"
	"github.com/streamingfast/logging"
	"go.uber.org/zap"
	"os"
)

func RegisterReaderNodeFirehoseApp[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) {
	appLogger, appTracer := logging.PackageLogger("reader-node-firehose", chain.LoggerPackageID("reader-node-firehose"))

	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          "reader-node-firehose",
		Title:       "Reader Node (Firehose)",
		Description: "Blocks reading node, unmanaged, reads Firehose logs from standard input and transform them into Firehose chain specific blocks",
		RegisterFlags: func(cmd *cobra.Command) error {
			cmd.Flags().String("reader-node-firehose-endpoint", "", "Firehose endpoint to connect to.")
			cmd.Flags().String("reader-node-firehose-state", "{data-dir}/reader/state", "State file to store the cursor from the Firehose connection in.")
			cmd.Flags().String("reader-node-firehose-compression", "zstd", "Firehose compression, one of 'gzip', 'zstd' or 'none'.")
			cmd.Flags().Bool("reader-node-firehose-insecure", false, "Skip TLS validation when connecting to a Firehose endpoint.")
			cmd.Flags().Bool("reader-node-firehose-plaintext", false, "Connect to a Firehose endpoint using a non-encrypted, plaintext connection.")
			cmd.Flags().String("reader-node-firehose-api-key-env-var", "FIREHOSE_API_KEY", "Look for an API key directly in this environment variable to authenticate against endpoint (alternative to api-token-env-var)")
			cmd.Flags().String("reader-node-firehose-api-token-env-var", "FIREHOSE_API_TOKEN", "Look for a JWT in this environment variable to authenticate against endpoint (alternative to api-key-env-var)")

			return nil
		},
		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			sfDataDir := runtime.AbsDataDir
			archiveStoreURL := firecore.MustReplaceDataDir(sfDataDir, viper.GetString("common-one-block-store-url"))

			metricID := "reader-node-firehose"
			headBlockTimeDrift := metrics.NewHeadBlockTimeDrift(metricID)
			headBlockNumber := metrics.NewHeadBlockNumber(metricID)
			appReadiness := metrics.NewAppReadiness(metricID)
			metricsAndReadinessManager := nodeManager.NewMetricsAndReadinessManager(headBlockTimeDrift, headBlockNumber, appReadiness, viper.GetDuration("reader-node-readiness-max-latency"))
			return firehose_reader.New(&firehose_reader.Config{
				GRPCAddr:                   viper.GetString("reader-node-grpc-listen-addr"),
				OneBlocksStoreURL:          archiveStoreURL,
				MindReadBlocksChanCapacity: viper.GetInt("reader-node-blocks-chan-capacity"),
				StartBlockNum:              viper.GetUint64("reader-node-start-block-num"),
				StopBlockNum:               viper.GetUint64("reader-node-stop-block-num"),
				WorkingDir:                 firecore.MustReplaceDataDir(sfDataDir, viper.GetString("reader-node-working-dir")),
				OneBlockSuffix:             viper.GetString("reader-node-one-block-suffix"),

				FirehoseConfig: firehose_reader.FirehoseConfig{
					Endpoint:      viper.GetString("reader-node-firehose-endpoint"),
					StateFile:     firecore.MustReplaceDataDir(sfDataDir, viper.GetString("reader-node-firehose-state")),
					InsecureConn:  viper.GetBool("reader-node-firehose-insecure"),
					PlaintextConn: viper.GetBool("reader-node-firehose-plaintext"),
					Compression:   viper.GetString("reader-node-firehose-compression"),
					ApiKey:        os.Getenv(viper.GetString("reader-node-firehose-api-token-env-var")),
					Jwt:           os.Getenv(viper.GetString("reader-node-firehose-api-key-env-var")),
				},
			}, &firehose_reader.Modules{
				MetricsAndReadinessManager: metricsAndReadinessManager,
			}, appLogger, appTracer), nil
		},
	})
}
