// Copyright 2019 dfuse Platform Inc.
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

package relayer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/firehose"
	"github.com/stretchr/testify/require"
)

// TestFetchFirstStreamableBlockServedFromHub is a regression test for the
// `sf.firehose.v2.Fetch/Block` hang on a freshly started chain.
//
// On startup the chain has only produced a handful of blocks, so:
//   - the requested block (the first streamable block) is the hub's lowest
//     retained block and is present in the live hub, but
//   - no merged-blocks bundle has been flushed yet (the merger needs a full
//     bundle's worth of blocks).
//
// BlockGetter.Get used a strict `>` comparison against hub.LowestBlockNum(), so
// a request for exactly that block skipped the hub and fell through to the empty
// merged-blocks store, where the single-block fetcher retries a missing file
// forever — hanging the Fetch RPC with no response and no error.
//
// This test reproduces that state with a dummy-blockchain reader-node feeding a
// live hub, an EMPTY merged-blocks store and a nil forked store (so the only
// place the lowest block lives is the hub), and asserts that Get returns the
// block promptly instead of hanging.
func TestFetchFirstStreamableBlockServedFromHub(t *testing.T) {
	if os.Getenv("TEST_E2E") == "" && os.Getenv("CI") == "" {
		t.Skip("skipping e2e test; set TEST_E2E=1 or CI=1 to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tmpDir := t.TempDir()

	readerHostPort := findFreeHostPort(t)
	relayerListenAddr := fmt.Sprintf("localhost:%d", findFreeHostPort(t))
	t.Logf("reader-node host port : %d", readerHostPort)
	t.Logf("relayer listen addr   : %s", relayerListenAddr)

	// ── Start the reader-node container ──────────────────────────────────────
	blockchainDataDir := filepath.Join(tmpDir, "blockchain-data")
	require.NoError(t, os.MkdirAll(blockchainDataDir, 0755))
	oneBlockDataDir := blockchainDataDir + "/reader/storage/one-blocks"

	readerContainer := startReaderNodeContainer(t, ctx, tmpDir, blockchainDataDir, readerHostPort, false)
	t.Cleanup(func() {
		d := time.Duration(0)
		_ = readerContainer.Stop(context.Background(), &d)
		_ = readerContainer.Terminate(context.Background())
	})

	readerAddr := fmt.Sprintf("localhost:%d", readerHostPort)

	// ── Build a live hub fed by the reader-node (via a Relayer) ───────────────
	oneBlocksStore, err := dstore.NewDBinStore("file://" + oneBlockDataDir)
	require.NoError(t, err)

	liveSourceFactory := bstream.SourceFactory(func(h bstream.Handler) bstream.Source {
		return NewMultiplexedSource(h, []SourceAddr{{URL: readerAddr}}, 30*time.Second, 10)
	})

	r := NewRelayer(liveSourceFactory, relayerListenAddr, oneBlocksStore)
	go r.Run()
	t.Cleanup(func() { r.Shutdown(nil); <-r.Terminated() })

	t.Log("waiting for relayer hub to be ready…")
	select {
	case <-r.hub.Ready:
		t.Log("relayer hub is ready")
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for relayer hub to become ready")
	}

	// Wait until the hub has a lowest retained block and the head has advanced a
	// few blocks past it, so we are firmly in the "block in hub, no merged bundle
	// yet" window the bug requires.
	var lowest uint64
	require.Eventually(t, func() bool {
		lowest = r.hub.LowestBlockNum()
		return lowest > 0 && r.hub.HeadNum() > lowest+2
	}, 30*time.Second, 100*time.Millisecond, "hub did not advance past its lowest retained block")
	t.Logf("hub lowest retained block: %d, head: %d", lowest, r.hub.HeadNum())

	// ── Empty merged-blocks store, nil forked store ──────────────────────────
	// No bundle has been flushed: this is exactly the fresh-chain condition. With
	// no forked store either, the lowest block exists ONLY in the live hub.
	emptyMergedDir := filepath.Join(tmpDir, "merged-blocks-empty")
	require.NoError(t, os.MkdirAll(emptyMergedDir, 0755))
	mergedStore, err := dstore.NewDBinStore("file://" + emptyMergedDir)
	require.NoError(t, err)

	blockGetter := firehose.NewBlockGetter(mergedStore, nil, r.hub)

	// ── Fetch the first streamable block ─────────────────────────────────────
	// Run Get in a goroutine guarded by a timeout: the pre-fix bug hangs inside
	// the merged-store FileSource and does NOT honour context cancellation, so a
	// regression must be detected by the wall-clock guard rather than ctx.
	getCtx, getCancel := context.WithTimeout(ctx, 15*time.Second)
	defer getCancel()

	type result struct {
		blk *pbbstream.Block
		err error
	}
	done := make(chan result, 1)
	go func() {
		blk, err := blockGetter.Get(getCtx, lowest, "", zlog)
		done <- result{blk, err}
	}()

	select {
	case res := <-done:
		require.NoError(t, res.err, "Get on the first streamable block should succeed from the hub")
		require.NotNil(t, res.blk, "Get should return the first streamable block")
		require.Equal(t, lowest, res.blk.Number)
		t.Logf("served first streamable block %d from hub", res.blk.Number)
	case <-time.After(20 * time.Second):
		t.Fatal("BlockGetter.Get hung on the first streamable block — regression of the Fetch/Block hang")
	}
}
