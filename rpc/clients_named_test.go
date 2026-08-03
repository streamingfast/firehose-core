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
