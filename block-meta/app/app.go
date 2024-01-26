package app

import (
	"context"
	"fmt"

	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/dstore"
	blockmeta "github.com/streamingfast/firehose-core/block-meta"
	"github.com/streamingfast/firehose-core/block-meta/metrics"
	"github.com/streamingfast/logging"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
)

type Config struct {
	StartBlockResolver   func(ctx context.Context) (uint64, error)
	EndBlock             uint64
	MergedBlocksStoreURL string
	BlockMetaStoreURL    string
	GRPCListenAddr       string
}

type App struct {
	*shutter.Shutter
	config *Config
	logger *zap.Logger
	tracer logging.Tracer
}

func New(config *Config, logger *zap.Logger, tracer logging.Tracer) *App {
	return &App{
		Shutter: shutter.New(),
		config:  config,
		logger:  logger,
		tracer:  tracer,
	}
}

func (a *App) Run() error {
	mergedBlocksStore, err := dstore.NewDBinStore(a.config.MergedBlocksStoreURL)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.OnTerminating(func(error) {
		cancel()
	})

	startBlock, err := a.config.StartBlockResolver(ctx)
	if err != nil {
		return err
	}

	store, err := blockmeta.NewStore(a.config.BlockMetaStoreURL, a.logger, a.tracer)
	if err != nil {
		return fmt.Errorf("unable to create block meta store: %w", err)
	}

	indexer := blockmeta.NewIndexer(
		zlog,
		a.config.GRPCListenAddr,
		startBlock,
		a.config.EndBlock,
		mergedBlocksStore,
		store,
	)

	dmetrics.Register(metrics.MetricSet)

	a.OnTerminating(indexer.Shutdown)
	indexer.OnTerminated(a.Shutdown)

	go indexer.Launch()

	zlog.Info("block meta indexer running",
		zap.Uint64("start_block", startBlock),
		zap.Uint64("end_block", a.config.EndBlock),
	)
	return nil
}
