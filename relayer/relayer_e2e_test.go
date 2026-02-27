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
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	dockercontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dgrpc"
	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/relayer/metrics"
	"github.com/streamingfast/logging"
	pbheadinfo "github.com/streamingfast/pbgo/sf/headinfo/v1"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap/zapcore"
)

const dummyBlockchainImage = "ghcr.io/streamingfast/dummy-blockchain:762f40d"

func init() {
	logging.InstantiateLoggers(logging.WithDefaultLevel(zapcore.InfoLevel))
	dmetrics.Register(metrics.MetricSet)
}

// TestRelayerReconnectsAfterSourceRestart verifies that a Relayer reconnects
// to the upstream reader-node after it goes down and comes back up, and that
// downstream consumers continue receiving blocks without having to reconnect
// themselves.
//
// Scenario:
//  1. Start a dummy-blockchain container running only the reader-node component.
//  2. Create a NewRelayer that connects to the reader-node and serves blocks.
//  3. A test subscriber connects to the relayer and counts received blocks.
//  4. Stop the dummy-blockchain container for a few seconds.
//  5. Restart the dummy-blockchain container on the same host port.
//  6. Wait up to 15 s and assert that blocks are flowing again.
func TestRelayerReconnectsAfterSourceRestart(t *testing.T) {
	if os.Getenv("TEST_E2E") == "" && os.Getenv("CI") == "" {
		t.Skip("skipping e2e test; set TEST_E2E=1 or CI=1 to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// ── 1. Temporary storage directory ──────────────────────────────────────
	tmpDir, err := os.MkdirTemp("", "firehose-relayer-e2e-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	// ── 2. Pick free host ports ──────────────────────────────────────────────
	// The reader-node gRPC host port is fixed so that after stop+start the
	// relayer can reconnect to the exact same address without reconfiguration.
	readerHostPort := findFreeHostPort(t)
	discardHostPort := findFreeHostPort(t)
	relayerListenAddr := fmt.Sprintf("localhost:%d", findFreeHostPort(t))

	t.Logf("reader-node host port : %d", readerHostPort)
	t.Logf("discard host port     : %d", discardHostPort)
	t.Logf("relayer listen addr   : %s", relayerListenAddr)

	// ── 3. Start the reader-node container ──────────────────────────────────
	// blockchainDataDir is mounted as /data inside the container (the
	// dummy-blockchain --store-dir) so that the chain state survives a
	// stop+start cycle and the node resumes from the same height.
	blockchainDataDir := tmpDir + "/blockchain-data"
	require.NoError(t, os.MkdirAll(blockchainDataDir, 0755))

	oneBlockDataDir := blockchainDataDir + "/reader/storage/one-blocks"

	readerContainer := startReaderNodeContainer(t, ctx, tmpDir, blockchainDataDir, readerHostPort, false)
	t.Cleanup(func() {
		d := time.Duration(0)
		_ = readerContainer.Stop(context.Background(), &d)
		_ = readerContainer.Terminate(context.Background())
	})

	readerAddr := fmt.Sprintf("localhost:%d", readerHostPort)
	t.Logf("reader-node gRPC addr : %s", readerAddr)

	// ── 4. Build and start the Relayer ───────────────────────────────────────
	oneBlocksStore, err := dstore.NewDBinStore("file://" + oneBlockDataDir)
	require.NoError(t, err)

	// Wrap newMultiplexedSourceWithDelay into a bstream.SourceFactory so the hub can
	// recreate the source on reconnect without knowing the addresses.
	// newMultiplexedSourceWithDelay is a test-local variant of NewMultiplexedSource
	// that includes a time.Sleep to simulate slow source initialisation.
	liveSourceFactory := bstream.SourceFactory(func(h bstream.Handler) bstream.Source {
		return NewMultiplexedSource(h, []string{readerAddr}, 30*time.Second, 10)
	})

	r := NewRelayer(liveSourceFactory, relayerListenAddr, oneBlocksStore)

	go r.Run()
	t.Cleanup(func() { r.Shutdown(nil); <-r.Terminated() })

	// Wait for the hub to be ready (it signals by closing hub.Ready).
	t.Log("waiting for relayer hub to be ready…")
	select {
	case <-r.hub.Ready:
		t.Log("relayer hub is ready")
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for relayer hub to become ready")
	}

	// ── 5. Connect a long-lived subscriber to the relayer ────────────────────
	conn, err := dgrpc.NewInternalNoWaitClientConn(relayerListenAddr)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	streamClient := pbbstream.NewBlockStreamClient(conn)
	headInfoClient := pbheadinfo.NewHeadInfoClient(conn)

	var blocksReceived atomic.Int64

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	streamErrCh := make(chan error, 1)
	go func() {
		stream, err := streamClient.Blocks(streamCtx, &pbbstream.BlockRequest{
			Burst:     -1, // start from LIB so we get blocks immediately
			Requester: "e2e-test",
		})
		if err != nil {
			streamErrCh <- fmt.Errorf("Blocks() RPC failed: %w", err)
			return
		}
		for {
			_, err := stream.Recv()
			if err != nil {
				streamErrCh <- err
				return
			}
			blocksReceived.Add(1)
		}
	}()

	// ── 6. Let blocks flow for 2 seconds ─────────────────────────────────────
	time.Sleep(2 * time.Second)

	blocksBeforeStop := blocksReceived.Load()
	require.Greater(t, blocksBeforeStop, int64(0),
		"should have received blocks from the reader-node before stopping it")

	// Snapshot the hub's head block number before stopping the container.
	headInfoBeforeStop, err := headInfoClient.GetHeadInfo(ctx, &pbheadinfo.HeadInfoRequest{})
	require.NoError(t, err, "GetHeadInfo should succeed while reader-node is running")
	t.Logf("hub head block before stop: num=%d id=%s", headInfoBeforeStop.HeadNum, headInfoBeforeStop.HeadID)
	require.Greater(t, headInfoBeforeStop.HeadNum, uint64(0),
		"hub head block number should be positive before stopping the container")

	// ── 7. Stop the container ────────────────────────────────────────────────
	t.Log("stopping reader-node container…")
	stopTimeout := time.Duration(0)
	require.NoError(t, readerContainer.Stop(ctx, &stopTimeout))
	t.Log("reader-node container stopped")

	// Give the relayer a moment to detect the lost connection.
	time.Sleep(3 * time.Second)

	// set this now
	blocksBeforeStop = blocksReceived.Load()
	t.Logf("blocks received before stop: %d", blocksBeforeStop)

	// ── 8a. Restart on a DIFFERENT host port with one-blocks discarded ───────
	// This simulates a reader-node coming back up in a state the relayer should
	// NOT reconnect to (wrong port). We let it run for 5 s to confirm the relayer
	// does not start receiving blocks from it, then stop it.
	t.Log("starting discard container on separate port…")
	discardContainer := startReaderNodeContainer(t, ctx, tmpDir, blockchainDataDir, discardHostPort, true)
	t.Cleanup(func() {
		d := time.Duration(0)
		_ = discardContainer.Stop(context.Background(), &d)
		_ = discardContainer.Terminate(context.Background())
	})
	t.Log("discard container running; sleeping 5 s…")
	time.Sleep(5 * time.Second)

	blocksAfterDiscardStart := blocksReceived.Load()
	require.Equal(t, blocksBeforeStop, blocksAfterDiscardStart,
		"relayer must NOT forward blocks from the discard container (wrong port)")

	t.Log("stopping discard container…")
	stopDiscardTimeout := time.Duration(0)
	require.NoError(t, discardContainer.Stop(ctx, &stopDiscardTimeout))
	t.Log("discard container stopped")

	// ── 8b. Wipe one-block files so the hub cannot bridge the gap ────────────
	// The discard container advanced the chain state without writing one-block
	// files. Remove whatever one-block files exist so the relayer has no way to
	// fill the gap when the real node restarts — this is the condition we want
	// to reproduce.
	t.Logf("removing one-block files from %s…", oneBlockDataDir)
	require.NoError(t, os.RemoveAll(oneBlockDataDir),
		"failed to remove one-block storage directory")
	t.Log("one-block files removed")

	// ── 8c. Restart the container on the SAME host port as the first run ─────
	t.Log("restarting reader-node container on original port…")
	require.NoError(t, readerContainer.Start(ctx))
	t.Log("reader-node container restarted")

	// ── 9. Wait up to 15 s for blocks to resume and head block to advance ────
	// The MultiplexedSource retries every ~5 s, so 15 s is a comfortable margin.
	deadline := time.Now().Add(15 * time.Second)
	var blocksAfterRestart int64
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		blocksAfterRestart = blocksReceived.Load()
		if blocksAfterRestart > blocksBeforeStop {
			break
		}
	}

	t.Logf("blocks received after restart: %d", blocksAfterRestart)

	// Drain any stream error that may have been produced (reconnect is expected
	// to be transparent to this subscriber, but log it for visibility).
	select {
	case streamErr := <-streamErrCh:
		t.Logf("note: stream returned an error during the test: %v", streamErr)
	default:
	}

	require.Greater(t, blocksAfterRestart, blocksBeforeStop,
		"relayer must forward new blocks after the reader-node restarted")

	// ── 10. Assert the hub's head block number advanced after the restart ─────
	// Poll GetHeadInfo until the head number moves past the pre-stop value, or
	// until the deadline. This confirms the hub's internal state (not just the
	// subscriber stream) reflects the new chain head.
	headDeadline := time.Now().Add(10 * time.Second)
	var headInfoAfterRestart *pbheadinfo.HeadInfoResponse
	for time.Now().Before(headDeadline) {
		headInfoAfterRestart, err = headInfoClient.GetHeadInfo(ctx, &pbheadinfo.HeadInfoRequest{})
		if err == nil && headInfoAfterRestart.HeadNum > headInfoBeforeStop.HeadNum {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, err, "GetHeadInfo should succeed after reader-node restarted")
	t.Logf("hub head block after restart: num=%d id=%s (was %d before stop)",
		headInfoAfterRestart.HeadNum, headInfoAfterRestart.HeadID, headInfoBeforeStop.HeadNum)
	require.Greater(t, headInfoAfterRestart.HeadNum, headInfoBeforeStop.HeadNum,
		"hub head block must have advanced past its pre-stop value after the reader-node restarted")
}

// ── helpers ───────────────────────────────────────────────────────────────────

// startReaderNodeContainer launches the dummy-blockchain image with only the
// reader-node component active, binding container port 10010 to readerHostPort
// on the loopback interface so the port survives a stop+start cycle.
//
// When discardOneBlocks is true, one-block files are written to a throwaway path
// inside the container (/tmp/discard-one-blocks) instead of the shared storage
// volume. Both variants use the same internal container port (10010); only the
// host-side port differs, so the two instances never collide.
func startReaderNodeContainer(t *testing.T, ctx context.Context, tmpDir string, blockchainDataDir string, readerHostPort int, discardOneBlocks bool) testcontainers.Container {
	t.Helper()

	// Arguments forwarded to the dummy-blockchain binary by the reader-node
	// supervisor. A small burst and fast block-rate ensure blocks arrive quickly.
	readerArgs := "start" +
		" --log-level=warn" +
		" --tracer=firehose" +
		" --store-dir=/data/dummy" +
		" --genesis-block-burst=5" +
		" --block-rate=200" + // ~200 ms per block ≈ 5 blocks/s
		" --block-size=100" +
		" --genesis-height=0" +
		" --server-addr=:9777" +
		" --with-reorgs=false" +
		" --with-skipped-blocks=false"

	// Both normal and discard containers listen on the same internal port (10010)
	// inside their own network namespace. Only the host-side port differs, so
	// there is no collision between the two container instances.
	const containerGRPCPort = 10010
	containerPort := nat.Port(fmt.Sprintf("%d/tcp", containerGRPCPort))

	// When discarding one-blocks, redirect writes to a throwaway path that is
	// not shared with the host, keeping the real one-block store pristine.
	oneBlockStoreURL := "file:///app/firehose-data/storage/one-blocks"
	if discardOneBlocks {
		oneBlockStoreURL = "file:///tmp/discard-one-blocks"
	}

	cmd := []string{
		"start",
		"reader-node",
		"-c", "",
		"--data-dir=/data/reader",
		"--advertise-chain-name=acme-dummy-blockchain",
		"--reader-node-path=dummy-blockchain",
		"--reader-node-arguments=" + readerArgs,
		"--advertise-block-id-encoding=hex",
		// Explicitly pin the reader-node gRPC port inside the container.
		"--reader-node-grpc-listen-addr=:10010",
		"--common-one-block-store-url=" + oneBlockStoreURL,
	}

	req := testcontainers.ContainerRequest{
		Image: dummyBlockchainImage,
		Cmd:   cmd,
		Env: map[string]string{
			"DLOG": ".*=info",
		},
		ExposedPorts: []string{string(containerPort)},
		HostConfigModifier: func(hc *dockercontainer.HostConfig) {
			// Fix the host-side binding so stop+start reuses the same port.
			hc.PortBindings = nat.PortMap{
				containerPort: []nat.PortBinding{
					{HostIP: "127.0.0.1", HostPort: fmt.Sprintf("%d", readerHostPort)},
				},
			}
			hc.Binds = []string{
				tmpDir + ":/app/firehose-data/storage/",
				// Keep the dummy-blockchain's own state (blocks, etc.) across
				// container restarts so the node can resume from the same height.
				blockchainDataDir + ":/data",
			}
		},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort(containerPort),
			wait.ForLog("serving gRPC").WithStartupTimeout(30*time.Second),
		),
	}

	t.Logf("starting container image=%s reader-host-port=%d discard-one-blocks=%v", dummyBlockchainImage, readerHostPort, discardOneBlocks)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err, "failed to start reader-node container")
	return c
}

// findFreeHostPort asks the OS for a free TCP port on the loopback interface.
func findFreeHostPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
