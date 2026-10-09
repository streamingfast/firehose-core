package index_builder

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dgrpc"
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

	HTTPHealthzListenAddr string

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
		a.config.GRPCListenAddr,
	)

	gs, err := dgrpc.NewInternalClient(a.config.GRPCListenAddr)
	if err != nil {
		return fmt.Errorf("cannot create readiness probe")
	}
	a.readinessProbe = pbhealth.NewHealthClient(gs)

	dmetrics.Register(metrics.MetricSet)

	a.OnTerminating(indexBuilder.Shutdown)
	indexBuilder.OnTerminated(a.Shutdown)

	if a.config.HTTPHealthzListenAddr != "" {
		a.startHTTPHealthzServer(indexBuilder)
	}

	go indexBuilder.Launch()

	zlog.Info("index builder running")
	return nil
}

func (a *App) startHTTPHealthzServer(indexBuilder *index_builder.IndexBuilder) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if a.config.IsPendingShutdown != nil && a.config.IsPendingShutdown() {
			http.Error(w, "not ready: shutting down", http.StatusServiceUnavailable)
			return
		}
		resp, err := indexBuilder.Check(context.Background(), &pbhealth.HealthCheckRequest{})
		if err != nil || resp.Status != pbhealth.HealthCheckResponse_SERVING {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ready\n"))
	})

	srv := &http.Server{Addr: a.config.HTTPHealthzListenAddr, Handler: mux}
	a.OnTerminating(func(_ error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	zlog.Info("starting index builder http healthz server", zap.String("addr", a.config.HTTPHealthzListenAddr))
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zlog.Error("index builder http healthz server failed", zap.Error(err))
			a.Shutdown(err)
		}
	}()
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
