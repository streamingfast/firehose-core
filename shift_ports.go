package firecore

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ShiftAddressPort takes an address string in common formats and shifts the port
// number by the given offset. Supported formats:
//
//   - ":10010"            -> ":10110"  (offset=100)
//   - "localhost:6060"    -> "localhost:6160"  (offset=100)
//   - "0.0.0.0:9000"     -> "0.0.0.0:9100"  (offset=100)
//   - "dns:///host:10010" -> "dns:///host:10110"  (offset=100)
//
// Kubernetes-style addresses with dotted FQDN hostnames are also supported:
//
//   - "my-service.my-namespace.svc.cluster.local:10010" -> "...:10110"  (offset=100)
//   - "dns:///my-service.my-namespace.svc.cluster.local:10010" -> "dns:///...:10110"  (offset=100)
//   - "my-service.my-namespace:10010" -> "my-service.my-namespace:10110"  (offset=100)
func ShiftAddressPort(addr string, offset int) (string, error) {
	// Handle gRPC-style URIs like "dns:///host:port"
	if idx := strings.Index(addr, "://"); idx != -1 {
		scheme := addr[:idx+3]
		rest := addr[idx+3:]

		// Strip leading slashes after scheme (e.g., "dns:///host:port" -> "host:port")
		trimmed := strings.TrimLeft(rest, "/")
		slashPrefix := rest[:len(rest)-len(trimmed)]

		shifted, err := shiftHostPort(trimmed, offset)
		if err != nil {
			return "", err
		}

		return scheme + slashPrefix + shifted, nil
	}

	return shiftHostPort(addr, offset)
}

// shiftHostPort shifts the port in a "host:port" or ":port" string.
func shiftHostPort(hostPort string, offset int) (string, error) {
	host, portStr, err := net.SplitHostPort(hostPort)
	if err != nil {
		return "", fmt.Errorf("splitting host/port from %q: %w", hostPort, err)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", fmt.Errorf("parsing port %q: %w", portStr, err)
	}

	newPort := port + offset
	if newPort < 0 || newPort > 65535 {
		return "", fmt.Errorf("shifted port %d (from %d + %d) is out of valid range [0, 65535]", newPort, port, offset)
	}

	return net.JoinHostPort(host, strconv.Itoa(newPort)), nil
}
