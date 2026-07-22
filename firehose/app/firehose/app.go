// Copyright 2020 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package firehose

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/blockstream"
	"github.com/streamingfast/bstream/hub"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/bstream/transform"
	"github.com/streamingfast/dauth"
	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/dsession"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/firehose"
	"github.com/streamingfast/firehose-core/firehose/info"
	"github.com/streamingfast/firehose-core/firehose/metrics"
	"github.com/streamingfast/firehose-core/firehose/server"
	coremetrics "github.com/streamingfast/firehose-core/metrics"
	"github.com/streamingfast/logging"
	"github.com/streamingfast/shutter"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

type Config struct {
	MergedBlocksStoreURL    string
	OneBlocksStoreURL       string
	ForkedBlocksStoreURL    string
	BlockStreamAddr         string        // gRPC endpoint to get real-time blocks, can be "" in which live streams is disabled
	DiscardPartialBlocks    bool          // When true, partial (flash) blocks received from the live source are dropped before reaching the forkable hub
	GRPCListenAddr          string        // gRPC address where this app will listen to
	GRPCShutdownGracePeriod time.Duration // The duration we allow for gRPC connections to terminate gracefully prior forcing shutdown
	ServiceDiscoveryURL     *url.URL
	ServerOptions           []server.Option `json:"-"`
}

type Modules struct {
	// Required dependencies
	Authenticator         dauth.Authenticator
	SessionPool           dsession.SessionPool
	HeadTimeDriftMetric   *dmetrics.HeadTimeDrift
	HeadBlockNumberMetric *dmetrics.HeadBlockNum
	TransformRegistry     *transform.Registry
	CheckPendingShutdown  func() bool
	InfoServer            *info.InfoServer

	// Optional dependencies
	FinalizedBlockNumberMetric *coremetrics.FinalizedBlockNum
}

type App struct {
	*shutter.Shutter
	config  *Config
	modules *Modules
	logger  *zap.Logger
	tracer  logging.Tracer
	isReady *atomic.Bool
}

func New(logger *zap.Logger, tracer logging.Tracer, config *Config, modules *Modules) *App {
	return &App{
		Shutter: shutter.New(),
		config:  config,
		modules: modules,
		logger:  logger,
		tracer:  tracer,

		isReady: atomic.NewBool(false),
	}
}

func (a *App) Run() error {
	dmetrics.Register(metrics.Metricset)

	a.logger.Info("running firehose", zap.Reflect("config", a.config))
	if err := a.config.Validate(); err != nil {
		return fmt.Errorf("invalid app config: %w", err)
	}

	mergedBlocksStore, err := dstore.NewDBinStore(a.config.MergedBlocksStoreURL)
	if err != nil {
		return fmt.Errorf("failed setting up block store from url %q: %w", a.config.MergedBlocksStoreURL, err)
	}

	oneBlocksStore, err := dstore.NewDBinStore(a.config.OneBlocksStoreURL)
	if err != nil {
		return fmt.Errorf("failed setting up block store from url %q: %w", a.config.OneBlocksStoreURL, err)
	}

	// set to empty store interface if URL is ""
	var forkedBlocksStore dstore.Store
	if a.config.ForkedBlocksStoreURL != "" {
		forkedBlocksStore, err = dstore.NewDBinStore(a.config.ForkedBlocksStoreURL)
		if err != nil {
			return fmt.Errorf("failed setting up block store from url %q: %w", a.config.ForkedBlocksStoreURL, err)
		}
	}

	withLive := a.config.BlockStreamAddr != ""

	var forkableHub *hub.ForkableHub

	if withLive {
		discardPartialBlocks := a.config.DiscardPartialBlocks
		if discardPartialBlocks {
			a.logger.Info("partial (flash) blocks will be discarded from the live source before reaching the forkable hub")
		}

		liveSourceFactory := bstream.SourceFactory(func(h bstream.Handler) bstream.Source {

			return blockstream.NewSource(
				context.Background(),
				a.config.BlockStreamAddr,
				2,
				bstream.HandlerFunc(func(blk *pbbstream.Block, obj any) error {
					if discardPartialBlocks && blk.PartialIndex != 0 {
						return nil
					}
					a.modules.HeadBlockNumberMetric.SetUint64(blk.Number)
					a.modules.HeadTimeDriftMetric.SetBlockTime(blk.Time())
					if a.modules.FinalizedBlockNumberMetric != nil {
						a.modules.FinalizedBlockNumberMetric.SetUint64(blk.LibNum)
					}
					return h.ProcessBlock(blk, obj)
				}),
				blockstream.WithRequester("firehose"),
			)
		})

		// the hub must hold at least two merged-blocks files worth of final
		// blocks so the joining source can hand off from a file boundary
		keepFinalBlocks := int(max(500, 2*bstream.DefaultMergedBlocksBundleSize))
		forkableHub = hub.NewForkableHubWithOptions(liveSourceFactory, keepFinalBlocks, oneBlocksStore, []hub.Option{hub.WithLogger(a.logger)})
		forkableHub.OnTerminated(a.Shutdown)

		go forkableHub.Run()
	}

	streamFactory := firecore.NewStreamFactory(
		mergedBlocksStore,
		forkedBlocksStore,
		forkableHub,
		a.modules.TransformRegistry,
	)

	blockGetter := firehose.NewBlockGetter(mergedBlocksStore, forkedBlocksStore, forkableHub)

	firehoseServer := server.New(
		a.modules.TransformRegistry,
		streamFactory,
		blockGetter,
		a.logger,
		a.modules.Authenticator,
		a.IsReady,
		a.config.GRPCListenAddr,
		a.config.ServiceDiscoveryURL,
		a.modules.InfoServer,
		a.config.ServerOptions...,
	)

	a.OnTerminating(func(_ error) {
		firehoseServer.Shutdown(a.config.GRPCShutdownGracePeriod)
	})
	firehoseServer.OnTerminated(a.Shutdown)

	go func() {
		if withLive {
			a.logger.Info("waiting until hub is real-time synced")
			select {
			case <-forkableHub.Ready:
				metrics.AppReadiness.SetReady()
			case <-a.Terminating():
				return
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := a.modules.InfoServer.Init(ctx, forkableHub, mergedBlocksStore, oneBlocksStore, a.logger); err != nil {
			a.Shutdown(fmt.Errorf("cannot initialize info server: %w", err))
		}

		a.logger.Info("launching gRPC firehoseServer", zap.Bool("live_support", withLive))
		a.isReady.CAS(false, true)
		firehoseServer.Launch()
	}()

	return nil
}

// IsReady return `true` if the apps is ready to accept requests, `false` is returned
// otherwise.
func (a *App) IsReady(ctx context.Context) bool {
	if a.IsTerminating() {
		return false
	}
	if a.modules.CheckPendingShutdown != nil && a.modules.CheckPendingShutdown() {
		return false
	}
	if !a.modules.Authenticator.Ready(ctx) {
		return false
	}

	return a.isReady.Load()
}

// Validate inspects itself to determine if the current config is valid according to
// Firehose rules.
func (config *Config) Validate() error {
	return nil
}
