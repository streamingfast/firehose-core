package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testSortFetcher struct {
}

func (t *testSortFetcher) FetchSortValue(ctx context.Context, client *rollClient) (sortValue uint64, err error) {
	return client.sortValue, nil
}

func TestClientsSort(t *testing.T) {
	rollingStrategy := NewStickyRollingStrategy[*rollClient]()
	rollingStrategy.reset()

	clients := NewClients(2*time.Second, rollingStrategy, zlogTest)
	clients.Add(&rollClient{name: "c.1", sortValue: 100})
	clients.Add(&rollClient{name: "c.2", sortValue: 101})
	clients.Add(&rollClient{name: "c.3", sortValue: 102})
	clients.Add(&rollClient{name: "c.a", sortValue: 103})
	clients.Add(&rollClient{name: "c.b", sortValue: 104})

	fetcher := &testSortFetcher{}

	err := Sort(context.Background(), clients, fetcher, SortDirectionDescending)
	require.NoError(t, err)

	var names []string
	for _, client := range clients.clients {
		names = append(names, client.name)
	}

	require.Equal(t, []string{"c.b", "c.a", "c.3", "c.2", "c.1"}, names)

	err = Sort(context.Background(), clients, fetcher, SortDirectionAscending)
	require.NoError(t, err)

	names = []string{}
	for _, client := range clients.clients {
		names = append(names, client.name)
	}

	require.Equal(t, []string{"c.1", "c.2", "c.3", "c.a", "c.b"}, names)

}
