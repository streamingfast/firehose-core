package client

import (
	"testing"
)

func TestParseEndpointURL(t *testing.T) {
	tests := []struct {
		name                     string
		endpoint                 string
		initialPlaintext         bool
		expectedEndpoint         string
		expectedPlaintext        bool
	}{
		{
			name:              "http without port should add :80",
			endpoint:          "http://example.com",
			initialPlaintext:  false,
			expectedEndpoint:  "example.com:80",
			expectedPlaintext: true,
		},
		{
			name:              "http with port should keep the port",
			endpoint:          "http://example.com:8080",
			initialPlaintext:  false,
			expectedEndpoint:  "example.com:8080",
			expectedPlaintext: true,
		},
		{
			name:              "https without port should add :443",
			endpoint:          "https://example.com",
			initialPlaintext:  true,
			expectedEndpoint:  "example.com:443",
			expectedPlaintext: false,
		},
		{
			name:              "https with port should keep the port",
			endpoint:          "https://example.com:9443",
			initialPlaintext:  true,
			expectedEndpoint:  "example.com:9443",
			expectedPlaintext: false,
		},
		{
			name:              "plain endpoint without scheme should remain unchanged",
			endpoint:          "example.com:443",
			initialPlaintext:  false,
			expectedEndpoint:  "example.com:443",
			expectedPlaintext: false,
		},
		{
			name:              "plain endpoint without scheme should preserve plaintext setting",
			endpoint:          "example.com:443",
			initialPlaintext:  true,
			expectedEndpoint:  "example.com:443",
			expectedPlaintext: true,
		},
		{
			name:              "IP address http without port should add :80",
			endpoint:          "http://192.168.1.1",
			initialPlaintext:  false,
			expectedEndpoint:  "192.168.1.1:80",
			expectedPlaintext: true,
		},
		{
			name:              "IP address https without port should add :443",
			endpoint:          "https://192.168.1.1",
			initialPlaintext:  false,
			expectedEndpoint:  "192.168.1.1:443",
			expectedPlaintext: false,
		},
		{
			name:              "IP address with port should preserve port",
			endpoint:          "http://192.168.1.1:8080",
			initialPlaintext:  false,
			expectedEndpoint:  "192.168.1.1:8080",
			expectedPlaintext: true,
		},
		{
			name:              "IPv6 address with http should work",
			endpoint:          "http://[::1]:8080",
			initialPlaintext:  false,
			expectedEndpoint:  "[::1]:8080",
			expectedPlaintext: true,
		},
		{
			name:              "IPv6 address with https should work",
			endpoint:          "https://[::1]:9443",
			initialPlaintext:  false,
			expectedEndpoint:  "[::1]:9443",
			expectedPlaintext: false,
		},
		{
			name:              "localhost http without port should add :80",
			endpoint:          "http://localhost",
			initialPlaintext:  false,
			expectedEndpoint:  "localhost:80",
			expectedPlaintext: true,
		},
		{
			name:              "localhost https without port should add :443",
			endpoint:          "https://localhost",
			initialPlaintext:  false,
			expectedEndpoint:  "localhost:443",
			expectedPlaintext: false,
		},
		{
			name:              "short http prefix should not match",
			endpoint:          "http:/",
			initialPlaintext:  false,
			expectedEndpoint:  "http:/",
			expectedPlaintext: false,
		},
		{
			name:              "short https prefix should not match",
			endpoint:          "https:/",
			initialPlaintext:  true,
			expectedEndpoint:  "https:/",
			expectedPlaintext: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint, plaintext := parseEndpointURL(tt.endpoint, tt.initialPlaintext)

			if endpoint != tt.expectedEndpoint {
				t.Errorf("expected endpoint %q, got %q", tt.expectedEndpoint, endpoint)
			}

			if plaintext != tt.expectedPlaintext {
				t.Errorf("expected plaintext %v, got %v", tt.expectedPlaintext, plaintext)
			}
		})
	}
}
