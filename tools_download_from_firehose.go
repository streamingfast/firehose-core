package firecore

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose/client"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func newToolsDownloadFromFirehoseCmd[B Block](chain *Chain[B]) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download-from-firehose <endpoint> <start> <stop> <destination>",
		Short: "Download blocks from Firehose and save them to merged-blocks",
		Args:  cobra.ExactArgs(4),
		RunE:  createToolsDownloadFromFirehoseE(chain),
		Example: ExamplePrefixed(chain, "tools download-from-firehose", `
			# Adjust <url> based on your actual network
			mainnet.eth.streamingfast.io:443 1000 2000 ./output_dir
		`),
	}

	// addFirehoseClientFlagsToSet(cmd.Flags(), chain)

	return cmd
}

func createToolsDownloadFromFirehoseE[B Block](chain *Chain[B]) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		endpoint := args[0]
		start, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing start block num: %w", err)
		}
		stop, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing stop block num: %w", err)
		}
		destFolder := args[3]

		apiTokenEnvVar := sflags.MustGetString(cmd, "api-token-env-var")
		apiToken := os.Getenv(apiTokenEnvVar)

		plaintext := sflags.MustGetBool(cmd, "plaintext")
		insecure := sflags.MustGetBool(cmd, "insecure")

		return downloadFirehoseBlocks(
			ctx,
			endpoint,
			apiToken,
			insecure,
			plaintext,
			start,
			stop,
			destFolder,
			func(in *anypb.Any) (*bstream.Block, error) {
				block := chain.BlockFactory()
				if err := anypb.UnmarshalTo(in, block, proto.UnmarshalOptions{}); err != nil {
					return nil, fmt.Errorf("unmarshal anypb: %w", err)
				}

				return chain.BlockEncoder.Encode(block)
			},
			func(in *bstream.Block) (*bstream.Block, error) {
				return in, nil
			},
			rootLog,
		)
	}
}

type firehoseResponseDecoder func(in *anypb.Any) (*bstream.Block, error)

func downloadFirehoseBlocks(
	ctx context.Context,
	endpoint string,
	jwt string,
	insecure bool,
	plaintext bool,
	startBlock uint64,
	stopBlock uint64,
	destURL string,
	respDecoder firehoseResponseDecoder,
	tweakBlock func(*bstream.Block) (*bstream.Block, error),
	logger *zap.Logger) error {

	var retryDelay = time.Second * 4

	firehoseClient, connClose, grpcCallOpts, err := client.NewFirehoseClient(endpoint, jwt, insecure, plaintext)
	if err != nil {
		return err
	}
	defer connClose()

	store, err := dstore.NewDBinStore(destURL)
	if err != nil {
		return err
	}

	mergeWriter := &mergedBlocksWriter{
		store:         store,
		writerFactory: bstream.GetBlockWriterFactory,
		tweakBlock:    tweakBlock,
		logger:        logger,
	}

	for {

		request := &pbfirehose.Request{
			StartBlockNum:   int64(startBlock),
			StopBlockNum:    stopBlock,
			FinalBlocksOnly: true,
		}

		stream, err := firehoseClient.Blocks(ctx, request, grpcCallOpts...)
		if err != nil {
			return fmt.Errorf("unable to start blocks stream: %w", err)
		}

		for {
			response, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					return nil
				}

				logger.Error("Stream encountered a remote error, going to retry",
					zap.Duration("retry_delay", retryDelay),
					zap.Error(err),
				)
				<-time.After(retryDelay)
				break
			}

			blk, err := respDecoder(response.Block)
			if err != nil {
				return fmt.Errorf("error decoding response to bstream block: %w", err)
			}
			if err := mergeWriter.ProcessBlock(blk, nil); err != nil {
				return fmt.Errorf("write to blockwriter: %w", err)
			}

		}

	}

}
