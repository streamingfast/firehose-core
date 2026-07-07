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
	"context"
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
		Use:   "one-block (<file> | <store> <block_num>)",
		Short: "Prints a block from a one-block file or from a store a block number",
		Args:  cobra.RangeArgs(1, 2),
		RunE:  createToolsPrintOneBlockE(chain),
	}

	toolsPrintMergedBlocksCmd := &cobra.Command{
		Use:   "merged-blocks <store|file> [<start_block>[:<end_block>]]",
		Short: "Prints merged blocks file from store, using range if specified",
		Args:  cobra.RangeArgs(1, 2),
		RunE:  createToolsPrintMergedBlocksE(chain, logger),
	}

	toolsPrintCmd.AddCommand(toolsPrintOneBlockCmd)
	toolsPrintCmd.AddCommand(toolsPrintMergedBlocksCmd)

	toolsPrintCmd.PersistentFlags().Bool("transactions", false, "When in 'text' output mode, also print transactions summary")

	return toolsPrintCmd
}

func createToolsPrintMergedBlocksE[B firecore.Block](chain *firecore.Chain[B], logger *zap.Logger) firecore.CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		outputPrinter, err := GetOutputPrinter(cmd, chain.BlockFileDescriptor())
		cli.NoError(err, "Unable to get output printer")

		store, err := dstore.NewDBinStore(args[0])
		cli.NoError(err, "Unable to create store %q", args[0])

		bundleSize, err := firecore.GetMergedBlocksBundleSizeFlag(cmd)
		cli.NoError(err, "Invalid merged-blocks bundle size")

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

		if looksLikeMergedBlocksFile(args[0]) {
			store, err = dstore.NewDBinStore(storeURLFromFileInput(args[0]))
			cli.NoError(err, "Unable to create store %q", path.Dir(args[0]))

			storeFileRange := storeURLFileLikeRange(args[0], bundleSize)
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
			bstream.FileSourceWithBundleSize(bundleSize),
		}
		if !blockRange.IsOpen() {
			// The range end is exclusive but FileSource's stop block is inclusive, so stop one
			// block earlier. When the requested range runs past the available data, FileSource
			// prints everything it has and then errors on the first missing file.
			if stopBlock := blockRange.MustGetStopBlock(); stopBlock > 0 {
				options = append(options, bstream.FileSourceWithStopBlock(stopBlock-1))
			}
		} else {
			// Open range (print everything available): there is no stop block to reach, so cap it
			// at the last available merged-blocks file. Otherwise FileSource would print every
			// block and then error on the (expected) missing next file.
			lastBase, found, err := highestMergedBlocksBase(cmd.Context(), store, uint64(blockRange.GetStartBlock()), bundleSize)
			cli.NoError(err, "Unable to locate last merged-blocks file in store %q", args[0])
			if found {
				options = append(options, bstream.FileSourceWithStopBlock(lastBase+bundleSize-1))
			}
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

var oneBlockFileRegex = regexp.MustCompile(`^(\d{10}).+(?:.dbin(?:.zst)?)?$`)
var mergedBlocksFileRegex = regexp.MustCompile(`^(\d{10})(?:.dbin(?:.zst)?)?$`)

func looksLikeOneBlockFile(path string) bool {
	base := filepath.Base(path)

	return oneBlockFileRegex.MatchString(base)
}

func looksLikeMergedBlocksFile(path string) bool {
	base := filepath.Base(path)

	return mergedBlocksFileRegex.MatchString(base)
}

func storeURLFileLikeRange(path string, bundleSize uint64) types.BlockRange {
	base := filepath.Base(path)
	groups := mergedBlocksFileRegex.FindStringSubmatch(base)
	if len(groups) != 2 {
		panic(fmt.Sprintf("unexpected number of groups (got %d, expected 2) in regex %q match against %q", len(groups), mergedBlocksFileRegex, base))
	}

	startBlock, _ := strconv.ParseUint(groups[1], 10, 64)
	return types.NewClosedRange(int64(startBlock), uint64(startBlock)+bundleSize)
}

// highestMergedBlocksBase returns the base block number of the last merged-blocks
// file at or after startBlock's bundle boundary. Instead of listing the whole
// store, it probes for file existence in exponentially growing steps and then
// binary-searches, so it costs O(log n) store lookups. found is false when no
// merged-blocks file exists at the starting boundary.
//
// It assumes the gap-free store the merger produces. A hole makes the probe
// return some existing base past it rather than the true last file; that is
// harmless here because FileSource reads sequentially and errors on the first
// missing file anyway, surfacing the hole just as it did before.
func highestMergedBlocksBase(ctx context.Context, store dstore.Store, startBlock, bundleSize uint64) (base uint64, found bool, err error) {
	firstBase := startBlock - (startBlock % bundleSize)

	if exists, err := mergedBlocksFileExists(ctx, store, firstBase); err != nil || !exists {
		return 0, false, err
	}

	// Exponential search for an upper bound whose file does NOT exist. lo always
	// points at a base known to exist, hi at one known to be missing.
	lo := firstBase
	var hi uint64
	for step := uint64(1); ; step *= 2 {
		candidate := firstBase + step*bundleSize
		exists, err := mergedBlocksFileExists(ctx, store, candidate)
		if err != nil {
			return 0, false, err
		}
		if !exists {
			hi = candidate
			break
		}
		lo = candidate
	}

	// Binary search in (lo, hi] for the highest existing base.
	for hi-lo > bundleSize {
		mid := lo + ((hi-lo)/bundleSize/2)*bundleSize
		exists, err := mergedBlocksFileExists(ctx, store, mid)
		if err != nil {
			return 0, false, err
		}
		if exists {
			lo = mid
		} else {
			hi = mid
		}
	}

	return lo, true, nil
}

func mergedBlocksFileExists(ctx context.Context, store dstore.Store, base uint64) (bool, error) {
	return store.FileExists(ctx, fmt.Sprintf("%010d", base))
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

		storeInput := args[0]

		var store dstore.Store
		var files []string

		if looksLikeOneBlockFile(storeInput) {
			var filename string
			store, filename, err = dstore.NewStoreFromFileURL(storeInput, dstore.Compression("zstd"))
			if err != nil {
				return fmt.Errorf("unable to create store at path %q: %w", store, err)
			}
			files = []string{filename}
		} else {
			store, err = dstore.NewDBinStore(storeInput)
			if err != nil {
				return fmt.Errorf("unable to create store at path %q: %w", store, err)
			}

			cli.Ensure(len(args) == 2, "Missing block number argument")

			blockNum, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil {
				return fmt.Errorf("unable to parse block number %q: %w", args[1], err)
			}

			files, err = findOneBlockFiles(ctx, store, blockNum)
			if err != nil {
				return fmt.Errorf("find one blocks file: %w", err)
			}
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
				if errors.Is(err, io.EOF) {
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
	err := printer.PrintTo(pbBlock, os.Stdout)
	if err != nil {
		return fmt.Errorf("marshalling block to json: %w", err)
	}

	return nil
}

// findOneBlockFiles finds all the files in the store that are prefixed with
// the block number. There could be multiple files for a single block number,
// this function will return all of them.
func findOneBlockFiles(ctx context.Context, store dstore.Store, blockNum uint64) ([]string, error) {
	var files []string
	filePrefix := fmt.Sprintf("%010d", blockNum)
	err := store.Walk(ctx, filePrefix, func(filename string) (err error) {
		files = append(files, filename)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk files: %w", err)
	}

	return files, nil
}
