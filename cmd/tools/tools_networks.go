// Copyright 2021 dfuse Platform Inc.
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

package tools

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	registry "github.com/pinax-network/graph-networks-libs/packages/golang/lib"
	. "github.com/streamingfast/cli"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	firecore "github.com/streamingfast/firehose-core"
	networks "github.com/streamingfast/firehose-networks"
	"go.uber.org/zap"
)

func NewToolsNetworksCmd[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) *cobra.Command {
	return ToCobraCmd(Group(
		"networks",
		"Networks registry related tools",

		Command(
			toolsNetworksListRunner(chain, logger),
			"list",
			"List registered networks with their Firehose & Substreams endpoints",
			Flags(func(flags *pflag.FlagSet) {
				flags.BoolP("name-only", "n", false, "Only print network names (one per line)")
				flags.String("only", "", "Filter networks using a regular expression (matches against ID, aliases, full name, and short name)")
			}),
			Description(`
				Lists all networks from The Graph Networks Registry that have Firehose support.

				By default, prints each network with a compact header showing its ID and aliases,
				followed by registered Firehose and Substreams endpoints.

				Use --name-only to print only the network IDs (useful for scripting).

				Use --only to filter networks by a regular expression. The pattern is matched against
				network IDs, aliases, full names, and short names. Examples:
				  --only="^eth"          # Networks starting with "eth"
				  --only="arbitrum"      # Networks containing "arbitrum"
				  --only="mainnet$"      # Networks ending with "mainnet"
			`),
		),
	))
}

func toolsNetworksListRunner[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) firecore.CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		nameOnly := sflags.MustGetBool(cmd, "name-only")
		onlyPattern := sflags.MustGetString(cmd, "only")

		networksRegistry := networks.GetRegistry()

		// Apply regex filter if provided
		var matchingNetworks []*registry.Network
		if onlyPattern != "" {
			re, err := regexp.Compile(onlyPattern)
			cli.NoError(err, "Invalid regular expression for --only flag: %q", onlyPattern)

			matchingNetworks = networksRegistry.Search(re)
		} else {
			// Collect all networks
			for _, network := range networksRegistry {
				matchingNetworks = append(matchingNetworks, network)
			}
		}

		// Sort networks by ID for consistent output
		sort.Slice(matchingNetworks, func(i, j int) bool {
			return matchingNetworks[i].ID < matchingNetworks[j].ID
		})

		if nameOnly {
			for _, network := range matchingNetworks {
				fmt.Println(network.ID)
			}
			return nil
		}

		// Print with full details
		for i, network := range matchingNetworks {
			if i > 0 {
				fmt.Println()
			}

			// Compact header: ID with aliases if available
			header := network.ID
			if len(network.Aliases) > 0 {
				header += fmt.Sprintf(" [%s]", strings.Join(network.Aliases, ", "))
			}
			fmt.Println(header)

			// Print Firehose endpoints
			if len(network.Services.Firehose) > 0 {
				fmt.Println("  Firehose:")
				for _, endpoint := range network.Services.Firehose {
					fmt.Printf("    - %s\n", endpoint)
				}
			} else {
				fmt.Println("  Firehose: (none)")
			}

			// Print Substreams endpoints
			if len(network.Services.Substreams) > 0 {
				fmt.Println("  Substreams:")
				for _, endpoint := range network.Services.Substreams {
					fmt.Printf("    - %s\n", endpoint)
				}
			} else {
				fmt.Println("  Substreams: (none)")
			}
		}

		return nil
	}
}
