package rpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type rollClient struct {
	callCount int
	name      string
	sortValue uint64
	pool      *Clients[*rollClient] // only set by tests that reach back into the pool
}

func (r *rollClient) fetchSortValue(_ context.Context) (sortValue uint64, err error) {
	return r.sortValue, nil
}

func TestStickyRollingStrategy(t *testing.T) {

	rollingStrategy := NewStickyRollingStrategy[*rollClient]()
	rollingStrategy.reset()

	clients := NewClients(2*time.Second, rollingStrategy, zlogTest)
	clients.Add(&rollClient{name: "c.1"})
	clients.Add(&rollClient{name: "c.2"})
	clients.Add(&rollClient{name: "c.3"})
	clients.Add(&rollClient{name: "c.a"})
	clients.Add(&rollClient{name: "c.b"})

	var clientNames []string
	_, err := WithClients(clients, func(ctx context.Context, client *rollClient) (v any, err error) {
		clientNames = append(clientNames, client.name)
		if client.name == "c.3" {
			return nil, nil
		}

		return nil, fmt.Errorf("next please")
	})

	require.NoError(t, err)
	//require.ErrorIs(t, err, ErrorNoMoreClient)
	require.Equal(t, []string{"c.1", "c.2", "c.3"}, clientNames)

	_, err = WithClients(clients, func(ctx context.Context, client *rollClient) (v any, err error) {
		clientNames = append(clientNames, client.name)
		return nil, fmt.Errorf("next please")
	})

	require.ErrorIs(t, err, ErrorNoMoreClient)
	require.Equal(t, []string{"c.1", "c.2", "c.3", "c.3", "c.a", "c.b", "c.1", "c.2"}, clientNames)

}

func TestRollingStrategySequentialWithSpreadStart_startsOnDifferentClientEachCall(t *testing.T) {
	rollingStrategy := NewRollingStrategySequential[*rollClient](WithSpreadStart())

	clients := NewClients(2*time.Second, rollingStrategy, zlogTest)
	clients.Add(&rollClient{name: "c.1"})
	clients.Add(&rollClient{name: "c.2"})
	clients.Add(&rollClient{name: "c.3"})

	for _, want := range []string{"c.1", "c.2", "c.3", "c.1"} {
		var got string
		_, err := WithClients(clients, func(ctx context.Context, client *rollClient) (v any, err error) {
			got = client.name
			return nil, nil
		})

		require.NoError(t, err)
		require.Equal(t, want, got)
	}
}

func TestRollingStrategySequentialWithSpreadStart_failoverWrapsFromStart(t *testing.T) {
	rollingStrategy := NewRollingStrategySequential[*rollClient](WithSpreadStart())

	clients := NewClients(2*time.Second, rollingStrategy, zlogTest)
	clients.Add(&rollClient{name: "c.1"})
	clients.Add(&rollClient{name: "c.2"})
	clients.Add(&rollClient{name: "c.3"})

	// consume the first call's start (c.1) so the next call starts at c.2
	_, err := WithClients(clients, func(ctx context.Context, client *rollClient) (v any, err error) {
		return nil, nil
	})
	require.NoError(t, err)

	var clientNames []string
	_, err = WithClients(clients, func(ctx context.Context, client *rollClient) (v any, err error) {
		clientNames = append(clientNames, client.name)
		if client.name == "c.1" {
			return nil, nil
		}
		return nil, fmt.Errorf("next please")
	})

	require.NoError(t, err)
	require.Equal(t, []string{"c.2", "c.3", "c.1"}, clientNames)
}

func TestRollingStrategySequentialWithSpreadStart_resetToDeclaredOrderRestartsAtFirstClient(t *testing.T) {
	rollingStrategy := NewRollingStrategySequential[*rollClient](WithSpreadStart())

	clients := NewClients(2*time.Second, rollingStrategy, zlogTest)
	clients.Add(&rollClient{name: "c.1"})
	clients.Add(&rollClient{name: "c.2"})

	_, err := WithClients(clients, func(ctx context.Context, client *rollClient) (v any, err error) {
		return nil, nil
	})
	require.NoError(t, err)

	clients.Reset()

	var got string
	_, err = WithClients(clients, func(ctx context.Context, client *rollClient) (v any, err error) {
		got = client.name
		return nil, nil
	})

	require.NoError(t, err)
	require.Equal(t, "c.1", got)
}
