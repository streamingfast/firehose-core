// Copyright 2021 dfuse Platform Inc.
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
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/blockstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"go.uber.org/zap"
)

func toolsRelayerWriteOneBlocksRunner(logger *zap.Logger) firecore.CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		endpoint := args[0]
		destination := args[1]

		stop := int64(-1)
		stopIsRelative := false

		partialOnly := sflags.MustGetBool(cmd, "partial-only")
		withPartials := sflags.MustGetBool(cmd, "with-partials") || partialOnly
		oneBlockSuffix := sflags.MustGetString(cmd, "one-block-suffix")
		secretKey := os.Expand(sflags.MustGetString(cmd, "secret-key"), os.Getenv)

		if len(args) == 3 {
			input := strings.TrimPrefix(args[2], ":")

			var err error
			stop, err = strconv.ParseInt(strings.TrimPrefix(input, "+"), 10, 64)
			cli.NoError(err, "Invalid stop block number argument %q", args[2])

			stopIsRelative = strings.HasPrefix(input, "+")
		}

		store, err := dstore.NewStore(destination, "dbin.zst", "zstd", false)
		if err != nil {
			return fmt.Errorf("unable to create destination store %q: %w", destination, err)
		}

		opts := []blockstream.SourceOption{
			blockstream.WithRequester("firecore tools"),
		}
		if withPartials {
			opts = append(opts, blockstream.WithPartialBlocks())
		}
		if secretKey != "" {
			opts = append(opts, blockstream.WithSecretKey(secretKey))
		}

		blockCount := 0
		source := blockstream.NewSource(
			cmd.Context(),
			endpoint,
			1,
			bstream.HandlerFunc(func(blk *pbbstream.Block, obj any) error {
				isPartial := blk.PartialIndex != 0

				if partialOnly && !isPartial {
					return nil
				}

				suffix := oneBlockSuffix
				if isPartial {
					suffix = fmt.Sprintf("%s-p%d", oneBlockSuffix, blk.PartialIndex)
				}

				filename := bstream.BlockFileNameWithSuffix(blk, suffix)

				if err := writeOneBlockFile(cmd.Context(), store, filename, blk); err != nil {
					return fmt.Errorf("writing one-block file %q: %w", filename, err)
				}

				logger.Debug("wrote one-block file",
					zap.String("filename", filename),
					zap.Uint64("block_num", blk.Number),
					zap.Int32("partial_index", blk.PartialIndex),
					zap.Bool("last_partial", blk.LastPartial),
				)

				blockCount++

				if stop == -1 {
					return nil
				}

				if stopIsRelative && blockCount >= int(stop) {
					return bstream.ErrStopBlockReached
				}

				if !stopIsRelative && blk.Number >= uint64(stop) {
					return bstream.ErrStopBlockReached
				}

				return nil
			}),
			opts...,
		)

		// Blocking
		source.Run()
		cli.Ensure(source.Err() == nil || errors.Is(source.Err(), bstream.ErrStopBlockReached), "Source run failed: %s", source.Err())

		return nil
	}
}

func writeOneBlockFile(ctx context.Context, store dstore.Store, filename string, block *pbbstream.Block) error {
	pipeRead, pipeWrite := io.Pipe()

	writeErrCh := make(chan error, 1)
	go func() {
		writeErrCh <- store.WriteObject(ctx, filename, pipeRead)
	}()

	blockWriter, err := bstream.NewDBinBlockWriter(pipeWrite)
	if err != nil {
		pipeWrite.CloseWithError(err)
		<-writeErrCh
		return fmt.Errorf("creating block writer: %w", err)
	}

	pipeWrite.CloseWithError(blockWriter.Write(block))

	return <-writeErrCh
}
