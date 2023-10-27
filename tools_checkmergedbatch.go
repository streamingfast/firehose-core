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
	"strconv"

	"github.com/spf13/cobra"
	"github.com/streamingfast/firehose-core/tools"
)

var toolsCheckMergedBlocksBatchCmd = &cobra.Command{
	Use:   "merged-blocks-batch <store-url> <results-store-url> <start> <stop>",
	Short: "Checks for any holes or unlinkable blocks in merged blocks files, writing the results as files in a results store",
	Args:  cobra.ExactArgs(4),
	RunE:  checkMergedBlocksBatchRunE,
}

func init() {
	toolsCheckCmd.AddCommand(toolsCheckMergedBlocksBatchCmd)
}

func checkMergedBlocksBatchRunE(cmd *cobra.Command, args []string) error {
	storeURL := args[0]
	resultsStoreURL := args[1]
	start, err := strconv.ParseUint(args[2], 10, 64)
	if err != nil {
		return err
	}
	stop, err := strconv.ParseUint(args[3], 10, 64)
	if err != nil {
		return err
	}
	fileBlockSize := uint64(100)

	blockRange := tools.BlockRange{
		Start: int64(start),
		Stop:  &stop,
	}

	return tools.CheckMergedBlocksBatch(cmd.Context(), storeURL, resultsStoreURL, fileBlockSize, blockRange)
}
