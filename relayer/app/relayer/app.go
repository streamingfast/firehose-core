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

package relayer

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/relayer"
	"github.com/streamingfast/firehose-core/relayer/metrics"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
	pbhealth "google.golang.org/grpc/health/grpc_health_v1"
)

var RelayerStartAborted = fmt.Errorf("getting start block aborted by relayer application terminating signal")

type Config struct {
	Sources               []relayer.SourceAddr
	GRPCListenAddr        string
	HTTPHealthzListenAddr string
	SourceRequestBurst    int
	MaxSourceLatency      time.Duration
	OneBlocksURL          string

	IsPendingShutdown func() bool `json:"-"`
}

func (c *Config) ZapFields() []zap.Field {
	addrs := make([]string, len(c.Sources))
	for i, s := range c.Sources {
		addrs[i] = s.URL
	}
	return []zap.Field{
		zap.Strings("sources_addr", addrs),
		zap.String("grpc_listen_addr", c.GRPCListenAddr),
		zap.String("http_healthz_listen_addr", c.HTTPHealthzListenAddr),
		zap.Int("source_request_burst", c.SourceRequestBurst),
		zap.Duration("max_source_latency", c.MaxSourceLatency),
		zap.String("one_blocks_url", c.OneBlocksURL),
	}
}

type App struct {
	*shutter.Shutter
	config *Config

	relayer *relayer.Relayer
}

func New(config *Config) *App {
	return &App{
		Shutter: shutter.New(),
		config:  config,
	}
}

func (a *App) Run() error {
	dmetrics.Register(metrics.MetricSet)

	oneBlocksStore, err := dstore.NewDBinStore(a.config.OneBlocksURL)
	if err != nil {
		return fmt.Errorf("getting block store: %w", err)
	}

	liveSourceFactory := bstream.SourceFactory(func(h bstream.Handler) bstream.Source {
		return relayer.NewMultiplexedSource(
			h,
			a.config.Sources,
			a.config.MaxSourceLatency,
			a.config.SourceRequestBurst,
		)
	})

	zlog.Info("starting relayer", a.config.ZapFields()...)
	a.relayer = relayer.NewRelayer(
		liveSourceFactory,
		a.config.GRPCListenAddr,
		oneBlocksStore,
	)

	a.OnTerminating(a.relayer.Shutdown)
	a.relayer.OnTerminated(a.Shutdown)

	if a.config.HTTPHealthzListenAddr != "" {
		a.startHTTPHealthzServer()
	}

	a.relayer.Run()
	return nil
}

func (a *App) startHTTPHealthzServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if a.config.IsPendingShutdown != nil && a.config.IsPendingShutdown() {
			http.Error(w, "not ready: shutting down", http.StatusServiceUnavailable)
			return
		}
		resp, err := a.relayer.Check(context.Background(), emptyHealthCheckRequest)
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

	zlog.Info("starting relayer http healthz server", zap.String("addr", a.config.HTTPHealthzListenAddr))
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zlog.Error("relayer http healthz server failed", zap.Error(err))
			a.Shutdown(err)
		}
	}()
}

var emptyHealthCheckRequest = &pbhealth.HealthCheckRequest{}

func (a *App) IsReady() bool {
	if a.relayer == nil {
		return false
	}

	resp, err := a.relayer.Check(context.Background(), emptyHealthCheckRequest)
	if err != nil {
		zlog.Info("readiness check failed", zap.Error(err))
		return false
	}

	return resp.Status == pbhealth.HealthCheckResponse_SERVING
}
