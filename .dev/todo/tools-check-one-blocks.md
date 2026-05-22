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

*Round 4 of feedback*

```
❌ Missing blocks in range [#69,815,500 to #74,677,299]
❌ Missing blocks in range [#74,677,489 to #74,772,819]
❌ Missing blocks in range [#74,772,900 to #74,776,699]
❌ Missing blocks in range [#74,776,826 to #74,776,828]
❌ Missing blocks in range [#74,776,900 to #74,822,999]

One-blocks check complete
──────────────────────────────────────────────────
Block range found: #69,815,484 to #74,823,106 (589 distinct heights)
  Total files processed   : 591
  Highest finalized block : #74,823,104
  Missing blocks          : 5,007,034
  Missing ranges          : 5
  Forked heights          : 0
  Broken parent linkages  : 0
  ✘ Parent chain is NOT continuous (5,007,034 missing block(s), 0 broken parent link(s))
  ✘ Status: PROBLEMS FOUND
```

1. In addition to missing range, also shows before the available range, that will help get a better, for example here above we would have got:

```
<checkmark> Available blocks in range [#69,815,484 to #69,815,499]
❌ Missing blocks in range [#69,815,500 to #74,677,299]
<same here for range #74,677,300 - #74,677,488>
❌ Missing blocks in range [#74,677,489 to #74,772,819]
```

1. Let's remove `Parent chain is NOT continuous ...` line, I file it's useless all information is already there

1. For `✘ Status: PROBLEMS FOUND`, lets use:

- `Status<proper alignement>: ok` (in green when all good)
- `Status<proper alignement>: broken` (in red when any problem detected)

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

**Last Updated:** 2026-05-22
**Current Step:** Step 5 — Round 4 Dev Feedback Addressed, Ready for Re-Review
**Status:** All three round-4 feedback items addressed; all 11 tests pass; committed as `2cb2fce`.

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

### Step 5 (current)
Addressed round-4 dev feedback:
1. **Interleaved available/missing ranges** — When a gap is detected, the code now first prints `✅ Available blocks in range [#from to #to]` for the contiguous segment that just ended, then `❌ Missing blocks in range [#from to #to]` for the gap. The trailing available segment (after the last gap, or the entire range if no gaps exist) is printed at the start of `summary()` just before the separator. Implemented by adding a `currentAvailableStart` field to `oneBlocksState`, initialised to the first block seen and reset to the current block number each time a gap is detected.
2. **Removed redundant continuity line** — The `✘ Parent chain is NOT continuous` / `🆗 Parent chain is continuous` lines have been removed from the summary. The individual counts already carry that information.
3. **Simplified Status line** — No emoji prefix; reads `Status                  : ok` (green) or `Status                  : broken` (red), aligned with the other summary labels.
Committed as `2cb2fce`.
