package relayer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSourceAddress(t *testing.T) {
	t.Setenv("TEST_RELAYER_SECRET", "s3cr3t")

	tests := []struct {
		name      string
		in        string
		want      SourceAddr
		expectErr bool
	}{
		{"plain", ":10010", SourceAddr{URL: ":10010"}, false},
		{"secret", ":10010?secret=abc", SourceAddr{URL: ":10010", SecretKey: "abc"}, false},
		{"retry", "my.source:12345?retry_interval=120s", SourceAddr{URL: "my.source:12345", RetryInterval: 120 * time.Second}, false},
		{"secret and retry", ":10010?secret=${TEST_RELAYER_SECRET}&retry_interval=2m", SourceAddr{URL: ":10010", SecretKey: "s3cr3t", RetryInterval: 2 * time.Minute}, false},
		{"invalid retry", ":10010?retry_interval=soon", SourceAddr{}, true},
		{"negative retry", ":10010?retry_interval=-5s", SourceAddr{}, true},
		{"zero retry", ":10010?retry_interval=0s", SourceAddr{}, true},
		{"retry below minimum", ":10010?retry_interval=4s", SourceAddr{}, true},
		{"retry at minimum", ":10010?retry_interval=5s", SourceAddr{URL: ":10010", RetryInterval: 5 * time.Second}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSourceAddress(tt.in)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
