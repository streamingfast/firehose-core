package logs

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	"go.uber.org/zap"
)

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

  # Show connections for the last 30 minutes
  firecore tools substreams logs connections sfinfra --gcp-project my-project --since 30m

  # Show connections for a specific date range
  firecore tools substreams logs connections sfinfra --gcp-project my-project --date-range 2024-01-15T10:00:00Z/2024-01-15T12:00:00Z

  # Filter by Kubernetes namespace
  firecore tools substreams logs connections sfinfra --gcp-project my-project -n eth-mainnet`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnections(cmd.Context(), args[0], cmd, logger)
		},
	}

	cmd.Flags().String("backend", "gcp", "Log backend to use (currently only 'gcp' supported)")
	cmd.Flags().String("since", "", "Look back duration (e.g., '1h', '30m', '2d'). Mutually exclusive with --date-range")
	cmd.Flags().String("date-range", "", "Date range in format '<start>[/<end>]'. End defaults to now. Mutually exclusive with --since")
	cmd.Flags().StringP("k8s-namespace", "n", "", "Kubernetes namespace to filter logs")
	cmd.Flags().String("gcp-project", "", "GCP project ID (required for GCP backend)")

	return cmd
}

func runConnections(ctx context.Context, userID string, cmd *cobra.Command, logger *zap.Logger) error {
	backend := sflags.MustGetString(cmd, "backend")
	since := sflags.MustGetString(cmd, "since")
	dateRange := sflags.MustGetString(cmd, "date-range")
	namespace := sflags.MustGetString(cmd, "k8s-namespace")
	gcpProject := sflags.MustGetString(cmd, "gcp-project")

	logger.Debug("connections command invoked",
		zap.String("user_id", userID),
		zap.String("backend", backend),
		zap.String("since", since),
		zap.String("date_range", dateRange),
		zap.String("namespace", namespace),
		zap.String("gcp_project", gcpProject),
	)

	// Validate flags
	if since != "" && dateRange != "" {
		return fmt.Errorf("--since and --date-range are mutually exclusive")
	}

	// Parse time range
	startTime, endTime, err := parseTimeRange(since, dateRange)
	if err != nil {
		return fmt.Errorf("parsing time range: %w", err)
	}

	logger.Debug("parsed time range",
		zap.Time("start_time", startTime),
		zap.Time("end_time", endTime),
	)

	// Create backend
	var logBackend LogBackend
	switch backend {
	case "gcp":
		if gcpProject == "" {
			return fmt.Errorf("--gcp-project is required when using GCP backend")
		}
		logBackend, err = NewGCPBackend(ctx, gcpProject, logger)
		if err != nil {
			return fmt.Errorf("creating GCP backend: %w", err)
		}
		defer logBackend.Close()
	default:
		return fmt.Errorf("unsupported backend: %s (only 'gcp' is supported)", backend)
	}

	// Query logs
	opts := QueryOptions{
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
	result := CorrelateConnections(entries, startTime)

	// Print results
	printConnectionsTable(result, userID, namespace, startTime, endTime)

	return nil
}

// parseTimeRange parses the --since or --date-range flags into start/end times
// If neither is provided, defaults to 1 hour ago
func parseTimeRange(since, dateRange string) (time.Time, time.Time, error) {
	now := time.Now()

	// Default: 1 hour ago
	if since == "" && dateRange == "" {
		return now.Add(-1 * time.Hour), now, nil
	}

	// Parse --since
	if since != "" {
		duration, err := parseDuration(since)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --since value: %w", err)
		}
		return now.Add(-duration), now, nil
	}

	// Parse --date-range
	return parseDateRange(dateRange)
}

// parseDuration parses a human-friendly duration string
// Supports: s, m, h, d (seconds, minutes, hours, days)
func parseDuration(s string) (time.Duration, error) {
	// Try standard Go duration first
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// Handle days suffix
	re := regexp.MustCompile(`^(\d+)d$`)
	if matches := re.FindStringSubmatch(s); len(matches) == 2 {
		days, err := strconv.Atoi(matches[1])
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	return 0, fmt.Errorf("invalid duration format: %s (use e.g., '1h', '30m', '2d')", s)
}

// parseDateRange parses a date range in format "<start>[/<end>]"
// If end is omitted, uses current time
func parseDateRange(dateRange string) (time.Time, time.Time, error) {
	// Split by '/' separator
	parts := strings.SplitN(dateRange, "/", 2)

	if len(parts) == 0 || parts[0] == "" {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid date range format")
	}

	startTime, err := parseDateTime(parts[0])
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing start time: %w", err)
	}

	var endTime time.Time
	if len(parts) == 1 || parts[1] == "" {
		endTime = time.Now()
	} else {
		endTime, err = parseDateTime(parts[1])
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parsing end time: %w", err)
		}
	}

	if endTime.Before(startTime) {
		return time.Time{}, time.Time{}, fmt.Errorf("end time must be after start time")
	}

	return startTime, endTime, nil
}

// Supported date/time formats in order of precedence
var dateTimeFormats = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02",
	"2006-01-02T15:04:05",
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
