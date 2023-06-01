package firecore

import (
	"fmt"
	"os"
	"strings"
	"time"

	// Needs to be in this file which is the main entry of wrapper binary

	"github.com/streamingfast/cli"
	_ "github.com/streamingfast/dauth/authenticator/null"   // auth null plugin
	_ "github.com/streamingfast/dauth/authenticator/secret" // auth secret/hard-coded plugin
	_ "github.com/streamingfast/dauth/ratelimiter/null"     // ratelimiter plugin
	"github.com/streamingfast/logging"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/dlauncher/flags"
	"github.com/streamingfast/dlauncher/launcher"
	"go.uber.org/zap"
)

var allFlags = make(map[string]bool) // used as global because of async access to cobra init functions

var rootCmd = &cobra.Command{}
var rootLog *zap.Logger

// Main is the main entry point that configures everything and should be called from your Go
// 'main' entrypoint directly.
func Main(chain *Chain) {
	chain.Validate()
	chain.Init()

	binaryName := chain.BinaryName()
	rootLog, _ = logging.RootLogger(binaryName, chain.RootLoggerPackageID())

	cobra.OnInitialize(func() {
		allFlags = flags.AutoBind(rootCmd, binaryName)
	})

	rootCmd.Use = binaryName
	rootCmd.Short = fmt.Sprintf("Firehose on %s", chain.LongName)
	rootCmd.Version = chain.VersionString()

	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(toolsCmd)

	rootCmd.PersistentFlags().StringP("data-dir", "d", "./firehose-data", "Path to data storage for all components of the Firehose stack")
	rootCmd.PersistentFlags().StringP("config-file", "c", "./firehose.yaml", "Configuration file to use. No config file loaded if set to an empty string.")

	rootCmd.PersistentFlags().String("log-format", "text", "Format for logging to stdout. Either 'text' or 'stackdriver'")
	rootCmd.PersistentFlags().Bool("log-to-file", true, "Also write logs to {data-dir}/firehose.log.json ")
	rootCmd.PersistentFlags().String("log-level-switcher-listen-addr", "localhost:1065", cli.FlagDescription(`
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
	rootCmd.PersistentFlags().CountP("log-verbosity", "v", "Enables verbose output (-vvvv for max verbosity)")

	rootCmd.PersistentFlags().String("metrics-listen-addr", ":9102", "If non-empty, the process will listen on this address to server the Prometheus metrics collected by the components.")
	rootCmd.PersistentFlags().String("pprof-listen-addr", "localhost:6060", "If non-empty, the process will listen on this address for pprof analysis (see https://golang.org/pkg/net/http/pprof/)")
	rootCmd.PersistentFlags().Duration("startup-delay", 0, cli.FlagDescription(`
		Delay before launching the components defined in config file or via the command line arguments. This can be used to perform
		maintenance operations on a running container or pod prior it will actually start processing. Useful for example to clear
		a persistent disks of its content before starting, cleary cached content to try to resolve bugs, etc.
	`))

	registerCommonFlags(chain)
	registerReaderNodeApp(chain)
	registerReaderNodeStdinApp(chain)
	registerMergerApp()
	registerRelayerApp()
	registerFirehoseApp(chain)

	configureStartCmd(chain)
	configureToolsCheckCmd(chain)
	configureToolsPrintCmd(chain)

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
		if err := setupCmd(cmd); err != nil {
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
