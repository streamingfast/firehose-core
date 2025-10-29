package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"

	"github.com/streamingfast/dgrpc"
	networks "github.com/streamingfast/firehose-networks"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"golang.org/x/oauth2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/credentials/oauth"
	"google.golang.org/grpc/metadata"
)

// parseEndpointURL parses HTTP/HTTPS URLs and extracts endpoint configuration
// then returns the processed endpoint and whether to use plaintext connection.
//
// It will also try to resolve endpoint via networks.GetFirehoseEndpoint
// (Networks Registry) which check both id and aliases. If there is a corresponding
// resolved endpoint, it is returned with plaintext set to false.
func parseEndpointURL(endpoint string, usePlainTextConnection bool) (string, bool) {
	// Check for http:// or https:// prefix and adjust settings accordingly
	if len(endpoint) > 7 && endpoint[:7] == "http://" {
		usePlainTextConnection = true
		parsedURL, err := url.Parse(endpoint)
		if err == nil && parsedURL.Port() == "" {
			// No port specified, append default port for HTTP
			endpoint = parsedURL.Host + ":80"
		} else if err == nil {
			// Port is already specified or there was an error, just strip the scheme
			endpoint = parsedURL.Host
		} else {
			// Fallback to simple stripping if parsing fails
			endpoint = endpoint[7:]
		}
	} else if len(endpoint) > 8 && endpoint[:8] == "https://" {
		usePlainTextConnection = false
		parsedURL, err := url.Parse(endpoint)
		if err == nil && parsedURL.Port() == "" {
			// No port specified, append default port for HTTPS
			endpoint = parsedURL.Host + ":443"
		} else if err == nil {
			// Port is already specified or there was an error, just strip the scheme
			endpoint = parsedURL.Host
		} else {
			// Fallback to simple stripping if parsing fails
			endpoint = endpoint[8:]
		}
	}

	if resolvedEndpoint := networks.GetFirehoseEndpoint(endpoint); resolvedEndpoint != "" {
		// Resolved endpoint from alias is always non-plaintext
		return resolvedEndpoint, false
	}

	return endpoint, usePlainTextConnection
}

// firehoseClient, closeFunc, grpcCallOpts, err := NewFirehoseClient(endpoint, jwt, insecure, plaintext)
// defer closeFunc()
// stream, err := firehoseClient.Blocks(context.Background(), request, grpcCallOpts...)
func NewFirehoseClient(endpoint, jwt, apiKey string, useInsecureTSLConnection, usePlainTextConnection bool) (cli pbfirehose.StreamClient, closeFunc func() error, callOpts []grpc.CallOption, err error) {

	// Parse endpoint URL and adjust plaintext setting if http:// or https:// prefix is used
	endpoint, usePlainTextConnection = parseEndpointURL(endpoint, usePlainTextConnection)

	if useInsecureTSLConnection && usePlainTextConnection {
		return nil, nil, nil, fmt.Errorf("option --insecure and --plaintext are mutually exclusive, they cannot be both specified at the same time")
	}

	var dialOptions []grpc.DialOption
	switch {
	case usePlainTextConnection:
		dialOptions = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	case useInsecureTSLConnection:
		dialOptions = []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true}))}
	}

	conn, err := dgrpc.NewExternalClientConn(endpoint, dialOptions...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to create external gRPC client: %w", err)
	}
	closeFunc = conn.Close
	cli = pbfirehose.NewStreamClient(conn)

	if !usePlainTextConnection {
		if jwt != "" {
			credentials := oauth.TokenSource{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: jwt})}
			callOpts = append(callOpts, grpc.PerRPCCredentials(credentials))
		} else if apiKey != "" {
			callOpts = append(callOpts, grpc.PerRPCCredentials(&ApiKeyAuth{ApiKey: apiKey}))
		}
	}

	return
}

type ApiKeyAuth struct {
	ApiKey string
}

func (a *ApiKeyAuth) GetRequestMetadata(ctx context.Context, uri ...string) (out map[string]string, err error) {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		md = metadata.New(nil)
	}
	out = make(map[string]string)
	for k, v := range md {
		if len(v) != 0 {
			out[k] = v[0]
		}
	}
	if a.ApiKey != "" {
		out["x-api-key"] = a.ApiKey
	}
	return
}

func (a *ApiKeyAuth) RequireTransportSecurity() bool {
	return true
}

func NewFirehoseFetchClient(endpoint, jwt, apiKey string, useInsecureTSLConnection, usePlainTextConnection bool) (cli pbfirehose.FetchClient, closeFunc func() error, callOpts []grpc.CallOption, err error) {

	// Parse endpoint URL and adjust plaintext setting if http:// or https:// prefix is used
	endpoint, usePlainTextConnection = parseEndpointURL(endpoint, usePlainTextConnection)

	if useInsecureTSLConnection && usePlainTextConnection {
		return nil, nil, nil, fmt.Errorf("option --insecure and --plaintext are mutually exclusive, they cannot be both specified at the same time")
	}

	var dialOptions []grpc.DialOption
	switch {
	case usePlainTextConnection:
		dialOptions = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	case useInsecureTSLConnection:
		dialOptions = []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{InsecureSkipVerify: true}))}
	}

	if !usePlainTextConnection {
		if jwt != "" {
			credentials := oauth.NewOauthAccess(&oauth2.Token{AccessToken: jwt, TokenType: "Bearer"})
			callOpts = append(callOpts, grpc.PerRPCCredentials(credentials))
		} else if apiKey != "" {
			callOpts = append(callOpts, grpc.PerRPCCredentials(&ApiKeyAuth{ApiKey: apiKey}))
		}
	}

	conn, err := dgrpc.NewExternalClient(endpoint, dialOptions...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("unable to create external gRPC client: %w", err)
	}
	closeFunc = conn.Close
	cli = pbfirehose.NewFetchClient(conn)

	return
}
