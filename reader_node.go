package firecore

import (
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/kballard/go-shellquote"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/bstream/blockstream"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/dlauncher/launcher"
	nm "github.com/streamingfast/firehose-core/nodemanager"
	"github.com/streamingfast/logging"
	nodeManager "github.com/streamingfast/node-manager"
	nodeManagerApp "github.com/streamingfast/node-manager/app/node_manager"
	"github.com/streamingfast/node-manager/metrics"
	reader "github.com/streamingfast/node-manager/mindreader"
	"github.com/streamingfast/node-manager/operator"
	pbbstream "github.com/streamingfast/pbgo/sf/bstream/v1"
	pbheadinfo "github.com/streamingfast/pbgo/sf/headinfo/v1"
	"github.com/streamingfast/snapshotter"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

func registerReaderNodeApp[B Block](chain *Chain[B]) {
	appLogger, appTracer := logging.PackageLogger("reader", chain.LoggerPackageID("reader"))

	// FIXME: We use `chain.ShortName` here, but we are actually want the final binary name, I think we should have the executable name.
	// However a problem here to get this value is that we are "before" the actual flags registration that happens a bit lower via
	// `launcher.RegisterApp`, so we cannot get the value yet because the flag is not defined. Maybe solveable with our refactoring
	// of `node-manager` that is planned.
	supervisedProcessLogger, _ := logging.PackageLogger(fmt.Sprintf("reader.%s", chain.ShortName), chain.LoggerPackageID(fmt.Sprintf("reader/%s", chain.ShortName)), logging.LoggerDefaultLevel(zap.InfoLevel))

	launcher.RegisterApp(rootLog, &launcher.AppDef{
		ID:          "reader-node",
		Title:       fmt.Sprintf("%s Reader Node", chain.LongName),
		Description: fmt.Sprintf("%s node with built-in operational manager", chain.LongName),
		RegisterFlags: func(cmd *cobra.Command) error {
			cmd.Flags().String("reader-node-path", chain.ExecutableName, cli.FlagDescription(`
				Process that will be invoked to sync the chain, can be a full path or just the binary's name, in which case the binary is
				searched for paths listed by the PATH environment variable (following operating system rules around PATH handling).
			`))
			cmd.Flags().String("reader-node-data-dir", "{data-dir}/reader/data", "Directory for node data")
			cmd.Flags().Bool("reader-node-debug-firehose-logs", false, "[DEV] Prints firehose instrumentation logs to standard output, should be use for debugging purposes only")
			cmd.Flags().Bool("reader-node-log-to-zap", true, cli.FlagDescription(`
				When sets to 'true', all standard error output emitted by the invoked process defined via 'reader-node-path'
				is intercepted, split line by line and each line is then transformed and logged through the Firehose stack
				logging system. The transformation extracts the level and remove the timestamps creating a 'sanitized' version
				of the logs emitted by the blockchain's managed client process. If this is not desirable, disabled the flag
				and all the invoked process standard error will be redirect to 'fireacme' standard's output.
			`))
			cmd.Flags().String("reader-node-manager-api-addr", ReaderNodeManagerAPIAddr, "Acme node manager API address")
			cmd.Flags().Duration("reader-node-readiness-max-latency", 30*time.Second, "Determine the maximum head block latency at which the instance will be determined healthy. Some chains have more regular block production than others.")
			cmd.Flags().String("reader-node-arguments", "", "If not empty, overrides the list of default node arguments (computed from node type and role). Start with '+' to append to default args instead of replacing. ")
			cmd.Flags().StringSlice("reader-node-backups", []string{}, "Repeatable, space-separated key=values definitions for backups. Example: 'type=gke-pvc-snapshot prefix= tag=v1 freq-blocks=1000 freq-time= project=myproj'")
			cmd.Flags().String("reader-node-grpc-listen-addr", ReaderNodeGRPCAddr, "The gRPC listening address to use for serving real-time blocks")
			cmd.Flags().Bool("reader-node-discard-after-stop-num", false, "Ignore remaining blocks being processed after stop num (only useful if we discard the reader data after reprocessing a chunk of blocks)")
			cmd.Flags().String("reader-node-working-dir", "{data-dir}/reader/work", "Path where reader will stores its files")
			cmd.Flags().Uint("reader-node-start-block-num", 0, "Blocks that were produced with smaller block number then the given block num are skipped")
			cmd.Flags().Uint("reader-node-stop-block-num", 0, "Shutdown reader when we the following 'stop-block-num' has been reached, inclusively.")
			cmd.Flags().Int("reader-node-blocks-chan-capacity", 100, "Capacity of the channel holding blocks read by the reader. Process will shutdown superviser/geth if the channel gets over 90% of that capacity to prevent horrible consequences. Raise this number when processing tiny blocks very quickly")
			cmd.Flags().String("reader-node-one-block-suffix", "default", cli.FlagDescription(`
				Unique identifier for reader, so that it can produce 'oneblock files' in the same store as another instance without competing
				for writes. You should set this flag if you have multiple reader running, each one should get a unique identifier, the
				hostname value is a good value to use.
			`))

			return nil
		},
		InitFunc: func(runtime *launcher.Runtime) error {
			return nil
		},
		FactoryFunc: func(runtime *launcher.Runtime) (launcher.App, error) {
			sfDataDir := runtime.AbsDataDir

			nodePath := viper.GetString("reader-node-path")
			nodeDataDir := MustReplaceDataDir(sfDataDir, viper.GetString("reader-node-data-dir"))

			readinessMaxLatency := viper.GetDuration("reader-node-readiness-max-latency")
			debugFirehose := viper.GetBool("reader-node-debug-firehose-logs")
			logToZap := viper.GetBool("reader-node-log-to-zap")
			shutdownDelay := viper.GetDuration("common-system-shutdown-signal-delay") // we reuse this global value
			httpAddr := viper.GetString("reader-node-manager-api-addr")
			backupConfigs := viper.GetStringSlice("reader-node-backups")

			backupModules, backupSchedules, err := operator.ParseBackupConfigs(appLogger, backupConfigs, map[string]operator.BackupModuleFactory{
				"gke-pvc-snapshot": gkeSnapshotterFactory,
			})

			if err != nil {
				return nil, fmt.Errorf("parse backup configs: %w", err)
			}

			hostname, _ := os.Hostname()
			nodeArguments, err := buildNodeArguments(sfDataDir, nodeDataDir, hostname, viper.GetString("reader-node-arguments"))
			if err != nil {
				return nil, fmt.Errorf("cannot build node bootstrap arguments: %w", err)
			}

			headBlockTimeDrift := metrics.NewHeadBlockTimeDrift("reader-node")
			headBlockNumber := metrics.NewHeadBlockNumber("reader-node")
			appReadiness := metrics.NewAppReadiness("reader-node")

			metricsAndReadinessManager := nodeManager.NewMetricsAndReadinessManager(
				headBlockTimeDrift,
				headBlockNumber,
				appReadiness,
				readinessMaxLatency,
			)

			superviser := nm.SupervisorFactory(chain.ExecutableName, nodePath, nodeArguments, appLogger)
			superviser.RegisterLogPlugin(nm.NewNodeLogPlugin(logToZap, debugFirehose, supervisedProcessLogger))

			var bootstrapper operator.Bootstrapper
			if chain.ReaderNodeBootstrapperFactory != nil {
				bootstrapper, err = chain.ReaderNodeBootstrapperFactory(startCmd, nodeDataDir)
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

			blockStreamServer := blockstream.NewUnmanagedServer(blockstream.ServerOptionWithLogger(appLogger))
			oneBlocksStoreURL := MustReplaceDataDir(sfDataDir, viper.GetString("common-one-block-store-url"))
			workingDir := MustReplaceDataDir(sfDataDir, viper.GetString("reader-node-working-dir"))
			gprcListenAddr := viper.GetString("reader-node-grpc-listen-addr")
			batchStartBlockNum := viper.GetUint64("reader-node-start-block-num")
			batchStopBlockNum := viper.GetUint64("reader-node-stop-block-num")
			oneBlockFileSuffix := viper.GetString("reader-node-one-block-suffix")
			blocksChanCapacity := viper.GetInt("reader-node-blocks-chan-capacity")

			readerPlugin, err := reader.NewMindReaderPlugin(
				oneBlocksStoreURL,
				workingDir,
				func(lines chan string) (reader.ConsolerReader, error) {
					return chain.ConsoleReaderFactory(lines, chain.BlockEncoder, appLogger, appTracer)
				},
				batchStartBlockNum,
				batchStopBlockNum,
				blocksChanCapacity,
				metricsAndReadinessManager.UpdateHeadBlock,
				func(error) {
					chainOperator.Shutdown(nil)
				},
				oneBlockFileSuffix,
				blockStreamServer,
				appLogger,
				appTracer,
			)
			if err != nil {
				return nil, fmt.Errorf("new reader plugin: %w", err)
			}

			superviser.RegisterLogPlugin(readerPlugin)

			return nodeManagerApp.New(&nodeManagerApp.Config{
				HTTPAddr: httpAddr,
				GRPCAddr: gprcListenAddr,
			}, &nodeManagerApp.Modules{
				Operator:                   chainOperator,
				MindreaderPlugin:           readerPlugin,
				MetricsAndReadinessManager: metricsAndReadinessManager,
				RegisterGRPCService: func(server grpc.ServiceRegistrar) error {
					pbheadinfo.RegisterHeadInfoServer(server, blockStreamServer)
					pbbstream.RegisterBlockStreamServer(server, blockStreamServer)

					return nil
				},
			}, appLogger), nil
		},
	})
}

var variablesRegex = regexp.MustCompile(`\{(data-dir|node-data-dir|hostname)\}`)

func buildNodeArguments(dataDir, nodeDataDir, hostname string, args string) ([]string, error) {
	out := variablesRegex.ReplaceAllStringFunc(args, func(match string) string {
		switch match {
		case "{data-dir}":
			return dataDir
		case "{node-data-dir}":
			return nodeDataDir
		case "{hostname}":
			return hostname
		default:
			return fmt.Sprintf("<!%%Unknown(%s)%%!>", match)
		}
	})

	// Split arguments according to standard shell rules
	return shellquote.Split(out)
}

func gkeSnapshotterFactory(conf operator.BackupModuleConfig) (operator.BackupModule, error) {
	return snapshotter.NewGKEPVCSnapshotter(conf)
}
