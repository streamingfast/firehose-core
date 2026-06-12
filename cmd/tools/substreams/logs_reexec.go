package substreams

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	"github.com/cenkalti/backoff/v4"
	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"github.com/streamingfast/firehose-core/cmd/tools/substreams/logs"
	sflogging "github.com/streamingfast/logging"
	"github.com/streamingfast/substreams/client"
	"github.com/streamingfast/substreams/manifest"
	pbsubstreamsrpc "github.com/streamingfast/substreams/pb/sf/substreams/rpc/v2"
	"github.com/streamingfast/substreams/sink"
	"go.uber.org/zap"
	"google.golang.org/protobuf/encoding/protojson"
)

var _, reexecTracer = sflogging.PackageLogger("substreams-reexec", "github.com/streamingfast/firehose-core/cmd/tools/substreams/reexec")

func NewToolsLogsReexecCmd(logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reexec <trace-id> [<date-range>]",
		Short: "Replay a Substreams request from a trace ID found in logs",
		Long: `Looks up the original Substreams request in GCP Cloud Logging using the given
trace ID and date range, downloads the package from the state store, then replays
the stream exactly as the original request.

The trace-id must be 0-32 characters.

The Substreams endpoint is inferred automatically from the k8s namespace found in the
log entry (via the networks registry). Use --endpoint to override or when inference fails.

The date-range is optional; when omitted it defaults to the last 7 days.

The date-range argument(s) accept various formats:

  Relative:   1d  2hr  30m  "1 day ago"  "2 hours ago"  "30 minutes ago"
  Timestamp:  2024-01-15T10:00:00Z  (past → search start to now)
  Range:      "2024-01-15T10:00:00Z:2024-01-15T12:00:00Z"  (single arg with colon)
              "2024-01-15T10:00:00Z/2024-01-15T12:00:00Z"  (single arg with slash)
  Two args:   2024-01-15T10:00:00Z  2024-01-15T12:00:00Z`,
		Example: `  firecore tools substreams logs reexec bfb0980c436f3fd6f5564a31311d583f 2h \
    --state-store gs://my-bucket/substreams-states \
    --gcp-project my-project

  firecore tools substreams logs reexec bfb0980c436f3fd6f5564a31311d583f \
    "2026-04-20T04:00:00Z:2026-04-20T06:00:00Z" \
    --state-store gs://my-bucket/states \
    --gcp-project my-project -o protojson`,
		Args: cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReexec(cmd.Context(), args, cmd, logger)
		},
	}

	cmd.Flags().String("state-store", "", "State store URL where spkg files are stored (supports file:// and gs://)")
	cmd.MarkFlagRequired("state-store")

	cmd.Flags().String("endpoint", "", "Substreams gRPC endpoint (inferred from the log's namespace when not set)")
	cmd.Flags().String("api-key", "", "API key for Substreams endpoint authentication (or set SUBSTREAMS_API_KEY env var)")
	cmd.Flags().String("gcp-project", "", "GCP project ID used for log querying")
	cmd.MarkFlagRequired("gcp-project")

	cmd.Flags().String("cursor", "", "Starting cursor, overrides the cursor from the log entry")
	cmd.Flags().Bool("production-mode", true, "Override execution mode: 'true' forces production mode, 'false' forces development mode; when not provided, keeps the original request's mode")
	cmd.Flags().Bool("insecure", false, "Allow insecure TLS connections to the endpoint")
	cmd.Flags().Bool("plain-text", false, "Use unencrypted plain-text connection to the endpoint")
	cmd.Flags().StringP("output", "o", "clock", "Output format for blocks: clock, json, or protojson")

	return cmd
}

func runReexec(ctx context.Context, args []string, cmd *cobra.Command, logger *zap.Logger) error {
	traceID := args[0]
	dateArgs := args[1:]

	if len(traceID) > 32 {
		return fmt.Errorf("trace-id must be at most 32 characters, got %d (%q)", len(traceID), traceID)
	}

	stateStore := sflags.MustGetString(cmd, "state-store")
	endpointFlag := sflags.MustGetString(cmd, "endpoint")
	cursorFlag := sflags.MustGetString(cmd, "cursor")
	productionModeFlag, productionModeFlagProvided := sflags.MustGetBoolProvided(cmd, "production-mode")
	apiKey := sflags.MustGetString(cmd, "api-key")
	if apiKey == "" {
		apiKey = os.Getenv("SUBSTREAMS_API_KEY")
	}
	gcpProject := sflags.MustGetString(cmd, "gcp-project")
	insecure := sflags.MustGetBool(cmd, "insecure")
	plainText := sflags.MustGetBool(cmd, "plain-text")
	outputFormat := sflags.MustGetString(cmd, "output")

	switch outputFormat {
	case "clock", "json", "protojson":
	default:
		return fmt.Errorf("invalid output format %q: must be clock, json, or protojson", outputFormat)
	}

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

	logger.Debug("reexec invoked",
		zap.String("trace_id", traceID),
		zap.Time("start", startTime),
		zap.Time("end", endTime),
		zap.String("state_store", stateStore),
		zap.String("gcp_project", gcpProject),
	)

	fmt.Println(stylex.Title("Substreams Request Re-execution"))
	fmt.Println(stylex.Dim(stylex.Separator(80)))
	fmt.Printf("%s %s\n", stylex.Label("Trace ID:"), stylex.Value(traceID))
	fmt.Printf("%s %s → %s\n",
		stylex.Label("Time range:"),
		stylex.Value(startTime.UTC().Format(time.RFC3339)),
		stylex.Value(endTime.UTC().Format(time.RFC3339)),
	)
	fmt.Println()

	fmt.Print(stylex.Label("Searching GCP logs... "))
	req, matchCount, err := queryIncomingRequestByTraceID(ctx, gcpProject, traceID, startTime, endTime, logger)
	if err != nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("querying GCP logs: %w", err)
	}
	if req == nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("no log found for trace ID %q in the given time range", traceID)
	}

	if matchCount > 1 {
		fmt.Println(stylex.Warn("⚠"))
		fmt.Printf("%s\n", stylex.Warnf("  Found %d matching logs — using the first (oldest) one", matchCount))
	} else {
		fmt.Println(stylex.Success("✓"))
	}

	fmt.Printf("%s %s\n", stylex.Label("Output module:"), stylex.Value(req.OutputModule))
	fmt.Printf("%s %s\n", stylex.Label("Module hash:"), stylex.Value(req.OutputModuleHash))
	fmt.Printf("%s %d\n", stylex.Label("Start block:"), req.StartBlock)
	if req.StopBlock > 0 {
		fmt.Printf("%s %d\n", stylex.Label("Stop block:"), req.StopBlock)
	} else {
		fmt.Printf("%s %s\n", stylex.Label("Stop block:"), stylex.Dim("open-ended"))
	}
	if productionModeFlagProvided {
		req.ProductionMode = productionModeFlag
		fmt.Printf("%s %v %s\n", stylex.Label("Production mode:"), req.ProductionMode, stylex.Dim("(forced by --production-mode)"))
	} else {
		fmt.Printf("%s %v\n", stylex.Label("Production mode:"), req.ProductionMode)
	}
	fmt.Printf("%s %v\n", stylex.Label("Final blocks only:"), req.FinalBlocksOnly)
	if req.Namespace != "" {
		fmt.Printf("%s %s\n", stylex.Label("Namespace:"), stylex.Value(req.Namespace))
	}
	if cursorFlag != "" {
		req.Cursor = cursorFlag
		fmt.Printf("%s %s\n", stylex.Label("Cursor:"), stylex.Dimf("%s... (from --cursor flag)", cursorFlag[:min(24, len(cursorFlag))]))
	} else if req.Cursor != "" {
		shown := req.Cursor[:min(24, len(req.Cursor))]
		fmt.Printf("%s %s\n", stylex.Label("Cursor:"), stylex.Dimf("%s... (from log)", shown))
	}
	fmt.Println()

	// Resolve endpoint
	endpoint, err := ResolveEndpoint(endpointFlag, req.Namespace)
	if err != nil {
		return err
	}

	// Resolve and load the package (full or partial)
	fmt.Print(stylex.Label("Loading spkg... "))
	pkg, spkgURL, isPartial, err := ResolveAndLoadSpkg(ctx, stateStore, req.OutputModuleHash)
	if err != nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("loading spkg: %w", err)
	}
	fmt.Println(stylex.Success("✓"))
	fmt.Printf("%s %s\n", stylex.Label("Package:"), stylex.Value(SpkgPackageLabel(pkg)))
	fmt.Printf("%s %s\n", stylex.Label("Spkg URL:"), stylex.Dim(spkgURL))
	if isPartial {
		fmt.Printf("%s\n", stylex.Warn("Partial package detected — skipping module output type and package validation"))
	}
	fmt.Println()

	outputModule, err := FindModuleInPackage(pkg, req.OutputModule)
	if err != nil {
		return fmt.Errorf("finding output module: %w", err)
	}

	moduleHashBytes, err := hex.DecodeString(req.OutputModuleHash)
	if err != nil {
		return fmt.Errorf("decoding module hash %q: %w", req.OutputModuleHash, err)
	}

	var authType client.AuthType
	if apiKey != "" {
		authType = client.ApiKey
	}

	clientConfig := client.NewSubstreamsClientConfig(client.SubstreamsClientConfigOptions{
		Endpoint:  endpoint,
		AuthToken: apiKey,
		AuthType:  authType,
		Insecure:  insecure,
		PlainText: plainText,
	})

	mode := sink.SubstreamsModeDevelopment
	if req.ProductionMode {
		mode = sink.SubstreamsModeProduction
	}

	sinkerConfig := &sink.SinkerConfig{
		Pkg:              pkg,
		OutputModule:     outputModule,
		OutputModuleHash: manifest.ModuleHash(moduleHashBytes),
		ClientConfig:     clientConfig,
		Mode:             mode,
		StartBlock:       req.StartBlock,
		StopBlock:        req.StopBlock,
		FinalBlocksOnly:  req.FinalBlocksOnly,
		NoopMode:         req.NoopMode,
		MaxRetries:       0,
		BackOff:          backoff.NewExponentialBackOff(),
		Logger:           logger,
		Tracer:           reexecTracer,
	}

	sinker, err := sink.NewFromConfig(sinkerConfig)
	if err != nil {
		return fmt.Errorf("creating sinker: %w", err)
	}

	var cursor *sink.Cursor
	if req.Cursor != "" {
		cursor, err = sink.NewCursor(req.Cursor)
		if err != nil {
			return fmt.Errorf("parsing cursor from log: %w", err)
		}
	}

	fmt.Printf("%s %s\n", stylex.Label("Endpoint:"), stylex.Value(endpoint))
	fmt.Printf("%s %s\n", stylex.Label("Output:"), stylex.Value(outputFormat))
	fmt.Println(stylex.Dim(stylex.Separator(80)))
	fmt.Println()

	var (
		lastBlock  *pbsubstreamsrpc.BlockScopedData
		lastCursor *sink.Cursor
	)

	handler := sink.NewSinkerHandlers(
		func(ctx context.Context, data *pbsubstreamsrpc.BlockScopedData, isLive *bool, cur *sink.Cursor) error {
			lastBlock = data
			lastCursor = cur
			return outputBlock(data, outputFormat)
		},
		func(ctx context.Context, undoSignal *pbsubstreamsrpc.BlockUndoSignal, cur *sink.Cursor) error {
			return nil
		},
	)

	done := make(chan struct{})
	var sinkerErr error
	sinker.OnTerminated(func(err error) {
		sinkerErr = err
		close(done)
	})

	go sinker.Run(ctx, cursor, handler)
	<-done

	if lastBlock != nil {
		printReexecReport(lastBlock, lastCursor, endpoint, spkgURL, req, outputModule.Name)
	}

	if errors.Is(sinkerErr, context.Canceled) {
		return nil
	}
	return sinkerErr
}

// reexecRequest holds parsed fields from the "incoming Substreams Blocks request" log entry.
type reexecRequest struct {
	OutputModuleHash string
	OutputModule     string
	StartBlock       int64
	StopBlock        uint64
	Cursor           string
	ProductionMode   bool
	FinalBlocksOnly  bool
	NoopMode         bool
	TraceID          string
	SessionID        string
	Namespace        string
}

// queryIncomingRequestByTraceID searches GCP logs for the incoming Substreams
// request matching the given trace ID. Returns the oldest match (selected by
// parsed timestamp, since the backend iterates newest-first) and the total
// count of matches found.
func queryIncomingRequestByTraceID(ctx context.Context, gcpProject, traceID string, startTime, endTime time.Time, logger *zap.Logger) (*reexecRequest, int, error) {
	backend, err := logs.NewGCPBackend(ctx, gcpProject, logger)
	if err != nil {
		return nil, 0, fmt.Errorf("creating GCP backend: %w", err)
	}
	defer backend.Close()

	entries, err := backend.QueryLogs(ctx, logs.QueryOptions{
		TraceID:   traceID,
		StartTime: startTime,
		EndTime:   endTime,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("querying logs: %w", err)
	}

	var oldest *logs.LogEntry
	var oldestTS time.Time
	count := 0
	for i := range entries {
		entry := &entries[i]
		if !entry.IsIncomingRequest() {
			continue
		}
		count++
		ts, tsOK := parseLogTimestamp(entry.Timestamp)
		if oldest == nil || (tsOK && ts.Before(oldestTS)) {
			oldest = entry
			if tsOK {
				oldestTS = ts
			}
		}
	}

	if oldest == nil {
		return nil, count, nil
	}
	return &reexecRequest{
		OutputModuleHash: oldest.OutputModuleHash,
		OutputModule:     oldest.OutputModule,
		StartBlock:       oldest.StartBlock,
		StopBlock:        oldest.StopBlock,
		Cursor:           oldest.Cursor,
		ProductionMode:   oldest.ProductionMode,
		FinalBlocksOnly:  oldest.FinalBlocksOnly,
		NoopMode:         oldest.NoopMode,
		TraceID:          oldest.TraceID,
		SessionID:        oldest.SessionID,
		Namespace:        oldest.Namespace,
	}, count, nil
}

// ─── Block output ──────────────────────────────────────────────────────────────

func outputBlock(data *pbsubstreamsrpc.BlockScopedData, format string) error {
	switch format {
	case "clock":
		fmt.Println(blockClockLine(data))
		return nil
	case "json", "protojson":
		return outputBlockProtoJSON(data)
	default:
		return fmt.Errorf("unknown output format %q", format)
	}
}

// blockClockLine formats a block as a single human-readable line used by both
// the clock output mode and the termination report.
func blockClockLine(data *pbsubstreamsrpc.BlockScopedData) string {
	clock := data.Clock

	id := clock.Id
	var idShort string
	if len(id) <= 10 {
		idShort = id
	} else {
		idShort = id[:5] + ".." + id[len(id)-5:]
	}

	typeStr := "<none>"
	var payloadSize int
	if data.Output != nil && data.Output.MapOutput != nil {
		typeURL := data.Output.MapOutput.TypeUrl
		if _, after, found := strings.Cut(typeURL, "googleapis.com/"); found {
			typeURL = after
		}
		typeStr = typeURL
		payloadSize = len(data.Output.MapOutput.Value)
	}

	return fmt.Sprintf("Block #%s (%s) type=%s payload=%s age=%s",
		humanize.Comma(int64(clock.Number)),
		idShort,
		typeStr,
		humanize.Bytes(uint64(payloadSize)),
		formatDuration(time.Since(clock.Timestamp.AsTime())),
	)
}

// ─── Termination report ────────────────────────────────────────────────────────

func printReexecReport(lastBlock *pbsubstreamsrpc.BlockScopedData, lastCursor *sink.Cursor, endpoint, spkgURL string, req *reexecRequest, moduleName string) {
	cursorStr := ""
	if lastCursor != nil {
		cursorStr = lastCursor.String()
	}

	fmt.Println()
	fmt.Println(stylex.Dim(stylex.Separator(80)))
	fmt.Printf("%s %s\n", stylex.Label("Last block:"), blockClockLine(lastBlock))
	if cursorStr != "" {
		fmt.Printf("%s %s\n", stylex.Label("Last cursor:"), cursorStr)
	}
	fmt.Println()

	fmt.Printf("%s\n", stylex.Label("Re-run with substreams:"))
	fmt.Printf("  %s\n", buildSubstreamsRunCmd(endpoint, spkgURL, moduleName, cursorStr, req))
}

func buildSubstreamsRunCmd(endpoint, spkgURL, moduleName, cursor string, req *reexecRequest) string {
	var parts []string
	parts = append(parts, "substreams run")
	parts = append(parts, fmt.Sprintf("-e %s", endpoint))
	if req.ProductionMode {
		parts = append(parts, "--production-mode")
	}
	if req.FinalBlocksOnly {
		parts = append(parts, "--final-blocks-only")
	}
	if req.StopBlock > 0 {
		parts = append(parts, fmt.Sprintf("-t %d", req.StopBlock))
	}
	if cursor != "" {
		parts = append(parts, fmt.Sprintf(`--cursor "%s"`, cursor))
	}
	parts = append(parts, spkgURL)
	parts = append(parts, moduleName)
	return strings.Join(parts, " \\\n    ")
}

func outputBlockProtoJSON(data *pbsubstreamsrpc.BlockScopedData) error {
	out, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshalling block as protojson: %w", err)
	}
	fmt.Println(string(out))
	return nil
}
