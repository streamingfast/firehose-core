package test

import (
	"context"
	"net"
	"testing"

	"github.com/test-go/testify/require"

	pbmetering "github.com/streamingfast/dmetering/pb/sf/metering/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type MeteringTestServer struct {
	pbmetering.UnimplementedMeteringServer
	httpListenAddr string
	t              *testing.T
	bufferedEvents []*pbmetering.Events
}

func NewMeteringServer(t *testing.T, httpListenAddr string) *MeteringTestServer {
	return &MeteringTestServer{
		t:              t,
		httpListenAddr: httpListenAddr,
		bufferedEvents: make([]*pbmetering.Events, 0),
	}
}

func (s *MeteringTestServer) Run() {
	lis, err := net.Listen("tcp", s.httpListenAddr)
	if err != nil {
		require.NoError(s.t, err)
	}

	grpcServer := grpc.NewServer()

	pbmetering.RegisterMeteringServer(grpcServer, s)

	s.t.Logf("[Metering]: Server listening port %s", s.httpListenAddr)
	if err = grpcServer.Serve(lis); err != nil {
		require.NoError(s.t, err)
	}
}

func (s *MeteringTestServer) Emit(ctx context.Context, events *pbmetering.Events) (*emptypb.Empty, error) {
	s.bufferedEvents = append(s.bufferedEvents, events)
	return &emptypb.Empty{}, nil
}

func (s *MeteringTestServer) mustEmbedUnimplementedMeteringServer() {
	panic("implement me")
}

func (s *MeteringTestServer) clearBufferedEvents() {
	s.bufferedEvents = make([]*pbmetering.Events, 0)
}
