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
	"strings"
	"sync/atomic"
	"time"

	"github.com/streamingfast/dstore"
	"go.uber.org/zap"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/blockstream"
	"github.com/streamingfast/bstream/forkable"
	"github.com/streamingfast/bstream/hub"
	dgrpcfactory "github.com/streamingfast/dgrpc/server/factory"
	"github.com/streamingfast/firehose-core/relayer/metrics"
	"github.com/streamingfast/shutter"
	pbhealth "google.golang.org/grpc/health/grpc_health_v1"
)

// SourceAddr holds a parsed relayer source address, optionally carrying a
// secret key extracted from the "?secret=<key>" query parameter and a
// reconnection interval extracted from the "?retry_interval=<duration>" query parameter.
type SourceAddr struct {
	// URL is the gRPC endpoint address (everything before the '?' separator).
	URL string
	// SecretKey is the value of the "secret" query parameter, or empty when
	// no authentication is required.
	SecretKey string
	// RetryInterval is the minimum time to wait after the source disconnects
	// before connecting to it again. Zero keeps the multiplexed source's own
	// reconnect loop interval (5s), which is also the lower bound.
	RetryInterval time.Duration
}

const (
	getHeadInfoTimeout = 10 * time.Second
)

type Relayer struct {
	*shutter.Shutter

	grpcListenAddr         string
	liveSourceFactory      bstream.SourceFactory
	oneBlocksSourceFactory bstream.SourceFromNumFactoryWithSkipFunc

	hub *hub.ForkableHub

	ready bool

	blockStreamServer *hub.BlockstreamServer
}

func NewRelayer(
	liveSourceFactory bstream.SourceFactory,
	grpcListenAddr string,
	oneBlocksStore dstore.Store,
) *Relayer {
	r := &Relayer{
		Shutter:           shutter.New(),
		grpcListenAddr:    grpcListenAddr,
		liveSourceFactory: liveSourceFactory,
	}

	gs := dgrpcfactory.ServerFromOptions()
	pbhealth.RegisterHealthServer(gs.ServiceRegistrar(), r)

	options := []forkable.Option{
		forkable.WithFilters(bstream.StepNew | bstream.StepPartial),
		forkable.WithMetrics(metrics.HeadBlockNumber, metrics.HeadBlockTimeDrift, metrics.HeadBlockRelativeDrift),
		forkable.WithFinalizedBlockNumMetric(metrics.FinalizedBlockNumber),
	}

	forkableHub := hub.NewForkableHubWithOptions(
		r.liveSourceFactory,
		10,
		oneBlocksStore,
		[]hub.Option{hub.WithMaxConsecutiveUnlinkableBlocks(5), hub.WithLogger(zlog)},
		options...,
	)

	r.hub = forkableHub
	gs.OnTerminated(r.Shutdown)
	r.blockStreamServer = r.hub.NewBlockstreamServer(gs)
	return r

}

func NewMultiplexedSource(handler bstream.Handler, sources []SourceAddr, maxSourceLatency time.Duration, sourceRequestBurst int) bstream.Source {
	ctx := context.Background()

	var sourceFactories []bstream.SourceFactory
	for _, src := range sources {
		src := src // capture loop variable
		sourceName := urlToLoggerName(src.URL)
		logger := zlog.Named("src").Named(sourceName)
		var lastTerminatedAt atomic.Int64
		sf := func(subHandler bstream.Handler) bstream.Source {

			gate := bstream.NewRealtimeGate(maxSourceLatency, subHandler, bstream.GateOptionWithLogger(logger))
			var upstreamHandler bstream.Handler
			upstreamHandler = bstream.HandlerFunc(func(blk *pbbstream.Block, obj interface{}) error {
				metrics.SourceHeadBlockTimeDrift.SetBlockTime(sourceName, blk.Timestamp.AsTime())
				if ztrace.Enabled() {
					logger.Debug("received block", zap.Uint64("number", blk.Number), zap.String("id", blk.Id), zap.Int64("latency_ms", time.Since(blk.Timestamp.AsTime()).Milliseconds()))
				}
				return gate.ProcessBlock(blk, &namedObj{
					Obj:  obj,
					Name: sourceName,
				})
			})

			opts := []blockstream.SourceOption{
				blockstream.WithLogger(logger),
				blockstream.WithRequester("relayer"),
				blockstream.WithPartialBlocks(),
			}
			if src.SecretKey != "" {
				opts = append(opts, blockstream.WithSecretKey(src.SecretKey))
			}
			source := blockstream.NewSource(ctx, src.URL, int64(sourceRequestBurst), upstreamHandler, opts...)
			if src.RetryInterval <= 0 {
				return source
			}

			source.OnTerminated(func(_ error) {
				lastTerminatedAt.Store(time.Now().UnixNano())
			})

			var wait time.Duration
			if last := lastTerminatedAt.Load(); last != 0 {
				wait = src.RetryInterval - time.Since(time.Unix(0, last))
			}
			return &delayedSource{Source: source, wait: wait, logger: logger}
		}
		sourceFactories = append(sourceFactories, sf)
	}

	return bstream.NewMultiplexedSource(sourceFactories, handler, bstream.MultiplexedSourceWithLogger(zlog))
}

// delayedSource waits before running the wrapped source, so a source that
// just disconnected is not reconnected before its RetryInterval has elapsed.
type delayedSource struct {
	*blockstream.Source
	wait   time.Duration
	logger *zap.Logger
}

func (s *delayedSource) Run() {
	if s.wait > 0 {
		s.logger.Info("waiting before reconnecting to source", zap.Duration("wait", s.wait))
		select {
		case <-time.After(s.wait):
		case <-s.Terminating():
			return
		}
	}
	s.Source.Run()
}

func urlToLoggerName(url string) string {
	return strings.TrimPrefix(strings.TrimPrefix(url, "dns:///"), ":")
}

func (r *Relayer) Run() {
	go r.hub.Run()
	zlog.Info("waiting for hub to be ready...")
	<-r.hub.Ready

	// Seed head metrics from the bootstrapped hub head so we report it
	// immediately, instead of only once a live block flows through the forkable.
	if headNum, headID, headTime, libNum, err := r.hub.HeadInfo(); err == nil {
		zlog.Info("seeding head metrics from hub head",
			zap.Uint64("head_num", headNum),
			zap.String("head_id", headID),
			zap.Time("head_time", headTime),
			zap.Uint64("lib_num", libNum),
		)
		metrics.HeadBlockNumber.SetUint64(headNum)
		metrics.HeadBlockTimeDrift.SetBlockTime(headTime)
		metrics.HeadBlockRelativeDrift.SetLastBlock(headTime)
		metrics.FinalizedBlockNumber.SetUint64(libNum)
	}

	r.OnTerminating(func(e error) {
		zlog.Info("closing block stream server")
		r.blockStreamServer.Close()
	})

	r.blockStreamServer.Launch(r.grpcListenAddr)

	zlog.Info("relayer started")
	r.ready = true
	metrics.AppReadiness.SetReady()

	<-r.hub.Terminating()
	r.Shutdown(r.hub.Err())
}

type namedObj struct {
	Name string
	Obj  interface{}
}
