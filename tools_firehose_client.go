package firecore

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/jsonpb"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
)

func newToolsFirehoseClientCmd[B Block](chain *Chain[B]) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "firehose-client <endpoint> <start> <stop>",
		Short: "Connects to a Firehose endpoint over gRPC and print block stream as JSON to terminal",
		Args:  cobra.ExactArgs(3),
		RunE:  getFirehoseClientE(chain),
	}

	addFirehoseClientFlagsToSet(cmd.Flags(), chain)
	cmd.Flags().Bool("print-cursor-only", false, "Skip block decoding, only print the step cursor (useful for performance testing)")

	return cmd
}

func getFirehoseClientE[B Block](chain *Chain[B]) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		firehoseClient, connClose, requestInfo, err := getFirehoseClientFromCmd(cmd, args[0], chain)
		if err != nil {
			return err
		}
		defer connClose()

		start, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing start block num: %w", err)
		}
		stop, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing stop block num: %w", err)
		}

		printCursorOnly := sflags.MustGetBool(cmd, "print-cursor-only")

		request := &pbfirehose.Request{
			StartBlockNum:   start,
			StopBlockNum:    stop,
			Transforms:      requestInfo.Transforms,
			FinalBlocksOnly: requestInfo.FinalBlocksOnly,
			Cursor:          requestInfo.Cursor,
		}

		stream, err := firehoseClient.Blocks(ctx, request, requestInfo.GRPCCallOpts...)
		if err != nil {
			return fmt.Errorf("unable to start blocks stream: %w", err)
		}

		meta, err := stream.Header()
		if err != nil {
			rootLog.Warn("cannot read header")
		} else {
			if hosts := meta.Get("hostname"); len(hosts) != 0 {
				rootLog = rootLog.With(zap.String("remote_hostname", hosts[0]))
			}
		}
		rootLog.Info("connected")

		type respChan struct {
			ch chan string
		}

		resps := make(chan *respChan, 10)
		allDone := make(chan bool)

		if !printCursorOnly {
			// print the responses linearly
			go func() {
				for resp := range resps {
					line := <-resp.ch
					fmt.Println(line)
				}
				close(allDone)
			}()
		}

		for {
			response, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					break
				}
				return fmt.Errorf("stream error while receiving: %w", err)
			}

			if printCursorOnly {
				fmt.Printf("%s - %s\n", response.Step.String(), response.Cursor)
				continue
			}

			resp := &respChan{
				ch: make(chan string),
			}
			resps <- resp

			// async process the response
			go func() {
				line, err := jsonpb.MarshalToString(response)
				if err != nil {
					rootLog.Error("marshalling to string", zap.Error(err))
				}
				resp.ch <- line
			}()
		}
		if printCursorOnly {
			return nil
		}

		close(resps)
		<-allDone
		return nil
	}
}
