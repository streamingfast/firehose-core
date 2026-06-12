package substreams

import (
	"fmt"
	"strings"

	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	networks "github.com/streamingfast/firehose-networks"
)

// ResolveEndpoint returns the endpoint to use: the explicit flag value if set,
// otherwise inferred from the k8s namespace via the networks registry. When
// inferred, a one-line note is printed to stdout for the user.
func ResolveEndpoint(flagValue, namespace string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if namespace == "" {
		return "", fmt.Errorf("could not infer endpoint: no namespace found in log entry; provide --endpoint explicitly")
	}

	endpoint := InferEndpointFromNamespace(namespace)
	if endpoint == "" {
		return "", fmt.Errorf(
			"could not infer Substreams endpoint from namespace %q; provide --endpoint explicitly (e.g. --endpoint mainnet.eth.streamingfast.io:443)",
			namespace,
		)
	}

	fmt.Printf("%s %s %s\n",
		stylex.Label("Endpoint (inferred):"),
		stylex.Value(endpoint),
		stylex.Dimf("(from namespace %q)", namespace),
	)
	return endpoint, nil
}

// InferEndpointFromNamespace tries to find a Substreams endpoint for the given k8s namespace.
// Namespaces are typically "<chain>-<network>" (e.g. "eth-polygon-mainnet").
// It progressively strips leading dash-separated segments until a match is found:
//
//	eth-polygon-mainnet → polygon-mainnet → mainnet
func InferEndpointFromNamespace(namespace string) string {
	for s := namespace; s != ""; {
		if ep := networks.GetSubstreamsEndpoint(s); ep != "" {
			return ep
		}
		_, rest, found := strings.Cut(s, "-")
		if !found {
			break
		}
		s = rest
	}
	return ""
}
