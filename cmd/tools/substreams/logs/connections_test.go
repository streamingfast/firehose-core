package logs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"1 hour", "1h", time.Hour, false},
		{"30 minutes", "30m", 30 * time.Minute, false},
		{"90 seconds", "90s", 90 * time.Second, false},
		{"1.5 hours", "1h30m", time.Hour + 30*time.Minute, false},
		{"1 day", "1d", 24 * time.Hour, false},
		{"2 days", "2d", 48 * time.Hour, false},
		{"7 days", "7d", 7 * 24 * time.Hour, false},
		{"invalid", "abc", 0, true},
		{"empty", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDuration(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestParseTimeRange(t *testing.T) {
	now := time.Now()

	t.Run("default is 1 hour", func(t *testing.T) {
		start, end, err := parseTimeRange("", "")
		require.NoError(t, err)
		assert.WithinDuration(t, now, end, time.Second)
		assert.WithinDuration(t, now.Add(-time.Hour), start, time.Second)
	})

	t.Run("since 30m", func(t *testing.T) {
		start, end, err := parseTimeRange("30m", "")
		require.NoError(t, err)
		assert.WithinDuration(t, now, end, time.Second)
		assert.WithinDuration(t, now.Add(-30*time.Minute), start, time.Second)
	})

	t.Run("since 2d", func(t *testing.T) {
		start, end, err := parseTimeRange("2d", "")
		require.NoError(t, err)
		assert.WithinDuration(t, now, end, time.Second)
		assert.WithinDuration(t, now.Add(-48*time.Hour), start, time.Second)
	})
}

func TestParseDateRange(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantStart string
		wantEnd   string
		wantErr   bool
	}{
		{
			name:      "full range RFC3339",
			input:     "2024-01-15T10:00:00Z-2024-01-15T12:00:00Z",
			wantStart: "2024-01-15T10:00:00Z",
			wantEnd:   "2024-01-15T12:00:00Z",
			wantErr:   false,
		},
		{
			name:      "start only with trailing dash",
			input:     "2024-01-15T10:00:00Z-",
			wantStart: "2024-01-15T10:00:00Z",
			wantEnd:   "", // Will be "now"
			wantErr:   false,
		},
		{
			name:      "start only no trailing dash",
			input:     "2024-01-15T10:00:00Z",
			wantStart: "2024-01-15T10:00:00Z",
			wantEnd:   "", // Will be "now"
			wantErr:   false,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "end before start",
			input:   "2024-01-15T12:00:00Z-2024-01-15T10:00:00Z",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, err := parseDateRange(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			expectedStart, _ := time.Parse(time.RFC3339, tt.wantStart)
			assert.Equal(t, expectedStart, start)

			if tt.wantEnd != "" {
				expectedEnd, _ := time.Parse(time.RFC3339, tt.wantEnd)
				assert.Equal(t, expectedEnd, end)
			} else {
				// End should be close to now
				assert.WithinDuration(t, time.Now(), end, time.Second)
			}
		})
	}
}

func TestSplitDateRange(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "full range",
			input: "2024-01-15T10:00:00Z-2024-01-15T12:00:00Z",
			want:  []string{"2024-01-15T10:00:00Z", "2024-01-15T12:00:00Z"},
		},
		{
			name:  "start only with trailing dash",
			input: "2024-01-15T10:00:00Z-",
			want:  []string{"2024-01-15T10:00:00Z", ""},
		},
		{
			name:  "start only no trailing dash",
			input: "2024-01-15T10:00:00Z",
			want:  []string{"2024-01-15T10:00:00Z"},
		},
		{
			name:  "simple date format",
			input: "2024-01-15-2024-01-16",
			want:  []string{"2024-01-15", "2024-01-16"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitDateRange(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseDateTime(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"RFC3339", "2024-01-15T10:00:00Z", "2024-01-15T10:00:00Z", false},
		{"RFC3339Nano", "2024-01-15T10:00:00.123456789Z", "2024-01-15T10:00:00.123456789Z", false},
		{"simple date", "2024-01-15", "2024-01-15T00:00:00Z", false},
		{"date with time no tz", "2024-01-15T10:00:00", "2024-01-15T10:00:00Z", false},
		{"invalid", "not-a-date", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDateTime(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			// For date-only input, it becomes midnight UTC
			if tt.input == "2024-01-15" {
				expected, _ := time.Parse("2006-01-02", "2024-01-15")
				assert.Equal(t, expected, got)
			} else if tt.input == "2024-01-15T10:00:00" {
				expected, _ := time.Parse("2006-01-02T15:04:05", "2024-01-15T10:00:00")
				assert.Equal(t, expected, got)
			}
		})
	}
}
