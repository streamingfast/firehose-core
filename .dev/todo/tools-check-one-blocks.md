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

1. `filesSeen` is never pruned, if a one-blocks dir as 24M blocks we will most probably run out of memory, use one block file LIB to determine what to prune.
2. `state.process(file)` is called serialily with the `Walk` process, this prevent the next file to be listed, let's use a channel to process out of bound the file names
3. Use our stylex library to render the progress with nicer rendering and coloring + emoticons.

## Spec & Implementation

### Implementation

New file: `cmd/tools/check/one_blocks.go`

- `newCheckOneBlocksCmd()` — creates the cobra subcommand `tools check one-blocks <store-url>`
  - Flag `--progress-each uint64` (default 0 = disabled): print progress every N blocks
- `oneBlocksState` struct — tracks in-flight state during the walk:
  - `blocksByID map[string]*OneBlockFile` — lookup by block ID suffix
  - `blocksByNum map[uint64][]*OneBlockFile` — lookup by block number (for hole-length estimation)
  - `highestFinalizedBlock uint64` — updated from each file's `LibNum`
  - `holeCount int` — cumulative hole count
- `process(file)` — called for each file as it is streamed:
  1. Update `highestFinalizedBlock` from `file.LibNum`
  2. Register block in both maps
  3. Check linkability: if `file.Num > 0` and `parentID` not in `blocksByID` **and** parent num is above the finalized boundary → hole detected, print message
  4. Estimate hole length by walking backwards in `blocksByNum`
  5. Print progress if `progressEach > 0`
  6. Prune state at/below `highestFinalizedBlock`
- `summary()` — prints totals at end
- Registered in `check.go` alongside existing subcommands

### Tests (6 passing)

- `TestOneBlocksState_NoHole` — consecutive chain produces no holes
- `TestOneBlocksState_DetectsHole` — missing block 1 between 0 and 2 → 1 hole
- `TestOneBlocksState_PrunesStateOnFinalization` — libNum-driven pruning works correctly
- `TestOneBlocksState_NoHoleAfterFinalized` — parents pruned by finalization don't count as holes
- `TestOneBlocksState_DetectHoleLength` — hole length estimation returns ≥ 1
- `TestNewOneBlocksState_ProgressEach` — flag wired correctly

## State Tracker

**Last Updated:** 2026-05-15
**Current Step:** Step 3 — Dev Feedback Addressed, Ready for Re-Review
**Status:** All three feedback items addressed; tests pass.

### Step 2 (completed)
Implementation done; all tests pass; committed as `90cef4d`.

### Step 3 (current)
Addressed dev feedback:
1. **`filesSeen` memory leak** — Replaced the unbounded `filesSeen map[string]bool` with `seenCanonical map[string]struct{}` that is pruned in lock-step with `blocksByNum` inside `pruneBelow()`. Entries below `highestFinalizedBlock` are deleted from both maps, bounding memory to the live finalization window.
2. **Serial Walk/process** — Added a buffered channel (`fileCh`, capacity 128) between the Walk goroutine (producer) and the processing loop (consumer). The store listing now runs concurrently with processing so that fetching the next filename is not blocked by processing the current one.
3. **stylex rendering** — Replaced all plain `fmt.Printf` calls with `stylex` helpers: errors/holes use `stylex.Error`/`stylex.Warn`, progress uses `stylex.Dim`/`stylex.Value`, summary uses `stylex.Title`/`stylex.Label`/`stylex.Success`/`stylex.Error` with ✔/✘/⚠/↻ emoticons.
