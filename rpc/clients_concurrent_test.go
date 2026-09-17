package rpc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// TestWithClients_doesNotSerialize guards the fix in WithClientsContext: the
// clients lock must only protect rolling-strategy selection, NOT the call to
// f. If the lock is (re)introduced around f, concurrent callers serialize and
// only one can be inside f at a time.
//
// We run inside a synctest bubble so the assertion is deterministic: once all
// callers are inside f and durably blocked on `release`, synctest.Wait returns
// and we can read exactly how many made it in. If the lock is held across f,
// only the first caller reaches f (the rest block on the mutex) and the bubble
// never reaches an all-durably-blocked state, so the test hangs and fails.
func TestWithClients_doesNotSerialize(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clients := NewClients[any](time.Second, NewStickyRollingStrategy[any](), zlogTest)
		for i := 0; i < 4; i++ {
			clients.Add(new(any))
		}

		const n = 4
		var inside atomic.Int32
		release := make(chan struct{})

		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = WithClients(clients, func(ctx context.Context, c any) (any, error) {
					inside.Add(1) // record that we're inside f
					<-release     // hold here until every caller is inside f
					return nil, nil
				})
			}()
		}

		// Block until every caller goroutine is durably blocked. With the lock
		// correctly scoped, all n reach f and park on <-release; with the lock
		// held across f, only one does and the rest sit on the mutex.
		synctest.Wait()

		if got := inside.Load(); got != n {
			t.Fatalf("only %d/%d callers entered f concurrently: WithClients is serializing on the clients lock", got, n)
		}

		close(release)
		wg.Wait()
	})
}

// TestRollingStrategySequentialWithSpreadStart_concurrentCallsUseDistinctStarts guards the point
// of WithSpreadStart: concurrent WithClientsContext calls must not all
// start on the same provider. Selection (reset+next) is serialized by clients.lock,
// so N concurrent calls against N clients must claim exactly one starting client
// each, with no provider picked twice and none left unused.
//
// Run inside a synctest bubble, with every caller blocked inside f on `release`:
// by the time synctest.Wait returns, all n calls have necessarily gone through
// selection already (that's what let them reach f), so we're asserting on a
// genuine concurrent race for the shared start-index counter, not on calls that
// happened to run one after another by scheduler luck.
func TestRollingStrategySequentialWithSpreadStart_concurrentCallsUseDistinctStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		names := []string{"c.1", "c.2", "c.3", "c.4"}

		clients := NewClients(2*time.Second, NewRollingStrategySequential[*rollClient](WithSpreadStart()), zlogTest)
		for _, name := range names {
			clients.Add(&rollClient{name: name})
		}

		var mu sync.Mutex
		used := map[string]int{}
		release := make(chan struct{})

		var wg sync.WaitGroup
		for range names {
			wg.Go(func() {
				_, err := WithClients(clients, func(_ context.Context, client *rollClient) (any, error) {
					mu.Lock()
					used[client.name]++
					mu.Unlock()
					<-release
					return nil, nil
				})
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			})
		}

		synctest.Wait()

		if len(used) != len(names) {
			t.Fatalf("expected all %d providers to be used as a starting client, got %v", len(names), used)
		}
		for _, name := range names {
			if used[name] != 1 {
				t.Errorf("provider %s used %d times, want exactly once", name, used[name])
			}
		}

		close(release)
		wg.Wait()
	})
}
