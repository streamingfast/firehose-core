package apps

import (
	"sync"
	"time"

	"github.com/spf13/cobra"
)

var registerSSOnce sync.Once

func registerCommonSubstreamsFlags(cmd *cobra.Command) {
	registerSSOnce.Do(func() {
		cmd.Flags().Uint64("substreams-state-bundle-size", uint64(1_000), "Interval in blocks at which to save store snapshots and output caches")
		cmd.Flags().String("substreams-state-store-url", "{sf-data-dir}/localdata", "where substreams state data are stored")
		cmd.Flags().String("substreams-state-store-default-tag", "", "If non-empty, will be appended to {substreams-state-store-url} (ex: 'v1'). Can be overriden per-request with 'X-Substreams-Cache-Tag' header")
		cmd.Flags().Duration("substreams-block-execution-timeout", 3*time.Minute, "Maximum execution time for a block before the request is canceled")
		cmd.Flags().String("substreams-stores-scratch-space", "{sf-data-dir}/substreams/stores-scratch", "Local directory used as scratch space for store KV backends (e.g. mmap files)")
		cmd.Flags().String("substreams-stores-backend", "memory", "KV backend to use for FullKV stores: 'memory' (default, Go heap) or 'mmap' (bbolt-backed, opt-in)")
	})
}
