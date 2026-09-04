package rpc

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClients_DuplicateAndStartAt(t *testing.T) {
	tests := []struct {
		name   string
		source []string
		start  int
		expect []string
	}{
		{"start at first", []string{"c0", "c1", "c2"}, 0, []string{"c0", "c1", "c2"}},
		{"start in the middle", []string{"c0", "c1", "c2"}, 1, []string{"c1", "c2", "c0"}},
		{"start at last", []string{"c0", "c1", "c2"}, 2, []string{"c2", "c0", "c1"}},
		{"start wraps around", []string{"c0", "c1", "c2"}, 4, []string{"c1", "c2", "c0"}},
		{"negative start wraps around", []string{"c0", "c1", "c2"}, -1, []string{"c2", "c0", "c1"}},
		{"single client", []string{"c0"}, 7, []string{"c0"}},
		{"no client", []string{}, 3, []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := newTestClients(tt.source...)

			duplicate := clients.DuplicateAndStartAt(tt.start)

			require.Equal(t, tt.expect, clientNames(duplicate))
			require.Equal(t, tt.source, clientNames(clients), "the source list must not be modified")
		})
	}
}

func TestClients_DuplicateAndStartAt_IsIndependentFromSource(t *testing.T) {
	clients := newTestClients("c0", "c1")
	duplicate := clients.DuplicateAndStartAt(0)

	// The rolling strategy carries a mutable position. Sharing it across duplicates
	// would let concurrent pollers corrupt each other's rolling, so each duplicate
	// must own a fresh one.
	require.NotSame(t, clients.rollingStrategy, duplicate.rollingStrategy)

	clients.Add(&testClient{name: "c2"})
	require.Equal(t, []string{"c0", "c1"}, clientNames(duplicate), "adding to the source must not reach the duplicate")

	require.Equal(t, clients.maxBlockFetchDuration, duplicate.maxBlockFetchDuration)
	require.Equal(t, clients.logger, duplicate.logger)
}

func TestClients_DuplicateAndStartAt_EmptyYieldsNoMoreClient(t *testing.T) {
	duplicate := newTestClients().DuplicateAndStartAt(0)

	_, err := WithClients(duplicate, func(ctx context.Context, client *testClient) (any, error) {
		require.Fail(t, "no client is registered, the callback must never run")
		return nil, nil
	})

	require.ErrorIs(t, err, ErrorNoMoreClient)
}

// TestClients_DuplicateAndStartAt_ParallelUse is the core guarantee behind parallel
// block polling: several duplicates used at the same time each roll through the
// whole client list on their own, starting from their own offset. Run under -race,
// it also proves the duplicates share no mutable state.
func TestClients_DuplicateAndStartAt_ParallelUse(t *testing.T) {
	names := []string{"c0", "c1", "c2", "c3"}
	clients := newTestClients(names...)

	got := make([][]string, len(names))
	errs := make([]error, len(names))

	var wg sync.WaitGroup
	for i := range names {
		wg.Go(func() {
			duplicate := clients.DuplicateAndStartAt(i)

			// Fail on the first client so the strategy has to roll to the next one.
			_, errs[i] = WithClients(duplicate, func(ctx context.Context, client *testClient) (any, error) {
				got[i] = append(got[i], client.name)
				if len(got[i]) < 2 {
					return nil, fmt.Errorf("please roll to the next client")
				}

				return nil, nil
			})
		})
	}
	wg.Wait()

	for i, name := range names {
		require.NoError(t, errs[i], "duplicate starting at %s should have found a working client", name)
		require.Equal(t, []string{name, names[(i+1)%len(names)]}, got[i],
			"duplicate starting at %s rolled through the wrong clients", name)
	}
}

// TestClients_DuplicateAndStartAt_ConcurrentWithMutation covers duplicating while the
// source list is being mutated, which happens for real when `StartSorting` reorders
// the clients underneath an in-flight poll.
func TestClients_DuplicateAndStartAt_ConcurrentWithMutation(t *testing.T) {
	clients := newTestClients("c0", "c1", "c2")

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 50 {
			clients.Add(&testClient{name: "added-" + strconv.Itoa(i)})
		}
	})
	wg.Go(func() {
		for i := range 50 {
			duplicate := clients.DuplicateAndStartAt(i)
			assert.NotEmpty(t, clientNames(duplicate))
		}
	})
	wg.Wait()
}

func BenchmarkClients_DuplicateAndStartAt(b *testing.B) {
	for _, size := range []int{1, 4, 16} {
		b.Run(strconv.Itoa(size)+"_clients", func(b *testing.B) {
			clients := newTestClients()
			for i := range size {
				clients.Add(&testClient{name: strconv.Itoa(i)})
			}

			b.ReportAllocs()

			i := 0
			for b.Loop() {
				_ = clients.DuplicateAndStartAt(i)
				i++
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

type testClient struct {
	name string
}

func newTestClients(names ...string) *Clients[*testClient] {
	clients := NewClients(1*time.Second, NewStickyRollingStrategy[*testClient](), zlogTest)
	for _, name := range names {
		clients.Add(&testClient{name: name})
	}

	return clients
}

func clientNames(clients *Clients[*testClient]) []string {
	names := make([]string, len(clients.clients))
	for i, client := range clients.clients {
		names[i] = client.name
	}

	return names
}
