package wellknown

import (
	"log"
	"slices"
	"sync"

	registry "github.com/pinax-network/graph-networks-libs/packages/golang/lib"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
)

var (
	registryNetworks     map[string]*registry.Network
	registryNetworksOnce sync.Once
)

// GetRegistryNetworks returns a map of network ID to *registry.Network, using Pinax's registry as primary source.
// Only networks with a Firehose endpoint are included.
// If fetching fails, it falls back to loading from a local JSON file ("TheGraphNetworksRegistry.json").
func GetRegistryNetworks() map[string]*registry.Network {
	registryNetworksOnce.Do(func() {
		reg, err := registry.FromLatestVersion()
		if err != nil {
			// Fallback: try to load from local file
			// TODO: Validate where to put the registry file
			reg, err = registry.FromFile("TheGraphNetworksRegistry.json")
			if err != nil {
				log.Fatalf("Failed to load registry from both network and file: %v", err)
			}
		}
		m := make(map[string]*registry.Network)
		for i, net := range reg.Networks {
			if len(net.Services.Firehose) > 0 {
				m[net.ID] = &reg.Networks[i]
			}
		}
		registryNetworks = m
		// Optionally add custom networks for testing
		addCustomNetwork()
	})
	return registryNetworks
}

// addCustomNetwork can be used to add a custom network to the registry map for testing or development.
// For now, this is a stub. To use, implement logic to add a *registry.Network to registryNetworks.
func addCustomNetwork() {
	// Example usage:
	// registryNetworks["customnet"] = &registry.Network{ID: "customnet", ...}
}

// ChainByGenesisBlock returns the *registry.Network whose genesis block matches the given blockNum and blockID (hash).
func ChainByGenesisBlock(blockNum uint64, blockID string) *registry.Network {
	for _, network := range GetRegistryNetworks() {
		if network.Genesis != nil &&
			uint64(network.Genesis.Height) == blockNum &&
			network.Genesis.Hash == blockID {
			return network
		}
	}
	return nil
}

// ChainByName returns the *registry.Network whose name or alias matches the given name.
func ChainByName(name string) *registry.Network {
	for _, network := range GetRegistryNetworks() {
		if network.ID == name || network.FullName == name || network.ShortName == name {
			return network
		}
		if slices.Contains(network.Aliases, name) {
			return network
		}
	}
	return nil
}

// BlockIdEncodingForNetwork returns the InfoResponse_BlockIdEncoding for a given network based on its Firehose.BytesEncoding.
func BlockIdEncodingForNetwork(network *registry.Network) pbfirehose.InfoResponse_BlockIdEncoding {
	if network == nil || network.Firehose == nil {
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_UNSET
	}
	switch network.Firehose.BytesEncoding {
	case "hex":
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_HEX
	case "0xhex":
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_0X_HEX
	case "base58":
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_BASE58
	case "base64":
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_BASE64
	case "base64url":
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_BASE64URL
	default:
		return pbfirehose.InfoResponse_BLOCK_ID_ENCODING_UNSET
	}
}
