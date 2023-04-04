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
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dlauncher/launcher"
	"go.uber.org/zap"
)

var startCmd = &cobra.Command{Use: "start", Args: cobra.ArbitraryArgs}

func configureStartCmd(chain *Chain) {
	binaryName := chain.BinaryName()

	startCmd.Short = fmt.Sprintf("Starts `%s` services all at once", binaryName)
	startCmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
		cmd.SilenceUsage = true

		dataDir := viper.GetString("global-data-dir")
		rootLog.Debug(fmt.Sprintf("%s binary started", binaryName), zap.String("data_dir", dataDir))

		configFile := viper.GetString("global-config-file")
		rootLog.Info(fmt.Sprintf("starting Firehose on %s with config file '%s'", chain.LongName, configFile))

		err = start(chain, dataDir, args)
		if err != nil {
			return fmt.Errorf("unable to launch: %w", err)
		}

		rootLog.Info("terminated")
		return
	}
}

func start(chain *Chain, dataDir string, args []string) (err error) {
	dataDirAbs, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("unable to setup directory structure: %w", err)
	}

	err = makeDirs([]string{dataDirAbs})
	if err != nil {
		return err
	}

	tracker := bstream.NewTracker(chain.BlockDifferenceThresholdConsideredNear)

	modules := &launcher.Runtime{
		AbsDataDir: dataDirAbs,
		Tracker:    tracker,
	}

	blocksCacheEnabled := viper.GetBool("common-blocks-cache-enabled")
	if blocksCacheEnabled {
		bstream.GetBlockPayloadSetter = bstream.ATMCachedPayloadSetter

		cacheDir := MustReplaceDataDir(modules.AbsDataDir, viper.GetString("common-blocks-cache-dir"))
		storeUrl := MustReplaceDataDir(modules.AbsDataDir, viper.GetString("common-merged-blocks-store-url"))
		maxRecentEntryBytes := viper.GetInt("common-blocks-cache-max-recent-entry-bytes")
		maxEntryByAgeBytes := viper.GetInt("common-blocks-cache-max-entry-by-age-bytes")
		bstream.InitCache(storeUrl, cacheDir, maxRecentEntryBytes, maxEntryByAgeBytes)
	}

	bstream.GetProtocolFirstStreamableBlock = uint64(viper.GetInt("common-first-streamable-block"))

	err = bstream.ValidateRegistry()
	if err != nil {
		return fmt.Errorf("protocol specific hooks not configured correctly: %w", err)
	}

	launch := launcher.NewLauncher(rootLog, modules)
	rootLog.Debug("launcher created")

	runByDefault := func(app string) bool { return true }

	apps := launcher.ParseAppsFromArgs(args, runByDefault)
	if len(args) == 0 {
		apps = launcher.ParseAppsFromArgs(launcher.Config["start"].Args, runByDefault)
	}
	rootLog.Info(fmt.Sprintf("launching applications: %s", strings.Join(apps, ",")))
	if err = launch.Launch(apps); err != nil {
		return err
	}

	signalHandler := setupSignalHandler(viper.GetDuration("common-system-shutdown-signal-delay"))
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
