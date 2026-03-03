package substreams

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormatModuleWithHash(t *testing.T) {
	tests := []struct {
		name       string
		moduleName string
		hash       string
		want       string
	}{
		{"no hash", "map_blocks", "", "map_blocks"},
		{"short hash", "map_blocks", "abc", "map_blocks (abc)"},
		{"exact 6 chars", "map_blocks", "abc123", "map_blocks (abc123)"},
		{"long hash", "map_blocks", "d3b1920483180cbcd2fd10abcabbee431146f4c8", "map_blocks (d3b192...)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatModuleWithHash(tt.moduleName, tt.hash)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractNetwork(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		want      string
	}{
		{"empty", "", "-"},
		{"simple", "eth-mainnet", "eth-mainnet"},
		{"with full suffix", "eth-mainnet-full", "eth-mainnet"},
		{"with tier1 suffix", "opt-mainnet-tier1", "opt-mainnet"},
		{"with tier2 suffix", "arb-mainnet-tier2", "arb-mainnet"},
		{"with archive suffix", "eth-goerli-archive", "eth-goerli"},
		{"no suffix match", "some-namespace-other", "some-namespace-other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractNetwork(tt.namespace)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{"milliseconds", 500 * time.Millisecond, "500ms"},
		{"1 second", time.Second, "1.0s"},
		{"30 seconds", 30 * time.Second, "30.0s"},
		{"1 minute", time.Minute, "1m0s"},
		{"1 minute 30 seconds", time.Minute + 30*time.Second, "1m30s"},
		{"1 hour", time.Hour, "1h0m"},
		{"1 hour 30 minutes", time.Hour + 30*time.Minute, "1h30m"},
		{"2 hours 15 minutes", 2*time.Hour + 15*time.Minute, "2h15m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.duration)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"needs truncation", "hello world", 8, "hello..."},
		{"very short max", "hello", 2, "he"},
		{"max 3", "hello", 3, "hel"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateString(tt.input, tt.maxLen)
			assert.Equal(t, tt.want, got)
		})
	}
}
