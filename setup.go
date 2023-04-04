package firecore

import (
	"fmt"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/dlauncher/launcher"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

func setupCmd(cmd *cobra.Command) error {
	cmd.SilenceUsage = true

	cmds := extractCmd(cmd)
	subCommand := cmds[len(cmds)-1]

	forceConfigOn := []*cobra.Command{startCmd}
	logToFileOn := []*cobra.Command{startCmd}

	if configFile := viper.GetString("global-config-file"); configFile != "" {
		exists, err := fileExists(configFile)
		if err != nil {
			return fmt.Errorf("unable to check if config file exists: %w", err)
		}

		if !exists && isMatchingCommand(cmds, forceConfigOn) {
			return fmt.Errorf("config file %q not found. Did you 'fireacme init'?", configFile)
		}

		if exists {
			if err := launcher.LoadConfigFile(configFile); err != nil {
				return fmt.Errorf("unable to read config file %q: %w", configFile, err)
			}
		}
	}

	subconf := launcher.Config[subCommand]
	if subconf != nil {
		for k, v := range subconf.Flags {
			validFlag := false
			if _, ok := allFlags["global-"+k]; ok {
				viper.SetDefault("global-"+k, v)
				validFlag = true
			}
			if _, ok := allFlags[k]; ok {
				viper.SetDefault(k, v)
				validFlag = true
			}
			if !validFlag {
				return fmt.Errorf("invalid flag %s in config file under command %s", k, subCommand)
			}
		}
	}

	launcher.SetupLogger(rootLog, &launcher.LoggingOptions{
		WorkingDir: viper.GetString("global-data-dir"),
		// We add +1 so our default verbosity is to show all packages in INFO mode
		Verbosity:     viper.GetInt("global-log-verbosity") + 1,
		LogFormat:     viper.GetString("global-log-format"),
		LogToFile:     isMatchingCommand(cmds, logToFileOn) && viper.GetBool("global-log-to-file"),
		LogListenAddr: viper.GetString("global-log-level-switcher-listen-addr"),
		LogToStderr:   true,
	})
	launcher.SetupTracing("fireacme")
	launcher.SetupAnalyticsMetrics(rootLog, viper.GetString("global-metrics-listen-addr"), viper.GetString("global-pprof-listen-addr"))

	return nil
}

func isMatchingCommand(cmds []string, runSetupOn []*cobra.Command) bool {
	for _, c := range runSetupOn {
		baseChunks := extractCmd(c)
		if strings.Join(cmds, ".") == strings.Join(baseChunks, ".") {
			return true
		}
	}
	return false
}

func extractCmd(cmd *cobra.Command) []string {
	cmds := []string{}
	for {
		if cmd == nil {
			break
		}
		cmds = append(cmds, cmd.Use)
		cmd = cmd.Parent()
	}

	out := make([]string, len(cmds))

	for itr, v := range cmds {
		newIndex := len(cmds) - 1 - itr
		out[newIndex] = v
	}
	return out
}

func fileExists(file string) (bool, error) {
	stat, err := os.Stat(file)
	if os.IsNotExist(err) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	return !stat.IsDir(), nil
}

var isShuttingDown = atomic.NewBool(false)

// setupSignalHandler this is a graceful delay to allow residual traffic sent by the load balancer to be processed
// without returning 500. Once the delay has passed then the service can be shutdown
func setupSignalHandler(gracefulShutdownDelay time.Duration) <-chan os.Signal {
	outgoingSignals := make(chan os.Signal, 10)
	signals := make(chan os.Signal)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	seen := 0

	go func() {
		for {
			s := <-signals
			switch s {
			case syscall.SIGTERM, syscall.SIGINT:
				seen++

				if seen > 3 {
					rootLog.Info("received termination signal 3 times, forcing kill")
					rootLog.Sync()
					os.Exit(1)
				}

				if !isShuttingDown.Load() {
					rootLog.Info("received termination signal (Ctrl+C multiple times to force kill)", zap.Stringer("signal", s))
					isShuttingDown.Store(true)

					go time.AfterFunc(gracefulShutdownDelay, func() {
						outgoingSignals <- s
					})

					break
				}

				rootLog.Info("received termination signal twice, shutting down now", zap.Stringer("signal", s))
				outgoingSignals <- s
			}
		}
	}()

	return outgoingSignals
}
