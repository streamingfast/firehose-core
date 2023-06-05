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
	"github.com/spf13/cobra"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/dlauncher/launcher"
	"go.uber.org/zap"
)

func registerCommonFlags[B Block](chain *Chain[B]) {
	launcher.RegisterCommonFlags = func(_ *zap.Logger, cmd *cobra.Command) error {
		// Common stores configuration flags
		cmd.Flags().String("common-one-block-store-url", OneBlockStoreURL, "[COMMON] Store URL to read/write one-block files")
		cmd.Flags().String("common-merged-blocks-store-url", MergedBlocksStoreURL, "[COMMON] Store URL where to read/write merged blocks.")
		cmd.Flags().String("common-forked-blocks-store-url", ForkedBlocksStoreURL, "[COMMON] Store URL where to read/write forked block files that we want to keep.")
		cmd.Flags().String("common-live-blocks-addr", RelayerServingAddr, "[COMMON] gRPC endpoint to get real-time blocks.")

		cmd.Flags().String("common-index-store-url", IndexStoreURL, "[COMMON] Store URL where to read/write index files (if used on the chain).")
		cmd.Flags().IntSlice("common-index-block-sizes", []int{100000, 10000, 1000, 100}, "Index bundle sizes that that are considered valid when looking for block indexes")

		cmd.Flags().Bool("common-blocks-cache-enabled", false, cli.FlagDescription(`
			[COMMON] Use a disk cache to store the blocks data to disk and instead of keeping it in RAM. By enabling this, block's Protobuf content, in bytes,
			is kept on file system instead of RAM. This is done as soon the block is downloaded from storage. This is a tradeoff between RAM and Disk, if you
			are going to serve only a handful of concurrent requests, it's suggested to keep is disabled, if you encounter heavy RAM consumption issue, specially
			by the firehose component, it's definitely a good idea to enable it and configure it properly through the other 'common-blocks-cache-...' flags. The cache is
			split in two portions, one keeping N total bytes of blocks of the most recently used blocks and the other one keeping the N earliest blocks as
			requested by the various consumers of the cache.
		`))
		cmd.Flags().String("common-blocks-cache-dir", BlocksCacheDirectory, cli.FlagDescription(`
			[COMMON] Blocks cache directory where all the block's bytes will be cached to disk instead of being kept in RAM.
			This should be a disk that persists across restarts of the Firehose component to reduce the the strain on the disk
			when restarting and streams reconnects. The size of disk must at least big (with a 10%% buffer) in bytes as the sum of flags'
			value for  'common-blocks-cache-max-recent-entry-bytes' and 'common-blocks-cache-max-entry-by-age-bytes'.
		`))
		cmd.Flags().Int("common-blocks-cache-max-recent-entry-bytes", 21474836480, cli.FlagDescription(`
			[COMMON] Blocks cache max size in bytes of the most recently used blocks, after the limit is reached, blocks are evicted from the cache.
		`))
		cmd.Flags().Int("common-blocks-cache-max-entry-by-age-bytes", 21474836480, cli.FlagDescription(`
			[COMMON] Blocks cache max size in bytes of the earliest used blocks, after the limit is reached, blocks are evicted from the cache.
		`))

		cmd.Flags().Int("common-first-streamable-block", int(chain.FirstStreamableBlock), "[COMMON] First streamable block of the chain")

		// Authentication, metering and rate limiter plugins
		cmd.Flags().String("common-auth-plugin", "null://", "[COMMON] Auth plugin URI, see streamingfast/dauth repository")
		cmd.Flags().String("common-metering-plugin", "null://", "[COMMON] Metering plugin URI, see streamingfast/dmetering repository")

		// System Behavior
		cmd.Flags().Uint64("common-auto-mem-limit-percent", 0, "[COMMON] Automatically sets GOMEMLIMIT to a percentage of memory limit from cgroup (useful for container environments)")
		cmd.Flags().Bool("common-auto-max-procs", false, "[COMMON] Automatically sets GOMAXPROCS to max cpu available from cgroup (useful for container environments)")
		cmd.Flags().Duration("common-system-shutdown-signal-delay", 0, cli.FlagDescription(`
			[COMMON] Add a delay between receiving SIGTERM signal and shutting down apps.
			Apps will respond negatively to /healthz during this period
		`))
		return nil
	}
}
