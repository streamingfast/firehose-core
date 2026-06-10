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
if one-blocks are linkable. Reports missing block ranges, forks, and parent-chain continuity
issues as they are detected.

Uses the finalized block number (LibNum) embedded in each one-block file to prune internal
state so it does not grow infinitely.

Progress is reported using an automatic ladder (every 10K blocks below 100K processed,
every 100K below 500K, every 500K below 1M, then every 1M). Pass a non-zero
--progress-each value to override with a fixed cadence.`,
		Args: cobra.ExactArgs(1),
		RunE: toolsCheckOneBlocksE,
	}

	cmd.Flags().Uint64("progress-each", 0, "Override progress reporting cadence (0 uses an automatic ladder: 10K → 100K → 500K → 1M, then every 1M)")

	return cmd
}

// missingRange describes a contiguous range of block heights for which no one-block
// file was found during the streaming walk.
type missingRange struct {
	from uint64
	to   uint64
}

// forkReport tracks a height for which multiple distinct block IDs were seen.
type forkReport struct {
	num        uint64
	candidates int
}

// oneBlocksState tracks the current state while walking one-block files.
//
// The walk produces filenames in lexicographic order, which (because the canonical
// filename starts with a zero-padded block number) yields blocks in non-decreasing
// block number order. We use that ordering to detect missing block ranges inline
// during the walk: any jump in block number larger than 1 indicates a gap.
//
// We also keep a small in-memory map of recent blocks (pruned by `LibNum`) to:
//   - detect parent-chain continuity issues (when consecutive heights are present
//     but the new block's PreviousID does not match a known block at the previous
//     height — typically the sign of a fork inside the store);
//   - detect forks (multiple distinct IDs at the same height).
type oneBlocksState struct {
	// blocksByID maps block ID suffix to OneBlockFile; used to look up parents.
	// Entries are pruned below highestFinalizedBlock.
	blocksByID map[string]*bstream.OneBlockFile

	// blocksByNum maps block number to a list of known one-block files at that height.
	// Entries are pruned below highestFinalizedBlock.
	blocksByNum map[uint64][]*bstream.OneBlockFile

	// seenCanonical tracks canonical names that have already been processed. It is
	// pruned in lock-step with blocksByNum so it never grows beyond the live window.
	seenCanonical map[string]struct{}

	// highestFinalizedBlock is the highest lib num seen so far.
	highestFinalizedBlock uint64

	// firstBlockNum / lastBlockNum is the range observed during the walk.
	firstBlockNum    uint64
	lastBlockNum     uint64
	hasFirstBlock    bool
	distinctHeights  uint64
	lastDistinctSeen uint64

	// currentAvailableStart is the first block number of the current contiguous
	// run of available blocks. It is reset to the current block number each time a
	// gap is detected.
	currentAvailableStart uint64

	// processedCount is total files processed.
	processedCount uint64

	// nextProgressAt is the next processedCount value at which to emit a progress line.
	nextProgressAt uint64

	// missing is the list of missing block ranges detected inline during the walk.
	missing []missingRange

	// missingBlockCount caches the total number of missing block heights across all
	// ranges in `missing`, so the summary doesn't have to recompute it.
	missingBlockCount uint64

	// forks is the list of heights at which multiple distinct IDs were seen.
	forks []forkReport

	// reportedForkAtNum tracks heights for which a fork has already been reported so
	// we don't emit the same fork twice.
	reportedForkAtNum map[uint64]int

	// brokenParentCount tracks how many times a contiguous height arrived with a
	// PreviousID that did not match any block we have in state at the previous height
	// (a parent-chain continuity failure that is distinct from a simple gap).
	brokenParentCount uint64

	// progressEachOverride is the user-provided override for the progress cadence.
	// When 0, the automatic ladder is used.
	progressEachOverride uint64
}

func newOneBlocksState(progressEachOverride uint64) *oneBlocksState {
	s := &oneBlocksState{
		blocksByID:           make(map[string]*bstream.OneBlockFile),
		blocksByNum:          make(map[uint64][]*bstream.OneBlockFile),
		seenCanonical:        make(map[string]struct{}),
		reportedForkAtNum:    make(map[uint64]int),
		progressEachOverride: progressEachOverride,
	}
	s.nextProgressAt = s.computeNextProgress(0)
	return s
}

// computeNextProgress returns the next processed-count value at which a progress line
// should be emitted, given the current processed count. The ladder is:
//
//	count <  100K -> every 10K
//	count <  500K -> every 100K
//	count <    1M -> every 500K
//	count >=   1M -> every 1M
//
// If the user supplied a non-zero override via --progress-each, that value is used
// instead at every step.
func (s *oneBlocksState) computeNextProgress(currentCount uint64) uint64 {
	step := s.progressStep(currentCount)
	// Snap "currentCount" to the next multiple of step.
	return ((currentCount / step) + 1) * step
}

// progressStep returns the ladder step for the given processed count.
func (s *oneBlocksState) progressStep(currentCount uint64) uint64 {
	if s.progressEachOverride != 0 {
		return s.progressEachOverride
	}
	switch {
	case currentCount < 100_000:
		return 10_000
	case currentCount < 500_000:
		return 100_000
	case currentCount < 1_000_000:
		return 500_000
	default:
		return 1_000_000
	}
}

// printAvailableRange prints an inline "available blocks in range" line.
func printAvailableRange(from, to uint64) {
	fmt.Printf("%s Available blocks in range [%s to %s]\n",
		stylex.Success("✅"),
		stylex.Successf("#%s", humanize.Comma(int64(from))),
		stylex.Successf("#%s", humanize.Comma(int64(to))),
	)
}

// printMissingRange prints an inline "missing blocks in range" line.
// The label is padded with two extra spaces so the opening '[' aligns with
// the corresponding "Available blocks in range" label (9 vs 7 chars difference).
func printMissingRange(from, to uint64) {
	fmt.Printf("%s Missing blocks in range   [%s to %s]\n",
		stylex.Errorf("❌"),
		stylex.Errorf("#%s", humanize.Comma(int64(from))),
		stylex.Errorf("#%s", humanize.Comma(int64(to))),
	)
}

// process handles a newly encountered one-block file.
func (s *oneBlocksState) process(file *bstream.OneBlockFile) {
	// Deduplicate within the live window using seenCanonical.
	if _, already := s.seenCanonical[file.CanonicalName]; already {
		return
	}
	s.seenCanonical[file.CanonicalName] = struct{}{}

	s.processedCount++

	// Track block range and distinct height count.
	if !s.hasFirstBlock {
		s.firstBlockNum = file.Num
		s.currentAvailableStart = file.Num
		s.hasFirstBlock = true
	}

	// We rely on lexicographic-by-num ordering produced by the Walk to detect
	// missing block ranges and contiguous parent continuity inline.
	if s.lastBlockNum != file.Num || s.distinctHeights == 0 {
		// New distinct height.
		if s.hasFirstBlock && s.distinctHeights > 0 && file.Num > s.lastDistinctSeen+1 {
			// Inline gap detection: every height between lastDistinctSeen+1 and
			// file.Num-1 inclusive is missing.
			//
			// Print the available range that just ended, then the missing range,
			// streaming inline as they are discovered.
			printAvailableRange(s.currentAvailableStart, s.lastDistinctSeen)

			from := s.lastDistinctSeen + 1
			to := file.Num - 1
			s.missing = append(s.missing, missingRange{from: from, to: to})
			s.missingBlockCount += to - from + 1
			printMissingRange(from, to)

			// The new available segment starts at the current block.
			s.currentAvailableStart = file.Num
		}
		s.distinctHeights++
		s.lastDistinctSeen = file.Num
	}
	s.lastBlockNum = file.Num

	// Update finalization tracker.
	if file.LibNum > s.highestFinalizedBlock {
		s.highestFinalizedBlock = file.LibNum
	}

	// Detect forks: more than one distinct ID at the same height.
	existingAtHeight := s.blocksByNum[file.Num]
	isDuplicate := false
	for _, prior := range existingAtHeight {
		if prior.ID == file.ID {
			isDuplicate = true
			break
		}
	}
	if !isDuplicate {
		if len(existingAtHeight) >= 1 {
			// Fork. Emit a single line per height; update candidate count on
			// subsequent forks so the summary stays accurate.
			candidates := len(existingAtHeight) + 1
			if _, already := s.reportedForkAtNum[file.Num]; !already {
				s.forks = append(s.forks, forkReport{num: file.Num, candidates: candidates})
				fmt.Printf("%s Block %s has %d candidates (fork)\n",
					stylex.Warn("🔀"),
					stylex.Warnf("#%s", humanize.Comma(int64(file.Num))),
					candidates,
				)
			} else {
				// Update count for the already-tracked entry.
				for i := range s.forks {
					if s.forks[i].num == file.Num {
						s.forks[i].candidates = candidates
						break
					}
				}
			}
			s.reportedForkAtNum[file.Num] = candidates
		}

		// Register this block in the state maps (only when not duplicate, to avoid
		// growing the slice with identical entries).
		s.blocksByID[file.ID] = file
		s.blocksByNum[file.Num] = append(s.blocksByNum[file.Num], file)
	}

	// Inline parent-chain continuity check: if the parent block number is within the
	// active window (i.e. above the finalized boundary) and we have *something* at
	// that height, but no entry with the expected ID, the parent linkage is broken.
	// Gaps (no entry at all at that height) are already reported via the inline
	// missing-range detection above, so we explicitly skip that case here.
	if file.Num > 0 {
		parentNum := file.Num - 1
		if parentNum > s.highestFinalizedBlock || s.highestFinalizedBlock == 0 {
			if _, parentFound := s.blocksByID[file.PreviousID]; !parentFound {
				if _, anyAtParent := s.blocksByNum[parentNum]; anyAtParent {
					// We have block(s) at the parent height, but none matches the
					// expected PreviousID — broken linkage (typically a fork edge
					// that wasn't captured upstream).
					s.brokenParentCount++
					fmt.Printf("%s Block %s expects parent %s but no matching block was found at %s\n",
						stylex.Errorf("⚠"),
						stylex.Errorf("#%s", humanize.Comma(int64(file.Num))),
						stylex.Dim(file.PreviousID),
						stylex.Errorf("#%s", humanize.Comma(int64(parentNum))),
					)
				}
				// If `anyAtParent` is false the inline missing-range detection
				// already (or will) handle reporting; nothing more to do here.
			}
		}
	}

	// Emit progress according to the ladder (or the user override).
	if s.processedCount >= s.nextProgressAt {
		fmt.Printf("%s Processed %s files | current %s | finalized %s | missing %s | forks %s\n",
			stylex.Dim("↻"),
			stylex.Value(humanize.Comma(int64(s.processedCount))),
			stylex.Value(fmt.Sprintf("#%s", humanize.Comma(int64(file.Num)))),
			stylex.Value(fmt.Sprintf("#%s", humanize.Comma(int64(s.highestFinalizedBlock)))),
			missingCountStyled(s.missingBlockCount),
			forkCountStyled(len(s.forks)),
		)
		s.nextProgressAt = s.computeNextProgress(s.processedCount)
	}

	// Prune state below the finalized boundary.
	s.pruneBelow(s.highestFinalizedBlock)
}

// missingCountStyled renders the missing block count with appropriate coloring.
func missingCountStyled(count uint64) string {
	if count == 0 {
		return stylex.Success("0")
	}
	return stylex.Errorf("%s", humanize.Comma(int64(count)))
}

// forkCountStyled renders the fork count with appropriate coloring.
func forkCountStyled(count int) string {
	if count == 0 {
		return stylex.Success("0")
	}
	return stylex.Warnf("%d", count)
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
	// Print the trailing available segment (after the last gap, or the entire
	// range if no gaps were detected).
	if s.hasFirstBlock {
		printAvailableRange(s.currentAvailableStart, s.lastBlockNum)
	}

	fmt.Println()
	fmt.Println(stylex.Title("One-blocks check complete"))
	fmt.Println(stylex.Dim(stylex.Separator(50)))

	rangeLine := fmt.Sprintf("Block range found: %s to %s (%s distinct heights)",
		stylex.Value(fmt.Sprintf("#%s", humanize.Comma(int64(s.firstBlockNum)))),
		stylex.Value(fmt.Sprintf("#%s", humanize.Comma(int64(s.lastBlockNum)))),
		stylex.Value(humanize.Comma(int64(s.distinctHeights))),
	)
	fmt.Println(rangeLine)

	fmt.Printf("  %s %s\n", stylex.Label("Total files processed   :"), stylex.Value(humanize.Comma(int64(s.processedCount))))
	fmt.Printf("  %s %s\n", stylex.Label("Highest finalized block :"), stylex.Value(fmt.Sprintf("#%s", humanize.Comma(int64(s.highestFinalizedBlock)))))
	fmt.Printf("  %s %s\n", stylex.Label("Missing blocks          :"), missingCountStyled(s.missingBlockCount))
	fmt.Printf("  %s %s\n", stylex.Label("Missing ranges          :"), missingCountStyled(uint64(len(s.missing))))
	fmt.Printf("  %s %s\n", stylex.Label("Forked heights          :"), forkCountStyled(len(s.forks)))
	fmt.Printf("  %s %s\n", stylex.Label("Broken parent linkages  :"), missingCountStyled(s.brokenParentCount))

	// Status line: no emoji prefix, just color-coded value.
	hasIssues := s.missingBlockCount > 0 || s.brokenParentCount > 0 || len(s.forks) > 0
	if hasIssues {
		fmt.Printf("  %s %s\n", stylex.Label("Status                  :"), stylex.Error("broken"))
	} else {
		fmt.Printf("  %s %s\n", stylex.Label("Status                  :"), stylex.Success("ok"))
	}
}

func toolsCheckOneBlocksE(cmd *cobra.Command, args []string) error {
	storeURL := args[0]
	progressEachOverride := sflags.MustGetUint64(cmd, "progress-each")

	blocksStore, err := dstore.NewDBinStore(storeURL)
	if err != nil {
		return fmt.Errorf("unable to create blocks store: %w", err)
	}

	state := newOneBlocksState(progressEachOverride)

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
