# Implementation Plan: substreams-logs-connections

## ULTIMATE GOAL

Implement a new CLI command `firecore tools substreams logs connections <user_id>` that:

1. Queries Cloud Logging (GCP) to find Substreams connection logs for a specific organization
2. Correlates connection logs by trace ID to match "incoming request" with "request stats" logs
3. Identifies active (no stats yet) vs closed (has stats) connections
4. Presents a compact summary table with key connection details including:
   - Status, Full Trace ID, Network, Source IP, Module name with inline hash, Duration, Blocks processed, Errors
5. Warns about orphaned stats logs at the end of the report

The architecture should use a Backend interface to abstract log querying, allowing future support for backends other than GCP.

## Status: COMPLETED

## Implementation Tasks

### Priority 1: Core Infrastructure (Required First)

- [x] **Create directory structure** - Create `cmd/tools/substreams/logs/` directory for organizing the logs-related commands. This follows the existing pattern seen in `cmd/tools/firehose/` and `cmd/tools/check/`.

- [x] **Define data structures** (`cmd/tools/substreams/logs/types.go`)
  - Define `ConnectionLog` struct with fields from incoming request (trace_id, user_id, ip_address, output_module, output_module_hash, start_block, stop_block, production_mode, timestamp) and resource labels (namespace, cluster_name, pod_name)
  - Define `ConnectionStats` struct with fields from stats log (total_blocks_processed, block_rate_per_sec, time_to_first_data, resolved_start_block, error, end_timestamp)
  - Define `QueryOptions` struct for backend query parameters (user_id, namespace, start_time, end_time)
  - Define connection status constants: `StatusActive`, `StatusClosed`, `StatusError`

- [x] **Define Backend interface** (`cmd/tools/substreams/logs/backend.go`)
  - Define `LogBackend` interface with method `QueryConnections(ctx context.Context, opts QueryOptions) ([]ConnectionLog, error)`
  - This abstraction will allow future backends (Loki, Elasticsearch) without changing command code

### Priority 2: GCP Backend Implementation

- [x] **Implement GCP backend** (`cmd/tools/substreams/logs/gcp_backend.go`)
  - Create `GCPBackend` struct holding `*logadmin.Client` and `projectID`
  - Implement `NewGCPBackend(ctx context.Context, projectID string) (*GCPBackend, error)` constructor
  - Implement `QueryConnections` method:
    1. Build Cloud Logging filter string for both log message types
    2. Use `client.Entries()` with filter, timestamp range, and NewestFirst ordering
    3. Iterate through entries, extracting jsonPayload fields via type assertion to `map[string]interface{}`
    4. Extract resource.labels for namespace, cluster, pod information
    5. Return slice of ConnectionLog
  - Build filter query like: `jsonPayload.user_id="<user_id>" AND (jsonPayload.message="incoming Substreams Blocks request" OR (jsonPayload.message="substreams request stats" AND jsonPayload.tier="tier1"))`
  - Handle pagination if result set is large (Cloud Logging returns paginated results)

- [x] **Add GCP Cloud Logging dependency** - Add `cloud.google.com/go/logging` to go.mod (note: `cloud.google.com/go/storage` already in go.mod, so auth infrastructure exists)

### Priority 3: Connection Correlation Logic

- [x] **Implement connection matching** (`cmd/tools/substreams/logs/connections.go` or within command)
  - Create function `correlateConnections(entries []ConnectionLog) (connections []ConnectionLog, orphanedCount int)`
  - Build map of trace_id -> ConnectionLog for incoming requests
  - For each stats entry, find matching incoming request by trace_id and populate Stats field
  - Track orphaned stats logs (stats without matching incoming request) - count them for warning
  - Mark connections appropriately:
    - `StatusActive`: has incoming request, no stats
    - `StatusClosed`: has both incoming and stats, no error
    - `StatusError`: has both, but stats.Error is non-empty
  - Sort final list by timestamp (newest first for display)
  - Return both the connections list and the count of orphaned stats

### Priority 4: CLI Command Structure

- [x] **Create logs parent command** (`cmd/tools/substreams/logs/logs.go`)
  - Create `NewToolsLogsCmd(logger *zap.Logger) *cobra.Command` that returns parent "logs" command
  - Use pattern: `&cobra.Command{Use: "logs", Short: "Substreams logs analysis tools"}`
  - Add connections subcommand

- [x] **Implement connections command** (`cmd/tools/substreams/logs/connections.go`)
  - Create `NewToolsLogsConnectionsCmd(logger *zap.Logger) *cobra.Command`
  - Define command with `Use: "connections <user_id>"`
  - Register flags:
    - `--backend` (string, default "gcp")
    - `--since` (string, e.g., "1h", "30m", "2d")
    - `--date-range` (string, e.g., "2024-01-15T10:00:00Z-2024-01-15T12:00:00Z")
    - `-n/--k8s-namespace` (string)
    - `--gcp-project` (string, required for GCP backend)
  - Implement RunE function:
    1. Parse and validate flags (ensure --since and --date-range are mutually exclusive)
    2. Parse time range (default to 1h if neither specified)
    3. Create appropriate backend based on --backend flag
    4. Call backend.QueryConnections()
    5. Correlate connections by trace_id
    6. Format and print table output

- [x] **Implement time range parsing**
  - Parse `--since` as Go duration (use `time.ParseDuration`, supporting "h", "m", "s", "d" for days)
  - Parse `--date-range` with format `<start>-[<end>]` where dates are RFC3339 or simpler formats
  - If only start provided in date-range, use current time as end
  - Convert to absolute start/end times for backend query

- [x] **Wire up to substreams command** (`cmd/tools/substreams/tools_substreams.go`)
  - Import the new logs package
  - Add `cmd.AddCommand(logs.NewToolsLogsCmd(logger))` to `NewToolsSubstreamsCmd`

### Priority 5: Table Output Formatting

- [x] **Implement table formatting** (`cmd/tools/substreams/logs/table.go`)
  - Reuse lipgloss styling pattern from `cmd/tools/substreams/store_size.go`
  - Create `printConnectionsTable(connections []ConnectionLog, orphanedCount int, opts DisplayOptions)`
  - Display columns: STATUS, TRACE_ID, NETWORK, SOURCE_IP, MODULE, DURATION, BLOCKS, ERROR
  - Show **full TRACE_ID** (32 character hex string) - important for debugging
  - Inline module hash in MODULE column: `<module_name> (<first_6_hash>...)`
  - Implement column width calculation for alignment
  - Format duration using `time.Since()` for active, or calculated duration for closed
  - Extract network name from namespace (strip common suffixes like "-tier1")
  - Truncate error messages to reasonable length (~30 chars)
  - Print summary footer: "Total: N connections (X active, Y closed, Z error)"
  - Print orphaned stats warning if any: "Warning: N orphaned stats logs found (stats without matching incoming request)"

- [x] **Implement TTY-aware styling**
  - Use `mattn/go-isatty` to detect TTY (pattern from store_size.go)
  - Use lipgloss for colors when TTY, plain text otherwise
  - Color coding: green for closed, yellow for active, red for error

### Priority 6: Error Handling and Edge Cases

- [x] **Implement robust error handling**
  - Handle GCP authentication errors with helpful messages
  - Handle empty result sets gracefully ("No connections found for organization: <user_id>")
  - Handle rate limiting from Cloud Logging API
  - Validate user_id argument is provided
  - Validate mutual exclusivity of --since and --date-range

- [x] **Handle orphaned stats logs**
  - Stats logs without matching incoming request (edge case)
  - Could indicate log rotation, retention policies, or query window not capturing incoming request
  - Collect count of orphaned stats during correlation
  - Display warning at end of CLI output: "Warning: N orphaned stats logs found (stats without matching incoming request)"
  - Do NOT display orphaned stats in main table (they lack essential fields like ip_address, start_block)

### Priority 7: Documentation and Polish

- [x] **Add command examples**
  - Use `cmd.Example` field like other commands in codebase
  - Show usage with --since, --date-range, --k8s-namespace
  - Show example output

- [x] **Add logging for debugging**
  - Use zap logger passed to command
  - Debug level: query being executed, number of entries found
  - Use existing `logging.PackageLogger` pattern from store_size_helper.go

## Completed Items

(none yet)

## File Changes Summary

### New Files

| File | Purpose |
|------|---------|
| `cmd/tools/substreams/logs/logs.go` | Parent "logs" command |
| `cmd/tools/substreams/logs/connections.go` | Main "connections" subcommand |
| `cmd/tools/substreams/logs/backend.go` | Backend interface definition |
| `cmd/tools/substreams/logs/gcp_backend.go` | GCP Cloud Logging implementation |
| `cmd/tools/substreams/logs/types.go` | Data structures |
| `cmd/tools/substreams/logs/table.go` | Table formatting utilities |
| `specs/substreams-logs-connections.md` | Feature specification |

### Modified Files

| File | Change |
|------|--------|
| `cmd/tools/substreams/tools_substreams.go` | Add import and register logs command |
| `go.mod` | Add `cloud.google.com/go/logging/logadmin` dependency (if not already available) |

## Dependencies

### Existing (already in go.mod)

- `cloud.google.com/go/storage` - GCP SDK auth infrastructure
- `github.com/charmbracelet/lipgloss` - Terminal styling
- `github.com/mattn/go-isatty` - TTY detection
- `github.com/dustin/go-humanize` - Human-readable formatting
- `github.com/spf13/cobra` - CLI framework
- `github.com/streamingfast/cli/sflags` - Flag utilities

### New Required

- `cloud.google.com/go/logging/logadmin` - Cloud Logging read API

## Testing Strategy

1. **Manual Testing**
   - Test against actual GCP project with Substreams logs
   - Verify table output formatting in TTY and non-TTY modes
   - Test with various time ranges
   - Test with namespace filtering

2. **Future Unit Tests** (not required for initial implementation)
   - Test time parsing functions
   - Test connection correlation logic
   - Test table formatting with mock data
   - Mock backend for command tests

## Technical Notes

### GCP Authentication

The GCP backend will use Application Default Credentials (ADC), same as the existing GCS code in `store_size_helper.go`. Users must have:
- `roles/logging.viewer` or equivalent on the GCP project
- Valid ADC via `gcloud auth application-default login` or service account

### Cloud Logging Query Optimization

- Use `NewestFirst()` option to get most recent logs first
- Use narrow timestamp filter to reduce scan scope
- Consider adding `PageSize` for pagination control if needed

### jsonPayload Field Access

Cloud Logging entries have `Payload` as `interface{}`. For structured logs, this will be `map[string]interface{}`. Need to handle:
- Type assertions safely
- Missing fields (some logs may not have all fields)
- Numeric types (may come as float64 from JSON)

### Example jsonPayload Structure (expected)

```json
{
  "message": "incoming Substreams Blocks request",
  "trace_id": "384bfd566dad2578b1f933d0983faec7",
  "user_id": "sfinfra",
  "ip_address": "34.57.244.33",
  "output_module": "map_blocks",
  "output_module_hash": "d3b1920483180cbcd2fd10abcabbee431146f4c8",
  "start_block": -1,
  "stop_block": 0,
  "production_mode": false
}
```

```json
{
  "message": "substreams request stats",
  "tier": "tier1",
  "trace_id": "384bfd566dad2578b1f933d0983faec7",
  "user_id": "sfinfra",
  "total_blocks_processed": 2,
  "block_rate_per_sec": "2.000",
  "time_to_first_data": 0.189361067,
  "resolved_start_block": 147657693,
  "error": "context canceled"
}
```

## References

- Specification: `specs/substreams-logs-connections.md`
- Similar command pattern: `cmd/tools/substreams/store_size.go`
- GCP Cloud Logging docs: https://pkg.go.dev/cloud.google.com/go/logging/logadmin
- Cloud Logging query syntax: https://cloud.google.com/logging/docs/view/logging-query-language
