package tools

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/mostynb/go-grpc-compression/zstd"
	"github.com/spf13/cobra"
	"github.com/streamingfast/firehose/client"
	"github.com/streamingfast/jsonpb"
	"github.com/streamingfast/logging"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
	"google.golang.org/protobuf/types/known/anypb"
)

type TransformsSetter func(cmd *cobra.Command) ([]*anypb.Any, error)

// You should add your custom 'transforms' flags to this command in your init(), then parse them in transformsSetter
var GetFirehoseClientCmd = func(zlog *zap.Logger, tracer logging.Tracer, transformsSetter TransformsSetter) *cobra.Command {
	out := &cobra.Command{
		Use:   "firehose-client",
		Short: "Connects to a Firehose endpoint over gRPC and print block stream as JSON to terminal",
		Args:  cobra.ExactArgs(3),
		RunE:  getFirehoseClientE(zlog, tracer, transformsSetter),
	}
	out.Flags().StringP("api-token-env-var", "a", "FIREHOSE_API_TOKEN", "Look for a JWT in this environment variable to authenticate against endpoint")
	out.Flags().String("compression", "none", "The HTTP compression: use either 'none', 'gzip' or 'zstd'")
	out.Flags().String("cursor", "", "Use this cursor with the request to resume your stream at the following block pointed by the cursor")
	out.Flags().BoolP("plaintext", "p", false, "Use plaintext connection to Firehose")
	out.Flags().BoolP("insecure", "k", false, "Use SSL connection to Firehose but skip SSL certificate validation")
	out.Flags().Bool("print-cursor-only", false, "Skip block decoding, only print the step cursor (useful for performance testing)")
	out.Flags().Bool("final-blocks-only", false, "Only ask for final blocks")

	return out
}

func getFirehoseClientE(zlog *zap.Logger, tracer logging.Tracer, transformsSetter TransformsSetter) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		endpoint := args[0]
		start, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing start block num: %w", err)
		}
		stop, err := strconv.ParseUint(args[2], 10, 64)
		if err != nil {
			return fmt.Errorf("parsing stop block num: %w", err)
		}
		apiTokenEnvVar := mustGetString(cmd, "api-token-env-var")
		jwt := os.Getenv(apiTokenEnvVar)

		cursor := mustGetString(cmd, "cursor")

		plaintext := mustGetBool(cmd, "plaintext")
		insecure := mustGetBool(cmd, "insecure")

		printCursorOnly := mustGetBool(cmd, "print-cursor-only")
		finalBlocksOnly := mustGetBool(cmd, "final-blocks-only")

		firehoseClient, connClose, grpcCallOpts, err := client.NewFirehoseClient(endpoint, jwt, insecure, plaintext)
		if err != nil {
			return err
		}
		defer connClose()

		compression := mustGetString(cmd, "compression")
		switch compression {
		case "gzip":
			grpcCallOpts = append(grpcCallOpts, grpc.UseCompressor(gzip.Name))
		case "zstd":
			grpcCallOpts = append(grpcCallOpts, grpc.UseCompressor(zstd.Name))
		case "none":
		default:
			return fmt.Errorf("invalid value for compression: only 'gzip', 'zstd' or 'none' are accepted")
		}

		var transforms []*anypb.Any
		if transformsSetter != nil {
			transforms, err = transformsSetter(cmd)
			if err != nil {
				return err
			}
		}

		request := &pbfirehose.Request{
			StartBlockNum:   start,
			StopBlockNum:    stop,
			Transforms:      transforms,
			FinalBlocksOnly: finalBlocksOnly,
			Cursor:          cursor,
		}

		stream, err := firehoseClient.Blocks(ctx, request, grpcCallOpts...)
		if err != nil {
			return fmt.Errorf("unable to start blocks stream: %w", err)
		}

		meta, err := stream.Header()
		if err != nil {
			zlog.Warn("cannot read header")
		} else {
			if hosts := meta.Get("hostname"); len(hosts) != 0 {
				zlog = zlog.With(zap.String("remote_hostname", hosts[0]))
			}
		}
		zlog.Info("connected")

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
					zlog.Error("marshalling to string", zap.Error(err))
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
