package logs

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildFilterAllMessages(t *testing.T) {
	start := time.Date(2026, 2, 18, 5, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	t.Run("message restriction is dropped", func(t *testing.T) {
		f := BuildFilter(QueryOptions{
			TraceID:     "abc123",
			StartTime:   start,
			EndTime:     end,
			AllMessages: true,
		})

		assert.Contains(t, f, `SEARCH("abc123")`)
		assert.NotContains(t, f, "jsonPayload.message")
		assert.Contains(t, f, `timestamp >= "2026-02-18T05:00:00Z"`)
		assert.Contains(t, f, `timestamp <= "2026-02-18T06:00:00Z"`)
	})

	t.Run("message restriction is kept by default", func(t *testing.T) {
		f := BuildFilter(QueryOptions{TraceID: "abc123", StartTime: start, EndTime: end})

		assert.Contains(t, f, `jsonPayload.message="incoming Substreams Blocks request"`)
	})
}

func TestConsoleURL(t *testing.T) {
	opts := QueryOptions{
		TraceID:     "abc123",
		StartTime:   time.Date(2026, 2, 18, 5, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 2, 18, 6, 0, 0, 0, time.UTC),
		AllMessages: true,
	}

	got := ConsoleURL("my-project", opts)

	require.True(t, strings.HasPrefix(got, "https://console.cloud.google.com/logs/query;query="), got)
	assert.True(t, strings.HasSuffix(got, "?project=my-project"), got)
	assert.NotContains(t, got, "+", "spaces must be percent-encoded, not turned into '+'")

	// The query segment must decode back to the exact filter we would query with
	querySegment := strings.TrimSuffix(strings.TrimPrefix(got, "https://console.cloud.google.com/logs/query;query="), "?project=my-project")
	encodedQuery, encodedTimeRange, found := strings.Cut(querySegment, ";timeRange=")
	require.True(t, found, "URL must carry a timeRange segment: %s", got)

	decodedQuery, err := url.QueryUnescape(encodedQuery)
	require.NoError(t, err)
	assert.Equal(t, BuildFilter(opts), decodedQuery)

	decodedTimeRange, err := url.QueryUnescape(encodedTimeRange)
	require.NoError(t, err)
	assert.Equal(t, "2026-02-18T05:00:00Z/2026-02-18T06:00:00Z", decodedTimeRange)
}
