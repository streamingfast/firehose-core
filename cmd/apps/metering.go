package apps

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/dmetering"
	"go.uber.org/zap"
)

// GetCommonMeteringPlugin returns the common metering plugin value to use
// for the application. It reads the `common-metering-plugin` flag
// from the command and returns the plugin after expanding the
// environment variables in it meaning 'paymentGateway://test?token=${TOKEN}'.
func GetCommonMeteringPluginValue() string {
	plugin := viper.GetString("common-metering-plugin")
	return os.ExpandEnv(plugin)
}

// GetCommonMeteringPlugin returns the common metering plugin to use
// for the application. It reads the `common-metering-plugin` flag
// from the command and returns the plugin after expanding the
// environment variables in it meaning 'paymentGateway://test?token=${TOKEN}'.
func GetCommonMeteringPlugin(cmd *cobra.Command, logger *zap.Logger) (dmetering.EventEmitter, error) {
	// We keep cmd as argument for future proofing, at which point we are going to break
	// GetCommonMeteringPluginValue above.
	_ = cmd

	eventEmitter, err := dmetering.New(GetCommonMeteringPluginValue(), logger)
	if err != nil {
		return nil, fmt.Errorf("new metering plugin: %w", err)
	}

	return eventEmitter, nil
}
