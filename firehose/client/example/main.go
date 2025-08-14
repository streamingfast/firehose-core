package main

import (
	"context"
	"fmt"
	"os"

	"github.com/streamingfast/cli"
	"github.com/streamingfast/firehose-core/firehose/client"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
)

func main() {
	apiToken := os.Getenv("SF_API_TOKEN")

	client, closeFunc, grpcOpts, err := client.NewFirehoseClient("polygon.streamingfast.io:443", apiToken, "", false, false)
	cli.NoError(err, "failed to create firehose client")
	defer closeFunc()

	stream, err := client.Blocks(context.Background(), &pbfirehose.Request{
		StartBlockNum: 1,
		StopBlockNum:  1 + 100,
	}, grpcOpts...)
	cli.NoError(err, "failed to create blocks stream")

	for {
		block, err := stream.Recv()
		cli.NoError(err, "failed to receive block")

		// Process the block as needed
		// For example, print the block number
		os.Stdout.WriteString("Received block: " + block.Metadata.Id + "\n")
	}

	fmt.Println("Completed processing blocks stream")
}
