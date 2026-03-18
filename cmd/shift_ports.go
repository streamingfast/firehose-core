package cmd

import (
	"fmt"

	"github.com/spf13/viper"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/cmd/apps"
	"go.uber.org/zap"
)

// portFlags is the explicit list of flag names on the 'start' command whose default
// values contain a port that should be shifted when --shift-ports is used. This
// includes both server-side listen addresses and client-side connection addresses so
// that internal wiring stays consistent.
var portFlags = []string{
	// Server-side listen addresses
	"firehose-grpc-listen-addr",
	"merger-grpc-listen-addr",
	"relayer-grpc-listen-addr",
	"reader-node-grpc-listen-addr",
	"reader-node-manager-api-addr",
	"substreams-tier1-grpc-listen-addr",
	"substreams-tier2-grpc-listen-addr",
	"index-builder-grpc-listen-addr",

	// Client-side connection addresses (defaults point to services above)
	"common-live-blocks-addr",
	"substreams-tier1-subrequests-endpoint",
}

// portSliceFlags is the explicit list of flag names that are StringSlice flags
// containing port addresses that should be shifted.
var portSliceFlags = []string{
	"relayer-source",
}

// globalPortFlags is the explicit list of global/persistent flag names (on rootCmd)
// whose default values contain a port that should be shifted. These are the
// infrastructure port flags for metrics, profiling, and log-level switching.
//
// These flags are stored in viper with a "global-" prefix (e.g. "global-metrics-listen-addr").
var globalPortFlags = []string{
	"metrics-listen-addr",
	"pprof-listen-addr",
	"log-level-switcher-listen-addr",
}

// applyShiftPorts reads the --shift-ports offset and, when non-zero, shifts the
// port number in every known port flag by the given offset. Flags that were
// explicitly set by the user on the command line are not modified.
//
// This handles both start command flags (looked up on apps.StartCmd) and global
// persistent flags (looked up on rootCmd, stored with "global-" prefix in viper).
func applyShiftPorts(logger *zap.Logger) error {
	offset := viper.GetInt("global-shift-ports")
	if offset == 0 {
		return nil
	}

	logger.Info("shifting ports by offset", zap.Int("offset", offset))

	// Shift start command flags
	for _, flagName := range portFlags {
		flag := apps.StartCmd.Flags().Lookup(flagName)
		if flag == nil {
			// Flag may not be registered (e.g. index-builder-grpc-listen-addr when no indexer)
			continue
		}

		if flag.Changed {
			logger.Debug("skipping shifted port (explicitly set by user)", zap.String("flag", flagName))
			continue
		}

		current := viper.GetString(flagName)
		if current == "" {
			continue
		}

		shifted, err := firecore.ShiftAddressPort(current, offset)
		if err != nil {
			return fmt.Errorf("shifting port for flag %q (value %q): %w", flagName, current, err)
		}

		logger.Debug("shifted port", zap.String("flag", flagName), zap.String("from", current), zap.String("to", shifted))
		viper.Set(flagName, shifted)
	}

	// Shift start command slice flags
	for _, flagName := range portSliceFlags {
		flag := apps.StartCmd.Flags().Lookup(flagName)
		if flag == nil {
			continue
		}

		if flag.Changed {
			logger.Debug("skipping shifted port slice (explicitly set by user)", zap.String("flag", flagName))
			continue
		}

		current := viper.GetStringSlice(flagName)
		if len(current) == 0 {
			continue
		}

		shifted := make([]string, 0, len(current))
		for _, addr := range current {
			s, err := firecore.ShiftAddressPort(addr, offset)
			if err != nil {
				return fmt.Errorf("shifting port for flag %q (value %q): %w", flagName, addr, err)
			}
			shifted = append(shifted, s)
		}

		logger.Debug("shifted ports (slice)", zap.String("flag", flagName), zap.Strings("from", current), zap.Strings("to", shifted))
		viper.Set(flagName, shifted)
	}

	// Shift global persistent flags (metrics, pprof, log-level-switcher)
	for _, flagName := range globalPortFlags {
		flag := rootCmd.PersistentFlags().Lookup(flagName)
		if flag == nil {
			continue
		}

		if flag.Changed {
			logger.Debug("skipping shifted global port (explicitly set by user)", zap.String("flag", flagName))
			continue
		}

		viperKey := "global-" + flagName
		current := viper.GetString(viperKey)
		if current == "" {
			continue
		}

		shifted, err := firecore.ShiftAddressPort(current, offset)
		if err != nil {
			return fmt.Errorf("shifting port for global flag %q (value %q): %w", flagName, current, err)
		}

		logger.Debug("shifted global port", zap.String("flag", flagName), zap.String("viper_key", viperKey), zap.String("from", current), zap.String("to", shifted))
		viper.Set(viperKey, shifted)
	}

	return nil
}
