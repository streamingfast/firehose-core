package substreams

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"github.com/streamingfast/firehose-core/cmd/tools/substreams/logs"
	"go.uber.org/zap"
)

// NewToolsLogsCmd returns the parent "logs" command for Substreams logs analysis tools
func NewToolsLogsCmd(logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Substreams logs analysis tools",
		Long: `Tools for analyzing Substreams logs from various backends.

Note: Currently only GCP Cloud Logging backend is supported.`,
	}

	cmd.AddCommand(NewToolsLogsConnectionCmd(logger))
	cmd.AddCommand(NewToolsLogsConnectionsCmd(logger))
	cmd.AddCommand(NewToolsLogsReexecCmd(logger))

	return cmd
}

const sinceFlagHelp = "Time range: relative duration ('30m', '2h', '1d', '1w', \"1 day ago\"), a single timestamp (past → search start to now), or a range '<start>/<end>' or '<start>:<end>'"

// NewToolsLogsConnectionsCmd returns the "connections" subcommand
func NewToolsLogsConnectionsCmd(logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connections <user_id>",
		Short: "Show Substreams connections for an organization",
		Long: `Queries log backend to find Substreams connections for a specific organization,
correlates incoming requests with their stats by trace ID, and presents a summary table.

Connections can be:
  - active: incoming request received, no stats yet (still running)
  - closed: both incoming request and stats present (completed normally)
  - error:  completed with an error

Note: Currently only GCP Cloud Logging backend is supported.`,
		Example: `  # Show connections for the last hour (default)
  firecore tools substreams logs connections sfinfra --gcp-project my-project

  # Show connections for the last 30 minutes, 2 days, or 1 week
  firecore tools substreams logs connections sfinfra --gcp-project my-project --since 30m
  firecore tools substreams logs connections sfinfra --gcp-project my-project --since 2d
  firecore tools substreams logs connections sfinfra --gcp-project my-project --since 1w

  # Show connections for a specific date range
  firecore tools substreams logs connections sfinfra --gcp-project my-project --since 2024-01-15T10:00:00Z/2024-01-15T12:00:00Z

  # Filter by Kubernetes namespace
  firecore tools substreams logs connections sfinfra --gcp-project my-project -n eth-mainnet`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnections(cmd.Context(), args[0], cmd, logger)
		},
	}

	cmd.Flags().String("backend", "gcp", "Log backend to use (currently only 'gcp' supported)")
	cmd.Flags().String("since", "", sinceFlagHelp)
	cmd.Flags().StringP("k8s-namespace", "n", "", "Kubernetes namespace to filter logs")
	cmd.Flags().String("gcp-project", "", "GCP project ID (required for GCP backend)")

	return cmd
}

func runConnections(ctx context.Context, userID string, cmd *cobra.Command, logger *zap.Logger) error {
	backend := sflags.MustGetString(cmd, "backend")
	since := sflags.MustGetString(cmd, "since")
	namespace := sflags.MustGetString(cmd, "k8s-namespace")
	gcpProject := sflags.MustGetString(cmd, "gcp-project")

	logger.Debug("connections command invoked",
		zap.String("user_id", userID),
		zap.String("backend", backend),
		zap.String("since", since),
		zap.String("namespace", namespace),
		zap.String("gcp_project", gcpProject),
	)

	// Parse time range
	startTime, endTime, err := parseTimeRange(since, time.Hour)
	if err != nil {
		return fmt.Errorf("parsing time range: %w", err)
	}

	logger.Debug("parsed time range",
		zap.Time("start_time", startTime),
		zap.Time("end_time", endTime),
	)

	fmt.Println(stylex.Title("Substreams Connections"))
	fmt.Println(stylex.Dim(stylex.Separator(120)))

	fmt.Printf("%s %s\n", stylex.Label("Organization:"), stylex.Value(userID))
	if namespace != "" {
		fmt.Printf("%s %s\n", stylex.Label("Namespace:"), stylex.Value(namespace))
	}
	duration := endTime.Sub(startTime)
	fmt.Printf("%s %s - %s (%s)\n",
		stylex.Label("Time range:"),
		stylex.Value(startTime.Local().Format("2006-01-02 15:04:05")),
		stylex.Value(endTime.Local().Format("2006-01-02 15:04:05")),
		stylex.Value(formatDuration(duration)),
	)
	fmt.Println(stylex.Dimf("Times shown in local timezone (%s)", localTimezoneDisplay()))
	fmt.Println()

	// Create backend
	var logBackend logs.Backend
	switch backend {
	case "gcp":
		if gcpProject == "" {
			return fmt.Errorf("--gcp-project is required when using GCP backend")
		}
		logBackend, err = logs.NewGCPBackend(ctx, gcpProject, logger)
		if err != nil {
			return fmt.Errorf("creating GCP backend: %w", err)
		}
		defer logBackend.Close()
	default:
		return fmt.Errorf("unsupported backend: %s (only 'gcp' is supported)", backend)
	}

	// Query logs
	opts := logs.QueryOptions{
		UserID:    userID,
		Namespace: namespace,
		StartTime: startTime,
		EndTime:   endTime,
	}

	entries, err := logBackend.QueryLogs(ctx, opts)
	if err != nil {
		return fmt.Errorf("querying logs: %w", err)
	}

	// Correlate connections
	result := logs.CorrelateConnections(entries, startTime)

	// Print results
	printConnectionsTable(result, userID, namespace, startTime, endTime)

	return nil
}

// parseTimeRange parses the --since flag into start/end times, using the same
// grammar as the 'reexec' date-range argument (ParseLogDateRange), so a value
// copied from one command works unchanged on the other. If not provided, looks
// back defaultLookback from now.
func parseTimeRange(since string, defaultLookback time.Duration) (time.Time, time.Time, error) {
	now := time.Now()

	if since == "" {
		return now.Add(-defaultLookback), now, nil
	}

	return ParseLogDateRange([]string{since})
}

// Supported date/time formats in order of precedence
var dateTimeFormats = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05Z",
}

// parseDateTime parses a datetime string in various formats
func parseDateTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)

	for _, format := range dateTimeFormats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("unrecognized date format: %s", s)
}
