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

package firecore

import (
	"fmt"
	"os"

	"github.com/mostynb/go-grpc-compression/zstd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/firehose/client"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/protobuf/types/known/anypb"
)

var toolsCmd = &cobra.Command{Use: "tools", Short: "Developer tools for operators and developers"}

func configureToolsCmd[B Block](
	chain *Chain[B],
) error {
	configureToolsCheckCmd(chain)
	configureToolsPrintCmd(chain)

	toolsCmd.AddCommand(newToolsDownloadFromFirehoseCmd(chain))
	toolsCmd.AddCommand(newToolsFirehoseClientCmd(chain))
	toolsCmd.AddCommand(newToolsFirehoseSingleBlockClientCmd(chain, rootLog, rootTracer))
	toolsCmd.AddCommand(newToolsFirehosePrometheusExporterCmd(chain, rootLog, rootTracer))

	if chain.Tools.MergedBlockUpgrader != nil {
		toolsCmd.AddCommand(NewToolsUpgradeMergedBlocksCmd(chain))
	}

	if chain.Tools.RegisterExtraCmd != nil {
		if err := chain.Tools.RegisterExtraCmd(chain, toolsCmd, rootLog, rootTracer); err != nil {
			return fmt.Errorf("registering extra tools command: %w", err)
		}
	}

	return nil
}

func addFirehoseClientFlagsToSet[B Block](flags *pflag.FlagSet, chain *Chain[B]) {
	flags.StringP("api-token-env-var", "a", "FIREHOSE_API_TOKEN", "Look for a JWT in this environment variable to authenticate against endpoint")
	flags.String("compression", "none", "The HTTP compression: use either 'none', 'gzip' or 'zstd'")
	flags.String("cursor", "", "Use this cursor with the request to resume your stream at the following block pointed by the cursor")
	flags.BoolP("plaintext", "p", false, "Use plaintext connection to Firehose")
	flags.BoolP("insecure", "k", false, "Use SSL connection to Firehose but skip SSL certificate validation")
	flags.Bool("final-blocks-only", false, "Only ask for final blocks")

	for flagName, transformFlag := range chain.Tools.TransformFlags {
		flags.String(flagName, "", transformFlag.Description)
	}
}

type firehoseRequestInfo struct {
	GRPCCallOpts    []grpc.CallOption
	Cursor          string
	FinalBlocksOnly bool
	Transforms      []*anypb.Any
}

func getFirehoseClientFromCmd[B Block](cmd *cobra.Command, endpoint string, chain *Chain[B]) (
	firehoseClient pbfirehose.StreamClient,
	connClose func() error,
	requestInfo *firehoseRequestInfo,
	err error,
) {
	requestInfo = &firehoseRequestInfo{}

	apiTokenEnvVar := sflags.MustGetString(cmd, "api-token-env-var")
	jwt := os.Getenv(apiTokenEnvVar)

	fmt.Println("JWT", jwt, apiTokenEnvVar)

	requestInfo.Cursor = sflags.MustGetString(cmd, "cursor")
	plaintext := sflags.MustGetBool(cmd, "plaintext")
	insecure := sflags.MustGetBool(cmd, "insecure")
	requestInfo.FinalBlocksOnly = sflags.MustGetBool(cmd, "final-blocks-only")

	firehoseClient, connClose, requestInfo.GRPCCallOpts, err = client.NewFirehoseClient(endpoint, jwt, insecure, plaintext)
	if err != nil {
		return nil, nil, nil, err
	}

	compression := sflags.MustGetString(cmd, "compression")

	var compressor grpc.CallOption
	switch compression {
	case "gzip":
		compressor = grpc.UseCompressor(gzip.Name)
	case "zstd":
		compressor = grpc.UseCompressor(zstd.Name)
	case "none":
		// Valid value but nothing to do
	default:
		err = fmt.Errorf("invalid value for compression: only 'gzip', 'zstd' or 'none' are accepted")
		return
	}

	if compressor != nil {
		requestInfo.GRPCCallOpts = append(requestInfo.GRPCCallOpts)
	}

	for flagName, transformFlag := range chain.Tools.TransformFlags {
		transformValue := sflags.MustGetString(cmd, flagName)
		if transformValue != "" {
			transform, err := transformFlag.Parser(transformValue)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("invalid value for %q: %w", flagName, err)
			}

			requestInfo.Transforms = append(requestInfo.Transforms, transform)
		}
	}

	return
}
