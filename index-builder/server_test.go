package index_builder

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pbhealth "google.golang.org/grpc/health/grpc_health_v1"
)

func TestIndexBuilder_ServesGRPCHealth(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	store, err := dstore.NewDBinStore("file://" + t.TempDir())
	require.NoError(t, err)

	handler := bstream.HandlerFunc(func(*pbbstream.Block, interface{}) error { return nil })
	app := NewIndexBuilder(zap.NewNop(), handler, 0, 0, store, addr)
	go app.Launch()
	defer app.Shutdown(nil)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()

	client := pbhealth.NewHealthClient(conn)
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		resp, err := client.Check(ctx, &pbhealth.HealthCheckRequest{})
		return err == nil && resp.Status == pbhealth.HealthCheckResponse_SERVING
	}, 10*time.Second, 100*time.Millisecond)
}
