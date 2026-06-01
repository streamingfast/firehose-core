# Add `firecore tools check one-blocks` Command

mode: feature
state: review
root_git: .worktrees/feature-tools-check-one-blocks
worktree: .worktrees/feature-tools-check-one-blocks
branch: feature/tools-check-one-blocks
target_branch: develop

> **Resume protocol:** read **Dev Feedback** and the **State Tracker** below first, then jump to the
> step marked `Current`. Ensure that you are in the correct worktree and branch according to preamble here. Update current with Developer feedback and update the tracker after every meaningful change.
> Do not mutate completed steps; append a new entry instead.

---

## Initial Description

Add a `firecore tools check one-blocks` command that checks holes in one-blocks.

Inspired by the existing `toolsCheckForksE` command (which walks one/forked blocks and shows results), this new command should:

- Walk one-blocks in streaming mode (walk + work simultaneously, not walk-all-then-work)
- Evaluate if one-blocks are linkable and report a problem if they are not
- Show holes and hole lengths (in block count)
- Use finalized block number to clean up state, ensuring the state doesn't grow infinitely
- Support `--progress-each` flag that shows progress each N blocks

## Dev Feedback

*Round 5 of feedback*

```
✅ Available blocks in range [#69,815,484 to #69,815,499]
❌ Missing blocks in range [#69,815,500 to #74,677,299]
✅ Available blocks in range [#74,677,300 to #74,677,488]
❌ Missing blocks in range [#74,677,489 to #74,772,819]
✅ Available blocks in range [#74,772,820 to #74,772,899]
❌ Missing blocks in range [#74,772,900 to #74,776,699]
✅ Available blocks in range [#74,776,700 to #74,776,825]
❌ Missing blocks in range [#74,776,826 to #74,776,828]
✅ Available blocks in range [#74,776,829 to #74,776,899]
❌ Missing blocks in range [#74,776,900 to #74,822,999]
✅ Available blocks in range [#74,823,000 to #74,823,106]
```

Let's align the block range otherwise it's hard to see.

Last change after which we are ready.

*Round 6 of feedback*

*SUPERSEDE Round 5*

There was a misunderstanding on the desired output and streaming behavior.
1. We didn't wanted to change the streaming behavior
1. We wanted this output:

```
✅ Available blocks in range [#69,815,484 to #69,815,499]
❌ Missing blocks in range   [#69,815,500 to #74,677,299]
✅ Available blocks in range [#74,677,300 to #74,677,488]
❌ Missing blocks in range   [#74,677,489 to #74,772,819]
✅ Available blocks in range [#74,772,820 to #74,772,899]
❌ Missing blocks in range   [#74,772,900 to #74,776,699]
✅ Available blocks in range [#74,776,700 to #74,776,825]
❌ Missing blocks in range   [#74,776,826 to #74,776,828]
✅ Available blocks in range [#74,776,829 to #74,776,899]
❌ Missing blocks in range   [#74,776,900 to #74,822,999]
✅ Available blocks in range [#74,823,000 to #74,823,106]
```

We accept that within `[...]` length may vary, important thing is alignment after `Available blocks in range/Missing blocks in range` which is known in advance.

Put back the streaming approach essentially reverting the previous 2 commits and implementing it the right way as described in round 6.

## Spec & Implementation

### Implementation

File: `cmd/tools/check/one_blocks.go`

- `newCheckOneBlocksCmd()` — creates the cobra subcommand `tools check one-blocks <store-url>`
  - Flag `--progress-each uint64` (default 0 = automatic ladder): override the progress cadence; when 0 the ladder 10K → 100K → 500K → 1M (then every 1M) is used.
- `oneBlocksState` struct — tracks in-flight state during the walk:
  - `blocksByID map[string]*OneBlockFile` — lookup by block ID suffix (pruned by `LibNum`)
  - `blocksByNum map[uint64][]*OneBlockFile` — lookup by block number (pruned by `LibNum`)
  - `seenCanonical map[string]struct{}` — dedup set (pruned)
  - `firstBlockNum`, `lastBlockNum`, `lastDistinctSeen`, `distinctHeights` — for the inline range/gap detection and the summary
  - `missing []missingRange`, `missingBlockCount` — gap ranges detected inline
  - `forks []forkReport`, `reportedForkAtNum` — fork heights detected inline
  - `brokenParentCount` — parent-chain breakage detected inline
  - `highestFinalizedBlock`, `processedCount`, `nextProgressAt`, `progressEachOverride`
- `process(file)` — called for each file as it is streamed:
  1. Dedup via `seenCanonical`; bump `processedCount`.
  2. Update `firstBlockNum`/`lastBlockNum`; when a new distinct height arrives, check for gaps and emit `❌ Missing blocks in range [#X to #Y]` for each gap.
  3. Update `highestFinalizedBlock` from `file.LibNum`.
  4. Detect forks: if another block with a different ID already exists at this height, append a `forkReport` and emit `🔀 Block #N has C candidates (fork)` once per height.
  5. Inline parent-chain continuity check: when the parent height is within the active window and at least one block exists at that height but none matches `PreviousID`, increment `brokenParentCount` and emit `⚠ Block #N expects parent … at #P`.
  6. Emit progress when `processedCount` reaches `nextProgressAt`; recompute `nextProgressAt` from the ladder (or override).
  7. Prune state at/below `highestFinalizedBlock`.
- `summary()` — prints the discovered block range, processed/finalized counters, missing/forked/broken counts and an accurate parent-chain continuity status (only `🆗 Parent chain is continuous` when both `missingBlockCount` and `brokenParentCount` are zero).
- Registered in `check.go` alongside existing subcommands.

### Tests (11 passing)

- `TestOneBlocksState_NoHole`
- `TestOneBlocksState_DetectsHole`
- `TestOneBlocksState_DetectsHoleRange` — height jump > 1 produces a single `missingRange` covering all missing heights
- `TestOneBlocksState_PrunesStateOnFinalization`
- `TestOneBlocksState_NoHoleAfterFinalized`
- `TestOneBlocksState_DetectsFork` — two distinct IDs at the same height
- `TestOneBlocksState_DuplicateNotFork` — re-processing the same canonical name is not a fork
- `TestOneBlocksState_BrokenParentLinkage` — contiguous heights with mismatched `PreviousID` increments `brokenParentCount`
- `TestNewOneBlocksState_ProgressEachOverride`
- `TestOneBlocksState_ProgressLadder` — `progressStep` returns 10K / 100K / 500K / 1M at the expected count boundaries
- `TestOneBlocksState_ProgressNextStep` — `computeNextProgress` advances correctly across ladder bands

## State Tracker

**Last Updated:** 2026-05-26
**Current Step:** Step 7 — Round 6 Dev Feedback Addressed, Ready for Re-Review
**Status:** Round-6 feedback addressed; inline streaming restored, label padding fixed; all 11 tests pass; committed as `4445469`.

### Step 2 (completed)
Implementation done; all tests pass; committed as `90cef4d`.

### Step 3 (completed)
Addressed dev feedback (round 1/2):
1. **`filesSeen` memory leak** — Replaced the unbounded `filesSeen map[string]bool` with `seenCanonical map[string]struct{}` that is pruned in lock-step with `blocksByNum` inside `pruneBelow()`. Entries below `highestFinalizedBlock` are deleted from both maps, bounding memory to the live finalization window.
2. **Serial Walk/process** — Added a buffered channel (`fileCh`, capacity 128) between the Walk goroutine (producer) and the processing loop (consumer). The store listing now runs concurrently with processing so that fetching the next filename is not blocked by processing the current one.
3. **stylex rendering** — Replaced all plain `fmt.Printf` calls with `stylex` helpers.
Committed as `ce5a114`.

### Step 4 (completed)
Addressed round-3 dev feedback:
1. **Double `##` in block-range print** — All formatting paths now build `#%s` once around an already-numeric value (no value carries a leading `#`), so the block range line reads `Block range found: #X to #Y (N distinct heights)` with a single `#`. Same fix applied throughout summary, progress, hole and fork lines.
2. **Progress ladder** — Replaced the flat `--progress-each` behavior with an automatic ladder when the flag is `0` (default): every 10K below 100K processed, every 100K below 500K, every 500K below 1M, then every 1M. Passing a non-zero `--progress-each` value still works as a manual override. Implemented in `progressStep` and `computeNextProgress`; covered by `TestOneBlocksState_ProgressLadder` and `TestOneBlocksState_ProgressNextStep`.
3. **Missing-blocks-range separator** — Output is now `❌ Missing blocks in range [#X to #Y]` (changed from the previous `,` separator).
4. **Double `##` in fork print** — Fork line now reads `🔀 Block #X has N candidates (fork)`.
5. **Parent-chain continuity is inlined** — Removed the standalone post-walk continuity pass. Linkability is now verified inline as each block is processed.
6. **`Parent chain is continuous` accuracy** — Summary reflects what was actually observed.

### Step 5 (completed)
Addressed round-4 dev feedback:
1. **Interleaved available/missing ranges** — When a gap is detected, the code now first prints `✅ Available blocks in range [#from to #to]` for the contiguous segment that just ended, then `❌ Missing blocks in range [#from to #to]` for the gap. The trailing available segment (after the last gap, or the entire range if no gaps exist) is printed at the start of `summary()` just before the separator. Implemented by adding a `currentAvailableStart` field to `oneBlocksState`, initialised to the first block seen and reset to the current block number each time a gap is detected.
2. **Removed redundant continuity line** — The `✘ Parent chain is NOT continuous` / `🆗 Parent chain is continuous` lines have been removed from the summary. The individual counts already carry that information.
3. **Simplified Status line** — No emoji prefix; reads `Status                  : ok` (green) or `Status                  : broken` (red), aligned with the other summary labels.
Committed as `2cb2fce`.

### Step 6 (completed)
Addressed round-5 dev feedback:
1. **Right-aligned block numbers in range output** — Range events (available and missing) are now buffered in a `rangeEvents []rangeEvent` slice during the walk instead of being printed inline. In `summary()`, the max formatted number width is computed across all events and `fmt.Sprintf("%*s", ...)` right-pads each number to that width so the `#`, `to`, and `]` columns all line up. The label "Missing blocks in range" is padded with one extra space to match "Available blocks in range" so the opening bracket is also column-aligned.
Committed as `df9e014`.

### Step 7 (current)
Addressed round-6 dev feedback (supersedes round-5):
1. **Reverted buffering approach** — Removed the `rangeEvent` type and `rangeEvents []rangeEvent` field introduced in round-5. Restored the inline/streaming approach where `✅` and `❌` lines are printed during the walk as they are discovered, not deferred to `summary()`.
2. **Label padding for `[` alignment** — "Missing blocks in range" padded with 2 extra spaces to match the width of "Available blocks in range" (both 25 chars), so the opening `[` aligns:
   ```
   ✅ Available blocks in range [#69,815,484 to #69,815,499]
   ❌ Missing blocks in range   [#69,815,500 to #74,677,299]
   ```
Committed as `4445469`.
