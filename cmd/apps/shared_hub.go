package apps

import (
	"fmt"
	"slices"
	"sync"

	"github.com/spf13/viper"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/hub"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/launcher"
	coremetrics "github.com/streamingfast/firehose-core/metrics"
	"github.com/streamingfast/logging"
	ssapp "github.com/streamingfast/substreams/app"
)

var liveHubLogger, _ = logging.PackageLogger("live-hub", "github.com/streamingfast/firehose-core/live-hub")

var (
	sharedLiveHubOnce sync.Once
	sharedLiveHubHub  *hub.ForkableHub
	sharedLiveHubErr  error
)

// sharedLiveHub returns the forkable hub the firehose and substreams-tier1 apps
// share when both are launched in the process, so that live blocks are held in
// memory once. It is built and started on the first call.
//
// It returns nil, each app then building its own hub, when one of the two apps
// is not launched or when there is no live source.
func sharedLiveHub(runtime *launcher.Runtime) (*hub.ForkableHub, error) {
	if !slices.Contains(runtime.Apps, "firehose") || !slices.Contains(runtime.Apps, "substreams-tier1") {
		return nil, nil
	}

	blockStreamAddr := viper.GetString("common-live-blocks-addr")
	if blockStreamAddr == "" {
		return nil, nil
	}

	sharedLiveHubOnce.Do(func() {
		mergedBlocksStoreURL, oneBlocksStoreURL, _, err := firecore.GetCommonStoresURLs(runtime.AbsDataDir)
		if err != nil {
			sharedLiveHubErr = err
			return
		}

		mergedBlocksStore, err := dstore.NewDBinStore(mergedBlocksStoreURL)
		if err != nil {
			sharedLiveHubErr = fmt.Errorf("failed setting up merged-blocks store from url %q: %w", mergedBlocksStoreURL, err)
			return
		}

		oneBlocksStore, err := dstore.NewDBinStore(oneBlocksStoreURL)
		if err != nil {
			sharedLiveHubErr = fmt.Errorf("failed setting up one-block store from url %q: %w", oneBlocksStoreURL, err)
			return
		}

		liveHubLogger.Info("firehose and substreams-tier1 share one forkable hub")
		sharedLiveHubHub = ssapp.NewLiveHub(ssapp.LiveHubConfig{
			BlockStreamAddr:        blockStreamAddr,
			MergedBlocksBundleSize: bstream.DefaultMergedBlocksBundleSize,
			OneBlocksStore:         oneBlocksStore,
			MergedBlocksStore:      mergedBlocksStore,
			MaxKeptFinalBlocks:     viper.GetInt("common-live-hub-max-final-blocks"),
			MergedBlocksLagMetric:  coremetrics.NewMergedBlocksLag("firehose", "substreams-tier1"),
			Requester:              "firehose+substreams-tier1",
			Logger:                 liveHubLogger,
			OnBlock: func(blk *pbbstream.Block) {
				headBlockNumMetric.SetUint64(blk.Number)
				headTimeDriftmetric.SetBlockTime(blk.Time())
				finalizedBlockNumMetric.SetUint64(blk.LibNum)
				ss1HeadBlockNumMetric.SetUint64(blk.Number)
				ss1HeadTimeDriftmetric.SetBlockTime(blk.Time())
			},
		})
		go sharedLiveHubHub.Run()
	})

	return sharedLiveHubHub, sharedLiveHubErr
}
