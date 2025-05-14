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

package apps

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dmetering"
	firecore "github.com/streamingfast/firehose-core"
	info "github.com/streamingfast/firehose-core/firehose/info"
	"github.com/streamingfast/firehose-core/launcher"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	tracing "github.com/streamingfast/sf-tracing"
	"github.com/streamingfast/substreams/networks"
	"go.uber.org/zap"
	"golang.org/x/exp/slices"
)

var StartCmd = &cobra.Command{Use: "start", Args: cobra.ArbitraryArgs}

func ConfigureStartCmd[B firecore.Block](chain *firecore.Chain[B], binaryName string, rootLog *zap.Logger) {
	StartCmd.Short = fmt.Sprintf("Starts `%s` services all at once", binaryName)
	StartCmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
		cmd.SilenceUsage = true

		dataDir := sflags.MustGetString(cmd, "data-dir")
		rootLog.Debug(fmt.Sprintf("%s binary started", binaryName), zap.String("data_dir", dataDir))

		configFile := sflags.MustGetString(cmd, "config-file")
		rootLog.Info(fmt.Sprintf("starting Firehose on %s with config file '%s'", chain.LongName, configFile))

		err = start(cmd, dataDir, args, chain, rootLog)
		if err != nil {
			return fmt.Errorf("unable to launch: %w", err)
		}

		rootLog.Info("terminated")
		return
	}
}

func start[B firecore.Block](cmd *cobra.Command, dataDir string, args []string, chain *firecore.Chain[B], rootLog *zap.Logger) (err error) {
	dataDirAbs, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("unable to setup directory structure: %w", err)
	}

	err = firecore.MakeDirs([]string{dataDirAbs})
	if err != nil {
		return err
	}

	bstream.GetProtocolFirstStreamableBlock = sflags.MustGetUint64(cmd, "common-first-streamable-block")

	err = bstream.ValidateRegistry()
	if err != nil {
		return fmt.Errorf("protocol specific hooks not configured correctly: %w", err)
	}

	eventEmitter, err := GetCommonMeteringPlugin(cmd, rootLog)
	if err != nil {
		return fmt.Errorf("unable to initialize dmetering: %w", err)
	}
	defer func() {
		eventEmitter.Shutdown(nil)
	}()
	dmetering.SetDefaultEmitter(eventEmitter)

	chainName := sflags.MustGetString(cmd, "advertise-chain-name")
	aliases := sflags.MustGetStringSlice(cmd, "advertise-chain-aliases")
	blockIdEncoding := sflags.MustGetString(cmd, "advertise-block-id-encoding")

	blockIDEncodingEnum := pbfirehose.InfoResponse_BLOCK_ID_ENCODING_UNSET
	firstStreamableBlock := bstream.GetProtocolFirstStreamableBlock

	// If --advertise-chain-name is set, but any of the other advertise flags are not, fill them from the registry
	if cmd.Flags().Changed("advertise-chain-name") &&
		(!cmd.Flags().Changed("advertise-chain-aliases") ||
			!cmd.Flags().Changed("advertise-block-id-encoding")) {

		network := networks.GetRegistryNetworksWithFirehose().Find(chainName)
		if network != nil {
			if !cmd.Flags().Changed("advertise-chain-aliases") {
				aliases = network.Aliases
			}
			if !cmd.Flags().Changed("advertise-block-id-encoding") {
				blockIDEncodingEnum = info.BlockIdEncodingForNetwork(network)
			}
			firstStreamableBlock = uint64(network.Genesis.Height)
		}
	}

	if blockIdEncoding != "" && blockIDEncodingEnum == pbfirehose.InfoResponse_BLOCK_ID_ENCODING_UNSET {
		v, found := pbfirehose.InfoResponse_BlockIdEncoding_value[blockIdEncoding]
		if !found {
			longCandidate := "BLOCK_ID_ENCODING_" + strings.ToUpper(blockIdEncoding)
			v, found = pbfirehose.InfoResponse_BlockIdEncoding_value[longCandidate]
			if !found {
				return fmt.Errorf("invalid block id encoding: %s", blockIdEncoding)
			}
		}
		blockIDEncodingEnum = pbfirehose.InfoResponse_BlockIdEncoding(v)
	}

	infoServer := info.NewInfoServer(
		chainName,
		aliases,
		blockIDEncodingEnum,
		sflags.MustGetStringSlice(cmd, "advertise-block-features"),
		firstStreamableBlock,
		!sflags.MustGetBool(cmd, "ignore-advertise-validation"),
		chain.InfoResponseFiller,
		rootLog,
	)

	launch := launcher.NewLauncher(rootLog, dataDirAbs, infoServer)
	rootLog.Debug("launcher created")

	runByDefault := func(app string) bool {
		appsNotRunningByDefault := []string{"reader-node-stdin", "reader-node-firehose"}
		return !slices.Contains(appsNotRunningByDefault, app)
	}

	apps := launcher.ParseAppsFromArgs(args, runByDefault)
	if len(args) == 0 && launcher.Config != nil && launcher.Config["start"] != nil {
		apps = launcher.ParseAppsFromArgs(launcher.Config["start"].Args, runByDefault)
	}

	serviceName := "firecore"
	if len(apps) == 1 {
		serviceName = serviceName + "/" + apps[0]
	}
	if err := tracing.SetupOpenTelemetry(context.Background(), serviceName); err != nil {
		return err
	}

	rootLog.Info(fmt.Sprintf("launching applications: %s", strings.Join(apps, ",")))
	if err = launch.Launch(apps); err != nil {
		return err
	}

	signalHandler, hasBeenSignaled, _ := cli.SetupSignalHandler(sflags.MustGetDuration(cmd, "common-system-shutdown-signal-delay"), rootLog)

	// We need to pass the signal handler so that runtime.IsPendingShutdown() is properly
	// linked to the signal handler, otherwise, it will always return false.
	launch.SwitchHasBeenSignaledAtomic(hasBeenSignaled)

	select {
	case <-signalHandler:
		rootLog.Info("received termination signal, quitting")
		go launch.Close()
	case appID := <-launch.Terminating():
		if launch.Err() == nil {
			rootLog.Info(fmt.Sprintf("application %s triggered a clean shutdown, quitting", appID))
		} else {
			rootLog.Info(fmt.Sprintf("application %s shutdown unexpectedly, quitting", appID))
			err = launch.Err()
		}
	}

	launch.WaitForTermination()

	return
}
