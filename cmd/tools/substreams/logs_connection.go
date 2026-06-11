package substreams

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"github.com/streamingfast/firehose-core/cmd/tools/substreams/logs"
	"go.uber.org/zap"
)

// NewToolsLogsConnectionCmd returns the "connection" subcommand which prints the
// .spkg URL and the relevant Substreams request information for a single trace ID.
func NewToolsLogsConnectionCmd(logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connection <trace-id> [<date-range>]",
		Short: "Show Substreams request details for a single trace ID",
		Long: `Looks up the Substreams request matching the given trace ID in GCP Cloud Logging
and prints the request details (output module, blocks, mode flags, namespace), the .spkg URL
on the state store, and the request stats (when available).

The trace-id must be 0-32 characters.

The date-range is optional; when omitted it defaults to the last 7 days.

The date-range argument(s) accept various formats:

  Relative:   1d  2hr  30m  "1 day ago"  "2 hours ago"  "30 minutes ago"
  Timestamp:  2024-01-15T10:00:00Z  (past → search start to now)
  Range:      "2024-01-15T10:00:00Z:2024-01-15T12:00:00Z"  (single arg with colon)
              "2024-01-15T10:00:00Z/2024-01-15T12:00:00Z"  (single arg with slash)
  Two args:   2024-01-15T10:00:00Z  2024-01-15T12:00:00Z`,
		Example: `  firecore tools substreams logs connection bfb0980c436f3fd6f5564a31311d583f \
    --gcp-project my-project

  firecore tools substreams logs connection bfb0980c436f3fd6f5564a31311d583f 2h \
    --state-store gs://my-bucket/substreams-states \
    --gcp-project my-project`,
		Args: cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConnection(cmd.Context(), args, cmd, logger)
		},
	}

	cmd.Flags().String("state-store", "", "State store URL where spkg files are stored (supports file:// and gs://). When set, the spkg is loaded to display the package name and full URL")
	cmd.Flags().String("gcp-project", "", "GCP project ID used for log querying")
	cmd.MarkFlagRequired("gcp-project")

	return cmd
}

func runConnection(ctx context.Context, args []string, cmd *cobra.Command, logger *zap.Logger) error {
	traceID := args[0]
	dateArgs := args[1:]

	if len(traceID) > 32 {
		return fmt.Errorf("trace-id must be at most 32 characters, got %d (%q)", len(traceID), traceID)
	}

	stateStore := sflags.MustGetString(cmd, "state-store")
	gcpProject := sflags.MustGetString(cmd, "gcp-project")

	var startTime, endTime time.Time
	if len(dateArgs) == 0 {
		endTime = time.Now()
		startTime = endTime.AddDate(0, 0, -7)
	} else {
		var err error
		startTime, endTime, err = ParseLogDateRange(dateArgs)
		if err != nil {
			return fmt.Errorf("parsing date range: %w", err)
		}
	}

	logger.Debug("connection invoked",
		zap.String("trace_id", traceID),
		zap.Time("start", startTime),
		zap.Time("end", endTime),
		zap.String("state_store", stateStore),
		zap.String("gcp_project", gcpProject),
	)

	fmt.Println(stylex.Title("Substreams Connection"))
	fmt.Println(stylex.Dim(stylex.Separator(80)))
	fmt.Printf("%s %s\n", stylex.Label("Trace ID:"), stylex.Value(traceID))
	fmt.Printf("%s %s → %s\n",
		stylex.Label("Time range:"),
		stylex.Value(startTime.UTC().Format(time.RFC3339)),
		stylex.Value(endTime.UTC().Format(time.RFC3339)),
	)
	fmt.Println()

	fmt.Print(stylex.Label("Searching GCP logs... "))
	backend, err := logs.NewGCPBackend(ctx, gcpProject, logger)
	if err != nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("creating GCP backend: %w", err)
	}
	defer backend.Close()

	entries, err := backend.QueryLogs(ctx, logs.QueryOptions{
		TraceID:   traceID,
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("querying logs: %w", err)
	}

	request, stats, requestCount := pickConnectionEntries(entries)
	if request == nil && stats == nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("no log found for trace ID %q in the given time range", traceID)
	}

	if requestCount > 1 {
		fmt.Println(stylex.Warn("⚠"))
		fmt.Printf("%s\n", stylex.Warnf("  Found %d incoming-request logs — using the first (oldest) one", requestCount))
	} else {
		fmt.Println(stylex.Success("✓"))
	}
	fmt.Println()

	printConnectionRequest(request, stats)

	moduleHash := ""
	if request != nil {
		moduleHash = request.OutputModuleHash
	} else {
		moduleHash = stats.OutputModuleHash
	}

	fmt.Println()
	if stateStore == "" {
		fmt.Printf("%s %s\n", stylex.Label("Spkg URL:"), stylex.Dim("<not resolved — pass --state-store to load it>"))
	} else if moduleHash == "" {
		fmt.Printf("%s %s\n", stylex.Label("Spkg URL:"), stylex.Warn("<output_module_hash not found in log entry>"))
	} else {
		fmt.Print(stylex.Label("Loading spkg... "))
		pkg, spkgURL, isPartial, err := ResolveAndLoadSpkg(ctx, stateStore, moduleHash)
		if err != nil {
			fmt.Println(stylex.Error("✗"))
			fmt.Printf("%s\n", stylex.Errorf("  %v", err))
		} else {
			fmt.Println(stylex.Success("✓"))
			fmt.Printf("%s %s\n", stylex.Label("Package:"), stylex.Value(SpkgPackageLabel(pkg)))
			fmt.Printf("%s %s\n", stylex.Label("Spkg URL:"), stylex.Value(spkgURL))
			if isPartial {
				fmt.Printf("%s\n", stylex.Warn("Partial package detected — module output type and package validation skipped"))
			}
		}
	}

	if request != nil && request.Namespace != "" {
		if endpoint := InferEndpointFromNamespace(request.Namespace); endpoint != "" {
			fmt.Printf("%s %s %s\n",
				stylex.Label("Endpoint (inferred):"),
				stylex.Value(endpoint),
				stylex.Dimf("(from namespace %q)", request.Namespace),
			)
		}
	}

	return nil
}

// pickConnectionEntries selects the oldest incoming-request log and the most
// recent stats log from the entries (both are optional). The total number of
// incoming-request logs is also returned to surface ambiguous matches.
//
// Timestamps are parsed to time.Time before comparing so mixed RFC3339 and
// RFC3339Nano formats (e.g. "...:00Z" vs "...:00.123Z") order correctly. If a
// timestamp fails to parse, the entry still wins over a nil incumbent (so we
// never return nil when matches exist) but loses any timestamp comparison.
func pickConnectionEntries(entries []logs.LogEntry) (request, stats *logs.LogEntry, requestCount int) {
	var requestTS, statsTS time.Time
	for i := range entries {
		entry := &entries[i]
		ts, tsOK := parseLogTimestamp(entry.Timestamp)
		switch {
		case entry.IsIncomingRequest():
			requestCount++
			if request == nil || (tsOK && ts.Before(requestTS)) {
				request = entry
				if tsOK {
					requestTS = ts
				}
			}
		case entry.IsRequestStats():
			if stats == nil || (tsOK && ts.After(statsTS)) {
				stats = entry
				if tsOK {
					statsTS = ts
				}
			}
		}
	}
	return request, stats, requestCount
}

func printConnectionRequest(request, stats *logs.LogEntry) {
	if request != nil {
		fmt.Println(stylex.Header("Request"))
		printField("Output module:", request.OutputModule)
		printField("Module hash:", request.OutputModuleHash)
		fmt.Printf("%s %d\n", stylex.Label("Start block:"), request.StartBlock)
		if request.StopBlock > 0 {
			fmt.Printf("%s %d\n", stylex.Label("Stop block:"), request.StopBlock)
		} else {
			fmt.Printf("%s %s\n", stylex.Label("Stop block:"), stylex.Dim("open-ended"))
		}
		fmt.Printf("%s %v\n", stylex.Label("Production mode:"), request.ProductionMode)
		fmt.Printf("%s %v\n", stylex.Label("Final blocks only:"), request.FinalBlocksOnly)
		if request.NoopMode {
			fmt.Printf("%s %v\n", stylex.Label("Noop mode:"), request.NoopMode)
		}
		if request.Cursor != "" {
			cur := request.Cursor
			if len(cur) > 24 {
				cur = cur[:24] + "..."
			}
			fmt.Printf("%s %s\n", stylex.Label("Cursor:"), stylex.Dim(cur))
		}
		printField("User ID:", request.UserID)
		printField("IP address:", request.IPAddress)
		printField("Namespace:", request.Namespace)
		printField("Cluster:", request.ClusterName)
		printField("Pod:", request.PodName)
		if ts, ok := parseLogTimestamp(request.Timestamp); ok {
			fmt.Printf("%s %s\n", stylex.Label("Started at:"), stylex.Value(ts.Local().Format("2006-01-02 15:04:05 MST")))
		}
	} else {
		fmt.Println(stylex.Warn("No incoming-request log found for this trace ID — only stats are shown"))
		printField("Output module:", stats.OutputModule)
		printField("Module hash:", stats.OutputModuleHash)
		printField("User ID:", stats.UserID)
		printField("Namespace:", stats.Namespace)
	}

	if stats == nil {
		fmt.Println()
		fmt.Println(stylex.Note("Stats log not found — request may still be running or stats may have been dropped"))
		return
	}

	fmt.Println()
	fmt.Println(stylex.Header("Stats"))
	fmt.Printf("%s %d\n", stylex.Label("Blocks processed:"), stats.TotalBlocksProcessed)
	if stats.BlockRatePerSec != "" {
		fmt.Printf("%s %s blk/s\n", stylex.Label("Block rate:"), stats.BlockRatePerSec)
	}
	if stats.TimeToFirstData > 0 {
		fmt.Printf("%s %s\n", stylex.Label("Time to first data:"), stylex.Value(formatDuration(time.Duration(stats.TimeToFirstData*float64(time.Second)))))
	}
	if stats.Duration > 0 {
		fmt.Printf("%s %s\n", stylex.Label("Duration:"), stylex.Value(formatDuration(time.Duration(stats.Duration*float64(time.Second)))))
	}
	if stats.ResolvedStartBlock > 0 {
		fmt.Printf("%s %d\n", stylex.Label("Resolved start block:"), stats.ResolvedStartBlock)
	}
	if stats.Error != "" {
		if logs.IsNormalDisconnect(stats.Error) {
			fmt.Printf("%s %s\n", stylex.Label("Termination:"), stylex.Dim(stats.Error))
		} else {
			fmt.Printf("%s %s\n", stylex.Label("Error:"), stylex.Error(stats.Error))
		}
	}
	if ts, ok := parseLogTimestamp(stats.Timestamp); ok {
		fmt.Printf("%s %s\n", stylex.Label("Ended at:"), stylex.Value(ts.Local().Format("2006-01-02 15:04:05 MST")))
	}
}

func printField(label, value string) {
	if value == "" {
		return
	}
	fmt.Printf("%s %s\n", stylex.Label(label), stylex.Value(value))
}

func parseLogTimestamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
