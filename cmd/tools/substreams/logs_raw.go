package substreams

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"github.com/streamingfast/firehose-core/cmd/tools/substreams/logs"
)

// rawLogSkippedFields are payload fields already rendered on their own by
// printRawLogs, or pure noise when reading a single request's logs
var rawLogSkippedFields = map[string]bool{
	"message":   true,
	"timestamp": true,
	"severity":  true,
	"level":     true,
	"logger":    true,
	"trace_id":  true,
}

const (
	// rawLogFieldIndent prefixes each field rendered under a message
	rawLogFieldIndent = "    "

	// rawLogTimeFormat is the local-time layout each log line starts with, the
	// timezone is kept so the lines can be matched against other tools
	rawLogTimeFormat = "2006-01-02 15:04:05.000 MST"

	// maxRawLogDurationSeconds is the value above which a duration-looking field
	// is left as-is, an epoch timestamp landing on such a key would otherwise be
	// rendered as a decades-long duration
	maxRawLogDurationSeconds = float64(10 * 365 * 24 * 60 * 60)
)

// rawLogField is a single payload field, ready to be rendered
type rawLogField struct {
	Key   string
	Value string
}

// printRawLogs renders log entries oldest first as
// `<time> <severity> <logger> <message>`, with the remaining payload fields
// listed one per line under it.
func printRawLogs(entries []logs.LogEntry) {
	if len(entries) == 0 {
		fmt.Println(stylex.Note("No log entry found for this trace ID in the given time range"))
		return
	}

	sorted := slices.Clone(entries)
	slices.SortStableFunc(sorted, func(a, b logs.LogEntry) int {
		return rawLogTime(a).Compare(rawLogTime(b))
	})

	for _, entry := range sorted {
		fmt.Printf("%s %s %s%s\n",
			rawLogTime(entry).Local().Format(rawLogTimeFormat),
			formatSeverity(entry.Severity),
			formatLogger(entry.Fields),
			entry.Message,
		)

		printRawLogFieldLines(rawLogFields(entry.Fields))
	}
}

// rawLogTime returns the entry's payload timestamp, falling back to the
// timestamp recorded by the backend when the payload carries none
func rawLogTime(entry logs.LogEntry) time.Time {
	if ts, ok := parseLogTimestamp(entry.Timestamp); ok {
		return ts
	}
	return entry.EntryTime
}

// formatSeverity renders the severity, colored by importance
func formatSeverity(severity string) string {
	if severity == "" {
		severity = "-"
	}

	switch severity {
	case "ERROR", "CRITICAL", "ALERT", "EMERGENCY":
		return stylex.Error(severity)
	case "WARNING":
		return stylex.Warn(severity)
	case "DEBUG":
		return stylex.Dim(severity)
	default:
		return stylex.Success(severity)
	}
}

// formatLogger renders the `logger` field between parenthesis ahead of the
// message, returning an empty string when the entry carries none
func formatLogger(fields map[string]any) string {
	logger, ok := fields["logger"].(string)
	if !ok || logger == "" {
		return ""
	}

	return stylex.LogLoggerf("(%s) ", logger)
}

// rawLogFields returns the payload fields left to render, sorted by key so
// successive runs are comparable
func rawLogFields(fields map[string]any) []rawLogField {
	if len(fields) == 0 {
		return nil
	}

	out := make([]rawLogField, 0, len(fields))
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		if rawLogSkippedFields[key] || strings.HasPrefix(key, "logging.googleapis.com/") {
			continue
		}

		out = append(out, rawLogField{Key: key, Value: formatRawLogFieldValue(key, fields[key])})
	}

	return out
}

// printRawLogFieldLines renders one `<key>: <value>` per line under the
// message, keys and values colored apart
func printRawLogFieldLines(fields []rawLogField) {
	for _, field := range fields {
		fmt.Printf("%s%s %s\n", rawLogFieldIndent, stylex.LogKeyf("%s:", field.Key), stylex.LogValue(field.Value))
	}
}

// formatRawLogFieldValue renders a payload value, rendering the numeric ones
// held by a duration-looking key as a human duration
func formatRawLogFieldValue(key string, value any) string {
	if seconds, ok := value.(float64); ok && isDurationKey(key) && seconds < maxRawLogDurationSeconds {
		return formatLogDuration(seconds)
	}

	return formatRawLogValue(value)
}

// isDurationKey reports whether the field is a duration expressed in seconds,
// keys carrying an explicit sub-second unit are left alone since they are not
// in seconds
func isDurationKey(key string) bool {
	for _, suffix := range []string{"_ms", "_us", "_ns", "_millis", "_milliseconds", "_micros", "_nanos"} {
		if strings.HasSuffix(key, suffix) {
			return false
		}
	}

	return strings.Contains(key, "duration") ||
		strings.Contains(key, "elapsed") ||
		strings.Contains(key, "latency") ||
		strings.HasPrefix(key, "time_to") ||
		strings.Contains(key, "_time_")
}

// formatLogDuration renders a duration expressed in seconds, keeping the
// sub-millisecond ones readable (`formatDuration` would floor them to `0ms`)
func formatLogDuration(seconds float64) string {
	duration := time.Duration(seconds * float64(time.Second))
	if duration > -time.Millisecond && duration < time.Millisecond {
		return duration.String()
	}

	return formatDuration(duration)
}

// formatRawLogValue renders a single payload value. Values are printed as-is,
// neither truncated nor quoted, so long payloads (module stats, errors) stay
// readable.
func formatRawLogValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return "null"
	default:
		if encoded, err := json.Marshal(v); err == nil {
			return string(encoded)
		}
		return fmt.Sprintf("%v", v)
	}
}
