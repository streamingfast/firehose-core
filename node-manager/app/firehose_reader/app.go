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

package firehose_reader

import (
	"context"
	"time"

	"github.com/streamingfast/bstream/blockstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/cli"
	dgrpcserver "github.com/streamingfast/dgrpc/server"
	dgrpcfactory "github.com/streamingfast/dgrpc/server/factory"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/logging"
	pbheadinfo "github.com/streamingfast/pbgo/sf/headinfo/v1"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
)

type Config struct {
	GRPCAddr            string
	OneBlocksStoreURL   string
	OneBlockSuffix      string
	StartBlockNum       uint64
	StopBlockNum        uint64
	ReadinessMaxLatency time.Duration
	StateFile           string
	FirehoseConfig      FirehoseConfig
}

type FirehoseConfig struct {
	ApiKey        string
	ApiToken      string
	Endpoint      string
	Compression   string
	PlaintextConn bool
	InsecureConn  bool
}

type App struct {
	*shutter.Shutter
	config    Config
	ReadyFunc func()
	zlogger   *zap.Logger
	tracer    logging.Tracer
}

func New(config Config, zlogger *zap.Logger, tracer logging.Tracer) *App {
	n := &App{
		Shutter:   shutter.New(),
		config:    config,
		ReadyFunc: func() {},
		zlogger:   zlogger,
		tracer:    tracer,
	}
	return n
}

func (a *App) Run() error {
	a.zlogger.Info("launching reader-node-firehose app (reading from firehose)", zap.Reflect("config", a.config))
	appCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	a.OnTerminating(func(err error) { cancel(err) })

	appWrapper := cli.NewApplication(appCtx)

	store, err := dstore.NewDBinStore(a.config.OneBlocksStoreURL)
	cli.NoError(err, "unable to create store")

	blockStreamServer := blockstream.NewUnmanagedServer(
		blockstream.ServerOptionWithLogger(a.zlogger),
		blockstream.ServerOptionWithBuffer(1),
	)

	server := &server{
		Shutter:    shutter.New(),
		listenAddr: a.config.GRPCAddr,
		logger:     a.zlogger,
	}

	syncer := &syncer{
		Shutter:           server.Shutter,
		appCtx:            appWrapper.Context(),
		blockStreamServer: blockStreamServer,
		config:            a.config,
		firehoseConfig:    a.config.FirehoseConfig,
		logger:            a.zlogger,
		store:             store,
	}

	appWrapper.SuperviseAndStart(server)
	appWrapper.SuperviseAndStart(syncer)

	err = appWrapper.BlockUntilTerminated(a.zlogger, 0)
	if err != nil {
		return err
	}

	// If we are at this point, it's because the syncer gracefully terminated which means
	// it reaches its stop block. The overall app launcher does not terminate on nil error.
	// So here, we shutdown the app to terminate the process.
	a.Shutdown(nil)

	return nil
}

func (a *App) OnReady(f func()) {
	a.ReadyFunc = f
}

func (a *App) IsReady() bool {
	return true
}

type server struct {
	*shutter.Shutter
	listenAddr        string
	blockStreamServer *blockstream.Server
	logger            *zap.Logger
}

func (s *server) Run() {
	gs := dgrpcfactory.ServerFromOptions(dgrpcserver.WithLogger(s.logger))

	serviceRegistrar := gs.ServiceRegistrar()
	pbheadinfo.RegisterHeadInfoServer(serviceRegistrar, s.blockStreamServer)
	pbbstream.RegisterBlockStreamServer(serviceRegistrar, s.blockStreamServer)

	s.OnTerminating(func(_ error) {
		// Clients connected to the server are the relayer, they have no idea
		// that the server is shutting down, so no need to drain connections here.
		gs.Shutdown(5 * time.Second)
	})

	s.logger.Info("starting block stream gRPC server")
	gs.Launch(s.listenAddr)
}
