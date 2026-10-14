package index_builder

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/stretchr/testify/require"
)

func TestApp_ServesHealthCheck(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	app := New(&Config{
		BlockHandler:         bstream.HandlerFunc(func(*pbbstream.Block, interface{}) error { return nil }),
		StartBlockResolver:   func(context.Context) (uint64, error) { return 0, nil },
		MergedBlocksStoreURL: "file://" + t.TempDir(),
		GRPCListenAddr:       addr,
	})
	require.NoError(t, app.Run())
	defer app.Shutdown(nil)

	// gRPC health check, which IsReady queries
	require.Eventually(t, app.IsReady, 10*time.Second, 100*time.Millisecond)

	// HTTP /healthz on the same address
	resp, err := http.Get("http://" + addr + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestApp_HealthCheckNotReadyWhenPendingShutdown(t *testing.T) {
	app := New(&Config{IsPendingShutdown: func() bool { return true }})

	isReady, _, err := app.healthCheck(context.Background())
	require.NoError(t, err)
	require.False(t, isReady)
}
