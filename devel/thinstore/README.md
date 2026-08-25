# thinstore — pruned store snapshots test bench

Re-runnable local bench for the "thin store" feature: substreams-tier1 resuming from
the last remaining store snapshot after intermediate snapshots were pruned.

Everything runs on the dummy blockchain, offline (no live component), with store
snapshots every 100 blocks so a 30k-blocks history yields 300 snapshots per store.

## One-time: generate merged blocks

```bash
go install github.com/streamingfast/dummy-blockchain@latest
./gen-blocks.sh -c            # ~1 min, writes ./firehose-data/storage/merged-blocks (0..30399)
```

`-c` wipes `./firehose-data`; without it the existing data is reused. Pass a block
count to change the size (`./gen-blocks.sh -c 60500`).

## Build the test package (once, or after editing `substreams/src/lib.rs`)

```bash
./substreams/build.sh         # cargo build (wasm32) + substreams pack
```

The package only reads the Clock, so it is chain agnostic, and every output is a pure
function of the block number, so any run can be compared against the baseline. Four
stages with several mappers and stores each, plus a block index:

    stage 1: index_parity, map_a, store_count, store_a_max
    stage 2: store_sum, store_a_last, map_b
    stage 3: store_c, map_c (filtered by index_parity)
    stage 4: map_out

## Run

Terminal 1 — tier1 (:10016) + tier2 (:10017) over the merged blocks, logs in `logs/offline.log`:

```bash
./start-offline.sh
```

Terminal 2 — the scenarios:

```bash
./test.sh                     # wipes the state store, rebuilds the baseline, prunes, tests
./test.sh -k                  # keeps the state store from a previous run
```

`test.sh`:

1. runs `map_out` over `[0, 30000)` in production mode → baseline output in
   `out/baseline.jsonl`, 1500 snapshots, 2700 output files and 300 index files on disk;
2. prunes with `firecore tools substreams prune-states --keep-every 1000`, then punches
   different extra snapshot holes per store and stage, deletes intermediate and final
   mapper outputs and index files over various ranges;
3. runs production-mode and dev-mode requests over ranges crossing those holes (outputs
   present near head but not earlier, partially present within the range, holes at
   every stage...), compares every block's output against the baseline slice and checks
   `logs/offline.log` for any attempt to load a pruned file. Each line prints the resume
   point tier1 picked. Ends with a full-range run over every hole at once.

Outputs land in `out/<scenario>.jsonl` (+ `.err`). Knobs: `LAST` (baseline end block),
`KEEP_EVERY`, `ENDPOINT`.

## Fuzz

```bash
./fuzz.py --seed 1 --iterations 20 --queries 6            # sequential queries
./fuzz.py --seed 2 --iterations 10 --parallel 4 --chaos   # concurrent queries, files deleted while they run
./fuzz.py --replay out/fuzz-failure-<seed>-<iteration>.json
```

Needs `out/baseline.jsonl` (from `test.sh`). Each iteration applies a random deletion
plan per module and file kind (random holes, keep-every-N, contiguous spans, everything
below a block, everything but a few, everything, boundaries chosen so stores share no
common kept snapshot), then runs random production/dev queries with ranges biased
towards the hole edges (±1, ±100 blocks), tiny ranges, long ranges and tail ranges.
Every result is compared to the baseline and the tier1 log is scanned for pruned-file
loads, panics and invalid transitions. A failure writes a replayable report.
