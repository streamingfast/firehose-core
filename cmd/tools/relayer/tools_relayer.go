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
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	. "github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/blockstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/cli"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/cmd/tools/print"
	"go.uber.org/zap"
)

func NewToolsRelayerGroup[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) *cobra.Command {
	return ToCobraCmd(Group(
		"relayer",
		"Relayer component related tools",

		Command(
			toolsRelayerStreamRunner(chain, logger),
			"stream <source> [:<stop>]",
			"Stream blocks from a live relayer source to the console",
			Flags(func(flags *pflag.FlagSet) {
				flags.Bool("clock", false, "For each block, only print the current timestamp, block timestamp, drift")
				flags.Bool("with-partials", false, "Ask for partial blocks in the blockstream request")
			}),

			RangeArgs(1, 2),
			Description(`
				Connects to a 'relayer' source and streams blocks to the console.

				The connection is done over gRPC on the 'sf.bstream.v1.BlockStream/Blocks'
				endpoint of the source.

				Blocks are printed to the console respecting the output format defined by the
				'output' flag. If not defined, the default output format is used.

				You can pass a stop block to the command, can be absolute or relative. If relative,
				only +N is supported, where N is the number of blocks seen so far. Default is
				to stream forever.
			`),
		),
	))
}

func toolsRelayerStreamRunner[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) firecore.CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		endpoint := args[0]

		stop := int64(-1)
		stopIsRelative := false

		onlyClock := sflags.MustGetBool(cmd, "clock")
		withPartials := sflags.MustGetBool(cmd, "with-partials")

		if len(args) == 2 {
			input := strings.TrimPrefix(args[1], ":")

			var err error
			stop, err = strconv.ParseInt(strings.TrimPrefix(input, "+"), 10, 64)
			cli.NoError(err, "Invalid stop block number argument %q", args[1])

			stopIsRelative = strings.HasPrefix(input, "+")
		}

		printer, err := print.GetOutputPrinter(cmd, chain.BlockFileDescriptor())
		if err != nil {
			return fmt.Errorf("unable to create output printer: %w", err)
		}

		opts := []blockstream.SourceOption{
			blockstream.WithRequester("firecore tools"),
		}

		if withPartials {
			opts = append(opts, blockstream.WithPartialBlocks())
		}

		blockCount := 0
		source := blockstream.NewSource(
			cmd.Context(),
			endpoint,
			1,
			bstream.HandlerFunc(func(blk *pbbstream.Block, obj any) error {
				if onlyClock {
					partialStr := ""
					if blk.PartialIndex != 0 {
						partialStr = fmt.Sprintf(" P%d", blk.PartialIndex)
					}
					if blk.LastPartial {
						partialStr += "*"
					}
					fmt.Printf("%d%s (%s) -- %s %s latency(ms): %5d\n", blk.Number, partialStr, blk.Id, blk.Timestamp.AsTime().UTC().Format("15:04:05"), time.Now().UTC().Format("15:04:05.000"), time.Since(blk.Timestamp.AsTime()).Milliseconds())
				} else {
					if err := printer.PrintTo(blk, os.Stderr); err != nil {
						return fmt.Errorf("unable to print block: %w", err)
					}
				}

				blockCount++

				// Continue forever if no stop provider
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
