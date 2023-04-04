package tools

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose/client"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/anypb"
)

// FirehoseResponseDecoder will usually look like this (+ error handling):
/*
	block := &pbcodec.Block{} 									// chain-specific protobuf Block
	anypb.UnmarshalTo(in, in.block, proto.UnmarshalOptions{})
	return codec.BlockFromProto(block) 							// chain-specific bstream block converter
*/
type FirehoseResponseDecoder func(in *anypb.Any) (*bstream.Block, error)

func DownloadFirehoseBlocks(
	ctx context.Context,
	endpoint string,
	jwt string,
	insecure bool,
	plaintext bool,
	startBlock uint64,
	stopBlock uint64,
	destURL string,
	respDecoder FirehoseResponseDecoder,
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
		tweakBlock: func(_ *cobra.Command, b *bstream.Block) (*bstream.Block, error) {
			return tweakBlock(b)
		},
		logger: logger,
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
