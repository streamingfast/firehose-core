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
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/mr-tron/base58"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/tools"
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
	Use:   "merged-blocks <store> <start_block>",
	Short: "Prints the content summary of a merged blocks file.",
	Args:  cobra.ExactArgs(2),
}

func init() {
	toolsCmd.AddCommand(toolsPrintCmd)

	toolsPrintCmd.AddCommand(toolsPrintOneBlockCmd)
	toolsPrintCmd.AddCommand(toolsPrintMergedBlocksCmd)

	toolsPrintCmd.PersistentFlags().StringP("output", "o", "text", "Output mode for block printing, either 'text', 'json' or 'jsonl'")
	toolsPrintCmd.PersistentFlags().Bool("transactions", false, "When in 'text' output mode, also print transactions summary")
}

func configureToolsPrintCmd[B Block](chain *Chain[B]) {
	toolsPrintOneBlockCmd.RunE = createToolsPrintOneBlockE(chain)
	toolsPrintMergedBlocksCmd.RunE = createToolsPrintMergedBlocksE(chain)
}

func createToolsPrintMergedBlocksE[B Block](chain *Chain[B]) CommandExecutor {
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

		startBlock, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid base block %q: %w", args[1], err)
		}
		blockBoundary := tools.RoundToBundleStartBlock(startBlock, 100)

		filename := fmt.Sprintf("%010d", blockBoundary)
		reader, err := store.OpenObject(ctx, filename)
		if err != nil {
			fmt.Printf("❌ Unable to read blocks filename %s: %s\n", filename, err)
			return err
		}
		defer reader.Close()

		readerFactory, err := bstream.NewDBinBlockReader(reader)
		if err != nil {
			fmt.Printf("❌ Unable to read blocks filename %s: %s\n", filename, err)
			return err
		}

		seenBlockCount := 0
		for {
			block, err := readerFactory.Read()
			if err != nil {
				if err == io.EOF {
					fmt.Fprintf(os.Stderr, "Total blocks: %d\n", seenBlockCount)
					return nil
				}
				return fmt.Errorf("error receiving blocks: %w", err)
			}

			seenBlockCount++

			if err := printBlock(block, chain, outputMode, printTransactions); err != nil {
				// Error is ready to be passed to the user as-is
				return err
			}
		}
	}
}

func createToolsPrintOneBlockE[B Block](chain *Chain[B]) CommandExecutor {
	return func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		if _, ok := chain.BlockFactory().(*pbbstream.Block); ok {
			//todo: fix this with buf registry
			return fmt.Errorf("this tool only works with blocks that are not of type *pbbstream.Block")
		}

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

			if err := printBlock(block, chain, outputMode, printTransactions); err != nil {
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
//	JSONL
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

func printBlock[B Block](pbBlock *pbbstream.Block, chain *Chain[B], outputMode PrintOutputMode, printTransactions bool) error {
	switch outputMode {
	case PrintOutputModeText:
		err := pbBlock.PrintBlock(printTransactions, os.Stdout)
		if err != nil {
			return fmt.Errorf("pbBlock text printing: %w", err)
		}

	case PrintOutputModeJSON, PrintOutputModeJSONL:
		var options []jsontext.Options
		if outputMode == PrintOutputModeJSON {
			options = append(options, jsontext.WithIndent("  "))
		}
		encoder := jsontext.NewEncoder(os.Stdout)

		var marshallers *json.Marshalers
		switch UnsafeJsonBytesEncoder {
		case "hex":
			marshallers = json.NewMarshalers(
				json.MarshalFuncV2(func(encoder *jsontext.Encoder, t []byte, options json.Options) error {
					return encoder.WriteToken(jsontext.String(hex.EncodeToString(t)))
				}),
			)
		case "base58":
			marshallers = json.NewMarshalers(
				json.MarshalFuncV2(func(encoder *jsontext.Encoder, t []byte, options json.Options) error {
					return encoder.WriteToken(jsontext.String(base58.Encode(t)))
				}),
			)
		}

		var marshallableBlock Block = pbBlock
		chainBlock := chain.BlockFactory()
		if _, ok := chainBlock.(*pbbstream.Block); !ok {

			marshallableBlock = chainBlock
			err := pbBlock.Payload.UnmarshalTo(marshallableBlock)
			if err != nil {
				return fmt.Errorf("pbBlock payload unmarshal: %w", err)
			}
		}

		err := json.MarshalEncode(encoder, marshallableBlock, json.WithMarshalers(marshallers))
		if err != nil {
			return fmt.Errorf("pbBlock JSON printing: json marshal: %w", err)
		}
	}

	return nil
}
