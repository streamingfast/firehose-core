package test

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/streamingfast/dmetering/file"
	"github.com/streamingfast/substreams/client"
	"github.com/streamingfast/substreams/manifest"
	pbsubstreamsrpc "github.com/streamingfast/substreams/pb/sf/substreams/rpc/v2"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"
)

type Case struct {
	name         string
	spkgRootPath string
	moduleName   string
	startBlock   uint64
	// set endBlock to 0 to connect live
	endBlock          uint64
	expectedReadBytes float64
}

func TestIntegration(t *testing.T) {
	if os.Getenv("RUN_INTEGRATION_TESTS") != "true" {
		t.Skip()
	}

	cases := []Case{
		{
			name:              "sunny path",
			spkgRootPath:      "./substreams_acme/substreams-acme-v0.1.0.spkg",
			moduleName:        "map_test_data",
			startBlock:        0,
			endBlock:          1000,
			expectedReadBytes: 696050,
		},

		{
			name:              "sunny path",
			spkgRootPath:      "./substreams_acme/substreams-acme-v0.1.0.spkg",
			moduleName:        "map_test_data",
			startBlock:        0,
			endBlock:          0,
			expectedReadBytes: 696050,
		},
	}

	ctx := context.Background()

	rootPath, err := filepath.Abs("../")
	if err != nil {
		t.Fatalf("getting absolute path: %v", err)
	}

	go func() {
		err = runTier1(ctx, t, rootPath)
		require.NoError(t, err)
	}()

	go func() {
		err = runTier2(ctx, t, rootPath)
		require.NoError(t, err)
	}()

	var meteringServer *MeteringTestServer
	go func() {
		meteringServer = NewMeteringServer(t, ":10016")
		meteringServer.Run()
	}()

	clientConfig := client.NewSubstreamsClientConfig("localhost:9003", "", 0, false, true)
	substreamsClient, _, _, _, err := client.NewSubstreamsClient(clientConfig)
	require.NoError(t, err)

	// WAIT SERVERS TO BE READY
	time.Sleep(15 * time.Second)

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.endBlock == 0 {
				// RUN LIVE
				go func() {
					err = runDummyNode(ctx, t)
					require.NoError(t, err)
				}()
			}

			err = requestTier1(ctx, t, c, substreamsClient)
			require.NoError(t, err)

			resultEvents := meteringServer.bufferedEvents
			var totalReadBytes float64
			for _, events := range resultEvents {
				for _, event := range events.Events {
					for _, metric := range event.Metrics {
						// TODO : CHOOSE THE RIGHT METRIC
						if metric.Key == "file_uncompressed_read_bytes" {
							totalReadBytes += metric.Value
						}
					}
				}
			}

			require.Equal(t, c.expectedReadBytes, totalReadBytes)
			meteringServer.clearBufferedEvents()
		})
	}
}

func runTier1(ctx context.Context, t *testing.T, rootDir string) error {
	cmdPath := filepath.Join(rootDir, "/cmd/firecore")
	firehoseDataStoragePath := filepath.Join(rootDir, "devel/standard/firehose-data/storage/")
	mergedBlocksStore := fmt.Sprintf("file://%s", filepath.Join(firehoseDataStoragePath, "merged-blocks"))
	forkedBlocksStore := fmt.Sprintf("file://%s", filepath.Join(firehoseDataStoragePath, "forked-blocks"))
	oneBlocksStore := fmt.Sprintf("file://%s", filepath.Join(firehoseDataStoragePath, "one-blocks"))

	tier1Args := []string{
		"run",
		cmdPath,
		"start", "substreams-tier1",
		"--config-file=",
		"--log-to-file=false",
		"--common-auth-plugin=null://",
		fmt.Sprintf("--common-tmp-dir=%s", t.TempDir()),
		fmt.Sprintf("--common-metering-plugin=grpc://localhost:10016?network=dummy_blockchain"),
		"--common-system-shutdown-signal-delay=30s",
		fmt.Sprintf("--common-merged-blocks-store-url=%s", mergedBlocksStore),
		fmt.Sprintf("--common-one-block-store-url=%s", oneBlocksStore),
		fmt.Sprintf("--common-forked-blocks-store-url=%s", forkedBlocksStore),
		"--common-live-blocks-addr=localhost:10014",
		"--common-first-streamable-block=0",
		"--substreams-tier1-grpc-listen-addr=:9003",
		"--substreams-tier1-subrequests-endpoint=localhost:9004",
		"--substreams-tier1-subrequests-insecure=false",
		"--substreams-tier1-subrequests-plaintext=true",
		fmt.Sprintf("--substreams-state-store-url=%s/substreams_dummy", t.TempDir()),
		"--substreams-state-store-default-tag=vtestdummy",
	}

	tier1Cmd := exec.CommandContext(ctx, "go", tier1Args...)

	err := handlingTestInstance(t, tier1Cmd, "TIER1", true)
	if err != nil {
		return fmt.Errorf("handling instance %w", err)
	}

	return err
}

func runTier2(ctx context.Context, t *testing.T, rootDir string) error {
	cmdPath := rootDir + "/cmd/firecore"
	tier2Args := []string{
		"run",
		cmdPath,
		"start", "substreams-tier2",
		"--config-file=",
		"--log-to-file=false",
		fmt.Sprintf("--common-tmp-dir=%s", t.TempDir()),
		"--substreams-tier2-grpc-listen-addr=:9004",
		"--substreams-tier1-subrequests-plaintext=true",
		"--substreams-tier1-subrequests-insecure=false",
	}

	tier2Cmd := exec.CommandContext(ctx, "go", tier2Args...)

	err := handlingTestInstance(t, tier2Cmd, "TIER2", true)
	if err != nil {
		return fmt.Errorf("handling instance %w", err)
	}

	return err
}

func runDummyNode(ctx context.Context, t *testing.T) error {
	launchDummyCmd := exec.CommandContext(ctx, "../devel/standard/start.sh")

	err := handlingTestInstance(t, launchDummyCmd, "DUMMY_BLOCKCHAIN", true)
	if err != nil {
		return fmt.Errorf("handling instance %w", err)
	}

	return err
}

func requestTier1(ctx context.Context, t *testing.T, testCase Case, substreamsClient pbsubstreamsrpc.StreamClient) error {
	manifestReader, err := manifest.NewReader(testCase.spkgRootPath)
	require.NoError(t, err)

	pkgBundle, err := manifestReader.Read()
	require.NoError(t, err)

	require.NotEmptyf(t, pkgBundle, "pkgBundle is empty")

	request := pbsubstreamsrpc.Request{
		StartBlockNum:                       int64(testCase.startBlock),
		StartCursor:                         "",
		StopBlockNum:                        testCase.endBlock,
		FinalBlocksOnly:                     false,
		ProductionMode:                      false,
		OutputModule:                        testCase.moduleName,
		Modules:                             pkgBundle.Package.Modules,
		DebugInitialStoreSnapshotForModules: nil,
		NoopMode:                            false,
	}

	stream, err := substreamsClient.Blocks(ctx, &request)
	require.NoError(t, err)

	for {
		block, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		t.Logf("[REQUESTER]: %v", block)
	}
	return nil
}
