package relayer

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// MinSourceRetryInterval must match the reconnect loop delay of bstream's MultiplexedSource.
const MinSourceRetryInterval = 5 * time.Second

// ParseSourceAddress parses a raw relayer source address, performing:
//  1. Environment variable interpolation: ${ENV_VAR_NAME} is replaced with os.Getenv("ENV_VAR_NAME").
//  2. Secret key extraction: if the address contains "?secret=<key>", the key is stripped from the
//     URL and stored separately in SourceAddr.SecretKey.
//  3. Retry interval extraction: if the address contains "?retry_interval=<duration>", the duration is stripped
//     from the URL and stored separately in SourceAddr.RetryInterval.
//
// Example inputs:
//
//	":10010"                          -> SourceAddr{URL: ":10010"}
//	":10010?secret=abc"               -> SourceAddr{URL: ":10010", SecretKey: "abc"}
//	"${HOST}:10010?secret=${SECRET}"  -> SourceAddr{URL: "<HOST>:10010", SecretKey: "<SECRET>"}
//	":10010?retry_interval=120s"      -> SourceAddr{URL: ":10010", RetryInterval: 120 * time.Second}
func ParseSourceAddress(raw string) (SourceAddr, error) {
	expanded := os.Expand(raw, os.Getenv)

	// We do not use net/url.Parse directly because the address portion
	// (e.g. "dns:///host:port" or ":10010") is not a valid URL on its own.
	addr, query, hasQuery := strings.Cut(expanded, "?")
	if addr == "" {
		return SourceAddr{}, fmt.Errorf("empty address in source %q (after env expansion: %q)", raw, expanded)
	}

	sa := SourceAddr{URL: addr}
	if !hasQuery {
		return sa, nil
	}

	vals, err := url.ParseQuery(query)
	if err != nil {
		return SourceAddr{}, fmt.Errorf("invalid query string in source %q: %w", expanded, err)
	}
	sa.SecretKey = vals.Get("secret")

	if retryInterval := vals.Get("retry_interval"); retryInterval != "" {
		sa.RetryInterval, err = time.ParseDuration(retryInterval)
		if err != nil {
			return SourceAddr{}, fmt.Errorf("invalid retry_interval in source %q: %w", expanded, err)
		}
		if sa.RetryInterval < MinSourceRetryInterval {
			return SourceAddr{}, fmt.Errorf("retry_interval in source %q is below the %s minimum", expanded, MinSourceRetryInterval)
		}
	}

	return sa, nil
}
