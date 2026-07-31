package logs

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ConsoleURL returns a Google Cloud Logging "Logs Explorer" URL that opens the
// browser on the exact same entries the given query options match, so the user
// can browse the actual logs interactively.
//
// The Logs Explorer takes its parameters as `;key=value` path segments, each
// percent-encoded, with the project passed as a regular query parameter.
//
// See https://cloud.google.com/logging/docs/view/logs-explorer-interface
func ConsoleURL(projectID string, opts QueryOptions) string {
	timeRange := fmt.Sprintf("%s/%s",
		opts.StartTime.UTC().Format(time.RFC3339),
		opts.EndTime.UTC().Format(time.RFC3339),
	)

	return fmt.Sprintf("https://console.cloud.google.com/logs/query;query=%s;timeRange=%s?project=%s",
		encodePathParam(BuildFilter(opts)),
		encodePathParam(timeRange),
		url.QueryEscape(projectID),
	)
}

// encodePathParam percent-encodes a value for use in a `;key=value` path
// segment. url.QueryEscape encodes spaces as `+`, which is only decoded as a
// space in query strings, so they are re-encoded as `%20`.
func encodePathParam(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
