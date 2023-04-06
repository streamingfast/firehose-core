package nodemanager

import (
	"regexp"

	logplugin "github.com/streamingfast/node-manager/log_plugin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	GetLogLevelFunc = getLogLevel
)

// This file configures a logging reader that transforms log lines received from the blockchain process running
// and then logs them inside the Firehose stack logging system.
//
// A default implementation uses a regex to identify the level of the line and turn it into our internal level value.
//
// You should override the `GetLogLevelFunc` above to determine the log level for your speficic chain
func NewNodeLogPlugin(readerNodeLogToZap bool, debugFirehose bool, nodelogger *zap.Logger) logplugin.LogPlugin {
	if readerNodeLogToZap {
		return logplugin.NewToZapLogPlugin(debugFirehose, nodelogger, logplugin.ToZapLogPluginLogLevel(GetLogLevelFunc), logplugin.ToZapLogPluginTransformer(stripTimeTransformer))
	}
	return logplugin.NewToConsoleLogPlugin(debugFirehose)
}

// This default implementation parses the log live from our dummy blockchain thate we
// instrumented in `firehose-acme`. The log lines look like:
//
//	time="2022-03-04T12:49:34-05:00" level=info msg="initializing node"
//
// So our regex look like the one below, extracting the `info` value from a group in the regexp.
var logLevelRegex = regexp.MustCompile("level=(debug|info|warn|warning|error)")

func getLogLevel(in string) zapcore.Level {
	// If the regex does not match the line, log to `INFO` so at least we see something by default.
	groups := logLevelRegex.FindStringSubmatch(in)
	if len(groups) <= 1 {
		return zap.InfoLevel
	}

	switch groups[1] {
	case "debug", "DEBUG":
		return zap.DebugLevel
	case "info", "INFO":
		return zap.InfoLevel
	case "warn", "warning", "WARN", "WARNING":
		return zap.WarnLevel
	case "error", "ERROR":
		return zap.ErrorLevel
	default:
		return zap.InfoLevel
	}
}

var timeRegex = regexp.MustCompile(`time="[0-9]{4}-[^"]+"\s*`)

func stripTimeTransformer(in string) string {
	return timeRegex.ReplaceAllString(in, "")
}
