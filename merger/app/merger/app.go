// Copyright 2019 dfuse Platform Inc.
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

package merger

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dgrpc"
	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/merger"
	"github.com/streamingfast/firehose-core/merger/metrics"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
	pbhealth "google.golang.org/grpc/health/grpc_health_v1"
)

type Config struct {
	StorageOneBlockFilesPath     string
	StorageMergedBlocksFilesPath string
	StorageForkedBlocksFilesPath string

	FilesDeleteThreads int
	MaxMergingThreads  int

	GRPCListenAddr        string
	HTTPHealthzListenAddr string

	PruneForkedBlocksAfter uint64

	PruneOneBlockFilesAfter uint64

	TimeBetweenPruning time.Duration
	TimeBetweenPolling time.Duration
	StopBlock          uint64

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
	zlog.Info("running merger", zap.Reflect("config", a.config))

	dmetrics.Register(metrics.MetricSet)

	oneBlockStoreStore, err := dstore.NewDBinStore(a.config.StorageOneBlockFilesPath)
	if err != nil {
		return fmt.Errorf("failed to init source archive store: %w", err)
	}

	mergedBlocksStore, err := dstore.NewDBinStore(a.config.StorageMergedBlocksFilesPath)
	if err != nil {
		return fmt.Errorf("failed to init destination archive store: %w", err)
	}

	var forkedBlocksStore dstore.Store
	if a.config.StorageForkedBlocksFilesPath != "" {
		forkedBlocksStore, err = dstore.NewDBinStore(a.config.StorageForkedBlocksFilesPath)
		if err != nil {
			return fmt.Errorf("failed to init destination archive store: %w", err)
		}
	}

	bundleSize := uint64(100)

	// we are setting the backoff here for dstoreIO
	io := merger.NewDStoreIO(
		zlog,
		oneBlockStoreStore,
		mergedBlocksStore,
		forkedBlocksStore,
		5,
		500*time.Millisecond,
		bundleSize,
		a.config.FilesDeleteThreads)

	m := merger.NewMerger(
		zlog,
		a.config.GRPCListenAddr,
		io,
		bstream.GetProtocolFirstStreamableBlock,
		bundleSize,
		a.config.PruneForkedBlocksAfter,
		a.config.PruneOneBlockFilesAfter,
		a.config.TimeBetweenPruning,
		a.config.TimeBetweenPolling,
		a.config.StopBlock,
		a.config.MaxMergingThreads,
	)
	zlog.Info("merger initiated")

	gs, err := dgrpc.NewInternalClient(a.config.GRPCListenAddr)
	if err != nil {
		return fmt.Errorf("cannot create readiness probe")
	}
	a.readinessProbe = pbhealth.NewHealthClient(gs)

	a.OnTerminating(func(err error) {
		m.Shutdown(err)
		<-m.Terminated()
		zlog.Info("merger terminated")
	})
	m.OnTerminated(a.Shutdown)

	if a.config.HTTPHealthzListenAddr != "" {
		a.startHTTPHealthzServer(m)
	}

	go m.Run()

	zlog.Info("merger running")
	return nil
}

func (a *App) startHTTPHealthzServer(m *merger.Merger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if a.config.IsPendingShutdown != nil && a.config.IsPendingShutdown() {
			http.Error(w, "not ready: shutting down", http.StatusServiceUnavailable)
			return
		}
		resp, err := m.Check(context.Background(), &pbhealth.HealthCheckRequest{})
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

	zlog.Info("starting merger http healthz server", zap.String("addr", a.config.HTTPHealthzListenAddr))
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zlog.Error("merger http healthz server failed", zap.Error(err))
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
		zlog.Info("merger readiness probe error", zap.Error(err))
		return false
	}

	if resp.Status == pbhealth.HealthCheckResponse_SERVING {
		return true
	}

	return false
}
