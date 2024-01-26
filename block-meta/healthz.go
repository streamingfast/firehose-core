package blockmeta

import (
	"context"

	pbhealth "google.golang.org/grpc/health/grpc_health_v1"
)

// Check is basic GRPC Healthcheck
func (app *Indexer) Check(ctx context.Context, in *pbhealth.HealthCheckRequest) (*pbhealth.HealthCheckResponse, error) {
	status := pbhealth.HealthCheckResponse_SERVING
	return &pbhealth.HealthCheckResponse{
		Status: status,
	}, nil
}

// Watch is basic GRPC Healthcheck as a stream
func (app *Indexer) Watch(req *pbhealth.HealthCheckRequest, stream pbhealth.Health_WatchServer) error {
	err := stream.Send(&pbhealth.HealthCheckResponse{
		Status: pbhealth.HealthCheckResponse_SERVING,
	})
	if err != nil {
		return err
	}

	<-stream.Context().Done()
	return nil
}
