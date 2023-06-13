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

package firecore

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/tools"
	"google.golang.org/protobuf/proto"
)

var toolsPrintCmd = &cobra.Command{
	Use:   "print",
	Short: "Prints of one block or merged blocks file",
}

var toolsPrintOneBlockCmd = &cobra.Command{
	Use:   "one-block <store> <block_num>",
	Short: "Prints a block from a one-block file",
	Args:  cobra.ExactArgs(2),
}

var toolsPrintMergedBlocksCmd = &cobra.Command{
	Use:   "merged-blocks <store> <block_num>",
	Short: "Prints the content summary of merged blocks file",
	Args:  cobra.ExactArgs(2),
}

func init() {
	toolsCmd.AddCommand(toolsPrintCmd)

	toolsPrintCmd.AddCommand(toolsPrintOneBlockCmd)
	toolsPrintCmd.AddCommand(toolsPrintMergedBlocksCmd)

	toolsPrintCmd.PersistentFlags().StringP("output", "o", "text", "Output mode for block printing, either 'text' or 'json'")
	toolsPrintCmd.PersistentFlags().Bool("transactions", false, "When in 'text' output mode, also print transactions summary")
}

func configureToolsPrintCmd[B Block](chain *Chain[B]) {
	blockPrinter := chain.BlockPrinter()

	toolsPrintOneBlockCmd.RunE = createToolsPrintOneBlockE(blockPrinter)
	toolsPrintMergedBlocksCmd.RunE = createToolsPrintMergedBlocksE(blockPrinter)
}

func createToolsPrintMergedBlocksE(blockPrinter BlockPrinterFunc) CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		outputMode, err := toolsPrintCmdGetOutputMode(cmd)
		if err != nil {
			return fmt.Errorf("invalid 'output' flag: %w", err)
		}

		printTransactions := sflags.MustGetBool(cmd, "transactions")

		storeURL := args[0]
		store, err := dstore.NewDBinStore(storeURL)
		if err != nil {
			return fmt.Errorf("unable to create store at path %q: %w", store, err)
		}

		blockNum, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("unable to parse block number %q: %w", args[1], err)
		}

		blockBoundary := tools.RoundToBundleStartBlock(uint32(blockNum), 100)

		filename := fmt.Sprintf("%010d", blockBoundary)
		reader, err := store.OpenObject(ctx, filename)
		if err != nil {
			fmt.Printf("❌ Unable to read blocks filename %s: %s\n", filename, err)
			return err
		}
		defer reader.Close()

		readerFactory, err := bstream.GetBlockReaderFactory.New(reader)
		if err != nil {
			fmt.Printf("❌ Unable to read blocks filename %s: %s\n", filename, err)
			return err
		}

		seenBlockCount := 0
		for {
			block, err := readerFactory.Read()
			if err != nil {
				if err == io.EOF {
					fmt.Printf("Total blocks: %d\n", seenBlockCount)
					return nil
				}
				return fmt.Errorf("error receiving blocks: %w", err)
			}

			seenBlockCount++

			if err := printBlock(block, outputMode, printTransactions, blockPrinter); err != nil {
				// Error is ready to be passed to the user as-is
				return err
			}
		}
	}
}

func createToolsPrintOneBlockE(blockPrinter BlockPrinterFunc) CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		outputMode, err := toolsPrintCmdGetOutputMode(cmd)
		if err != nil {
			return fmt.Errorf("invalid 'output' flag: %w", err)
		}

		printTransactions := sflags.MustGetBool(cmd, "transactions")

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

			readerFactory, err := bstream.GetBlockReaderFactory.New(reader)
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

			if err := printBlock(block, outputMode, printTransactions, blockPrinter); err != nil {
				// Error is ready to be passed to the user as-is
				return err
			}
		}
		return nil
	}
}

//go:generate go-enum -f=$GOFILE --marshal --names --nocase

// ENUM(
//
//	Text
//	JSON
//
// )
type PrintOutputMode uint

func toolsPrintCmdGetOutputMode(cmd *cobra.Command) (PrintOutputMode, error) {
	outputModeRaw := sflags.MustGetString(cmd, "output")

	var out PrintOutputMode
	if err := out.UnmarshalText([]byte(outputModeRaw)); err != nil {
		return out, fmt.Errorf("invalid value %q: %w", outputModeRaw, err)
	}

	return out, nil
}

func printBlock(block *bstream.Block, outputMode PrintOutputMode, printTransactions bool, blockPrinter BlockPrinterFunc) error {
	if outputMode == PrintOutputModeText {
		if err := blockPrinter(block, printTransactions, os.Stdout); err != nil {
			return fmt.Errorf("block text printing: %w", err)
		}
	} else {
		nativeBlock := block.ToProtocol().(proto.Message)
		data, err := json.MarshalIndent(nativeBlock, "", "  ")
		if err != nil {
			return fmt.Errorf("block JSON printing: json marshal: %w", err)
		}

		fmt.Println(string(data))
	}

	return nil
}
