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
	"fmt"
	"time"

	"github.com/streamingfast/bstream/blockstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/dauth"
	dauthgrpc "github.com/streamingfast/dauth/middleware/grpc"
	dgrpcserver "github.com/streamingfast/dgrpc/server"
	dgrpcfactory "github.com/streamingfast/dgrpc/server/factory"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/node-manager/mindreader"
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
	// GRPCSecretKey, when non-empty, requires every incoming gRPC call to present
	// the key as a Bearer token in the "authorization" metadata header.
	GRPCSecretKey string
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
	config             Config
	testModeComparator *mindreader.TestModeComparator
	zlogger            *zap.Logger
	tracer             logging.Tracer
}

func New(config Config, testModeComparator *mindreader.TestModeComparator, zlogger *zap.Logger, tracer logging.Tracer) *App {
	n := &App{
		Shutter:            shutter.New(),
		config:             config,
		testModeComparator: testModeComparator,
		zlogger:            zlogger,
		tracer:             tracer,
	}
	return n
}

func (a *App) Run() error {
	a.zlogger.Info("launching reader-node-firehose app (reading from firehose)",
		zap.Reflect("config", a.config),
		zap.Object("test_mode", a.testModeComparator),
	)
	appCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	a.OnTerminating(func(err error) { cancel(err) })

	defer func() {
		if err := a.testModeComparator.Close(); err != nil {
			a.zlogger.Warn("failed to close test mode comparator", zap.Error(err))
		}
	}()

	appWrapper := cli.NewApplication(appCtx)

	store, err := dstore.NewDBinStore(a.config.OneBlocksStoreURL)
	cli.NoError(err, "unable to create store")

	blockStreamServer := blockstream.NewUnmanagedServer(
		blockstream.ServerOptionWithLogger(a.zlogger),
		blockstream.ServerOptionWithBuffer(1),
	)

	syncer := &syncer{
		Shutter:            shutter.New(),
		appCtx:             appWrapper.Context(),
		blockStreamServer:  blockStreamServer,
		config:             a.config,
		firehoseConfig:     a.config.FirehoseConfig,
		logger:             a.zlogger,
		store:              store,
		testModeComparator: a.testModeComparator,
	}

	server := &server{
		Shutter:           shutter.New(),
		listenAddr:        a.config.GRPCAddr,
		blockStreamServer: blockStreamServer,
		secretKey:         a.config.GRPCSecretKey,
		healthCheck: func(ctx context.Context) (isReady bool, out interface{}, err error) {
			return AppReadiness.IsReady(), nil, nil
		},
		logger: a.zlogger,
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

func (a *App) IsReady() bool {
	return AppReadiness.IsReady()
}

type server struct {
	*shutter.Shutter
	listenAddr        string
	blockStreamServer *blockstream.Server
	secretKey         string
	healthCheck       dgrpcserver.HealthCheck
	logger            *zap.Logger
}

func (s *server) Run() {
	serverOpts := []dgrpcserver.Option{
		dgrpcserver.WithLogger(s.logger),
		dgrpcserver.WithHealthCheck(dgrpcserver.HealthCheckOverGRPC|dgrpcserver.HealthCheckOverHTTP, s.healthCheck),
	}

	if s.secretKey != "" {
		s.logger.Info("blockstream gRPC server secret key authentication enabled")

		auth, err := dauth.New("secret://"+s.secretKey, s.logger)
		if err != nil {
			// Run() has no error return; log fatally so the shutter catches it cleanly.
			s.logger.Error("failed to create blockstream authenticator, shutting down", zap.Error(err))
			s.Shutdown(fmt.Errorf("creating blockstream authenticator: %w", err))
			return
		}

		serverOpts = append(serverOpts,
			dgrpcserver.WithPostUnaryInterceptor(dauthgrpc.UnaryAuthChecker(auth, s.logger)),
			dgrpcserver.WithPostStreamInterceptor(dauthgrpc.StreamAuthChecker(auth, s.logger)),
		)
	}

	gs := dgrpcfactory.ServerFromOptions(serverOpts...)

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
