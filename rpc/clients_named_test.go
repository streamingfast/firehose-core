package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestClientsNames(t *testing.T) {
	clients := NewClients(2*time.Second, NewStickyRollingStrategy[*rollClient](), zlogTest)
	clients.Add(&rollClient{name: "c.1"})
	clients.AddNamed(&rollClient{name: "c.2"}, "quicknode")
	clients.Add(&rollClient{name: "c.3"})

	assert.Equal(t, []string{"client-0", "quicknode", "client-2"}, clients.Names())
}

func TestClientsNamesSortedAlongClients(t *testing.T) {
	clients := NewClients(2*time.Second, NewStickyRollingStrategy[*rollClient](), zlogTest)
	clients.AddNamed(&rollClient{name: "c.1", sortValue: 100}, "primary")
	clients.AddNamed(&rollClient{name: "c.2", sortValue: 101}, "fallback")

	require.NoError(t, Sort(context.Background(), clients, &testSortFetcher{}, SortDirectionDescending))

	assert.Equal(t, []string{"fallback", "primary"}, clients.Names())
}

func TestWithClientsErrorAttribution(t *testing.T) {
	clients := NewClients(2*time.Second, NewStickyRollingStrategy[*rollClient](), zlogTest)
	clients.AddNamed(&rollClient{name: "c.1"}, "primary")
	clients.AddNamed(&rollClient{name: "c.2"}, "quicknode")

	_, err := WithClients(clients, func(_ context.Context, client *rollClient) (any, error) {
		return nil, fmt.Errorf("boom on %s", client.name)
	})

	require.ErrorIs(t, err, ErrorNoMoreClient)
	assert.Contains(t, err.Error(), `provider "primary": boom on c.1`)
	assert.Contains(t, err.Error(), `provider "quicknode": boom on c.2`)
}

func TestWithClientsErrorAttributionAutoNamed(t *testing.T) {
	clients := NewClients(2*time.Second, NewRollingStrategyAlwaysUseFirst[*rollClient](), zlogTest)
	clients.Add(&rollClient{name: "c.1"})
	clients.Add(&rollClient{name: "c.2"})

	_, err := WithClients(clients, func(_ context.Context, client *rollClient) (any, error) {
		return nil, fmt.Errorf("boom on %s", client.name)
	})

	require.ErrorIs(t, err, ErrorNoMoreClient)
	assert.Contains(t, err.Error(), `provider "client-0": boom on c.1`)
	assert.Contains(t, err.Error(), `provider "client-1": boom on c.2`)
}

func TestWithClientsLogsRoll(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)

	clients := NewClients(2*time.Second, NewStickyRollingStrategy[*rollClient](), zap.New(core))
	clients.AddNamed(&rollClient{name: "c.1"}, "primary")
	clients.AddNamed(&rollClient{name: "c.2"}, "quicknode")

	_, err := WithClients(clients, func(_ context.Context, client *rollClient) (any, error) {
		if client.name == "c.2" {
			return nil, nil
		}

		return nil, fmt.Errorf("next please")
	})
	require.NoError(t, err)

	rolls := logs.FilterMessage("rolling to next RPC provider").All()
	require.Len(t, rolls, 1)

	fields := rolls[0].ContextMap()
	assert.Equal(t, "primary", fields["from_provider"])
	assert.Equal(t, "quicknode", fields["to_provider"])
}

// TestWithClientsRollLogIsSampled guards against log spam: a pool using
// RollingStrategyAlwaysUseFirst whose first provider is down rolls on every single
// call, we must not emit a warning each time.
func TestWithClientsRollLogIsSampled(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)

	clients := NewClients(2*time.Second, NewRollingStrategyAlwaysUseFirst[*rollClient](), zap.New(core))
	clients.AddNamed(&rollClient{name: "c.1"}, "primary")
	clients.AddNamed(&rollClient{name: "c.2"}, "quicknode")

	downPrimary := func(_ context.Context, client *rollClient) (any, error) {
		if client.name == "c.1" {
			return nil, fmt.Errorf("connection refused")
		}

		return nil, nil
	}

	for range 5 {
		_, err := WithClients(clients, downPrimary)
		require.NoError(t, err)
	}

	rolls := logs.FilterMessage("rolling to next RPC provider").All()
	require.Len(t, filterLevel(rolls, zapcore.WarnLevel), 1, "the 5 rolls of the same primary -> fallback pair must be sampled down to a single warning")

	// The sampled out ones are still there at debug level for whoever wants them.
	assert.Len(t, filterLevel(rolls, zapcore.DebugLevel), 4)

	// Once `rollLogInterval` elapsed, the roll warns again.
	clients.rollLoggedAt["primary -> quicknode"] = time.Now().Add(-rollLogInterval - time.Second)

	_, err := WithClients(clients, downPrimary)
	require.NoError(t, err)

	warns := filterLevel(logs.FilterMessage("rolling to next RPC provider").All(), zapcore.WarnLevel)
	assert.Len(t, warns, 2)
}

// TestWithClientsRollLogPerProviderPair ensures the sampling is per `from -> to`
// pair, rolling to a different provider must not be swallowed by an earlier roll.
func TestWithClientsRollLogPerProviderPair(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)

	clients := NewClients(2*time.Second, NewRollingStrategyAlwaysUseFirst[*rollClient](), zap.New(core))
	clients.AddNamed(&rollClient{name: "c.1"}, "primary")
	clients.AddNamed(&rollClient{name: "c.2"}, "quicknode")
	clients.AddNamed(&rollClient{name: "c.3"}, "last-resort")

	_, err := WithClients(clients, func(_ context.Context, client *rollClient) (any, error) {
		if client.name == "c.3" {
			return nil, nil
		}

		return nil, fmt.Errorf("boom")
	})
	require.NoError(t, err)

	var pairs [][2]string
	for _, roll := range logs.FilterMessage("rolling to next RPC provider").All() {
		fields := roll.ContextMap()
		pairs = append(pairs, [2]string{fields["from_provider"].(string), fields["to_provider"].(string)})
	}

	assert.Equal(t, [][2]string{{"primary", "quicknode"}, {"quicknode", "last-resort"}}, pairs)
}

func filterLevel(entries []observer.LoggedEntry, level zapcore.Level) []observer.LoggedEntry {
	var out []observer.LoggedEntry
	for _, entry := range entries {
		if entry.Level == level {
			out = append(out, entry)
		}
	}

	return out
}

// TestStickyRollingStrategyResetFailback ensures that a sticky strategy which rolled
// to the fallback provider after a transient error goes back to the declared-order
// primary once Clients.Reset is called.
func TestStickyRollingStrategyResetFailback(t *testing.T) {
	clients := NewClients(2*time.Second, NewStickyRollingStrategy[*rollClient](), zlogTest)
	clients.AddNamed(&rollClient{name: "c.1"}, "primary")
	clients.AddNamed(&rollClient{name: "c.2"}, "fallback")
	clients.AddNamed(&rollClient{name: "c.3"}, "last-resort")

	// Primary fails once, we roll to the fallback which answers.
	names := firstProviderNames(t, clients, func(client *rollClient) error {
		if client.name == "c.1" {
			return fmt.Errorf("transient")
		}
		return nil
	})
	assert.Equal(t, []string{"c.1", "c.2"}, names)

	// Sticky: without a reset we stay on the fallback, the primary is never retried.
	names = firstProviderNames(t, clients, func(*rollClient) error { return nil })
	assert.Equal(t, []string{"c.2"}, names)

	clients.Reset()

	// Failback: the declared-order primary is used again.
	names = firstProviderNames(t, clients, func(*rollClient) error { return nil })
	assert.Equal(t, []string{"c.1"}, names)
}

func TestRollingStrategyAlwaysUseFirstResetFailback(t *testing.T) {
	clients := NewClients(2*time.Second, NewRollingStrategyAlwaysUseFirst[*rollClient](), zlogTest)
	clients.AddNamed(&rollClient{name: "c.1"}, "primary")
	clients.AddNamed(&rollClient{name: "c.2"}, "fallback")

	// Roll away from the primary through the strategy directly, `WithClients` resets
	// the strategy on entry so it would hide the failback we want to assert.
	_, index, err := clients.rollingStrategy.next(clients)
	require.NoError(t, err)
	require.Equal(t, 0, index)

	_, index, err = clients.rollingStrategy.next(clients)
	require.NoError(t, err)
	require.Equal(t, 1, index)

	clients.Reset()

	client, index, err := clients.rollingStrategy.next(clients)
	require.NoError(t, err)
	assert.Equal(t, 0, index)
	assert.Equal(t, "c.1", client.name)
}

// firstProviderNames runs `f` through the clients pool, recording the name of each
// client that was tried, in order.
func firstProviderNames(t *testing.T, clients *Clients[*rollClient], f func(client *rollClient) error) []string {
	t.Helper()

	var tried []string
	_, err := WithClients(clients, func(_ context.Context, client *rollClient) (any, error) {
		tried = append(tried, client.name)
		return nil, f(client)
	})
	require.NoError(t, err)

	return tried
}
