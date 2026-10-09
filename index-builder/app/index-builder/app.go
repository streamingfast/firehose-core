package index_builder

import (
	"context"
	"fmt"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dgrpc"
	dgrpcserver "github.com/streamingfast/dgrpc/server"
	dgrpcfactory "github.com/streamingfast/dgrpc/server/factory"
	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/dstore"
	index_builder "github.com/streamingfast/firehose-core/index-builder"
	"github.com/streamingfast/firehose-core/index-builder/metrics"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
	pbhealth "google.golang.org/grpc/health/grpc_health_v1"
)

type Config struct {
	BlockHandler         bstream.Handler
	StartBlockResolver   func(ctx context.Context) (uint64, error)
	EndBlock             uint64
	MergedBlocksStoreURL string
	ForkedBlocksStoreURL string
	GRPCListenAddr       string

	IsPendingShutdown func() bool `json:"-"`
}

type App struct {
	*shutter.Shutter
	config         *Config
	readinessProbe pbhealth.HealthClient
}

func New(config *Config) *App {
	return &App{
		Shutter: shutter.New(),
		config:  config,
	}
}

func (a *App) Run() error {
	blockStore, err := dstore.NewDBinStore(a.config.MergedBlocksStoreURL)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.OnTerminating(func(error) {
		cancel()
	})

	startBlock, err := a.config.StartBlockResolver(ctx)
	if err != nil {
		return fmt.Errorf("resolve start block: %w", err)
	}

	indexBuilder := index_builder.NewIndexBuilder(
		zlog,
		a.config.BlockHandler,
		startBlock,
		a.config.EndBlock,
		blockStore,
	)

	gs, err := dgrpc.NewInternalClient(a.config.GRPCListenAddr)
	if err != nil {
		return fmt.Errorf("cannot create readiness probe")
	}
	a.readinessProbe = pbhealth.NewHealthClient(gs)

	dmetrics.Register(metrics.MetricSet)

	a.OnTerminating(indexBuilder.Shutdown)
	indexBuilder.OnTerminated(a.Shutdown)

	a.startGRPCServer()

	go indexBuilder.Launch()

	zlog.Info("index builder running")
	return nil
}

// startGRPCServer serves the health check over gRPC and HTTP (/healthz) on GRPCListenAddr.
func (a *App) startGRPCServer() {
	gs := dgrpcfactory.ServerFromOptions(
		dgrpcserver.WithLogger(zlog),
		dgrpcserver.WithHealthCheck(dgrpcserver.HealthCheckOverGRPC|dgrpcserver.HealthCheckOverHTTP, a.healthCheck),
	)
	gs.OnTerminated(a.Shutdown)
	a.OnTerminating(func(_ error) {
		gs.Shutdown(0)
	})

	go gs.Launch(a.config.GRPCListenAddr)
}

func (a *App) healthCheck(_ context.Context) (isReady bool, out interface{}, err error) {
	if a.config.IsPendingShutdown != nil && a.config.IsPendingShutdown() {
		return false, nil, nil
	}
	return true, nil, nil
}

func (a *App) IsReady() bool {
	if a.readinessProbe == nil {
		return false
	}

	resp, err := a.readinessProbe.Check(context.Background(), &pbhealth.HealthCheckRequest{})
	if err != nil {
		zlog.Info("index-builder readiness probe error", zap.Error(err))
		return false
	}

	if resp.Status == pbhealth.HealthCheckResponse_SERVING {
		return true
	}

	return false
}
