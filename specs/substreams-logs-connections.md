# Specification: firecore tools substreams logs connections

## Overview

This command queries Cloud Logging to find Substreams connection logs for a specific organization, correlates them by trace ID, and presents a summary table showing active and closed connections.

## Command Structure

```
firecore tools substreams logs connections <user_id> [flags]
```

### Arguments

| Argument | Required | Description |
|----------|----------|-------------|
| `user_id` | Yes | The organization ID to filter connections for (maps to `jsonPayload.user_id`) |

### Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--backend` | | `gcp` | Log backend to use (currently only `gcp` supported) |
| `--since` | | | Look back duration (e.g., "1h", "30m", "2d"). Mutually exclusive with `--date-range` |
| `--date-range` | | | Date range in format `<start>-[<end>]`. End defaults to "now" if not specified. Mutually exclusive with `--since` |
| `-n`, `--k8s-namespace` | `-n` | | Kubernetes namespace to filter logs (maps to `resource.labels.namespace_name`) |
| `--gcp-project` | | | GCP project ID to query logs from. Required when using GCP backend |

## Log Message Types

The command queries for two specific log messages:

### 1. Incoming Request Log

- **Message**: `"incoming Substreams Blocks request"`
- **Purpose**: Marks the start of a connection

**jsonPayload fields to extract**:
- `trace_id` - Unique identifier for correlating request/stats
- `user_id` - User identifier
- `ip_address` - Client IP address
- `output_module` - Output module name
- `output_module_hash` - Module hash
- `start_block` - Requested start block
- `stop_block` - Requested stop block (0 means open-ended)
- `production_mode` - Whether production mode is enabled

### 2. Request Stats Log (Tier1 only)

- **Message**: `"substreams request stats"`
- **Additional Filter**: `jsonPayload.tier = "tier1"`
- **Purpose**: Marks the end of a connection with statistics

**jsonPayload fields to extract**:
- `trace_id` - For correlation with incoming request
- `error` - Error message if request failed
- `total_blocks_processed` - Total blocks processed
- `block_rate_per_sec` - Processing rate
- `time_to_first_data` - Latency to first data (in human duration format)
- `resolved_start_block` - Actual start block resolved

### Resource Labels (from GCP envelope)

- `resource.labels.namespace_name` - Kubernetes namespace (used to infer network)
- `resource.labels.cluster_name` - Kubernetes cluster name
- `resource.labels.pod_name` - Pod name that handled the request

## Connection States

| State | Condition | Description |
|-------|-----------|-------------|
| `active` | Has incoming request, no stats log | Connection still in progress |
| `closed` | Has both incoming request and stats log | Connection completed |
| `error` | Closed with non-empty error field | Connection failed with error |

## Output Format

A compact table showing connections sorted by timestamp (newest first):

```
Connections for organization: sfinfra
Namespace: eth-mainnet-tier1
Time range: 2024-01-15 10:00:00 - 2024-01-15 12:00:00 (2h)

STATUS   TRACE_ID                          NETWORK      SOURCE_IP        MODULE                      DURATION  BLOCKS  ERROR
active   384bfd566dad2578b1f933d0983faec7  eth-mainnet  192.168.1.1      map_transfers (f3a8b2...)   5m32s     -       -
closed   abc123def456789012345678abcdef01  eth-mainnet  10.0.0.5         store_balances (c1d2e3...)  2m15s     15420   -
error    def456789abcdef0123456789abcdef0  eth-mainnet  192.168.1.1      map_events (a9b8c7...)      0s        0       deadline exceeded

Total: 3 connections (1 active, 1 closed, 1 error)

Warning: 2 orphaned stats logs found (stats without matching incoming request)
```

### Column Details

| Column | Description | Format |
|--------|-------------|--------|
| STATUS | Connection status | `active`, `closed`, `error` |
| TRACE_ID | Full trace ID | Full 32-character hex string |
| NETWORK | Inferred from namespace | Extract network name from namespace |
| SOURCE_IP | Client IP address | IPv4 or IPv6 |
| MODULE | Output module with hash | `<module_name> (<first_6_chars_hash>...)` |
| DURATION | Time since start or total duration | Human format (e.g., "5m32s") |
| BLOCKS | Blocks processed | Integer or `-` if active |
| ERROR | Error message | Truncated to ~30 chars or `-` |

## Architecture

### Backend Interface

```go
// LogBackend abstracts the log querying mechanism
type LogBackend interface {
    // QueryConnections returns all connections matching the criteria
    QueryConnections(ctx context.Context, opts QueryOptions) ([]ConnectionLog, error)
}

type QueryOptions struct {
    UserID       string
    Namespace    string
    StartTime    time.Time
    EndTime      time.Time
}
```

### Data Structures

```go
// ConnectionLog represents a single connection (may be partial if stats not yet received)
type ConnectionLog struct {
    // From incoming request
    TraceID        string
    UserID         string
    IPAddress      string
    OutputModule   string
    OutputModuleHash string
    StartBlock     uint64
    StopBlock      uint64
    ProductionMode bool
    Timestamp      time.Time

    // From resource labels
    Namespace      string
    ClusterName    string
    PodName        string

    // From stats (nil if not yet received)
    Stats          *ConnectionStats
}

type ConnectionStats struct {
    TotalBlocksProcessed uint64
    BlockRatePerSec      float64
    TimeToFirstData      time.Duration
    ResolvedStartBlock   uint64
    Error                string
    EndTimestamp         time.Time
}
```

## GCP Cloud Logging Query

The GCP backend should construct queries like:

```
resource.type="k8s_container"
jsonPayload.user_id="<user_id>"
(
  jsonPayload.message="incoming Substreams Blocks request" OR
  (jsonPayload.message="substreams request stats" AND jsonPayload.tier="tier1")
)
timestamp >= "<start_time>"
timestamp <= "<end_time>"
```

If `--k8s-namespace` is provided, add:
```
resource.labels.namespace_name="<namespace>"
```

## Error Handling

- If `--since` and `--date-range` are both provided, return an error
- If neither `--since` nor `--date-range` is provided, default to `--since 1h`
- If GCP project is not provided and cannot be auto-detected, return an error with guidance
- Handle pagination for large result sets (Cloud Logging has limits)
- Handle rate limiting gracefully

## Orphaned Stats Logs

Stats logs without a matching incoming request (orphaned) can occur due to:
- Log rotation or retention policies
- Query time window not capturing the incoming request
- Incomplete data

**Handling**:
- Collect all orphaned stats logs during correlation
- Display a warning at the end of the CLI output: `Warning: N orphaned stats logs found (stats without matching incoming request)`
- Do not display orphaned stats in the main table (they lack essential fields like ip_address, start_block, etc.)

## Dependencies

New dependencies required:
- `cloud.google.com/go/logging/logadmin` - For reading Cloud Logging entries

Existing dependencies to leverage:
- `github.com/charmbracelet/lipgloss` - For styled terminal output (already used in store_size.go)
- `github.com/mattn/go-isatty` - For TTY detection (already used)
- `github.com/dustin/go-humanize` - For human-readable formatting (already available)

## File Structure

```
cmd/tools/substreams/
    tools_substreams.go       # Existing - add new command registration
    store_size.go             # Existing
    store_size_helper.go      # Existing
    logs/                     # New directory for logs commands
        logs.go               # Parent command "logs"
        connections.go        # Main "connections" command implementation
        backend.go            # Backend interface definition
        gcp_backend.go        # GCP Cloud Logging implementation
        types.go              # Connection and stats data structures
        table.go              # Table formatting utilities
```

## Future Considerations

- Additional backends (e.g., Loki, Elasticsearch)
- JSON output format (`--output json`)
- CSV export (`--output csv`)
- Real-time streaming mode (`--follow`)
- Filter by module name (`--module`)
- Filter by error status (`--errors-only`)
