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
	"time"

	pbhealth "google.golang.org/grpc/health/grpc_health_v1"
)

// healthWatchInterval is how often Watch re-checks the status to stream changes
var healthWatchInterval = time.Second

// healthStatus is NOT_SERVING while the merger cannot make progress (hole in the
// merged-blocks files, or one-block files it cannot link), SERVING otherwise
func (m *Merger) healthStatus() pbhealth.HealthCheckResponse_ServingStatus {
	if m.blocked.Load() {
		return pbhealth.HealthCheckResponse_NOT_SERVING
	}
	return pbhealth.HealthCheckResponse_SERVING
}

// Check is basic GRPC Healthcheck
func (m *Merger) Check(ctx context.Context, in *pbhealth.HealthCheckRequest) (*pbhealth.HealthCheckResponse, error) {
	return &pbhealth.HealthCheckResponse{
		Status: m.healthStatus(),
	}, nil
}

func (m *Merger) List(ctx context.Context, in *pbhealth.HealthListRequest) (*pbhealth.HealthListResponse, error) {
	return &pbhealth.HealthListResponse{
		Statuses: map[string]*pbhealth.HealthCheckResponse{
			"merger": &pbhealth.HealthCheckResponse{
				Status: m.healthStatus(),
			},
		},
	}, nil
}

// Watch is basic GRPC Healthcheck as a stream, sending the status again each time it changes
func (m *Merger) Watch(req *pbhealth.HealthCheckRequest, stream pbhealth.Health_WatchServer) error {
	status := m.healthStatus()
	if err := stream.Send(&pbhealth.HealthCheckResponse{Status: status}); err != nil {
		return err
	}

	ticker := time.NewTicker(healthWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stream.Context().Done():
			return nil
		case <-ticker.C:
		}
		if current := m.healthStatus(); current != status {
			status = current
			if err := stream.Send(&pbhealth.HealthCheckResponse{Status: status}); err != nil {
				return err
			}
		}
	}
}
