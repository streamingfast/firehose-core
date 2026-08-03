package rpc

import (
	"context"
	"slices"
	"sort"
)

type SortValueFetcher[C any] interface {
	FetchSortValue(ctx context.Context, client C) (sortValue uint64, err error)
}

type SortDirection int

const (
	SortDirectionAscending SortDirection = iota
	SortDirectionDescending
)

func Sort[C any](ctx context.Context, clients *Clients[C], sortValueFetch SortValueFetcher[C], direction SortDirection) error {
	type sortable struct {
		clientIndex int
		sortValue   uint64
	}

	for {
		// Sort a snapshot rather than the pool itself: `FetchSortValue` below is a network
		// call per client, and holding the lock across them would block every
		// `WithClientsContext` for the whole round, so one hung provider would wedge the
		// entire pool (`StartSorting` gets no timeout, it's handed the app's context).
		clients.lock.Lock()
		pool := slices.Clone(clients.clients)
		names := slices.Clone(clients.names)
		clients.lock.Unlock()

		var sortableValues []sortable
		for i, client := range pool {
			var v uint64
			var err error
			v, err = sortValueFetch.FetchSortValue(ctx, client)
			if err != nil {
				//do nothing
			}
			sortableValues = append(sortableValues, sortable{i, v})
		}

		sort.Slice(sortableValues, func(i, j int) bool {
			if direction == SortDirectionAscending {
				return sortableValues[i].sortValue < sortableValues[j].sortValue
			}
			return sortableValues[i].sortValue > sortableValues[j].sortValue
		})

		var sorted []C
		var sortedNames []string
		for _, v := range sortableValues {
			sorted = append(sorted, pool[v.clientIndex])
			sortedNames = append(sortedNames, names[v.clientIndex])
		}

		// The pool only ever grows through `Add`/`AddNamed` or gets permuted by this very
		// function, so a length that still matches the snapshot means no client was added
		// while we were fetching and `sorted` covers the whole pool. A concurrent `Sort`
		// that published in between keeps the length, so the loser only overwrites the
		// order, never the set, and the next round settles it.
		clients.lock.Lock()
		published := len(clients.clients) == len(pool)
		if published {
			clients.clients = sorted
			clients.names = sortedNames
		}
		clients.lock.Unlock()

		if published {
			return nil
		}

		// A client was added mid-round and has no sort value yet, so publishing would drop
		// it from the pool. Redo the round with it included.
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
