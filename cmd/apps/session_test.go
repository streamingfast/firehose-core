package apps

import (
	"context"
	"testing"

	"github.com/streamingfast/dsession"
	"go.uber.org/zap"
)

type closingSessionPool struct {
	dsession.SessionPool
	closed bool
}

func (p *closingSessionPool) Close(ctx context.Context) error {
	p.closed = true
	return nil
}

type plainSessionPool struct {
	dsession.SessionPool
}

func TestCloseSessionPools(t *testing.T) {
	closing := &closingSessionPool{}

	sessionPoolsLock.Lock()
	sessionPools = []dsession.SessionPool{closing, &plainSessionPool{}}
	sessionPoolsLock.Unlock()

	closeSessionPools(zap.NewNop())

	if !closing.closed {
		t.Fatalf("expected the pool implementing Close to be closed")
	}

	sessionPoolsLock.Lock()
	remaining := len(sessionPools)
	sessionPoolsLock.Unlock()
	if remaining != 0 {
		t.Fatalf("expected closed pools to be forgotten, %d remain", remaining)
	}
}
