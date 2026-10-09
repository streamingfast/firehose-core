package index_builder

import (
	dgrpcfactory "github.com/streamingfast/dgrpc/server/factory"
	pbhealth "google.golang.org/grpc/health/grpc_health_v1"
)

func (app *IndexBuilder) startGRPCServer() {
	gs := dgrpcfactory.ServerFromOptions()
	gs.OnTerminated(app.Shutdown)
	app.logger.Info("grpc server created")

	app.OnTerminated(func(_ error) {
		gs.Shutdown(0)
	})
	pbhealth.RegisterHealthServer(gs.ServiceRegistrar(), app)
	app.logger.Info("server registered")

	go gs.Launch(app.grpcListenAddr)
}
