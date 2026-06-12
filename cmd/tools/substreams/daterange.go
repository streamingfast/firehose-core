package substreams

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseLogDateRange parses 1 or 2 args into a [start, end) time range.
//
// One arg supports:
//   - relative duration ("1d", "2hr", "30m", "1 day ago")
//   - "<start>/<end>" or "<start>:<end>" range (the colon split scans
//     right-to-left so timezone-suffixed timestamps like "...10:00:00Z" work)
//   - a single ISO timestamp, treated as [timestamp, now)
//
// Two args treats arg[0] as start and arg[1] as end.
func ParseLogDateRange(args []string) (time.Time, time.Time, error) {
	now := time.Now()
	switch len(args) {
	case 1:
		return parseSingleLogDateArg(args[0], now)
	case 2:
		return parseTwoLogDateArgs(args[0], args[1], now)
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

func parseSingleLogDateArg(s string, now time.Time) (time.Time, time.Time, error) {
	// Relative duration: "1d", "2hr", "30m", "1 day ago", …
	if d, ok := parseRelativeDuration(s); ok {
		return now.Add(-d), now, nil
	}

	// Range with "/" separator
	if idx := strings.Index(s, "/"); idx >= 0 {
		return parseLogRangeParts(s[:idx], s[idx+1:], now)
	}

	// Range with ":" separator — try each ":" from right to left so we
	// find the inter-timestamp colon first (timestamps end with Z or ±hh:mm).
	if start, end, ok := tryColonRangeSplit(s); ok {
		return validateLogRange(start, end, now)
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

func parseTwoLogDateArgs(s1, s2 string, now time.Time) (time.Time, time.Time, error) {
	start, err := parseDateTime(strings.TrimSpace(s1))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing start %q: %w", s1, err)
	}
	end, err := parseDateTime(strings.TrimSpace(s2))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing end %q: %w", s2, err)
	}
	return validateLogRange(start, end, now)
}

func parseLogRangeParts(s1, s2 string, now time.Time) (time.Time, time.Time, error) {
	start, err := parseDateTime(strings.TrimSpace(s1))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing start %q: %w", s1, err)
	}
	end, err := parseDateTime(strings.TrimSpace(s2))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parsing end %q: %w", s2, err)
	}
	return validateLogRange(start, end, now)
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

func validateLogRange(start, end, now time.Time) (time.Time, time.Time, error) {
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
