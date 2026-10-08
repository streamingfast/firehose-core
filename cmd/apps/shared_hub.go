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
// is not launched, when there is no live source, or when firehose drops partial
// blocks (--firehose-discard-partial-blocks), which tier1 needs.
func sharedLiveHub(runtime *launcher.Runtime) (*hub.ForkableHub, error) {
	if !slices.Contains(runtime.Apps, "firehose") || !slices.Contains(runtime.Apps, "substreams-tier1") {
		return nil, nil
	}

	blockStreamAddr := viper.GetString("common-live-blocks-addr")
	if blockStreamAddr == "" {
		return nil, nil
	}

	if viper.GetBool("firehose-discard-partial-blocks") {
		liveHubLogger.Info("firehose and substreams-tier1 keep separate hubs, firehose discards partial blocks and tier1 needs them")
		return nil, nil
	}

	sharedLiveHubOnce.Do(func() {
		_, oneBlocksStoreURL, _, err := firecore.GetCommonStoresURLs(runtime.AbsDataDir)
		if err != nil {
			sharedLiveHubErr = err
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
