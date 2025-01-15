package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"
	connect_go "connectrpc.com/connect"
	_ "github.com/mostynb/go-grpc-compression/zstd"
	"github.com/streamingfast/bstream/transform"
	"github.com/streamingfast/dauth"
	dauthgrpc "github.com/streamingfast/dauth/middleware/grpc"
	dgrpcserver "github.com/streamingfast/dgrpc/server"
	connectweb "github.com/streamingfast/dgrpc/server/connectrpc"
	"github.com/streamingfast/dmetering"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/firehose"
	"github.com/streamingfast/firehose-core/firehose/info"
	"github.com/streamingfast/firehose-core/firehose/rate"
	"github.com/streamingfast/firehose-core/metering"
	fhconnect "github.com/streamingfast/pbgo/sf/firehose/v2/pbfirehoseconnect"

	pbfirehoseV2 "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/protobuf/proto"
)

type Server struct {
	streamFactory     *firecore.StreamFactory
	transformRegistry *transform.Registry
	blockGetter       *firehose.BlockGetter

	initFunc     func(context.Context, *pbfirehoseV2.Request) context.Context
	postHookFunc func(context.Context, *pbfirehoseV2.Response)

	servers            []*wrappedServer
	enforceCompression bool
	logger             *zap.Logger

	rateLimiter rate.Limiter
}

type wrappedServer struct {
	*connectweb.ConnectWebServer
	listenAddr string
}

type Option func(*Server)

func WithLeakyBucketLimiter(size int, dripRate time.Duration) Option {
	return func(s *Server) {
		s.rateLimiter = rate.NewLeakyBucketLimiter(size, dripRate)
	}
}

func WithEnforceCompression(enforce bool) Option {
	return func(s *Server) {
		s.enforceCompression = enforce
	}
}

func New(
	transformRegistry *transform.Registry,
	streamFactory *firecore.StreamFactory,
	blockGetter *firehose.BlockGetter,
	logger *zap.Logger,
	authenticator dauth.Authenticator,
	isReady func(context.Context) bool,
	listenAddr string,
	serviceDiscoveryURL *url.URL,
	infoServer *info.InfoServer,
	opts ...Option,
) *Server {
	initFunc := func(ctx context.Context, _ *pbfirehoseV2.Request) context.Context {
		ctx = dmetering.WithBytesMeter(ctx)
		ctx = withRequestMeter(ctx)
		return ctx
	}

	postHookFunc := func(ctx context.Context, response *pbfirehoseV2.Response) {
		requestMeter := getRequestMeter(ctx)
		requestMeter.blocks++
		requestMeter.egressBytes += proto.Size(response)

		meter := dmetering.GetBytesMeter(ctx)
		auth := dauth.FromContext(ctx)
		metering.Send(ctx, meter, auth.UserID(), auth.APIKeyID(), auth.RealIP(), auth.Meta(), "sf.firehose.v2.Firehose/Blocks", response)
	}

	tracerProvider := otel.GetTracerProvider()

	s := &Server{
		transformRegistry: transformRegistry,
		blockGetter:       blockGetter,
		streamFactory:     streamFactory,
		initFunc:          initFunc,
		postHookFunc:      postHookFunc,
		logger:            logger,
		servers:           []*wrappedServer{},
	}

	for _, addr := range strings.Split(listenAddr, ",") {
		options := []dgrpcserver.Option{
			dgrpcserver.WithLogger(logger),
			dgrpcserver.WithHealthCheck(dgrpcserver.HealthCheckOverGRPC|dgrpcserver.HealthCheckOverHTTP, createHealthCheck(isReady)),
			dgrpcserver.WithPostUnaryInterceptor(otelgrpc.UnaryServerInterceptor(otelgrpc.WithTracerProvider(tracerProvider))),
			dgrpcserver.WithPostStreamInterceptor(otelgrpc.StreamServerInterceptor(otelgrpc.WithTracerProvider(tracerProvider))),
			dgrpcserver.WithGRPCServerOptions(grpc.MaxRecvMsgSize(25 * 1024 * 1024)),
			dgrpcserver.WithPostUnaryInterceptor(dauthgrpc.UnaryAuthChecker(authenticator, logger)),
			dgrpcserver.WithPostStreamInterceptor(dauthgrpc.StreamAuthChecker(authenticator, logger)),
			dgrpcserver.WithConnectPermissiveCORS(),
		}

		if serviceDiscoveryURL != nil {
			options = append(options, dgrpcserver.WithServiceDiscoveryURL(serviceDiscoveryURL))
		}

		if strings.Contains(addr, "*") {
			options = append(options, dgrpcserver.WithInsecureServer())
		} else {
			options = append(options, dgrpcserver.WithPlainTextServer())
		}

		streamHandlerGetter := func(opts ...connect_go.HandlerOption) (string, http.Handler) {
			return fhconnect.NewStreamHandler(s, opts...)
		}
		options = append(options, dgrpcserver.WithConnectReflection(pbfirehoseV2.Stream_ServiceDesc.ServiceName))

		fetchHandlerGetter := func(opts ...connect_go.HandlerOption) (string, http.Handler) {
			return fhconnect.NewFetchHandler(s, opts...)
		}
		options = append(options, dgrpcserver.WithConnectReflection(pbfirehoseV2.Fetch_ServiceDesc.ServiceName))

		handlerGetters := []connectweb.HandlerGetter{streamHandlerGetter, fetchHandlerGetter}
		if infoServer != nil {
			infoHandlerGetter := func(opts ...connect_go.HandlerOption) (string, http.Handler) {
				out, outh := fhconnect.NewEndpointInfoHandler(&InfoServerWrapper{rpcInfoServer: infoServer}, opts...)
				return out, outh
			}
			handlerGetters = append(handlerGetters, infoHandlerGetter)
			options = append(options, dgrpcserver.WithConnectReflection(pbfirehoseV2.EndpointInfo_ServiceDesc.ServiceName))
		}

		cleanAddr := strings.ReplaceAll(addr, "*", "")

		srv := connectweb.New(handlerGetters, options...)

		s.servers = append(s.servers, &wrappedServer{
			ConnectWebServer: srv,
			listenAddr:       cleanAddr,
		})

	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

func (s *Server) OnTerminated(f func(error)) {
	for _, server := range s.servers {
		server.OnTerminated(f)
	}
}

func (s *Server) Shutdown(timeout time.Duration) {
	// FIXME we need to implement the timeout here
	for _, server := range s.servers {
		server.Shutdown(nil)
	}
}

func (s *Server) Launch() {
	wg := sync.WaitGroup{}
	for _, server := range s.servers {
		wg.Add(1)
		go func() {
			server.Launch(server.listenAddr)
			for _, srv := range s.servers {
				srv.Shutdown(nil) // immediately shutdown all other servers when one terminates, in case a single one failed
			}
			wg.Done()
		}()
	}
	wg.Wait()
}

func createHealthCheck(isReady func(ctx context.Context) bool) dgrpcserver.HealthCheck {
	return func(ctx context.Context) (bool, interface{}, error) {
		return isReady(ctx), nil, nil
	}
}

type key int

var requestMeterKey key

type requestMeter struct {
	blocks      uint64
	egressBytes int
}

func getRequestMeter(ctx context.Context) *requestMeter {
	if rm, ok := ctx.Value(requestMeterKey).(*requestMeter); ok {
		return rm
	}
	return &requestMeter{} // not so useful but won't break tests
}
func withRequestMeter(ctx context.Context) context.Context {
	if _, ok := ctx.Value(requestMeterKey).(*requestMeter); ok {
		return ctx
	}
	return context.WithValue(ctx, requestMeterKey, &requestMeter{})
}

type InfoServerWrapper struct {
	rpcInfoServer *info.InfoServer
}

// Info implements pbsubstreamsrpcconnect.EndpointInfoHandler.
func (i *InfoServerWrapper) Info(ctx context.Context, req *connect.Request[pbfirehoseV2.InfoRequest]) (*connect.Response[pbfirehoseV2.InfoResponse], error) {
	resp, err := i.rpcInfoServer.Info(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
