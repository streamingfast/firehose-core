package logs

import (
	"testing"
	"time"

	"cloud.google.com/go/logging"
	"github.com/stretchr/testify/assert"
)

func TestEscapeFilterValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain hex trace id", "bfb0980c436f3fd6f5564a31311d583f", "bfb0980c436f3fd6f5564a31311d583f"},
		{"value with double quote", `foo"bar`, `foo\"bar`},
		{"value with backslash", `foo\bar`, `foo\\bar`},
		{"backslash before quote", `\"`, `\\\"`},
		{"newline stripped", "foo\nbar", "foobar"},
		{"carriage return stripped", "foo\rbar", "foobar"},
		{"tab stripped", "foo\tbar", "foobar"},
		{"break-out attempt is neutralized", `evil"
jsonPayload.message="X`, `evil\"jsonPayload.message=\"X`},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, escapeFilterValue(tt.in))
		})
	}
}

func TestBuildFilterEscapesValues(t *testing.T) {
	start := time.Date(2026, 2, 18, 5, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	t.Run("trace id with quote/backslash is escaped", func(t *testing.T) {
		f := BuildFilter(QueryOptions{
			TraceID:   `evil"\`,
			StartTime: start,
			EndTime:   end,
		})
		assert.Contains(t, f, `SEARCH("evil\"\\")`)
	})

	t.Run("user id with quote is escaped", func(t *testing.T) {
		f := BuildFilter(QueryOptions{
			UserID:    `org"injected`,
			StartTime: start,
			EndTime:   end,
		})
		assert.Contains(t, f, `jsonPayload.user_id="org\"injected"`)
	})

	t.Run("namespace with quote is escaped", func(t *testing.T) {
		f := BuildFilter(QueryOptions{
			UserID:    "sfinfra",
			Namespace: `evil"ns`,
			StartTime: start,
			EndTime:   end,
		})
		assert.Contains(t, f, `resource.labels.namespace_name="evil\"ns"`)
	})

	t.Run("trace id wins over user id", func(t *testing.T) {
		f := BuildFilter(QueryOptions{
			TraceID:   "abc123",
			UserID:    "should-be-ignored",
			StartTime: start,
			EndTime:   end,
		})
		assert.Contains(t, f, `SEARCH("abc123")`)
		assert.NotContains(t, f, "should-be-ignored")
	})
}

func TestSeverityString(t *testing.T) {
	tests := []struct {
		name string
		in   logging.Severity
		want string
	}{
		{"default is empty", logging.Default, ""},
		{"info is upper-cased", logging.Info, "INFO"},
		{"warning is upper-cased", logging.Warning, "WARNING"},
		{"error is upper-cased", logging.Error, "ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, severityString(tt.in))
		})
	}
}
