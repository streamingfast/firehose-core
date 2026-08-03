package rpc

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSortConcurrentWithPoolAccess is a race-detector regression test: Sort used to
// read clients.clients and clients.names without holding the lock, while StartSorting
// ran it on its own goroutine against a pool that AddNamed and WithClientsContext
// mutate under it.
func TestSortConcurrentWithPoolAccess(t *testing.T) {
	clients := NewClients(2*time.Second, NewStickyRollingStrategy[*rollClient](), zlogTest)
	for i := 0; i < 4; i++ {
		clients.AddNamed(&rollClient{name: fmt.Sprintf("c.%d", i), sortValue: uint64(i)}, fmt.Sprintf("p.%d", i))
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				require.NoError(t, Sort(context.Background(), clients, &testSortFetcher{}, SortDirectionDescending))
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 50; j++ {
			_ = clients.Names()
			_, _ = WithClients(clients, func(context.Context, *rollClient) (any, error) { return nil, nil })
		}
	}()

	wg.Wait()

	assert.Len(t, clients.Names(), 4)
}

func clientNames(clients *Clients[*rollClient]) []string {
	clients.lock.Lock()
	defer clients.lock.Unlock()

	var names []string
	for _, client := range clients.clients {
		names = append(names, client.name)
	}

	return names
}

// addingSortFetcher appends a client to the pool from inside FetchSortValue, once, so
// that it lands while Sort is between its snapshot and its publication.
type addingSortFetcher struct {
	once   sync.Once
	rounds int
	add    func()
}

func (f *addingSortFetcher) FetchSortValue(_ context.Context, client *rollClient) (uint64, error) {
	f.once.Do(func() { f.rounds++; f.add() })
	return client.sortValue, nil
}

// TestSortKeepsClientAddedMidRound guards the read-modify-write gap in Sort: it
// snapshots the pool, fetches a sort value per client over the network, then writes
// the pool back. A client added during that gap used to be silently overwritten out of
// existence by the write.
func TestSortKeepsClientAddedMidRound(t *testing.T) {
	clients := NewClients(2*time.Second, NewStickyRollingStrategy[*rollClient](), zlogTest)
	clients.AddNamed(&rollClient{name: "c.1", sortValue: 100}, "primary")
	clients.AddNamed(&rollClient{name: "c.2", sortValue: 101}, "fallback")

	fetcher := &addingSortFetcher{add: func() {
		clients.AddNamed(&rollClient{name: "c.3", sortValue: 102}, "late-arrival")
	}}

	require.NoError(t, Sort(context.Background(), clients, fetcher, SortDirectionDescending))

	// The round that raced the Add is discarded and redone, so the late client is not
	// merely kept, it is sorted along with the rest.
	assert.Equal(t, []string{"c.3", "c.2", "c.1"}, clientNames(clients))
	assert.Equal(t, []string{"late-arrival", "fallback", "primary"}, clients.Names())
}

// TestSortStopsRetryingOnCancelledContext ensures the retry above cannot spin forever
// once the app is shutting down.
func TestSortStopsRetryingOnCancelledContext(t *testing.T) {
	clients := NewClients(2*time.Second, NewStickyRollingStrategy[*rollClient](), zlogTest)
	clients.Add(&rollClient{name: "c.1", sortValue: 100})

	ctx, cancel := context.WithCancel(context.Background())

	fetcher := &addingSortFetcher{add: func() {
		clients.Add(&rollClient{name: "c.2", sortValue: 101})
		cancel()
	}}

	require.ErrorIs(t, Sort(ctx, clients, fetcher, SortDirectionDescending), context.Canceled)
}

// reentrantSortFetcher reads the pool back from inside FetchSortValue, once.
type reentrantSortFetcher struct {
	once  sync.Once
	names []string
}

func (f *reentrantSortFetcher) FetchSortValue(_ context.Context, client *rollClient) (uint64, error) {
	f.once.Do(func() { f.names = client.pool.Names() })
	return client.sortValue, nil
}

// TestSortDoesNotHoldLockDuringFetch guards against the tempting fix to the race
// above, wrapping all of Sort in the lock. FetchSortValue does a network call per
// client and Sort is handed the app context with no timeout, so holding the lock
// across the loop lets one hung provider wedge every WithClientsContext in the pool.
func TestSortDoesNotHoldLockDuringFetch(t *testing.T) {
	clients := NewClients(2*time.Second, NewStickyRollingStrategy[*rollClient](), zlogTest)
	clients.Add(&rollClient{name: "c.1", sortValue: 100, pool: clients})

	fetcher := &reentrantSortFetcher{}

	done := make(chan error, 1)
	go func() {
		done <- Sort(context.Background(), clients, fetcher, SortDirectionDescending)
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Sort is holding clients.lock across FetchSortValue")
	}

	assert.Equal(t, []string{"client-0"}, fetcher.names)
}
