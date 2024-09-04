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
	"fmt"
	"github.com/streamingfast/bstream/blockstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	dgrpcserver "github.com/streamingfast/dgrpc/server"
	dgrpcfactory "github.com/streamingfast/dgrpc/server/factory"
	nodeManager "github.com/streamingfast/firehose-core/node-manager"
	"github.com/streamingfast/firehose-core/node-manager/mindreader"
	"github.com/streamingfast/logging"
	pbheadinfo "github.com/streamingfast/pbgo/sf/headinfo/v1"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

type Config struct {
	GRPCAddr                   string
	OneBlocksStoreURL          string
	OneBlockSuffix             string
	MindReadBlocksChanCapacity int
	StartBlockNum              uint64
	StopBlockNum               uint64
	WorkingDir                 string
	LogToZap                   bool
	DebugDeepMind              bool
	FirehoseConfig             FirehoseConfig
}

type FirehoseConfig struct {
	Endpoint      string
	StateFile     string
	PlaintextConn bool
	InsecureConn  bool
	ApiKey        string
	Jwt           string
	Compression   string
}

type Modules struct {
	MetricsAndReadinessManager *nodeManager.MetricsAndReadinessManager
	RegisterGRPCService        func(server grpc.ServiceRegistrar) error
}

type App struct {
	*shutter.Shutter
	Config    *Config
	ReadyFunc func()
	modules   *Modules
	zlogger   *zap.Logger
	tracer    logging.Tracer
}

func New(c *Config, modules *Modules, zlogger *zap.Logger, tracer logging.Tracer) *App {
	n := &App{
		Shutter:   shutter.New(),
		Config:    c,
		ReadyFunc: func() {},
		modules:   modules,
		zlogger:   zlogger,
		tracer:    tracer,
	}
	return n
}

func (a *App) Run() error {
	a.zlogger.Info("launching reader-node-firehose app (reading from firehose)", zap.Reflect("config", a.Config))

	gs := dgrpcfactory.ServerFromOptions(dgrpcserver.WithLogger(a.zlogger))

	blockStreamServer := blockstream.NewUnmanagedServer(
		blockstream.ServerOptionWithLogger(a.zlogger),
		blockstream.ServerOptionWithBuffer(1),
	)

	firehoseReader, err := NewFirehoseReader(a.Config.FirehoseConfig, a.zlogger)
	if err != nil {
		return err
	}

	a.zlogger.Info("launching reader log plugin")
	mindreaderLogPlugin, err := mindreader.NewMindReaderPlugin(
		a.Config.OneBlocksStoreURL,
		a.Config.WorkingDir,
		firehoseReader.NoopConsoleReader,
		a.Config.StartBlockNum,
		a.Config.StopBlockNum,
		a.Config.MindReadBlocksChanCapacity,
		a.modules.MetricsAndReadinessManager.UpdateHeadBlock,
		func(_ error) {},
		a.Config.OneBlockSuffix,
		blockStreamServer,
		a.zlogger,
		a.tracer,
	)
	if err != nil {
		return err
	}

	a.zlogger.Debug("configuring shutter")
	mindreaderLogPlugin.OnTerminated(a.Shutdown)
	a.OnTerminating(mindreaderLogPlugin.Shutdown)

	serviceRegistrar := gs.ServiceRegistrar()
	pbheadinfo.RegisterHeadInfoServer(serviceRegistrar, blockStreamServer)
	pbbstream.RegisterBlockStreamServer(serviceRegistrar, blockStreamServer)

	if a.modules.RegisterGRPCService != nil {
		err := a.modules.RegisterGRPCService(gs.ServiceRegistrar())
		if err != nil {
			return fmt.Errorf("register extra grpc service: %w", err)
		}
	}
	gs.OnTerminated(a.Shutdown)
	go gs.Launch(a.Config.GRPCAddr)

	a.zlogger.Debug("launching firehose reader")
	err = firehoseReader.Launch(a.Config.StartBlockNum, a.Config.StopBlockNum, a.Config.FirehoseConfig.StateFile)
	if err != nil {
		return err
	}

	a.zlogger.Debug("running reader log plugin")
	mindreaderLogPlugin.Launch()
	go a.modules.MetricsAndReadinessManager.Launch()

	return nil
}

func (a *App) OnReady(f func()) {
	a.ReadyFunc = f
}

func (a *App) IsReady() bool {
	return true
}
