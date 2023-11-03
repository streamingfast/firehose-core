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
	"go.uber.org/zap"
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

	toolsCmd.AddCommand(newToolsCompareBlocksCmd(chain))
	toolsCmd.AddCommand(newToolsDownloadFromFirehoseCmd(chain, rootLog))
	toolsCmd.AddCommand(newToolsFirehoseClientCmd(chain, rootLog))
	toolsCmd.AddCommand(newToolsFirehoseSingleBlockClientCmd(chain, rootLog, rootTracer))
	toolsCmd.AddCommand(newToolsFirehosePrometheusExporterCmd(chain, rootLog, rootTracer))
	toolsCmd.AddCommand(newToolsUnmergeBlocksCmd(chain, rootLog))

	if chain.Tools.MergedBlockUpgrader != nil {
		toolsCmd.AddCommand(NewToolsUpgradeMergedBlocksCmd(chain))
	}

	if chain.Tools.RegisterExtraCmd != nil {
		if err := chain.Tools.RegisterExtraCmd(chain, toolsCmd, rootLog, rootTracer); err != nil {
			return fmt.Errorf("registering extra tools command: %w", err)
		}
	}

	var walkCmd func(node *cobra.Command)
	walkCmd = func(node *cobra.Command) {
		hideGlobalFlagsOnChildCmd(node)
		for _, child := range node.Commands() {
			walkCmd(child)
		}
	}
	walkCmd(toolsCmd)

	return nil
}

func addFirehoseStreamClientFlagsToSet[B Block](flags *pflag.FlagSet, chain *Chain[B]) {
	addFirehoseFetchClientFlagsToSet(flags, chain)

	flags.String("cursor", "", "Use this cursor with the request to resume your stream at the following block pointed by the cursor")
}

func addFirehoseFetchClientFlagsToSet[B Block](flags *pflag.FlagSet, chain *Chain[B]) {
	flags.StringP("api-token-env-var", "a", "FIREHOSE_API_TOKEN", "Look for a JWT in this environment variable to authenticate against endpoint")
	flags.String("compression", "none", "The HTTP compression: use either 'none', 'gzip' or 'zstd'")
	flags.BoolP("plaintext", "p", false, "Use plaintext connection to Firehose")
	flags.BoolP("insecure", "k", false, "Use SSL connection to Firehose but skip SSL certificate validation")
	if chain.Tools.TransformFlags != nil {
		chain.Tools.TransformFlags.Register(flags)
	}
}

type firehoseRequestInfo struct {
	GRPCCallOpts    []grpc.CallOption
	Cursor          string
	FinalBlocksOnly bool
	Transforms      []*anypb.Any
}

func getFirehoseFetchClientFromCmd[B Block](cmd *cobra.Command, logger *zap.Logger, endpoint string, chain *Chain[B]) (
	firehoseClient pbfirehose.FetchClient,
	connClose func() error,
	requestInfo *firehoseRequestInfo,
	err error,
) {
	return getFirehoseClientFromCmd[B, pbfirehose.FetchClient](cmd, logger, "fetch-client", endpoint, chain)
}

func getFirehoseStreamClientFromCmd[B Block](cmd *cobra.Command, logger *zap.Logger, endpoint string, chain *Chain[B]) (
	firehoseClient pbfirehose.StreamClient,
	connClose func() error,
	requestInfo *firehoseRequestInfo,
	err error,
) {
	return getFirehoseClientFromCmd[B, pbfirehose.StreamClient](cmd, logger, "stream-client", endpoint, chain)
}

func getFirehoseClientFromCmd[B Block, C any](cmd *cobra.Command, logger *zap.Logger, kind string, endpoint string, chain *Chain[B]) (
	firehoseClient C,
	connClose func() error,
	requestInfo *firehoseRequestInfo,
	err error,
) {
	requestInfo = &firehoseRequestInfo{}

	jwt := os.Getenv(sflags.MustGetString(cmd, "api-token-env-var"))
	plaintext := sflags.MustGetBool(cmd, "plaintext")
	insecure := sflags.MustGetBool(cmd, "insecure")

	if sflags.FlagDefined(cmd, "cursor") {
		requestInfo.Cursor = sflags.MustGetString(cmd, "cursor")
	}

	if sflags.FlagDefined(cmd, "final-blocks-only") {
		requestInfo.FinalBlocksOnly = sflags.MustGetBool(cmd, "final-blocks-only")
	}

	var rawClient any
	if kind == "stream-client" {
		rawClient, connClose, requestInfo.GRPCCallOpts, err = client.NewFirehoseClient(endpoint, jwt, insecure, plaintext)
	} else if kind == "fetch-client" {
		rawClient, connClose, err = client.NewFirehoseFetchClient(endpoint, jwt, insecure, plaintext)
	} else {
		panic(fmt.Errorf("unsupported Firehose client kind: %s", kind))
	}

	if err != nil {
		return firehoseClient, nil, nil, err
	}

	firehoseClient = rawClient.(C)

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
		return firehoseClient, nil, nil, fmt.Errorf("invalid value for compression: only 'gzip', 'zstd' or 'none' are accepted")

	}

	if compressor != nil {
		requestInfo.GRPCCallOpts = append(requestInfo.GRPCCallOpts, compressor)
	}

	requestInfo.Transforms, err = chain.Tools.TransformFlags.Parse(cmd, logger)
	if err != nil {
		return firehoseClient, nil, nil, fmt.Errorf("unable to parse transforms flags: %w", err)
	}

	return
}
