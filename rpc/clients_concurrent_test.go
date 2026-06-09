package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

// TestWithClients_doesNotSerialize guards the fix in WithClientsContext: the
// clients lock must only protect rolling-strategy selection, NOT the call to
// f. If the lock is (re)introduced around f, concurrent callers serialize and
// only one can be inside f at a time — this test then deadlocks until the
// per-entry timeout fires and fails.
func TestWithClients_doesNotSerialize(t *testing.T) {
	clients := NewClients[any](time.Second, NewStickyRollingStrategy[any](), zap.NewNop())
	for i := 0; i < 4; i++ {
		clients.Add(new(any))
	}

	const n = 4
	entered := make(chan struct{}, n)
	release := make(chan struct{})

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = WithClients(clients, func(ctx context.Context, c any) (any, error) {
				entered <- struct{}{} // signal we're inside f
				<-release             // hold here until every caller is inside f
				return nil, nil
			})
		}()
	}

	// All n callers must be able to be inside f at the same time. If the lock is
	// held across f, only the first gets in and the rest block on the mutex.
	for i := 0; i < n; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/%d callers entered f concurrently: WithClients is serializing on the clients lock", i, n)
		}
	}

	close(release)
	wg.Wait()
}
