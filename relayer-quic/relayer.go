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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/streamingfast/quic-block-transport/blockinfo"
	"github.com/streamingfast/quic-block-transport/quic"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	"github.com/streamingfast/bstream/hub"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/relayer-quic/metrics"
	"github.com/streamingfast/firehose-core/relayer-quic/source"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
)

const (
	getHeadInfoTimeout = 10 * time.Second
)

type Relayer struct {
	*shutter.Shutter

	quicListenAddr    string
	liveSourceFactory bstream.SourceFactory

	oneBlocksSourceFactory bstream.SourceFromNumFactoryWithSkipFunc

	hub *hub.ForkableHub

	ready      bool
	quicServer *quic.BlockServer
	blocks     chan *pbbstream.Block
	logger     *zap.Logger
}

func NewRelayer(
	liveSourceFactory bstream.SourceFactory,
	quicListenAddr string,
	oneBlocksStore dstore.Store,
	logger *zap.Logger,
) *Relayer {
	r := &Relayer{
		Shutter:           shutter.New(),
		quicListenAddr:    quicListenAddr,
		liveSourceFactory: liveSourceFactory,
		blocks:            make(chan *pbbstream.Block, 100),
		logger:            logger.Named("QUIC relayer"),
	}

	options := []forkable.Option{
		forkable.WithFilters(bstream.StepNew | bstream.StepPartial),
		forkable.WithMetrics(metrics.HeadBlockNumber, metrics.HeadBlockTimeDrift, metrics.HeadBlockRelativeDrift),
	}

	forkableHub := hub.NewForkableHub(
		r.liveSourceFactory,
		10,
		oneBlocksStore,
		options...,
	)

	r.hub = forkableHub
	r.quicServer = quic.NewBlockServer(r.quicListenAddr, generateSelfSignedTLSConfig(), r, r.logger)
	return r
}

func generateSelfSignedTLSConfig() *tls.Config {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(fmt.Sprintf("generating ECDSA key: %v", err))
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		panic(fmt.Sprintf("creating certificate: %v", err))
	}
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(fmt.Sprintf("marshaling EC private key: %v", err))
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		panic(fmt.Sprintf("loading X509 key pair: %v", err))
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"block-streamer"},
	}
}

func NewMultiplexedSource(handler bstream.Handler, sourceAddresses []string, maxSourceLatency time.Duration, sourceRequestBurst int) bstream.Source {
	ctx := context.Background()

	var sourceFactories []bstream.SourceFactory
	for _, u := range sourceAddresses {

		url := u // https://github.com/golang/go/wiki/CommonMistakes (url is given to the blockstream newSource)
		sourceName := urlToLoggerName(url)
		logger := zlog.Named("src").Named(sourceName)
		sf := func(subHandler bstream.Handler) bstream.Source {

			gate := bstream.NewRealtimeGate(maxSourceLatency, subHandler, bstream.GateOptionWithLogger(logger))
			var upstreamHandler bstream.Handler
			upstreamHandler = bstream.HandlerFunc(func(blk *pbbstream.Block, obj interface{}) error {
				if ztrace.Enabled() {
					logger.Debug("received block", zap.Uint64("number", blk.Number), zap.String("id", blk.Id), zap.Int64("latency_ms", time.Since(blk.Timestamp.AsTime()).Milliseconds()))
				}
				return gate.ProcessBlock(blk, &namedObj{
					Obj:  obj,
					Name: sourceName,
				})
			})

			src := source.NewQuicSource(ctx, url, upstreamHandler, logger)
			return src
		}
		sourceFactories = append(sourceFactories, sf)
	}

	return bstream.NewMultiplexedSource(sourceFactories, handler, bstream.MultiplexedSourceWithLogger(zlog))
}

func urlToLoggerName(url string) string {
	return strings.TrimPrefix(strings.TrimPrefix(url, "dns:///"), ":")
}

func (r *Relayer) Run() {
	go r.hub.Run()
	zlog.Info("waiting for hub to be ready...")
	<-r.hub.Ready

	ctx, cancel := context.WithCancel(context.Background())
	r.OnTerminating(func(e error) {
		cancel()
	})

	// Start a hub source from LIB with partials and push blocks into r.blocks
	h := bstream.HandlerFunc(func(blk *pbbstream.Block, _ any) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case r.blocks <- blk:
			return nil
		}
	})
	_, _, _, libNum, err := r.hub.HeadInfo()
	if err != nil {
		r.Shutdown(fmt.Errorf("getting head info: %w", err))
		return
	}
	src := r.hub.SourceFromBlockNumWithForks(libNum, h, true)
	r.OnTerminating(func(e error) { src.Shutdown(e) })
	go func() {
		src.Run()
		<-src.Terminated()
		if err := src.Err(); err != nil {
			r.Shutdown(err)
		}
	}()

	go func() {
		if err := r.quicServer.ListenAndServe(ctx); err != nil {
			r.Shutdown(err)
		}
	}()

	go func() {
		if err := r.quicServer.StreamBlocks(ctx); err != nil {
			r.Shutdown(err)
		}
	}()

	zlog.Info("relayer started")
	r.ready = true
	metrics.AppReadiness.SetReady()

	<-r.hub.Terminating()
	r.Shutdown(r.hub.Err())
}

func (r *Relayer) Next(ctx context.Context) (*blockinfo.BlockInfo, *quic.S2CompressedData, error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case blk := <-r.blocks:

		zlog.Info("next", zap.String("type", blk.Payload.TypeUrl))
		if blk.Payload.TypeUrl != "type.googleapis.com/sf.bstream.v1.S2CompressedData" {
			compressPayload, err := quic.CompressData(&quic.UncompressedData{Bytes: blk.Payload.Value})
			if err != nil {
				return nil, nil, fmt.Errorf("failed to compress block payload: %w", err)
			}
			info := &blockinfo.BlockInfo{
				Number:      blk.Number,
				ID:          blk.Id,
				ParentID:    blk.ParentId,
				ParentNum:   blk.ParentNum,
				LibNum:      blk.LibNum,
				Timestamp:   blk.Timestamp.AsTime(),
				PayloadSize: uint64(len(compressPayload.Bytes)),
			}

			return info, compressPayload, nil

		}

		info := &blockinfo.BlockInfo{
			Number:      blk.Number,
			ID:          blk.Id,
			ParentID:    blk.ParentId,
			ParentNum:   blk.ParentNum,
			LibNum:      blk.LibNum,
			Timestamp:   blk.Timestamp.AsTime(),
			PayloadSize: uint64(len(blk.Payload.Value)),
		}

		return info, &quic.S2CompressedData{blk.Payload.Value}, nil
	}
}

type namedObj struct {
	Name string
	Obj  interface{}
}
