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

package print

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/dstore"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/types"
	"go.uber.org/zap"
)

func NewToolsPrintCmd[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) *cobra.Command {
	toolsPrintCmd := &cobra.Command{
		Use:   "print",
		Short: "Prints of one block or merged blocks file",
	}

	toolsPrintOneBlockCmd := &cobra.Command{
		Use:   "one-block <store> <block_num>",
		Short: "Prints a block from a one-block file",
		Args:  cobra.ExactArgs(2),
	}

	toolsPrintMergedBlocksCmd := &cobra.Command{
		Use:   "merged-blocks <store|file> [<start_block>[:<end_block>]]",
		Short: "Prints merged blocks file from store, using range if specified",
		Args:  cobra.RangeArgs(1, 2),
	}

	toolsPrintCmd.AddCommand(toolsPrintOneBlockCmd)
	toolsPrintCmd.AddCommand(toolsPrintMergedBlocksCmd)

	toolsPrintCmd.PersistentFlags().Bool("transactions", false, "When in 'text' output mode, also print transactions summary")

	toolsPrintOneBlockCmd.RunE = createToolsPrintOneBlockE(chain)
	toolsPrintMergedBlocksCmd.RunE = createToolsPrintMergedBlocksE(chain, logger)

	return toolsPrintCmd
}

func createToolsPrintMergedBlocksE[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) firecore.CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		outputPrinter, err := GetOutputPrinter(cmd, chain.BlockFileDescriptor())
		cli.NoError(err, "Unable to get output printer")

		store, err := dstore.NewDBinStore(args[0])
		cli.NoError(err, "Unable to create store %q", args[0])

		blockRange := types.NewOpenRange(int64(bstream.GetProtocolFirstStreamableBlock))
		if len(args) > 1 {
			blockRange, err = types.GetBlockRangeFromArg(args[1])
			cli.NoError(err, "Unable to parse block range %q", args[1])

			// If the range is open, we assume the user wants to print a single block
			if blockRange.IsOpen() {
				blockRange = types.NewClosedRange(blockRange.GetStartBlock(), uint64(blockRange.GetStartBlock())+1)
			}

			cli.Ensure(blockRange.IsResolved(), "Invalid block range %q: print only accepts fully resolved range", blockRange)
		}

		if looksLikeStoreURLFile(args[0]) {
			store, err = dstore.NewDBinStore(storeURLFromFileInput(args[0]))
			cli.NoError(err, "Unable to create store %q", path.Dir(args[0]))

			storeFileRange := storeURLFileLikeRange(args[0])
			if len(args) <= 1 {
				// No block range explicitly specified, we will use the range from the filename
				blockRange = storeFileRange
			} else {
				cli.Ensure(storeFileRange.Contains(uint64(blockRange.GetStartBlock()), types.EndBoundaryExclusive), "Block range %q is not contained in the store file range %q", blockRange, storeFileRange)
				cli.Ensure(storeFileRange.Contains(uint64(blockRange.MustGetStopBlock()), types.EndBoundaryExclusive), "Block range %q is not contained in the store file range %q", blockRange, storeFileRange)
			}
		}

		options := []bstream.FileSourceOption{
			bstream.FileSourceErrorOnMissingMergedBlocksFile(),
		}
		if !blockRange.IsOpen() {
			options = append(options, bstream.FileSourceWithStopBlock(blockRange.MustGetStopBlock()))
		}

		source := bstream.NewFileSource(store, uint64(blockRange.GetStartBlock()), bstream.HandlerFunc(func(blk *pbbstream.Block, obj any) error {
			if !blockRange.Contains(blk.Number, types.RangeBoundaryExclusive) {
				return nil
			}

			return displayBlock(blk, chain, outputPrinter)
		}), logger, options...)

		// Blocking call, perform the work we asked for
		source.Run()

		if source.Err() != nil && !errors.Is(source.Err(), bstream.ErrStopBlockReached) {
			return source.Err()
		}

		return nil
	}
}

var mergedBlocksFileRegex = regexp.MustCompile(`^(\d{10})(?:.dbin(?:.zst)?)?$`)

func looksLikeStoreURLFile(path string) bool {
	base := filepath.Base(path)

	return mergedBlocksFileRegex.MatchString(base)
}

func storeURLFileLikeRange(path string) types.BlockRange {
	base := filepath.Base(path)
	groups := mergedBlocksFileRegex.FindStringSubmatch(base)
	if len(groups) != 2 {
		panic(fmt.Sprintf("unexpected number of groups (got %d, expected 2) in regex %q match against %q", len(groups), mergedBlocksFileRegex, base))
	}

	startBlock, _ := strconv.ParseUint(groups[1], 10, 64)
	return types.NewClosedRange(int64(startBlock), uint64(startBlock)+100)
}

func storeURLFromFileInput(input string) string {
	output := strings.TrimRight(input, filepath.Base(input))
	output = strings.TrimRightFunc(output, func(r rune) bool {
		return r == '/' || r == '\\'
	})

	return output
}

func createToolsPrintOneBlockE[B firecore.Block](chain *firecore.Chain[B]) firecore.CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		outputPrinter, err := GetOutputPrinter(cmd, chain.BlockFileDescriptor())
		cli.NoError(err, "Unable to get output printer")

		storeURL := args[0]
		store, err := dstore.NewDBinStore(storeURL)
		if err != nil {
			return fmt.Errorf("unable to create store at path %q: %w", store, err)
		}

		blockNum, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("unable to parse block number %q: %w", args[1], err)
		}

		var files []string
		filePrefix := fmt.Sprintf("%010d", blockNum)
		err = store.Walk(ctx, filePrefix, func(filename string) (err error) {
			files = append(files, filename)
			return nil
		})
		if err != nil {
			return fmt.Errorf("unable to find on block files: %w", err)
		}

		for _, filepath := range files {
			reader, err := store.OpenObject(ctx, filepath)
			if err != nil {
				fmt.Printf("❌ Unable to read block filename %s: %s\n", filepath, err)
				return err
			}
			defer reader.Close()

			readerFactory, err := bstream.NewDBinBlockReader(reader)
			if err != nil {
				fmt.Printf("❌ Unable to read blocks filename %s: %s\n", filepath, err)
				return err
			}

			block, err := readerFactory.Read()
			if err != nil {
				if err == io.EOF {
					break
				}
				return fmt.Errorf("reading block: %w", err)
			}

			if err := displayBlock(block, chain, outputPrinter); err != nil {
				// Error is ready to be passed to the user as-is
				return err
			}
		}
		return nil
	}
}

func displayBlock[B firecore.Block](pbBlock *pbbstream.Block, chain *firecore.Chain[B], printer OutputPrinter) error {
	if pbBlock == nil {
		return fmt.Errorf("block is nil")
	}

	if !firecore.UnsafeRunningFromFirecore {
		// Since we are running via a chain specific binary (i.e. fireeth) we can use a BlockFactory
		marshallableBlock := chain.BlockFactory()

		if err := pbBlock.Payload.UnmarshalTo(marshallableBlock); err != nil {
			return fmt.Errorf("pbBlock payload unmarshal: %w", err)
		}

		err := printer.PrintTo(marshallableBlock, os.Stdout)
		if err != nil {
			return fmt.Errorf("pbBlock JSON printing: json marshal: %w", err)
		}
		return nil
	}

	// since we are running directly the firecore binary we will *NOT* use the BlockFactory
	err := printer.PrintTo(pbBlock.Payload, os.Stdout)
	if err != nil {
		return fmt.Errorf("marshalling block to json: %w", err)
	}

	return nil
}
