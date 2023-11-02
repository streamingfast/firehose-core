package firecore

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func newToolsDownloadFromFirehoseCmd[B Block](chain *Chain[B], zlog *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download-from-firehose <endpoint> <range> <destination>",
		Short: "Download blocks from Firehose and save them to merged-blocks",
		Args:  cobra.ExactArgs(4),
		RunE:  createToolsDownloadFromFirehoseE(chain, zlog),
		Example: ExamplePrefixed(chain, "tools download-from-firehose", `
			# Adjust <url> based on your actual network
			mainnet.eth.streamingfast.io:443 1000 2000 ./output_dir
		`),
	}

	addFirehoseStreamClientFlagsToSet(cmd.Flags(), chain)
	cmd.Flags().Bool("dedupe-blocks", false, "use this flag to look for and remove block duplicates. This flag was added to address a specific situation, you shouldn't need this in normal operations")

	return cmd
}

func createToolsDownloadFromFirehoseE[B Block](chain *Chain[B], zlog *zap.Logger) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		endpoint := args[0]
		startBlock, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing start block num: %w", err)
		}
		stopBlock, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing stop block num: %w", err)
		}
		destFolder := args[3]

		firehoseClient, connClose, requestInfo, err := getFirehoseStreamClientFromCmd(cmd, zlog, endpoint, chain)
		if err != nil {
			return err
		}
		defer connClose()

		var retryDelay = time.Second * 4

		store, err := dstore.NewDBinStore(destFolder)
		if err != nil {
			return err
		}

		mergeWriter := &mergedBlocksWriter{
			store:         store,
			writerFactory: bstream.GetBlockWriterFactory,
			tweakBlock:    func(b *bstream.Block) (*bstream.Block, error) { return b, nil },
			logger:        zlog,
		}

		approximateLIBWarningIssued := false
		dedupeBlocks := sflags.MustGetBool(cmd, "dedupe-blocks")
		seen := make(map[string]bool)

		for {

			request := &pbfirehose.Request{
				StartBlockNum:   int64(startBlock),
				StopBlockNum:    stopBlock,
				FinalBlocksOnly: true,
				Cursor:          requestInfo.Cursor,
			}

			stream, err := firehoseClient.Blocks(ctx, request, requestInfo.GRPCCallOpts...)
			if err != nil {
				return fmt.Errorf("unable to start blocks stream: %w", err)
			}

			for {
				response, err := stream.Recv()
				if err != nil {
					if err == io.EOF {
						return nil
					}

					zlog.Error("stream encountered a remote error, going to retry",
						zap.Duration("retry_delay", retryDelay),
						zap.Error(err),
					)
					<-time.After(retryDelay)
					break
				}

				block := chain.BlockFactory()
				if err := anypb.UnmarshalTo(response.Block, block, proto.UnmarshalOptions{}); err != nil {
					return fmt.Errorf("unmarshal response block: %w", err)
				}

				if _, ok := block.(BlockLIBNumDerivable); !ok {
					// We must wrap the block in a BlockEnveloppe and "provide" the LIB number as itself minus 1 since
					// there is nothing we can do more here to obtain the value sadly. For chain where the LIB can be
					// derived from the Block itself, this code does **not** run (so it will have the correct value)
					if !approximateLIBWarningIssued {
						approximateLIBWarningIssued = true
						zlog.Warn("LIB number is approximated, it is not provided by the chain's Block model so we msut set it to block number minus 1 (which is kinda ok because only final blocks are retrieved in this download tool)")
					}

					number := block.GetFirehoseBlockNumber()
					libNum := number - 1
					if number <= bstream.GetProtocolFirstStreamableBlock {
						libNum = number
					}

					block = BlockEnveloppe{
						Block:  block,
						LIBNum: libNum,
					}
				}

				blk, err := chain.BlockEncoder.Encode(block)
				if err != nil {
					return fmt.Errorf("error decoding response to bstream block: %w", err)
				}
				if seen[blk.Id] {
					zlog.Info("skipping seen block (source merged-blocks had duplicates, skipping)", zap.String("id", blk.Id), zap.Uint64("num", blk.Number))
					continue
				}

				if dedupeBlocks {
					seen[blk.Id] = true
				}

				if err := mergeWriter.ProcessBlock(blk, nil); err != nil {
					return fmt.Errorf("write to blockwriter: %w", err)
				}
			}
		}
	}
}
