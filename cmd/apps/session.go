package apps

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/viper"
	"github.com/streamingfast/dsession"
	"go.uber.org/zap"
)

// sessionPoolCloseTimeout bounds how long the process waits, once every app has terminated, for
// its session pools to return their sessions. It has to fit in what is left of the pod's
// termination grace period after the shutdown signal delay.
const sessionPoolCloseTimeout = 5 * time.Second

// sessionPoolCloser is implemented by session pools holding sessions on a remote server, which
// stay counted against the organization until returned or until they expire there.
type sessionPoolCloser interface {
	Close(ctx context.Context) error
}

var (
	sessionPoolsLock sync.Mutex
	sessionPools     []dsession.SessionPool
)

// newCommonSessionPool creates the session pool configured by `common-session-plugin` and keeps
// it so closeSessionPools can return its sessions before the process exits.
func newCommonSessionPool(logger *zap.Logger) (dsession.SessionPool, error) {
	sessionPool, err := dsession.New(viper.GetString("common-session-plugin"), logger)
	if err != nil {
		return nil, fmt.Errorf("unable to create session pool: %w", err)
	}

	sessionPoolsLock.Lock()
	sessionPools = append(sessionPools, sessionPool)
	sessionPoolsLock.Unlock()

	return sessionPool, nil
}

// closeSessionPools returns the sessions still held by every pool created by
// newCommonSessionPool. Call it once the apps have terminated: a request cut by the shutdown
// releases its session in the background, and the process would otherwise exit before that
// release reaches the session server, leaving the client unable to reconnect until the session
// expires there. It does not depend on the shutdown signal delay, so it also runs when that
// delay is 0.
func closeSessionPools(logger *zap.Logger) {
	sessionPoolsLock.Lock()
	pools := sessionPools
	sessionPools = nil
	sessionPoolsLock.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), sessionPoolCloseTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, pool := range pools {
		closer, ok := pool.(sessionPoolCloser)
		if !ok {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			if err := closer.Close(ctx); err != nil {
				logger.Warn("session pool did not return all its sessions", zap.Error(err))
			}
		}()
	}
	wg.Wait()
}
