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
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/launcher"
	coremetrics "github.com/streamingfast/firehose-core/metrics"
	nodeManager "github.com/streamingfast/firehose-core/node-manager"
	nodeReaderStdinApp "github.com/streamingfast/firehose-core/node-manager/app/node_reader_stdin"
	"github.com/streamingfast/firehose-core/node-manager/metrics"
	"github.com/streamingfast/firehose-core/node-manager/mindreader"
	"github.com/streamingfast/logging"
	"go.uber.org/zap"
)

func RegisterReaderNodeStdinApp[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) {
	registerUnmanagedReaderNodeApp(chain, rootLog,
		"reader-node-stdin",
		"Reader Node (stdin)",
		"Blocks reading node, unmanaged, reads Firehose logs from standard input and transform them into Firehose chain specific blocks",
		false,
	)
}

// RegisterReaderNodeCustomApp registers 'reader-node-custom', the same as 'reader-node-stdin'
// but without reading standard input: the chain's console reader produces the blocks itself,
// for example by reading files a node writes, and is never given lines.
func RegisterReaderNodeCustomApp[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) {
	registerUnmanagedReaderNodeApp(chain, rootLog,
		"reader-node-custom",
		"Reader Node (custom)",
		"Blocks reading node, unmanaged, gets its blocks from the chain's own console reader instead of standard input or a node process",
		true,
	)
}

func registerUnmanagedReaderNodeApp[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger, appID, title, description string, noStdin bool) {
	appLogger, appTracer := logging.PackageLogger(appID, chain.LoggerPackageID(appID))

	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          appID,
		Title:       title,
		Description: description,
		RegisterFlags: func(cmd *cobra.Command) error {
			// Test mode flags are registered at the global level in reader_node.go
			return nil
		},
		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			sfDataDir := runtime.AbsDataDir

			// Initialize test mode comparator if enabled
			testModeComparator, err := createTestModeComparator(chain, appLogger)
			if err != nil {
				return nil, err
			}

			lineBufferSize, err := readerNodeLineBufferSize()
			if err != nil {
				return nil, err
			}

			_, oneBlocksStoreURL, _, err := firecore.GetCommonStoresURLs(sfDataDir)
			if err != nil {
				return nil, err
			}

			consoleReaderFactory := func(lines chan string) (mindreader.ConsolerReader, error) {
				return chain.ConsoleReaderFactory(lines, chain.BlockEncoder, appLogger, appTracer)
			}

			metricID := appID
			headBlockTimeDrift := metrics.NewHeadBlockTimeDrift(metricID)
			headBlockNumber := metrics.NewHeadBlockNumber(metricID)
			headBlockRelativeTime := metrics.NewHeadBlockRelativeTime(metricID)
			finalizedBlockNumber := coremetrics.NewFinalizedBlockNumber(metricID)
			appReadiness := metrics.NewAppReadiness(metricID)
			metricsAndReadinessManager := nodeManager.NewMetricsAndReadinessManager(headBlockTimeDrift, headBlockNumber, headBlockRelativeTime, appReadiness, viper.GetDuration("reader-node-readiness-max-latency"), nodeManager.WithFinalizedBlockNumberMetric(finalizedBlockNumber))

			return nodeReaderStdinApp.New(&nodeReaderStdinApp.Config{
				GRPCAddr:                   viper.GetString("reader-node-grpc-listen-addr"),
				OneBlocksStoreURL:          oneBlocksStoreURL,
				MindReadBlocksChanCapacity: viper.GetInt("reader-node-blocks-chan-capacity"),
				StartBlockNum:              viper.GetUint64("reader-node-start-block-num"),
				StopBlockNum:               viper.GetUint64("reader-node-stop-block-num"),
				WorkingDir:                 firecore.MustReplaceDataDir(sfDataDir, viper.GetString("reader-node-working-dir")),
				OneBlockSuffix:             viper.GetString("reader-node-one-block-suffix"),
				LineBufferSizeInBytes:      int64(lineBufferSize),
				GRPCSecretKey:              os.Expand(viper.GetString("reader-node-grpc-secret-key"), os.Getenv),
				NoStdin:                    noStdin,
			}, &nodeReaderStdinApp.Modules{
				ConsoleReaderFactory:       consoleReaderFactory,
				MetricsAndReadinessManager: metricsAndReadinessManager,
			}, testModeComparator, appLogger, appTracer), nil
		},
	})
}
