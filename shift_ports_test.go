package firecore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShiftAddressPort(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		offset   int
		expected string
	}{
		{
			name:     "port only, positive offset",
			addr:     ":10010",
			offset:   100,
			expected: ":10110",
		},
		{
			name:     "port only, negative offset",
			addr:     ":10010",
			offset:   -10,
			expected: ":10000",
		},
		{
			name:     "localhost with port",
			addr:     "localhost:6060",
			offset:   100,
			expected: "localhost:6160",
		},
		{
			name:     "ip with port",
			addr:     "0.0.0.0:9000",
			offset:   200,
			expected: "0.0.0.0:9200",
		},
		{
			name:     "dns scheme with triple slash",
			addr:     "dns:///myhost:10010",
			offset:   100,
			expected: "dns:///myhost:10110",
		},
		{
			name:     "offset of zero",
			addr:     ":10010",
			offset:   0,
			expected: ":10010",
		},
		{
			name:     "large offset",
			addr:     ":10000",
			offset:   50000,
			expected: ":60000",
		},
		{
			name:     "index builder default port",
			addr:     ":10009",
			offset:   100,
			expected: ":10109",
		},
		{
			name:     "reader node grpc default port",
			addr:     ":10010",
			offset:   100,
			expected: ":10110",
		},
		{
			name:     "reader node manager default port",
			addr:     ":10011",
			offset:   100,
			expected: ":10111",
		},
		{
			name:     "merger default port",
			addr:     ":10012",
			offset:   100,
			expected: ":10112",
		},
		{
			name:     "relayer default port",
			addr:     ":10014",
			offset:   100,
			expected: ":10114",
		},
		{
			name:     "firehose grpc default port",
			addr:     ":10015",
			offset:   100,
			expected: ":10115",
		},
		{
			name:     "substreams tier1 default port",
			addr:     ":10016",
			offset:   100,
			expected: ":10116",
		},
		{
			name:     "substreams tier2 default port",
			addr:     ":10017",
			offset:   100,
			expected: ":10117",
		},

		// Kubernetes-style FQDN addresses
		{
			name:     "k8s cross-namespace FQDN",
			addr:     "my-service.my-namespace.svc.cluster.local:10010",
			offset:   100,
			expected: "my-service.my-namespace.svc.cluster.local:10110",
		},
		{
			name:     "k8s same-namespace short name",
			addr:     "my-service:10010",
			offset:   100,
			expected: "my-service:10110",
		},
		{
			name:     "k8s namespace-qualified name",
			addr:     "my-service.my-namespace:10010",
			offset:   100,
			expected: "my-service.my-namespace:10110",
		},
		{
			name:     "dns scheme with k8s FQDN",
			addr:     "dns:///my-service.my-namespace.svc.cluster.local:10010",
			offset:   100,
			expected: "dns:///my-service.my-namespace.svc.cluster.local:10110",
		},
		{
			name:     "dns scheme with k8s short name",
			addr:     "dns:///my-service:10010",
			offset:   100,
			expected: "dns:///my-service:10110",
		},
		{
			name:     "dns scheme with k8s namespace-qualified name",
			addr:     "dns:///my-service.my-namespace:10010",
			offset:   100,
			expected: "dns:///my-service.my-namespace:10110",
		},

		// Infrastructure port defaults
		{
			name:     "metrics listen addr default",
			addr:     ":9102",
			offset:   100,
			expected: ":9202",
		},
		{
			name:     "pprof listen addr default",
			addr:     "localhost:6060",
			offset:   100,
			expected: "localhost:6160",
		},
		{
			name:     "log-level-switcher listen addr default",
			addr:     "localhost:1065",
			offset:   100,
			expected: "localhost:1165",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ShiftAddressPort(tt.addr, tt.offset)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestShiftAddressPort_Errors(t *testing.T) {
	tests := []struct {
		name        string
		addr        string
		offset      int
		expectedErr string
	}{
		{
			name:        "port exceeds max",
			addr:        ":60000",
			offset:      10000,
			expectedErr: "shifted port 70000 (from 60000 + 10000) is out of valid range [0, 65535]",
		},
		{
			name:        "port goes negative",
			addr:        ":100",
			offset:      -200,
			expectedErr: "shifted port -100 (from 100 + -200) is out of valid range [0, 65535]",
		},
		{
			name:        "invalid address format",
			addr:        "notanaddress",
			offset:      100,
			expectedErr: "splitting host/port from",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ShiftAddressPort(tt.addr, tt.offset)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

func TestShiftHostPort(t *testing.T) {
	tests := []struct {
		name     string
		hostPort string
		offset   int
		expected string
	}{
		{
			name:     "empty host with port",
			hostPort: ":8080",
			offset:   10,
			expected: ":8090",
		},
		{
			name:     "named host with port",
			hostPort: "myservice:9090",
			offset:   -90,
			expected: "myservice:9000",
		},
		{
			name:     "port zero",
			hostPort: ":0",
			offset:   100,
			expected: ":100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := shiftHostPort(tt.hostPort, tt.offset)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
