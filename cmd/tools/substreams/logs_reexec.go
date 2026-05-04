package substreams

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dustin/go-humanize"

	gcplogging "cloud.google.com/go/logging"
	"cloud.google.com/go/logging/logadmin"
	"github.com/cenkalti/backoff/v4"
	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	networks "github.com/streamingfast/firehose-networks"
	sflogging "github.com/streamingfast/logging"
	"github.com/streamingfast/substreams/client"
	"github.com/streamingfast/substreams/manifest"
	pbsubstreamsrpc "github.com/streamingfast/substreams/pb/sf/substreams/rpc/v2"
	pbsubstreams "github.com/streamingfast/substreams/pb/sf/substreams/v1"
	"github.com/streamingfast/substreams/sink"
	"go.uber.org/zap"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

var _, reexecTracer = sflogging.PackageLogger("substreams-reexec", "github.com/streamingfast/firehose-core/cmd/tools/substreams/reexec")

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
	Namespace        string // extracted from resource.labels.namespace_name
}

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
		startTime, endTime, err = parseReexecDateRange(dateArgs)
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
	req, matchCount, err := queryReexecLog(ctx, gcpProject, traceID, startTime, endTime, logger)
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
	fmt.Printf("%s %v\n", stylex.Label("Production mode:"), req.ProductionMode)
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
	endpoint, err := resolveEndpoint(endpointFlag, req.Namespace)
	if err != nil {
		return err
	}

	// Resolve and load the package (full or partial)
	fmt.Print(stylex.Label("Loading spkg... "))
	pkg, spkgURL, isPartial, err := resolveAndLoadSpkg(ctx, stateStore, req.OutputModuleHash)
	if err != nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("loading spkg: %w", err)
	}
	pkgLabel := "<unknown>"
	if len(pkg.PackageMeta) > 0 {
		pkgLabel = pkg.PackageMeta[0].Name + "@" + pkg.PackageMeta[0].Version
	}
	fmt.Println(stylex.Success("✓"))
	fmt.Printf("%s %s\n", stylex.Label("Package:"), stylex.Value(pkgLabel))
	fmt.Printf("%s %s\n", stylex.Label("Spkg URL:"), stylex.Dim(spkgURL))
	if isPartial {
		fmt.Printf("%s\n", stylex.Warn("Partial package detected — skipping module output type and package validation"))
	}
	fmt.Println()

	outputModule, err := findModuleInPackage(pkg, req.OutputModule)
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

// resolveEndpoint returns the endpoint to use: the explicit flag value if set,
// otherwise inferred from the k8s namespace via the networks registry.
func resolveEndpoint(flagValue, namespace string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if namespace == "" {
		return "", fmt.Errorf("could not infer endpoint: no namespace found in log entry; provide --endpoint explicitly")
	}

	endpoint := inferEndpointFromNamespace(namespace)
	if endpoint == "" {
		return "", fmt.Errorf(
			"could not infer Substreams endpoint from namespace %q; provide --endpoint explicitly (e.g. --endpoint mainnet.eth.streamingfast.io:443)",
			namespace,
		)
	}

	fmt.Printf("%s %s %s\n",
		stylex.Label("Endpoint (inferred):"),
		stylex.Value(endpoint),
		stylex.Dimf("(from namespace %q)", namespace),
	)
	return endpoint, nil
}

// inferEndpointFromNamespace tries to find a Substreams endpoint for the given k8s namespace.
// Namespaces are typically "<chain>-<network>" (e.g. "eth-polygon-mainnet").
// It progressively strips leading dash-separated segments until a match is found:
//
//	eth-polygon-mainnet → polygon-mainnet → mainnet
func inferEndpointFromNamespace(namespace string) string {
	for s := namespace; s != ""; {
		if ep := networks.GetSubstreamsEndpoint(s); ep != "" {
			return ep
		}
		_, rest, found := strings.Cut(s, "-")
		if !found {
			break
		}
		s = rest
	}
	return ""
}

// ─── Date range parsing ────────────────────────────────────────────────────────

// parseReexecDateRange parses 1 or 2 args into a [start, end) time range.
func parseReexecDateRange(args []string) (time.Time, time.Time, error) {
	now := time.Now()
	switch len(args) {
	case 1:
		return parseSingleReexecDateArg(args[0], now)
	case 2:
		return parseTwoReexecDateArgs(args[0], args[1], now)
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("expected 1-2 date range args, got %d", len(args))
	}
}

var relDurationRegex = regexp.MustCompile(`(?i)^\s*(\d+)\s*([a-z]+?)(?:\s+ago)?\s*$`)

// parseRelativeDuration parses strings like "1d", "2hr", "30m", "1 day ago".
func parseRelativeDuration(s string) (time.Duration, bool) {
	m := relDurationRegex.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	unit := strings.ToLower(m[2])
	switch {
	case strings.HasPrefix(unit, "d"):
		return time.Duration(n) * 24 * time.Hour, true
	case strings.HasPrefix(unit, "h"):
		return time.Duration(n) * time.Hour, true
	case strings.HasPrefix(unit, "m"):
		return time.Duration(n) * time.Minute, true
	}
	return 0, false
}

func parseSingleReexecDateArg(s string, now time.Time) (time.Time, time.Time, error) {
	// Relative duration: "1d", "2hr", "30m", "1 day ago", …
	if d, ok := parseRelativeDuration(s); ok {
		return now.Add(-d), now, nil
	}

	// Range with "/" separator
	if idx := strings.Index(s, "/"); idx >= 0 {
		return parseReexecRangeParts(s[:idx], s[idx+1:], now)
	}

	// Range with ":" separator — try each ":" from right to left so we
	// find the inter-timestamp colon first (timestamps end with Z or ±hh:mm).
	if start, end, ok := tryColonRangeSplit(s); ok {
		return validateReexecRange(start, end, now)
	}

	// Single ISO timestamp → [timestamp, now)
	t, err := parseDateTime(s)
	if err != nil {
		return time.Time{}, time.Time{},
			fmt.Errorf("unrecognized date-range format %q (try: 2h, 1 day ago, 2024-01-15T10:00:00Z, start:end)", s)
	}
	if t.After(now) {
		return time.Time{}, time.Time{},
			fmt.Errorf("timestamp %q is in the future — provide a past timestamp as start of range", s)
	}
	return t, now, nil
}

func parseTwoReexecDateArgs(s1, s2 string, now time.Time) (time.Time, time.Time, error) {
	start, err := parseDateTime(strings.TrimSpace(s1))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing start %q: %w", s1, err)
	}
	end, err := parseDateTime(strings.TrimSpace(s2))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing end %q: %w", s2, err)
	}
	return validateReexecRange(start, end, now)
}

func parseReexecRangeParts(s1, s2 string, now time.Time) (time.Time, time.Time, error) {
	start, err := parseDateTime(strings.TrimSpace(s1))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing start %q: %w", s1, err)
	}
	end, err := parseDateTime(strings.TrimSpace(s2))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing end %q: %w", s2, err)
	}
	return validateReexecRange(start, end, now)
}

// tryColonRangeSplit attempts to split s at a colon that separates two valid
// timestamps, scanning from right to left so we find inter-timestamp colons first.
func tryColonRangeSplit(s string) (time.Time, time.Time, bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != ':' {
			continue
		}
		left := strings.TrimSpace(s[:i])
		right := strings.TrimSpace(s[i+1:])
		if left == "" || right == "" {
			continue
		}
		start, err1 := parseDateTime(left)
		end, err2 := parseDateTime(right)
		if err1 == nil && err2 == nil {
			return start, end, true
		}
	}
	return time.Time{}, time.Time{}, false
}

func validateReexecRange(start, end, now time.Time) (time.Time, time.Time, error) {
	if start.After(now) && end.After(now) {
		return time.Time{}, time.Time{}, fmt.Errorf("both start and end are in the future")
	}
	if !end.After(start) {
		return time.Time{}, time.Time{},
			fmt.Errorf("end time %s is not after start time %s",
				end.UTC().Format(time.RFC3339), start.UTC().Format(time.RFC3339))
	}
	return start, end, nil
}

// ─── GCP log querying ──────────────────────────────────────────────────────────

func queryReexecLog(ctx context.Context, gcpProject, traceID string, startTime, endTime time.Time, logger *zap.Logger) (*reexecRequest, int, error) {
	logClient, err := logadmin.NewClient(ctx, gcpProject)
	if err != nil {
		return nil, 0, fmt.Errorf("creating logadmin client: %w", err)
	}
	defer logClient.Close()

	filter := buildReexecFilter(traceID, startTime, endTime)
	logger.Debug("querying GCP logs", zap.String("filter", filter))

	it := logClient.Entries(ctx, logadmin.Filter(filter))

	var first *reexecRequest
	count := 0

	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("iterating log entries: %w", err)
		}

		req, err := parseReexecPayload(entry)
		if err != nil {
			logger.Debug("skipping unparseable log entry", zap.Error(err))
			continue
		}

		count++
		if first == nil {
			first = req
		}
	}

	return first, count, nil
}

func buildReexecFilter(traceID string, startTime, endTime time.Time) string {
	return fmt.Sprintf(
		`resource.type="k8s_container"
SEARCH("%s")
jsonPayload.message="incoming Substreams Blocks request"
timestamp >= "%s"
timestamp <= "%s"`,
		traceID,
		startTime.UTC().Format(time.RFC3339),
		endTime.UTC().Format(time.RFC3339),
	)
}

func parseReexecPayload(entry *gcplogging.Entry) (*reexecRequest, error) {
	var payload map[string]any
	switch p := entry.Payload.(type) {
	case *structpb.Struct:
		payload = p.AsMap()
	case map[string]any:
		payload = p
	default:
		return nil, fmt.Errorf("unexpected payload type %T", entry.Payload)
	}

	req := &reexecRequest{
		OutputModuleHash: reexecPayloadString(payload, "output_module_hash"),
		OutputModule:     reexecPayloadString(payload, "output_module"),
		StartBlock:       reexecPayloadInt64(payload, "start_block"),
		StopBlock:        reexecPayloadUint64(payload, "stop_block"),
		Cursor:           reexecPayloadString(payload, "cursor"),
		ProductionMode:   reexecPayloadBool(payload, "production_mode"),
		FinalBlocksOnly:  reexecPayloadBool(payload, "final_blocks_only"),
		NoopMode:         reexecPayloadBool(payload, "noop_mode"),
		TraceID:          reexecPayloadString(payload, "trace_id"),
		SessionID:        reexecPayloadString(payload, "session_id"),
	}

	if entry.Resource != nil {
		req.Namespace = entry.Resource.Labels["namespace_name"]
	}

	return req, nil
}

// ─── spkg loading ──────────────────────────────────────────────────────────────

// resolveAndLoadSpkg checks which spkg variant exists under stateStore/moduleHash/
// and loads it. Older Substreams RPC versions produced .partial.spkg.zst files which
// require skipping package and module output type validation.
// Returns the package, the full URL that was loaded, whether it is partial, and any error.
func resolveAndLoadSpkg(ctx context.Context, stateStore, moduleHash string) (*pbsubstreams.Package, string, bool, error) {
	base := strings.TrimSuffix(stateStore, "/") + "/" + moduleHash

	store, err := dstore.NewSimpleStore(base)
	if err != nil {
		return nil, "", false, fmt.Errorf("creating store at %s: %w", base, err)
	}

	fullExists, err := store.FileExists(ctx, "substreams.spkg.zst")
	if err != nil {
		return nil, "", false, fmt.Errorf("checking substreams.spkg.zst: %w", err)
	}
	if fullExists {
		url := base + "/substreams.spkg.zst"
		pkg, err := loadSpkgFromURL(url, false)
		return pkg, url, false, err
	}

	partialURL := base + "/substreams.partial.spkg.zst"
	pkg, err := loadSpkgFromURL(partialURL, true)
	return pkg, partialURL, true, err
}

// loadSpkgFromURL reads and parses the spkg at the given URL. When skipValidation
// is true, module output type and package validation are disabled — required for
// .partial.spkg.zst files produced by older Substreams RPC versions.
func loadSpkgFromURL(spkgURL string, skipValidation bool) (*pbsubstreams.Package, error) {
	opts := []manifest.Option{manifest.SkipSourceCodeReader()}
	if skipValidation {
		opts = append(opts, manifest.SkipPackageValidationReader(), manifest.SkipModuleOutputTypeValidationReader())
	}
	reader, err := manifest.NewReader(spkgURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating reader for %s: %w", spkgURL, err)
	}
	bundle, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("reading spkg: %w", err)
	}
	return bundle.Package, nil
}

func findModuleInPackage(pkg *pbsubstreams.Package, name string) (*pbsubstreams.Module, error) {
	if pkg.Modules == nil {
		return nil, fmt.Errorf("package has no modules")
	}

	findByName := func(n string) *pbsubstreams.Module {
		for _, m := range pkg.Modules.Modules {
			if m.Name == n {
				return m
			}
		}
		return nil
	}

	if m := findByName(name); m != nil {
		return m, nil
	}

	// Some output module names carry a "<prefix>:<name>" form (e.g. "graph_node:map_events")
	// but the package stores modules without the prefix.  Strip it and retry.
	if _, stripped, found := strings.Cut(name, ":"); found {
		if m := findByName(stripped); m != nil {
			fmt.Printf("%s\n", stylex.Warnf("Output module %q not found; retrying with stripped name %q", name, stripped))
			return m, nil
		}
	}

	available := make([]string, len(pkg.Modules.Modules))
	for i, m := range pkg.Modules.Modules {
		available[i] = m.Name
	}
	return nil, fmt.Errorf("module %q not found in package (available: %s)", name, strings.Join(available, ", "))
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

// ─── Payload helpers ───────────────────────────────────────────────────────────

func reexecPayloadString(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func reexecPayloadInt64(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

func reexecPayloadUint64(m map[string]any, key string) uint64 {
	switch v := m[key].(type) {
	case float64:
		return uint64(v)
	case uint64:
		return v
	case int64:
		return uint64(v)
	case int:
		return uint64(v)
	}
	return 0
}

func reexecPayloadBool(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}
