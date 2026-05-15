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
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
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
	// Entries are pruned below highestFinalizedBlock, so this also serves as our
	// deduplication set: canonical names of blocks still in the window.
	blocksByID map[string]*bstream.OneBlockFile

	// blocksByNum maps block number to a list of known one-block files at that height.
	blocksByNum map[uint64][]*bstream.OneBlockFile

	// seenCanonical tracks canonical names that have already been processed. It is
	// pruned in lock-step with blocksByNum so it never grows beyond the live window.
	seenCanonical map[string]struct{}

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
		blocksByID:    make(map[string]*bstream.OneBlockFile),
		blocksByNum:   make(map[uint64][]*bstream.OneBlockFile),
		seenCanonical: make(map[string]struct{}),
		progressEach:  progressEach,
	}
}

// process handles a newly encountered one-block file.
func (s *oneBlocksState) process(file *bstream.OneBlockFile) {
	// Deduplicate within the live window using seenCanonical.
	if _, already := s.seenCanonical[file.CanonicalName]; already {
		return
	}
	s.seenCanonical[file.CanonicalName] = struct{}{}

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
				fmt.Printf("%s %s #%s (ID: %s): expected parent ID %s not found %s\n",
					stylex.Error("⚠"),
					stylex.Errorf("HOLE at block"),
					stylex.Value(humanize.Comma(int64(file.Num))),
					stylex.Dim(file.ID),
					stylex.Dim(parentID),
					stylex.Warnf("(hole length: %s blocks)", humanize.Comma(int64(holeLength))),
				)
			}
		}
	}

	// Print progress if configured
	if s.progressEach > 0 && s.processedCount%s.progressEach == 0 {
		fmt.Printf("%s processed %s files │ current #%s │ finalized #%s │ holes: %s\n",
			stylex.Dim("↻"),
			stylex.Value(humanize.Comma(int64(s.processedCount))),
			stylex.Value(humanize.Comma(int64(file.Num))),
			stylex.Value(humanize.Comma(int64(s.highestFinalizedBlock))),
			holeCountStyled(s.holeCount),
		)
	}

	// Prune state below the finalized boundary
	s.pruneBelow(s.highestFinalizedBlock)
}

// holeCountStyled renders the hole count with appropriate coloring.
func holeCountStyled(count int) string {
	if count == 0 {
		return stylex.Success("0")
	}
	return stylex.Errorf("%d", count)
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

	for num, files := range s.blocksByNum {
		if num <= finalizedNum {
			for _, f := range files {
				delete(s.blocksByID, f.ID)
				delete(s.seenCanonical, f.CanonicalName)
			}
			delete(s.blocksByNum, num)
		}
	}
}

// summary prints a final summary of the check.
func (s *oneBlocksState) summary() {
	fmt.Println()
	fmt.Println(stylex.Title("One-blocks check complete"))
	fmt.Println(stylex.Dim(stylex.Separator(50)))
	fmt.Printf("  %s %s\n", stylex.Label("Total files processed  :"), stylex.Value(humanize.Comma(int64(s.processedCount))))
	fmt.Printf("  %s %s\n", stylex.Label("Highest finalized block:"), stylex.Value(fmt.Sprintf("#%s", humanize.Comma(int64(s.highestFinalizedBlock)))))
	fmt.Printf("  %s %s\n", stylex.Label("Holes detected         :"), holeCountStyled(s.holeCount))
	if s.holeCount == 0 {
		fmt.Printf("  %s %s\n", stylex.Success("✔"), stylex.Success("Status: OK — no holes found"))
	} else {
		fmt.Printf("  %s %s\n", stylex.Error("✘"), stylex.Errorf("Status: PROBLEMS FOUND — %d hole(s)", s.holeCount))
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

	// fileCh decouples the Walk goroutine from the processing goroutine so that
	// listing the next file in the store is not blocked by the (potentially slow)
	// processing of the current file.
	fileCh := make(chan *bstream.OneBlockFile, 128)
	errCh := make(chan error, 1)

	// Producer: walk the store and send valid one-block files down the channel.
	go func() {
		defer close(fileCh)
		walkErr := blocksStore.Walk(cmd.Context(), "", func(filename string) error {
			file, parseErr := bstream.NewOneBlockFile(filename)
			if parseErr != nil {
				// Skip files that don't match the one-block format
				return nil
			}
			select {
			case fileCh <- file:
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			}
			return nil
		})
		if walkErr != nil {
			errCh <- walkErr
		}
	}()

	// Consumer: process files as they arrive.
	for file := range fileCh {
		state.process(file)
	}

	// Check if the producer encountered an error.
	select {
	case walkErr := <-errCh:
		return fmt.Errorf("error walking blocks store: %w", walkErr)
	default:
	}

	if state.processedCount == 0 {
		fmt.Println(stylex.Note("No one-block files found"))
		return nil
	}

	state.summary()
	return nil
}
