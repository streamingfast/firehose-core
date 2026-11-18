package merger

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/stretchr/testify/require"
	pbhealth "google.golang.org/grpc/health/grpc_health_v1"
)

func TestHealthz_Check(t *testing.T) {
	ctx := context.Background()
	m := NewMerger(
		testLogger,
		"6969",
		nil,
		1,
		100,
		100,
		100,
		time.Second,
		time.Second,
		0,
		1,
	)
	request := &pbhealth.HealthCheckRequest{}
	resp, err := m.Check(ctx, request)
	if err != nil {
		panic(err)
	}

	require.Equal(t, resp.Status, pbhealth.HealthCheckResponse_SERVING)
}

func TestHealthz_CheckNotServingWhenBlocked(t *testing.T) {
	m := newRunTestMerger(&TestMergerIO{}, 0, 10, 1)

	m.setBlocked(true)
	resp, err := m.Check(context.Background(), &pbhealth.HealthCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, pbhealth.HealthCheckResponse_NOT_SERVING, resp.Status)

	m.setBlocked(false)
	resp, err = m.Check(context.Background(), &pbhealth.HealthCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, pbhealth.HealthCheckResponse_SERVING, resp.Status)
}

// TestMergerRun_UnlinkableGapReportsUnhealthyUntilFilled verifies that a walk cut short by
// the unlinkable-blocks circuit breaker marks the merger unhealthy, and that it reports
// healthy again once the missing one-block files show up and the walk completes.
func TestMergerRun_UnlinkableGapReportsUnhealthyUntilFilled(t *testing.T) {
	const bundleSize = uint64(10) // maxUnlinkableBlocks = 40

	var gapBlocks []*bstream.OneBlockFile
	gapBlocks = append(gapBlocks, buildChain(10, 19, nil)...)
	gapBlocks = append(gapBlocks, buildChain(61, 120, nil)...) // 41+ unlinkable, trips errCheckLoop
	filledBlocks := buildChain(10, 120, nil)

	var blockedBeforeWalk []bool
	var merger *Merger
	testIO := &TestMergerIO{
		NextBundleFunc: func(_ context.Context, lowestBaseBlock uint64) (uint64, bstream.BlockRef, error) {
			if lowestBaseBlock < 10 {
				return 10, libRef(9), nil
			}
			return lowestBaseBlock, nil, nil
		},
		WalkOneBlockFilesFunc: func(_ context.Context, inclusiveLowerBlock uint64, callback func(*bstream.OneBlockFile) error) error {
			blockedBeforeWalk = append(blockedBeforeWalk, merger.blocked.Load())
			blocks := gapBlocks
			switch len(blockedBeforeWalk) {
			case 1, 2:
			case 3:
				blocks = filledBlocks
			default:
				merger.Shutdown(nil)
				return nil
			}
			for _, blk := range blocks {
				if blk.Num < inclusiveLowerBlock {
					continue
				}
				if err := callback(blk); err != nil {
					return err
				}
			}
			return nil
		},
		MergeAndStoreFunc: func(_ context.Context, _ uint64, _ []*bstream.OneBlockFile) error {
			return nil
		},
	}

	merger = newRunTestMerger(testIO, 0, bundleSize, 1)
	require.NoError(t, merger.run())
	merger.bundler.WaitForMerges()

	// walks 1 and 2 trip the breaker, walk 3 sees the gap filled
	require.Equal(t, []bool{false, true, true, false}, blockedBeforeWalk)
}

// TestMergerRun_MergedBlocksHoleReportsUnhealthy verifies that a hole in the merged-blocks
// files marks the merger unhealthy until NextBundle stops reporting it.
func TestMergerRun_MergedBlocksHoleReportsUnhealthy(t *testing.T) {
	var blockedBeforeNextBundle []bool
	var merger *Merger
	testIO := &TestMergerIO{
		NextBundleFunc: func(_ context.Context, lowestBaseBlock uint64) (uint64, bstream.BlockRef, error) {
			blockedBeforeNextBundle = append(blockedBeforeNextBundle, merger.blocked.Load())
			switch len(blockedBeforeNextBundle) {
			case 1:
				return 10, libRef(9), fmt.Errorf("%w: merged blocks skip from 10 to 30", ErrHoleFound)
			case 2:
				return 10, nil, nil
			default:
				merger.Shutdown(nil)
				return 10, nil, nil
			}
		},
		WalkOneBlockFilesFunc: func(_ context.Context, _ uint64, _ func(*bstream.OneBlockFile) error) error {
			return nil
		},
	}

	merger = newRunTestMerger(testIO, 0, 10, 1)
	require.NoError(t, merger.run())

	require.Equal(t, []bool{false, true, false}, blockedBeforeNextBundle)
}
