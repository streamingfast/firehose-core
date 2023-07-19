package firecore

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	dauthgrpc "github.com/streamingfast/dauth/grpc"
	dauthnull "github.com/streamingfast/dauth/null"
	dauthtrust "github.com/streamingfast/dauth/trust"
	"github.com/streamingfast/dlauncher/launcher"
	"github.com/streamingfast/dmetering"
	"github.com/streamingfast/logging"
	"go.uber.org/zap"
)

var rootCmd = &cobra.Command{}
var rootLog *zap.Logger
var rootTracer logging.Tracer

// Main is the main entry point that configures everything and should be called from your Go
// 'main' entrypoint directly.
func Main[B Block](chain *Chain[B]) {
	dauthgrpc.Register()
	dauthnull.Register()
	dauthtrust.Register()
	dmetering.RegisterDefault()

	chain.Validate()
	chain.Init()

	binaryName := chain.BinaryName()
	rootLog, rootTracer = logging.RootLogger(binaryName, chain.RootLoggerPackageID())

	cobra.OnInitialize(func() {
		cli.ConfigureViperForCommand(rootCmd, strings.ToUpper(binaryName))

		// Compatibility to fetch `viper.GetXXX(....)` without `start-` prefix for flags on startCmd
		startCmd.LocalFlags().VisitAll(func(flag *pflag.Flag) {
			viper.BindPFlag(flag.Name, flag)
			viper.BindEnv(sflags.MustGetViperKeyFromFlag(flag), strings.ToUpper(binaryName+"_"+strings.ReplaceAll(flag.Name, "-", "_")))
		})
	})

	rootCmd.Use = binaryName
	rootCmd.Short = fmt.Sprintf("Firehose on %s", chain.LongName)
	rootCmd.Version = chain.VersionString()

	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(toolsCmd)

	(func(flags *pflag.FlagSet) {
		flags.StringP("data-dir", "d", "./firehose-data", "Path to data storage for all components of the Firehose stack")
		flags.StringP("config-file", "c", "./firehose.yaml", "Configuration file to use. No config file loaded if set to an empty string.")

		flags.String("log-format", "text", "Format for logging to stdout. Either 'text' or 'stackdriver'")
		flags.Bool("log-to-file", true, "Also write logs to {data-dir}/firehose.log.json ")
		flags.String("log-level-switcher-listen-addr", "localhost:1065", cli.FlagDescription(`
			If non-empty, a JSON based HTTP server will listen on this address to let you switch the default logging level
			of all registered loggers to a different one on the fly. This enables switching to debug level on
			a live running production instance. Use 'curl -XPUT -d '{"level":"debug","inputs":"*"} http://localhost:1065' to
			switch the level for all loggers. Each logger (even in transitive dependencies, at least those part of the core
			StreamingFast's Firehose) are registered using two identifiers, the overarching component usually all loggers in a
			library uses the same component name like 'bstream' or 'merger', and a fully qualified ID which is usually the Go
			package fully qualified name in which the logger is defined. The 'inputs' can be either one or many component's name
			like 'bstream|merger|firehose' or a regex that is matched against the fully qualified name. If there is a match for a
			given logger, it will change its level to the one specified in 'level' field. The valid levels are 'trace', 'debug',
			'info', 'warn', 'error', 'panic'. Can be used to silence loggers by using 'panic' (well, technically it's not a full
			silence but almost), or make them more verbose and change it back later.
		`))
		flags.CountP("log-verbosity", "v", "Enables verbose output (-vvvv for max verbosity)")

		flags.String("metrics-listen-addr", ":9102", "If non-empty, the process will listen on this address to server the Prometheus metrics collected by the components.")
		flags.String("pprof-listen-addr", "localhost:6060", "If non-empty, the process will listen on this address for pprof analysis (see https://golang.org/pkg/net/http/pprof/)")
		flags.Duration("startup-delay", 0, cli.FlagDescription(`
			Delay before launching the components defined in config file or via the command line arguments. This can be used to perform
			maintenance operations on a running container or pod prior it will actually start processing. Useful for example to clear
			a persistent disks of its content before starting, cleary cached content to try to resolve bugs, etc.
		`))
	})(rootCmd.PersistentFlags())

	registerCommonFlags(chain)
	registerReaderNodeApp(chain)
	registerReaderNodeStdinApp(chain)
	registerMergerApp()
	registerRelayerApp()
	registerFirehoseApp(chain)
	registerSubstreamsTier1App(chain)
	registerSubstreamsTier2App(chain)

	if len(chain.BlockIndexerFactories) > 0 {
		registerIndexBuilderApp(chain)
	}

	if chain.RegisterExtraStartFlags != nil {
		chain.RegisterExtraStartFlags(startCmd.Flags())
	}

	configureStartCmd(chain)

	if err := configureToolsCmd(chain); err != nil {
		exitWithError("registering tools command", err)
	}

	if err := launcher.RegisterFlags(rootLog, startCmd); err != nil {
		exitWithError("registering application flags", err)
	}

	var availableCmds []string
	for app := range launcher.AppRegistry {
		availableCmds = append(availableCmds, app)
	}

	startCmd.SetHelpTemplate(fmt.Sprintf(startCmdHelpTemplate, strings.Join(availableCmds, "\n  ")))
	startCmd.Example = fmt.Sprintf("%s start reader-node", binaryName)

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if err := setupCmd(cmd, chain.BinaryName()); err != nil {
			return err
		}

		startupDelay := viper.GetDuration("global-startup-delay")
		if startupDelay > 0 {
			rootLog.Info("sleeping before starting apps", zap.Duration("delay", startupDelay))
			time.Sleep(startupDelay)
		}

		return nil
	}

	if err := rootCmd.Execute(); err != nil {
		exitWithError("failed to run", err)
	}
}

func exitWithError(message string, err error) {
	rootLog.Error(message, zap.Error(err))
	rootLog.Sync()
	os.Exit(1)
}

var startCmdHelpTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}} [all|command1 [command2...]]{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
  {{.Example}}{{end}}

Available Commands:
  %s{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`
