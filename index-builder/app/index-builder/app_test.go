package index_builder

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/stretchr/testify/require"
)

func TestApp_ServesHTTPHealthz(t *testing.T) {
	app := New(&Config{
		BlockHandler:          bstream.HandlerFunc(func(*pbbstream.Block, interface{}) error { return nil }),
		StartBlockResolver:    func(context.Context) (uint64, error) { return 0, nil },
		MergedBlocksStoreURL:  "file://" + t.TempDir(),
		GRPCListenAddr:        freeAddr(t),
		HTTPHealthzListenAddr: freeAddr(t),
	})
	require.NoError(t, app.Run())
	defer app.Shutdown(nil)

	url := "http://" + app.config.HTTPHealthzListenAddr + "/healthz"
	require.Eventually(t, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode == http.StatusOK && string(body) == "ready\n"
	}, 10*time.Second, 100*time.Millisecond)

	require.Eventually(t, app.IsReady, 10*time.Second, 100*time.Millisecond)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().String()
}
