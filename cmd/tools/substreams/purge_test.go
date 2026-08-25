package substreams

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestParseRetention(t *testing.T) {
	retention, err := parseRetention([]string{"default=30d", "pro:30d", "scaling=14d", "free=3d", "hourly=12h"})
	require.NoError(t, err)
	assert.Equal(t, map[string]time.Duration{
		"default": 30 * 24 * time.Hour,
		"pro":     30 * 24 * time.Hour,
		"scaling": 14 * 24 * time.Hour,
		"free":    3 * 24 * time.Hour,
		"hourly":  12 * time.Hour,
	}, retention)

	_, err = parseRetention([]string{"pro=30d"})
	require.ErrorContains(t, err, `must contain a "default" entry`)

	_, err = parseRetention([]string{"default=30d", "bogus"})
	require.ErrorContains(t, err, "expected 'plan=duration'")

	_, err = parseRetention([]string{"default=nope"})
	require.Error(t, err)
}

func TestPurgeConfigRetentionFor(t *testing.T) {
	cfg := &purgeConfig{retention: map[string]time.Duration{
		"default": 30 * 24 * time.Hour,
		"free":    3 * 24 * time.Hour,
	}}

	assert.Equal(t, 30*24*time.Hour, cfg.retentionFor("default"))
	assert.Equal(t, 3*24*time.Hour, cfg.retentionFor("free"))
	assert.Equal(t, 30*24*time.Hour, cfg.retentionFor("a-plan-nobody-configured"))
}

func TestPlanOfMarker(t *testing.T) {
	assert.Equal(t, "default", planOfMarker("net/substreams-states/hash/last_used.zst"))
	assert.Equal(t, "default", planOfMarker("net/substreams-states/hash/last_used"))
	assert.Equal(t, "pro", planOfMarker("net/substreams-states/hash/last_used_pro.zst"))
	assert.Equal(t, "scaling", planOfMarker("net/substreams-states/hash/last_used_scaling.zst"))
	assert.Equal(t, "some-new-plan", planOfMarker("net/substreams-states/hash/last_used_some-new-plan.zst"))
}

// By default nothing is spared: a condemned folder is emptied completely, spkg included.
func TestPurgeConfigIsKeptByDefault(t *testing.T) {
	cfg := &purgeConfig{}

	assert.False(t, cfg.isKept("net/substreams-states/hash/substreams.spkg.zst"))
	assert.False(t, cfg.isKept("net/substreams-states/hash/substreams.partial.spkg.zst"))
	assert.False(t, cfg.isKept("net/substreams-states/hash/last_used.zst"))
	assert.False(t, cfg.isKept("net/substreams-states/hash/outputs/0000123000-0000124000.output.zst"))
	assert.False(t, cfg.isKept("net/substreams-states/hash/states/0000124000-0000000000.kv.zst"))
}

func TestPurgeConfigIsKeptWithGlobs(t *testing.T) {
	cfg := &purgeConfig{keepGlobs: []string{"substreams.*.zst"}}

	assert.True(t, cfg.isKept("net/substreams-states/hash/substreams.spkg.zst"))
	assert.True(t, cfg.isKept("net/substreams-states/hash/substreams.partial.spkg.zst"))
	assert.False(t, cfg.isKept("net/substreams-states/hash/last_used.zst"))
	assert.False(t, cfg.isKept("net/substreams-states/hash/outputs/0000123000-0000124000.output.zst"))
}

// A folder survives as soon as ONE marker is within the retention of its own plan.
func TestMarkersKeepFolder(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	cfg := &purgeConfig{now: now, retention: map[string]time.Duration{
		"default": 30 * 24 * time.Hour,
		"pro":     30 * 24 * time.Hour,
		"scaling": 14 * 24 * time.Hour,
		"free":    3 * 24 * time.Hour,
	}}

	daysAgo := func(days int) time.Time { return now.AddDate(0, 0, -days).Truncate(24 * time.Hour) }

	keeps := func(markers ...marker) bool { return markersKeepFolder(cfg, markers) }

	assert.True(t, keeps(marker{"pro", daysAgo(15)}, marker{"free", daysAgo(15)}), "a 15 days old 'pro' use is within the 30d 'pro' retention")
	assert.True(t, keeps(marker{"scaling", daysAgo(13)}), "a 13 days old 'scaling' use is within the 14d 'scaling' retention")
	assert.False(t, keeps(marker{"free", daysAgo(4)}), "a 4 days old 'free' use is past the 3d 'free' retention")
	assert.True(t, keeps(marker{"free", daysAgo(2)}), "a 2 days old 'free' use is within the 3d 'free' retention")
	assert.False(t, keeps(marker{"scaling", daysAgo(20)}, marker{"free", daysAgo(10)}), "every plan past its own retention")
	assert.True(t, keeps(marker{"unknown-plan", daysAgo(20)}), "an unknown plan falls back to the 30d 'default' retention")
}

func TestParseRetentionDuration(t *testing.T) {
	tests := []struct {
		in       string
		expected time.Duration
	}{
		{"30d", 30 * 24 * time.Hour},
		{"0.5d", 12 * time.Hour},
		{"12h", 12 * time.Hour},
		{"90m", 90 * time.Minute},
	}

	for _, test := range tests {
		t.Run(test.in, func(t *testing.T) {
			out, err := parseRetentionDuration(test.in)
			require.NoError(t, err)
			assert.Equal(t, test.expected, out)
		})
	}

	_, err := parseRetentionDuration("0s")
	require.Error(t, err)
}

func TestNetworkResultFailed(t *testing.T) {
	assert.False(t, (&networkResult{network: "eth-mainnet", deletedFiles: 10}).failed())
	assert.False(t, (&networkResult{network: "eth-mainnet", skipped: true}).failed())
	assert.True(t, (&networkResult{network: "eth-mainnet", fatalErr: errors.New("boom")}).failed())
	assert.True(t, (&networkResult{network: "eth-mainnet", failedDeletes: 1}).failed())
	assert.True(t, (&networkResult{network: "eth-mainnet", listErrors: 1}).failed())
	assert.True(t, (&networkResult{network: "eth-mainnet", scanErrors: 1}).failed())
}

// A network blowing up must not hide the work the other networks did, but the command must
// still exit non-zero so an operator (or a cron) notices.
func TestReportPurgeResults(t *testing.T) {
	cfg := &purgeConfig{}

	err := reportPurgeResults([]*networkResult{
		{network: "eth-mainnet", deletedFiles: 12, purgedFolders: 2},
		{network: "sol-mainnet", deletedFiles: 3, purgedFolders: 1},
	}, cfg)
	require.NoError(t, err)

	err = reportPurgeResults([]*networkResult{
		{network: "eth-mainnet", deletedFiles: 12, purgedFolders: 2},
		{network: "sol-mainnet", fatalErr: errors.New("bucket unreachable")},
		{network: "base-mainnet", deletedFiles: 4, purgedFolders: 1, failedDeletes: 2},
	}, cfg)
	require.ErrorContains(t, err, "sol-mainnet")
	require.ErrorContains(t, err, "base-mainnet")
	require.NotContains(t, err.Error(), "eth-mainnet")
}

func TestWaitNextPass(t *testing.T) {
	logger := zap.NewNop()

	// A pass that outlasted the interval must not sleep at all.
	started := time.Now().Add(-2 * time.Hour)
	done := make(chan error, 1)
	go func() { done <- waitNextPass(context.Background(), started, time.Hour, logger) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("waitNextPass slept even though the pass outlasted the interval")
	}

	// A cancelled context aborts the wait instead of holding the daemon for a full interval.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- waitNextPass(ctx, time.Now(), time.Hour, logger) }()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("waitNextPass ignored the cancelled context")
	}
}
