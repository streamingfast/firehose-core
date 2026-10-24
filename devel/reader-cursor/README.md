# Reader cursor (`--reader-node-cursor`)

A fake chain, a reader for it, and two scripts. They show what `--reader-node-cursor` changes when firecore and its reader are killed together, and how a reader uses it.

## What it shows

A reader that records a block as done as soon as it prints it can lose blocks for good. Between the `FIRE BLOCK` line and the one-block file, the block sits in firecore's in-memory pipeline:
- the line channel;
- the blocks channel;
- the local file awaiting the next upload pass.

A SIGKILL of both processes loses those blocks. The reader then continues after them, and the merger can't link past the gap.

With `--reader-node-cursor`, firecore records the last block it stored, in `reader-cursor.json` in `--reader-node-working-dir`. It gives that back to the reader through the `{resume-*}` and `{cursor-file}` templates of `--reader-node-arguments`.

## The pieces

- **`fakechain/`:** a deterministic chain.
  - Block n's id is a hash of n.
  - In the stateful mode, the state after block n is a hash of the state before and n. A block's state can only be computed by going through every block before it.
- **`fakereader/`:** prints the chain as `FIRE` lines.
  - Modes:
    - `--mode=rewind`: keeps no state and can print any block again, like an RPC poller.
    - `--mode=stateful`: keeps a state committed every `--commit-interval` blocks that can't go back, like an indexer writing its own database.
  - Protocols:
    - `--protocol=legacy`: records the last block it printed and continues after it, as `blockpoller` does with `cursor.json`.
    - `--protocol=cursor`: uses the reader cursor and prints `FIRE CURSOR` lines.
- **`checkmerged/`:** reads a merged-blocks store. Every block from the first to the last must be there once, with the chain's ids and parents, and (`--stateful`) states that follow from the block before.
- **`kill-test.sh <rewind|stateful> <legacy|cursor> [kills] [target]`:**
  1. Runs `firecore start reader-node merger` with the reader.
  2. SIGKILLs both together `kills` times (default 8) at random moments, 2–5 s into each run.
  3. Runs to `target` (default 20,000).
  4. Checks the merged blocks.
- **`restore-test.sh [target]`:** a stateful reader whose state is put ahead of the cursor. It must refuse to start, then recover from a snapshot.

Both scripts build firecore and the example from this checkout into `work/bin` (`SKIP_BUILD=1` reuses them). Each run's data and firecore logs go to `work/<test>/`.

## Running

```sh
devel/reader-cursor/kill-test.sh rewind legacy     # merger stuck on lost blocks
devel/reader-cursor/kill-test.sh rewind cursor     # no gap
devel/reader-cursor/kill-test.sh stateful legacy   # merger stuck; the lost blocks need an older state
devel/reader-cursor/kill-test.sh stateful cursor   # no gap, every state follows from the block before
devel/reader-cursor/restore-test.sh
```

The `legacy` runs end with `FAIL: merged blocks end at …`, and the `cursor` runs with `OK: blocks 1 to …, no gap`. Each takes 1–3 minutes.

## How a reader uses the cursor

`fakereader/main.go`, `runRewind` and `runStateful`, is the reference.

### A reader that can produce any block again

Pollers, for example:
- Start at `{resume-lib-num}` + 1, or the configured start block when it's empty.
- Print the blocks again from there. firecore ignores the ones it already has.
- Optionally print `FIRE CURSOR <num> <id> <value>` after a block, to get `<value>` back as `{resume-cursor}`, a page token say.

### A reader whose state only moves forward

1. **On start, compare the state's height with `{resume-block-num}`.**
   - *Past it:* refuse to start. Those blocks can't be produced again without an older snapshot.
   - *At or before it:* index again without printing up to the cursor, then print from the next block. With `{resume-cursor}`, check the rebuilt state against it.
2. **Before committing state at block N,** wait until `{cursor-file}` shows N or later. The file can be read while the reader runs; commits wait for the next upload pass, under a second here.

### A reader that doesn't use the cursor yet

It runs as before when `--reader-node-cursor` is set, as long as `--reader-node-arguments` uses none of the new templates. firecore keeps the file anyway, and nothing waits on the reader.
