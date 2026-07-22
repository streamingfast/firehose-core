package apps

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/kballard/go-shellquote"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/bstream/blockstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/cli"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/launcher"
	coremetrics "github.com/streamingfast/firehose-core/metrics"
	nodeManager "github.com/streamingfast/firehose-core/node-manager"
	nodeManagerApp "github.com/streamingfast/firehose-core/node-manager/app/node_manager"
	"github.com/streamingfast/firehose-core/node-manager/metrics"
	reader "github.com/streamingfast/firehose-core/node-manager/mindreader"
	"github.com/streamingfast/firehose-core/node-manager/operator"
	fcproto "github.com/streamingfast/firehose-core/proto"
	sv "github.com/streamingfast/firehose-core/superviser"
	"github.com/streamingfast/logging"
	pbheadinfo "github.com/streamingfast/pbgo/sf/headinfo/v1"
	"github.com/streamingfast/snapshotter"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func RegisterReaderNodeApp[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) {
	appLogger, appTracer := logging.PackageLogger("reader-node", "reader-node")

	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          "reader-node",
		Title:       fmt.Sprintf("%s Reader Node", chain.LongName),
		Description: fmt.Sprintf("%s node with built-in operational manager", chain.LongName),
		RegisterFlags: func(cmd *cobra.Command) error {
			flags := cmd.Flags()

			flags.String("reader-node-path", chain.ExecutableName, cli.FlagDescription(`
				Process that will be invoked to sync the chain, can be a full path or just the binary's name, in which case the binary is
				searched for paths listed by the PATH environment variable (following operating system rules around PATH handling).
			`))
			flags.String("reader-node-data-dir", "{data-dir}/reader/data", "Directory for node data")
			flags.Bool("reader-node-debug-firehose-logs", false, "[DEV] Prints firehose instrumentation logs to standard output, should be use for debugging purposes only")
			flags.String("reader-node-manager-api-addr", firecore.ReaderNodeManagerAPIAddr, "Acme node manager API address")
			flags.Duration("reader-node-readiness-max-latency", 30*time.Second, "Determine the maximum head block latency at which the instance will be determined healthy. Some chains have more regular block production than others.")
			flags.String("reader-node-arguments", "", string(cli.Description(`
				Defines the node arguments that will be passed to the node on execution. Supports templating, where we will replace certain sub-string with the appropriate value

				  {data-dir}		 	   The current data-dir path defined by the flag 'data-dir'
				  {node-data-dir}		   The node data dir path defined by the flag 'reader-node-data-dir'
				  {hostname}			   The machine's hostname
				  {first-streamable-block} The first streamable block number defined by the flag 'common-first-streamable-block'
				  {start-block-num}		   The resolved start block number defined by the flag 'reader-node-start-block-num' (can be overwritten)
				  {stop-block-num}		   The stop block number defined by the flag 'reader-node-stop-block-num'

				Furthermore, environment variables expansion is also performed on the arguments passed to the node, so that every occurence
				of ${ENV_VAR} will be replaced with the value of the environment variable ENV_VAR if defined, or empty string if not.

				Example: 'run blockchain -start {start-block-num} -end {stop-block-num}' may yield 'run blockchain -start 200 -end 500'
			`)))
			flags.StringSlice("reader-node-backups", []string{}, "Repeatable, space-separated key=values definitions for backups. Example: 'type=gke-pvc-snapshot prefix= tag=v1 freq-blocks=1000 freq-time= project=myproj'")
			flags.String("reader-node-grpc-listen-addr", firecore.ReaderNodeGRPCAddr, "The gRPC listening address to use for serving real-time blocks")
			flags.Bool("reader-node-discard-after-stop-num", false, "Ignore remaining blocks being processed after stop num (only useful if we discard the reader data after reprocessing a chunk of blocks)")
			flags.String("reader-node-working-dir", "{data-dir}/reader/work", "Path where reader will stores its files")
			flags.Uint("reader-node-start-block-num", 0, "Blocks that were produced with smaller block number then the given block num are skipped")
			flags.Uint("reader-node-stop-block-num", 0, "Shutdown reader when we the following 'stop-block-num' has been reached, inclusively.")
			flags.Int("reader-node-blocks-chan-capacity", 100, "Capacity of the channel holding blocks read by the reader. Process will shutdown reader-node if the channel gets over 90% of that capacity to prevent horrible consequences. Raise this number when processing tiny blocks very quickly")
			flags.Uint64("reader-node-line-buffer-size", 209715200, "Capacity of the buffer for reading a single line out of the node, in bytes (This is a hard limit. Some future enormouse blocks may require raising this to process them).")
			flags.String("reader-node-one-block-suffix", "default", cli.FlagDescription(`
				Unique identifier for reader, so that it can produce 'oneblock files' in the same store as another instance without competing
				for writes. You should set this flag if you have multiple reader running, each one should get a unique identifier, the
				hostname value is a good value to use.
			`))
			flags.Bool("reader-node-test-mode", false, cli.FlagDescription(`
				[DEV] Reader node will run in test mode which disables writing of blocks to directory, instead, they are compared as received
				against another test instance defined by flag 'reader-node-test-mode-diff-against'
			`))
			flags.String("reader-node-test-mode-diff-against", "", cli.FlagDescription(`
				[DEV] When in test mode, the reader node will compare blocks it reads against the blocks read by the instance defined by
				this flag, should be an address like http://localhost:9090, http:// for plain text, https:// for TLS, with 'insecure=true' as
				query param if the TLS certificate is self-signed, 'apiKey=<key>' if the server requires API key authentication, environment
				variable FIREHOSE_API_KEY/FIREHOSE_API_TOKEN are also supported for defining the API key/token.
			`))
			flags.String("reader-node-test-mode-diff-output", "-", cli.FlagDescription(`
				[DEV] When in test mode, the reader will write diff output to the given file path, if empty, diffs are written to
				the standard output.
			`))
			flags.String("reader-node-grpc-secret-key", "", cli.FlagDescription(`
				Secret key that the blockstream gRPC server will require from every incoming connection.
				Connections must present the key as a Bearer token in the "authorization" metadata header
				(i.e. "authorization: Bearer <key>" or "authorization: <key>). When empty, no authentication is performed.

				Supports environment variable interpolation using the syntax ${ENV_VAR_NAME}, e.g. "${READER_SECRET_KEY}".

				Example: "mysecretkey" or "${READER_NODE_SECRET}"
			`))
			return nil
		},
		InitFunc: func(runtime *launcher.Runtime) error {
			return nil
		},
		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			sfDataDir := runtime.AbsDataDir

			// Initialize test mode comparator if enabled
			testModeComparator, err := createTestModeComparator(chain, appLogger)
			if err != nil {
				return nil, err
			}

			nodePath := viper.GetString("reader-node-path")
			if nodePath == "" {
				return nil, fmt.Errorf("the configuration value 'reader-node-path' cannot be empty, it must points to a Firehose aware blockchain node client or a Firehose aware poller, don't forget to set the 'reader-node-arguments' flag to pass the appropriate arguments to the node")
			}

			nodeDataDir := firecore.MustReplaceDataDir(sfDataDir, viper.GetString("reader-node-data-dir"))

			readinessMaxLatency := viper.GetDuration("reader-node-readiness-max-latency")
			debugFirehose := viper.GetBool("reader-node-debug-firehose-logs")
			shutdownDelay := viper.GetDuration("common-system-shutdown-signal-delay") // we reuse this global value
			httpAddr := viper.GetString("reader-node-manager-api-addr")
			backupConfigs := viper.GetStringSlice("reader-node-backups")

			backupModules, backupSchedules, err := operator.ParseBackupConfigs(appLogger, backupConfigs, map[string]operator.BackupModuleFactory{
				"gke-pvc-snapshot": gkeSnapshotterFactory,
			})
			if err != nil {
				return nil, fmt.Errorf("parse backup configs: %w", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()

			userDefined := viper.IsSet("reader-node-start-block-num")
			startBlockNum := viper.GetUint64("reader-node-start-block-num")
			firstStreamableBlock := viper.GetUint64("common-first-streamable-block")

			resolveStartBlockNum := startBlockNum
			if !userDefined {
				resolveStartBlockNum, err = firecore.UnsafeResolveReaderNodeStartBlock(ctx, startBlockNum, firstStreamableBlock, runtime, rootLog)
				if err != nil {
					return nil, fmt.Errorf("resolve start block: %w", err)
				}
			}

			stopBlockNum := viper.GetUint64("reader-node-stop-block-num")

			hostname, _ := os.Hostname()
			nodeArgumentResolver := createNodeArgumentsResolver(sfDataDir, nodeDataDir, hostname, firstStreamableBlock, resolveStartBlockNum, stopBlockNum)

			nodeArguments, err := buildNodeArguments(viper.GetString("reader-node-arguments"), nodeArgumentResolver)
			if err != nil {
				return nil, fmt.Errorf("cannot split 'reader-node-arguments' value: %w", err)
			}

			headBlockTimeDrift := metrics.NewHeadBlockTimeDrift("reader-node")
			headBlockNumber := metrics.NewHeadBlockNumber("reader-node")
			headBlockRelativeTime := metrics.NewHeadBlockRelativeTime("reader-node")
			finalizedBlockNumber := coremetrics.NewFinalizedBlockNumber("reader-node")
			appReadiness := metrics.NewAppReadiness("reader-node")

			metricsAndReadinessManager := nodeManager.NewMetricsAndReadinessManager(
				headBlockTimeDrift,
				headBlockNumber,
				headBlockRelativeTime,
				appReadiness,
				readinessMaxLatency,
				nodeManager.WithFinalizedBlockNumberMetric(finalizedBlockNumber),
			)

			lineBufferSize := viper.GetUint64("reader-node-line-buffer-size")
			metrics.LineBufferSize.SetUint64(lineBufferSize)

			superviser := sv.SupervisorFactory(chain.ExecutableName, nodePath, nodeArguments, lineBufferSize, appLogger)
			superviser.RegisterLogPlugin(sv.NewNodeLogPlugin(debugFirehose))

			var bootstrapper operator.Bootstrapper
			if chain.ReaderNodeBootstrapperFactory != nil {
				bootstrapper, err = chain.ReaderNodeBootstrapperFactory(StartCmd.Context(), appLogger, StartCmd, nodeArguments, nodeArgumentResolver)
				if err != nil {
					return nil, fmt.Errorf("new bootstrapper: %w", err)
				}
			}

			chainOperator, err := operator.New(
				appLogger,
				superviser,
				metricsAndReadinessManager,
				&operator.Options{
					ShutdownDelay:              shutdownDelay,
					EnableSupervisorMonitoring: true,
					Bootstrapper:               bootstrapper,
				})
			if err != nil {
				return nil, fmt.Errorf("unable to create chain operator: %w", err)
			}

			for name, mod := range backupModules {
				appLogger.Info("registering backup module", zap.String("name", name), zap.Any("module", mod))
				err := chainOperator.RegisterBackupModule(name, mod)
				if err != nil {
					return nil, fmt.Errorf("unable to register backup module %s: %w", name, err)
				}

				appLogger.Info("backup module registered", zap.String("name", name), zap.Any("module", mod))
			}

			for _, sched := range backupSchedules {
				chainOperator.RegisterBackupSchedule(sched)
			}

			_, oneBlocksStoreURL, _, err := firecore.GetCommonStoresURLs(sfDataDir)
			if err != nil {
				return nil, err
			}

			grpcSecretKey := os.Expand(viper.GetString("reader-node-grpc-secret-key"), os.Getenv)

			blockStreamServer := blockstream.NewUnmanagedServer(blockstream.ServerOptionWithLogger(appLogger))
			workingDir := firecore.MustReplaceDataDir(sfDataDir, viper.GetString("reader-node-working-dir"))
			gprcListenAddr := viper.GetString("reader-node-grpc-listen-addr")
			oneBlockFileSuffix := viper.GetString("reader-node-one-block-suffix")
			blocksChanCapacity := viper.GetInt("reader-node-blocks-chan-capacity")

			readerPlugin, err := reader.NewMindReaderPlugin(
				oneBlocksStoreURL,
				workingDir,
				func(lines chan string) (reader.ConsolerReader, error) {
					return chain.ConsoleReaderFactory(lines, chain.BlockEncoder, appLogger, appTracer)
				},
				resolveStartBlockNum,
				stopBlockNum,
				blocksChanCapacity,
				metricsAndReadinessManager.UpdateHeadBlock,
				func(error) {
					chainOperator.Shutdown(nil)
				},
				oneBlockFileSuffix,
				blockStreamServer,
				testModeComparator,
				appLogger,
				appTracer,
			)
			if err != nil {
				return nil, fmt.Errorf("new reader plugin: %w", err)
			}

			superviser.RegisterLogPlugin(readerPlugin)

			return nodeManagerApp.New(&nodeManagerApp.Config{
				HTTPAddr:      httpAddr,
				GRPCAddr:      gprcListenAddr,
				GRPCSecretKey: grpcSecretKey,
			}, &nodeManagerApp.Modules{
				Operator:                   chainOperator,
				MindreaderPlugin:           readerPlugin,
				MetricsAndReadinessManager: metricsAndReadinessManager,
				RegisterGRPCService: func(server grpc.ServiceRegistrar) error {
					pbheadinfo.RegisterHeadInfoServer(server, blockStreamServer)
					pbbstream.RegisterBlockStreamServer(server, blockStreamServer)

					return nil
				},
			}, testModeComparator, appLogger), nil
		},
	})
}

var variablesRegex = regexp.MustCompile(`\{(data-dir|node-data-dir|hostname|first-streamable-block|start-block-num|stop-block-num)\}`)

// overridable for testing purposes
var osEnvExpandGetter = os.Getenv

// buildNodeArguments will resolve and split the given string into arguments, replacing the variables with the appropriate values.
//
// We are using a function for testing purposes, so that we can test arguments resolving and splitting correctly.
func buildNodeArguments(in string, resolver firecore.ReaderNodeArgumentResolver) ([]string, error) {
	// Split arguments according to standard shell rules
	nodeArguments, err := shellquote.Split(resolver(in))
	if err != nil {
		return nil, fmt.Errorf("cannot split 'reader-node-arguments' value: %w", err)
	}

	// Replace environment variables, we do it per argument so that if the env var contains space,
	// it will not be extra split into multiple arguments, which would have been the case if we did it
	// before splitting the arguments using shell rules.
	for i, arg := range nodeArguments {
		nodeArguments[i] = os.Expand(arg, osEnvExpandGetter)
	}

	return nodeArguments, nil
}

func createNodeArgumentsResolver(dataDir, nodeDataDir, hostname string, firstStreamableBlock, startBlockNum, stopBlockNum uint64) firecore.ReaderNodeArgumentResolver {
	return func(in string) string {
		return variablesRegex.ReplaceAllStringFunc(in, func(match string) string {
			switch match {
			case "{data-dir}":
				return dataDir
			case "{node-data-dir}":
				return nodeDataDir
			case "{hostname}":
				return hostname
			case "{first-streamable-block}":
				return fmt.Sprintf("%d", firstStreamableBlock)
			case "{start-block-num}":
				return fmt.Sprintf("%d", startBlockNum)
			case "{stop-block-num}":
				return fmt.Sprintf("%d", stopBlockNum)
			default:
				return fmt.Sprintf("<!%%Unknown(%s)%%!>", match)
			}
		})
	}
}

func gkeSnapshotterFactory(conf operator.BackupModuleConfig) (operator.BackupModule, error) {
	return snapshotter.NewGKEPVCSnapshotter(conf)
}

// createTestModeComparator creates a test mode comparator if test mode is enabled.
// Returns nil if test mode is not enabled.
func createTestModeComparator[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) (*reader.TestModeComparator, error) {
	testModeEnabled := viper.GetBool("reader-node-test-mode")
	if !testModeEnabled {
		return nil, nil
	}

	testModeConfig := &reader.TestModeConfig{
		Enabled:    testModeEnabled,
		AgainstDSN: viper.GetString("reader-node-test-mode-diff-against"),
		DiffOutput: viper.GetString("reader-node-test-mode-diff-output"),
	}

	logger.Warn("========================================")
	logger.Warn("🧪 TEST MODE ENABLED 🧪")
	logger.Warn("Blocks will NOT be written to disk")
	logger.Warn("Blocks will NOT be relayed through live source")
	logger.Warn("Blocks will be compared against production endpoint")
	logger.Warn("========================================")

	chainBlock := chain.BlockFactory()
	sanitizer := chain.Tools.GetSanitizeBlockForCompare()

	registry, err := fcproto.NewRegistry(chainBlock.ProtoReflect().Descriptor().ParentFile())
	if err != nil {
		return nil, fmt.Errorf("creating proto registry: %w", err)
	}

	testModeComparator, err := reader.NewTestModeComparator(testModeConfig, registry, sanitizer, logger)
	if err != nil {
		return nil, fmt.Errorf("create test mode comparator: %w", err)
	}

	return testModeComparator, nil
}
