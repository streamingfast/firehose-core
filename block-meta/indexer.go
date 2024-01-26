package blockmeta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/bstream/stream"
	"github.com/streamingfast/dgrpc/server"
	"github.com/streamingfast/dgrpc/server/factory"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/block-meta/metrics"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
)

type Indexer struct {
	*shutter.Shutter
	logger *zap.Logger

	grpcListenAddr    string
	startBlockNum     uint64
	stopBlockNum      uint64
	mergedBlocksStore dstore.Store
	blockMetaStore    *Store

	seenFirstBlock bool
}

func NewIndexer(logger *zap.Logger, grpcListenAddr string, startBlockNum, stopBlockNum uint64, mergedBlocksStore dstore.Store, blockMetaStore *Store) *Indexer {
	return &Indexer{
		Shutter: shutter.New(),

		grpcListenAddr:    grpcListenAddr,
		startBlockNum:     startBlockNum,
		stopBlockNum:      stopBlockNum,
		mergedBlocksStore: mergedBlocksStore,
		blockMetaStore:    blockMetaStore,
		logger:            logger,
	}
}

func (app *Indexer) Launch() {
	server := factory.ServerFromOptions(
		server.WithPlainTextServer(),
		server.WithLogger(app.logger),
		server.WithHealthCheck(server.HealthCheckOverHTTP|server.HealthCheckOverGRPC, app.healthCheck),
	)

	app.OnTerminating(func(_ error) {
		server.Shutdown(5 * time.Second)
	})

	server.OnTerminated(func(err error) {
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			app.logger.Error("gRPC server unexpected failure", zap.Error(err))
		}
		app.Shutdown(err)
	})

	go server.Launch(app.grpcListenAddr)

	// Blocking call of the indexer
	err := app.launch()
	if errors.Is(err, stream.ErrStopBlockReached) {
		app.logger.Info("block meta reached stop block", zap.Uint64("stop_block_num", app.stopBlockNum))
		err = nil
	}

	app.logger.Info("block meta exited", zap.Error(err))
	app.Shutdown(err)
}

func (app *Indexer) launch() error {
	startBlockNum := app.startBlockNum
	stopBlockNum := app.stopBlockNum

	streamFactory := firecore.NewStreamFactory(
		app.mergedBlocksStore,
		nil,
		nil,
		nil,
	)
	ctx := context.Background()

	req := &pbfirehose.Request{
		StartBlockNum:   int64(startBlockNum),
		StopBlockNum:    stopBlockNum,
		FinalBlocksOnly: true,
	}

	handlerFunc := func(block *pbbstream.Block, _ interface{}) error {
		app.logger.Debug("handling block", zap.Uint64("block_num", block.Number))
		app.seenFirstBlock = true

		metrics.HeadBlockNumber.SetUint64(block.Number)
		metrics.HeadBlockTimeDrift.SetBlockTime(block.Time())
		metrics.AppReadiness.SetReady()

		app.logger.Debug("updated head block metrics", zap.Uint64("block_num", block.Number), zap.Time("block_time", block.Time()))

		writeCtx, cancelWrite := context.WithTimeout(ctx, 15*time.Second)
		defer cancelWrite()

		err := app.blockMetaStore.WriteBlockMeta(writeCtx, block.ToBlocKMeta())
		if err != nil {
			return fmt.Errorf("writing block meta: %w", err)
		}

		return nil
	}

	stream, err := streamFactory.New(
		ctx,
		bstream.HandlerFunc(handlerFunc),
		req,
		app.logger,
	)

	if err != nil {
		return fmt.Errorf("getting firehose stream: %w", err)
	}

	return stream.Run(ctx)
}

func (app *Indexer) healthCheck(ctx context.Context) (isReady bool, out interface{}, err error) {
	return app.seenFirstBlock, nil, nil
}
