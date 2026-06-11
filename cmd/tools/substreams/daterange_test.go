package substreams

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLogDateRange(t *testing.T) {
	now := time.Now()

	t.Run("relative duration single arg", func(t *testing.T) {
		start, end, err := ParseLogDateRange([]string{"2h"})
		require.NoError(t, err)
		assert.WithinDuration(t, now, end, 2*time.Second)
		assert.WithinDuration(t, now.Add(-2*time.Hour), start, 2*time.Second)
	})

	t.Run("relative duration with 'ago' suffix", func(t *testing.T) {
		start, end, err := ParseLogDateRange([]string{"1 day ago"})
		require.NoError(t, err)
		assert.WithinDuration(t, now, end, 2*time.Second)
		assert.WithinDuration(t, now.Add(-24*time.Hour), start, 2*time.Second)
	})

	t.Run("slash-separated range", func(t *testing.T) {
		start, end, err := ParseLogDateRange([]string{"2024-01-15T10:00:00Z/2024-01-15T12:00:00Z"})
		require.NoError(t, err)
		expectedStart, _ := time.Parse(time.RFC3339, "2024-01-15T10:00:00Z")
		expectedEnd, _ := time.Parse(time.RFC3339, "2024-01-15T12:00:00Z")
		assert.Equal(t, expectedStart, start)
		assert.Equal(t, expectedEnd, end)
	})

	t.Run("colon-separated range", func(t *testing.T) {
		start, end, err := ParseLogDateRange([]string{"2024-01-15T10:00:00Z:2024-01-15T12:00:00Z"})
		require.NoError(t, err)
		expectedStart, _ := time.Parse(time.RFC3339, "2024-01-15T10:00:00Z")
		expectedEnd, _ := time.Parse(time.RFC3339, "2024-01-15T12:00:00Z")
		assert.Equal(t, expectedStart, start)
		assert.Equal(t, expectedEnd, end)
	})

	t.Run("two args", func(t *testing.T) {
		start, end, err := ParseLogDateRange([]string{"2024-01-15T10:00:00Z", "2024-01-15T12:00:00Z"})
		require.NoError(t, err)
		expectedStart, _ := time.Parse(time.RFC3339, "2024-01-15T10:00:00Z")
		expectedEnd, _ := time.Parse(time.RFC3339, "2024-01-15T12:00:00Z")
		assert.Equal(t, expectedStart, start)
		assert.Equal(t, expectedEnd, end)
	})

	t.Run("end before start fails", func(t *testing.T) {
		_, _, err := ParseLogDateRange([]string{"2024-01-15T12:00:00Z/2024-01-15T10:00:00Z"})
		require.Error(t, err)
	})

	t.Run("zero args fails", func(t *testing.T) {
		_, _, err := ParseLogDateRange(nil)
		require.Error(t, err)
	})

	t.Run("future single timestamp fails", func(t *testing.T) {
		future := now.Add(24 * time.Hour).Format(time.RFC3339)
		_, _, err := ParseLogDateRange([]string{future})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "future")
	})
}

func TestParseRelativeDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
		ok    bool
	}{
		{"1d", 24 * time.Hour, true},
		{"2hr", 2 * time.Hour, true},
		{"30m", 30 * time.Minute, true},
		{"1 day ago", 24 * time.Hour, true},
		{"2 hours ago", 2 * time.Hour, true},
		{"0d", 0, false}, // zero is rejected
		{"abc", 0, false},
		{"", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, ok := parseRelativeDuration(tt.input)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}
