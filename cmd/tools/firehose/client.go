package firehose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/cmd/tools/print"
	"github.com/streamingfast/firehose-core/types"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func NewToolsFirehoseClientCmd[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "firehose-client <endpoint> <range>",
		Short: "Connects to a Firehose endpoint over gRPC and print block stream as JSON to terminal",
		Long: `Connects to a Firehose endpoint over gRPC and print block stream as JSON to terminal.

The endpoint can be specified in the following formats:
  - host:port (traditional format, uses --plaintext flag to determine connection type)
  - http://host[:port] (automatically uses plaintext connection, defaults to port 80)
  - https://host[:port] (automatically uses SSL connection, defaults to port 443)

When using http:// or https:// prefixes, the --plaintext flag is automatically determined from the URL scheme.`,
		Args: cobra.ExactArgs(2),
		RunE: getFirehoseClientE(chain, logger),
	}

	addFirehoseStreamClientFlagsToSet(cmd.Flags(), chain)

	cmd.Flags().Bool("final-blocks-only", false, "Only ask for final blocks")
	cmd.Flags().Bool("print-cursor-only", false, "Skip block decoding, only print the step cursor (useful for performance testing)")
	cmd.Flags().Bool("print-clock-only", false, "Skip block decoding, only print the block timestamp and latency")

	return cmd
}

type respChan struct {
	ch chan string
}

func getFirehoseClientE[B firecore.Block](chain *firecore.Chain[B], rootLog *zap.Logger) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		firehoseClient, connClose, requestInfo, err := getFirehoseStreamClientFromCmd(cmd, rootLog, args[0], chain)
		if err != nil {
			return err
		}
		defer connClose()

		blockRange, err := types.GetBlockRangeFromArg(args[1])
		if err != nil {
			return fmt.Errorf("invalid range %q: %w", args[1], err)
		}

		printCursorOnly := sflags.MustGetBool(cmd, "print-cursor-only")
		printClockOnly := sflags.MustGetBool(cmd, "print-clock-only")
		if printClockOnly && printCursorOnly {
			return fmt.Errorf("cannot print clock and cursor at the same time")
		}

		request := &pbfirehose.Request{
			StartBlockNum:   blockRange.Start,
			StopBlockNum:    blockRange.GetStopBlockOr(0),
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

		resps := make(chan *respChan, 10)
		allDone := make(chan bool)

		if !printCursorOnly && !printClockOnly {
			// print the responses linearly
			go func() {
				for resp := range resps {
					line := <-resp.ch
					fmt.Println(line)
				}
				close(allDone)
			}()
		}

		printer, err := print.GetOutputPrinter(cmd, chain.BlockFileDescriptor())
		if err != nil {
			return fmt.Errorf("unable to create output printer: %w", err)
		}

		var totalEgress int
		for {
			response, err := stream.Recv()

			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return fmt.Errorf("stream error while receiving: %w", err)
			}
			totalEgress += proto.Size(response)

			if printCursorOnly {
				fmt.Printf("%s - %s\n", response.Step.String(), response.Cursor)
				continue
			}
			if printClockOnly {
				fmt.Printf("%d -- %s %s latency(ms): %5d\n", response.Metadata.Num, response.Metadata.Time.AsTime().UTC().Format("15:04:05"), time.Now().UTC().Format("15:04:05.000"), time.Since(response.Metadata.Time.AsTime()).Milliseconds())
				continue
			}

			resp := &respChan{
				ch: make(chan string),
			}
			resps <- resp

			// async process the response
			go func() {
				buffer := bytes.NewBuffer(nil)
				err := printer.PrintTo(response, buffer)
				if err != nil {
					rootLog.Error("marshalling to string", zap.Error(err))
					resp.ch <- ""
					return
				}

				resp.ch <- buffer.String()
			}()
		}
		if printCursorOnly || printClockOnly {
			// The response-ordering goroutine below is only started when we actually decode
			// blocks; in cursor/clock-only mode nothing ever reads from 'resps' and nothing
			// ever closes 'allDone', so falling through would block forever.
			return nil
		}

		close(resps)
		<-allDone
		fmt.Fprintln(os.Stderr, "Total received data (uncompressed egress):", totalEgress)
		return nil
	}
}
