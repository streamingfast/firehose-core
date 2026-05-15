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

package check

import (
	"fmt"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
)

func newCheckOneBlocksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "one-blocks <one-blocks-store-url>",
		Short: "Checks for holes in one-block files and reports linkability issues",
		Long: `Walks one-block files in streaming mode (walk + process simultaneously) and evaluates
if one-blocks are linkable. Reports holes and their lengths (in block count).

Uses the finalized block number (LibNum) embedded in each one-block file to prune internal
state so it does not grow infinitely.`,
		Args: cobra.ExactArgs(1),
		RunE: toolsCheckOneBlocksE,
	}

	cmd.Flags().Uint64("progress-each", 0, "Print progress every N blocks processed (0 disables progress reporting)")

	return cmd
}

// oneBlocksState tracks the current state while walking one-block files.
type oneBlocksState struct {
	// blocksByID maps block ID suffix to OneBlockFile; used to look up parents.
	blocksByID map[string]*bstream.OneBlockFile

	// blocksByNum maps block number to a list of known one-block files at that height.
	blocksByNum map[uint64][]*bstream.OneBlockFile

	// highestFinalizedBlock is the highest lib num seen so far.
	highestFinalizedBlock uint64

	// holeCount is total number of holes detected.
	holeCount int

	// processedCount is total files processed.
	processedCount uint64

	// progressEach controls how often progress is printed (0 = never).
	progressEach uint64
}

func newOneBlocksState(progressEach uint64) *oneBlocksState {
	return &oneBlocksState{
		blocksByID:   make(map[string]*bstream.OneBlockFile),
		blocksByNum:  make(map[uint64][]*bstream.OneBlockFile),
		progressEach: progressEach,
	}
}

// process handles a newly encountered one-block file.
func (s *oneBlocksState) process(file *bstream.OneBlockFile) {
	s.processedCount++

	// Update finalization tracker
	if file.LibNum > s.highestFinalizedBlock {
		s.highestFinalizedBlock = file.LibNum
	}

	// Register this block
	s.blocksByID[file.ID] = file
	s.blocksByNum[file.Num] = append(s.blocksByNum[file.Num], file)

	// Check linkability: can we find this block's parent?
	if file.Num > 0 {
		parentID := file.PreviousID
		_, parentFound := s.blocksByID[parentID]
		if !parentFound {
			// The parent is not in our state. It may be below finalized boundary (normal) or a genuine hole.
			// A hole is when the previous block number is >= our state window start but the ID is not present.
			// If parent num is below the finalized boundary, it has been cleaned; we consider it ok.
			parentNum := file.Num - 1
			if parentNum > s.highestFinalizedBlock || s.highestFinalizedBlock == 0 {
				// We should have seen the parent but didn't — this is a hole.
				s.holeCount++
				holeLength := s.detectHoleLength(file)
				fmt.Printf("HOLE detected at block #%s (ID: %s): expected parent ID %s not found (hole length: %s blocks)\n",
					humanize.Comma(int64(file.Num)),
					file.ID,
					parentID,
					humanize.Comma(int64(holeLength)),
				)
			}
		}
	}

	// Print progress if configured
	if s.progressEach > 0 && s.processedCount%s.progressEach == 0 {
		fmt.Printf("Progress: processed %s one-block files, current block #%s, finalized up to #%s, holes found: %d\n",
			humanize.Comma(int64(s.processedCount)),
			humanize.Comma(int64(file.Num)),
			humanize.Comma(int64(s.highestFinalizedBlock)),
			s.holeCount,
		)
	}

	// Prune state below the finalized boundary
	s.pruneBelow(s.highestFinalizedBlock)
}

// detectHoleLength estimates how large a hole is by checking how many consecutive block numbers
// before the current block are missing.
func (s *oneBlocksState) detectHoleLength(file *bstream.OneBlockFile) uint64 {
	if file.Num == 0 {
		return 0
	}

	// Walk backwards from file.Num-1 counting missing blocks
	var length uint64
	num := file.Num - 1
	for {
		if _, ok := s.blocksByNum[num]; ok {
			break
		}
		length++
		if num == 0 || num <= s.highestFinalizedBlock {
			break
		}
		num--
	}

	if length == 0 {
		length = 1
	}

	return length
}

// pruneBelow removes all block state at or below the given block number.
func (s *oneBlocksState) pruneBelow(finalizedNum uint64) {
	if finalizedNum == 0 {
		return
	}

	nums := make([]uint64, 0)
	for num := range s.blocksByNum {
		if num <= finalizedNum {
			nums = append(nums, num)
		}
	}

	for _, num := range nums {
		files := s.blocksByNum[num]
		for _, f := range files {
			delete(s.blocksByID, f.ID)
		}
		delete(s.blocksByNum, num)
	}
}

// summary prints a final summary of the check.
func (s *oneBlocksState) summary() {
	fmt.Printf("\nOne-blocks check complete:\n")
	fmt.Printf("  Total files processed : %s\n", humanize.Comma(int64(s.processedCount)))
	fmt.Printf("  Highest finalized block: #%s\n", humanize.Comma(int64(s.highestFinalizedBlock)))
	fmt.Printf("  Holes detected        : %d\n", s.holeCount)
	if s.holeCount == 0 {
		fmt.Println("  Status: OK (no holes found)")
	} else {
		fmt.Printf("  Status: PROBLEMS FOUND (%d holes)\n", s.holeCount)
	}
}

func toolsCheckOneBlocksE(cmd *cobra.Command, args []string) error {
	storeURL := args[0]
	progressEach := sflags.MustGetUint64(cmd, "progress-each")

	blocksStore, err := dstore.NewDBinStore(storeURL)
	if err != nil {
		return fmt.Errorf("unable to create blocks store: %w", err)
	}

	state := newOneBlocksState(progressEach)

	// Walk one-block files in streaming mode: process each file as we encounter it.
	// Files come sorted by name, which means sorted by block number (the filename starts with block num).
	filesSeen := make(map[string]bool)
	err = blocksStore.Walk(cmd.Context(), "", func(filename string) error {
		file, err := bstream.NewOneBlockFile(filename)
		if err != nil {
			// Skip files that don't match the one-block format
			return nil
		}

		// Deduplicate: multiple filenames can refer to the same canonical block
		if filesSeen[file.CanonicalName] {
			// Still register filename but don't reprocess
			return nil
		}
		filesSeen[file.CanonicalName] = true

		state.process(file)
		return nil
	})
	if err != nil {
		return fmt.Errorf("error walking blocks store: %w", err)
	}

	if state.processedCount == 0 {
		fmt.Println("No one-block files found")
		return nil
	}

	state.summary()
	return nil
}
