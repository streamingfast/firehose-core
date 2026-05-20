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

*Round 3 of feedback*

- `Block range found: ##23 666 500 to ##74 823 106 (5960648 distinct heights)` Double # here in print
- We need some progress, let's use a ladder first at 10K, second at 100K, third at 500K, last as 1M (repeat each millions after) blocks so we account for large folder (rare but happens on reproc job)
- `❌ Missing blocks in range [#23 666 815, #23 666 825]` let's use `❌ Missing blocks in range [#23 666 815 to #23 666 825]`
- `🔀 Block ##69 815 484 has 2 candidates (fork)` double dash here too.
- `Checking parent-chain continuity...` this is printed at the very end just before summary but it's unclear what it check, should be simply done/included while checking for other?
- `> 🆗 Parent chain is continuous` this was printed but we reported multiple holes, chain cannot be continuous in this case.

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

**Last Updated:** 2026-05-20
**Current Step:** Step 4 — Round 3 Dev Feedback Addressed, Ready for Re-Review
**Status:** All six round-3 feedback items addressed; tests pass.

### Step 2 (completed)
Implementation done; all tests pass; committed as `90cef4d`.

### Step 3 (completed)
Addressed dev feedback (round 1/2):
1. **`filesSeen` memory leak** — Replaced the unbounded `filesSeen map[string]bool` with `seenCanonical map[string]struct{}` that is pruned in lock-step with `blocksByNum` inside `pruneBelow()`. Entries below `highestFinalizedBlock` are deleted from both maps, bounding memory to the live finalization window.
2. **Serial Walk/process** — Added a buffered channel (`fileCh`, capacity 128) between the Walk goroutine (producer) and the processing loop (consumer). The store listing now runs concurrently with processing so that fetching the next filename is not blocked by processing the current one.
3. **stylex rendering** — Replaced all plain `fmt.Printf` calls with `stylex` helpers.
Committed as `ce5a114`.

### Step 4 (current)
Addressed round-3 dev feedback:
1. **Double `##` in block-range print** — All formatting paths now build `#%s` once around an already-numeric value (no value carries a leading `#`), so the block range line reads `Block range found: #X to #Y (N distinct heights)` with a single `#`. Same fix applied throughout summary, progress, hole and fork lines.
2. **Progress ladder** — Replaced the flat `--progress-each` behavior with an automatic ladder when the flag is `0` (default): every 10K below 100K processed, every 100K below 500K, every 500K below 1M, then every 1M. Passing a non-zero `--progress-each` value still works as a manual override. Implemented in `progressStep` and `computeNextProgress`; covered by `TestOneBlocksState_ProgressLadder` and `TestOneBlocksState_ProgressNextStep`.
3. **Missing-blocks-range separator** — Output is now `❌ Missing blocks in range [#X to #Y]` (changed from the previous `,` separator).
4. **Double `##` in fork print** — Fork line now reads `🔀 Block #X has N candidates (fork)`.
5. **Parent-chain continuity is inlined** — Removed the standalone post-walk continuity pass. Linkability is now verified inline as each block is processed, in two complementary ways: (a) gaps in the height sequence are reported as soon as a height jump > 1 is observed (the `❌ Missing blocks in range […]` line); (b) when consecutive heights are present but the new block's `PreviousID` does not match any known block at the parent height, a `⚠ Block #X expects parent … but no matching block was found at #Y` line is emitted and `brokenParentCount` is incremented. New test `TestOneBlocksState_BrokenParentLinkage` covers (b).
6. **`Parent chain is continuous` accuracy** — Summary now reflects what was actually observed: if `missingBlockCount > 0` or `brokenParentCount > 0` the line becomes `✘ Parent chain is NOT continuous (M missing block(s), B broken parent link(s))`. Only when both counts are zero does the line read `🆗 Parent chain is continuous`.

Other touches:
- Updated `CHANGELOG.md` `Unreleased` entry to describe the new output style and progress ladder.
- Rewrote tests around the new state fields (`missingBlockCount`, `missing`, `forks`, `brokenParentCount`) and removed the obsolete per-block hole count assertions.
