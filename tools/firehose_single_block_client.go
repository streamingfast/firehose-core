package tools

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mostynb/go-grpc-compression/zstd"
	"github.com/spf13/cobra"
	"github.com/streamingfast/firehose/client"
	"github.com/streamingfast/jsonpb"
	"github.com/streamingfast/logging"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
)

// You should add your custom 'transforms' flags to this command in your init(), then parse them in transformsSetter
var GetFirehoseSingleBlockClientCmd = func(zlog *zap.Logger, tracer logging.Tracer) *cobra.Command {
	out := &cobra.Command{
		Use:     "firehose-single-block-client {endpoint} {block_num|block_num:block_id|cursor}",
		Short:   "fetch a single block from firehose and print as JSON",
		Args:    cobra.ExactArgs(2),
		RunE:    getFirehoseSingleBlockClientE(zlog, tracer),
		Example: "firehose-single-block-client --compression=gzip my.firehose.endpoint:443 2344:0x32d8e8d98a798da98d6as9d69899as86s9898d8ss8d87",
	}
	out.Flags().StringP("api-token-env-var", "a", "FIREHOSE_API_TOKEN", "Look for a JWT in this environment variable to authenticate against endpoint")
	out.Flags().String("compression", "none", "http compression: use either 'none', 'gzip' or 'zstd'")
	out.Flags().BoolP("plaintext", "p", false, "Use plaintext connection to firehose")
	out.Flags().BoolP("insecure", "k", false, "Skip SSL certificate validation when connecting to firehose")
	return out
}

func getFirehoseSingleBlockClientE(zlog *zap.Logger, tracer logging.Tracer) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		endpoint := args[0]
		req := &pbfirehose.SingleBlockRequest{}

		ref := args[1]
		if num, err := strconv.ParseUint(ref, 10, 64); err == nil {
			req.Reference = &pbfirehose.SingleBlockRequest_BlockNumber_{
				BlockNumber: &pbfirehose.SingleBlockRequest_BlockNumber{
					Num: num,
				},
			}
		} else if parts := strings.Split(ref, ":"); len(parts) == 2 {
			num, err := strconv.ParseUint(parts[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid block reference, cannot decode first part as block_num: %s, %w", ref, err)
			}
			req.Reference = &pbfirehose.SingleBlockRequest_BlockHashAndNumber_{
				BlockHashAndNumber: &pbfirehose.SingleBlockRequest_BlockHashAndNumber{
					Num:  num,
					Hash: parts[1],
				},
			}

		} else {
			req.Reference = &pbfirehose.SingleBlockRequest_Cursor_{
				Cursor: &pbfirehose.SingleBlockRequest_Cursor{
					Cursor: ref,
				},
			}
		}
		apiTokenEnvVar := mustGetString(cmd, "api-token-env-var")
		jwt := os.Getenv(apiTokenEnvVar)
		plaintext := mustGetBool(cmd, "plaintext")
		insecure := mustGetBool(cmd, "insecure")

		firehoseClient, connClose, err := client.NewFirehoseFetchClient(endpoint, jwt, insecure, plaintext)
		if err != nil {
			return err
		}
		defer connClose()

		var grpcCallOpts []grpc.CallOption
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

		resp, err := firehoseClient.Block(ctx, req)
		if err != nil {
			return err
		}

		line, err := jsonpb.MarshalToString(resp)
		if err != nil {
			return err
		}
		fmt.Println(line)
		return nil

	}
}
