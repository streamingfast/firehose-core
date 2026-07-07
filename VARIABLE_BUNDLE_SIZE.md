# Variable merged-blocks bundle size

Merged-blocks files were hardcoded to 100 blocks everywhere. This change makes the
size configurable per chain (e.g. 1000), while a single substreams-tier2 process can
still serve chains with different sizes at once. All work is on branch
`feature/variable-bundle-size` in the four repos.

## How it works

- **One process-wide default**: `bstream.DefaultMergedBlocksBundleSize` (default 100),
  set once at startup by the new `--common-merged-blocks-bundle-size` flag
  (`cmd/apps/start.go`), following the `GetProtocolFirstStreamableBlock` pattern. Every
  `FileSource` (firehose reads, cursor resolution, single-block fetch) picks it up.
- **Explicit override where per-request behavior is needed**:
  `bstream.FileSourceWithBundleSize` / `stream.WithMergedBlocksBundleSize`. Substreams
  tier2 always uses the per-request value from the subrequest, never the process flag.
- **Filenames unchanged** (`%010d` base block num); the size is configuration, not
  encoded in the file name. A merged-blocks consumer configured too small fails fast
  with a clear error; a size mismatch in either direction is also caught at startup by
  the advertise/info server (see caveats).
- **Valid values**: positive multiples of 100 (validated at startup and in tools).

## Changes per repo

### bstream (base of the stack)

- `DefaultMergedBlocksBundleSize` package var; `FileSource` default now reads it
  (`filesource.go`), `registry.go`.
- New `stream.WithMergedBlocksBundleSize(uint64)` option (`stream/options.go`).
- `FileSource` errors out when a file contains a block ≥ base + bundle size (guard
  against reading a 1000-blocks store at 100).
- Hub bootstrap rounding (`hub/hub.go substractAndRoundDownBlocks`) now takes the
  bundle size instead of hardcoding 100.

### substreams

- New internal proto field `ProcessRangeRequest.merged_blocks_bundle_size = 22`
  (0 ⇒ 100 for requests from older tier1s); helper
  `MergedBlocksBundleSizeOrDefault()`.
- `Tier1Config.MergedBlocksBundleSize` (default 100) →
  `Tier2RequestParameters.MergedBlocksBundleSize` → emitted by
  `orchestrator/work/worker.go NewRequest` on every subrequest → applied per-request in
  `service/tier2.go` (`StreamFactory` is built per request; no cross-chain leakage).
- Tier1's own reads: `StreamFactory.mergedBlocksBundleSize`, cursor resolver
  (`pipeline.NewCursorResolver` now takes `...bstream.FileSourceOption`), and
  `GetRecentFinalBlock` rounding (extracted `roundToBundleFinalBlock`, unit-tested).
- Hub `keepFinalBlocks = max(200, 2×bundleSize)`; live backfiller default delay
  `max(120, bundleSize+20)` (unchanged at 100, scales at 1000).
- `substreams tools tier2call --merged-blocks-bundle-size`.

### firehose-core

- `--common-merged-blocks-bundle-size` flag (default 100, validated multiple of 100),
  sets the bstream global at startup; wired into the merger (`merger.Config.BundleSize`)
  and substreams-tier1 (`config.MergedBlocksBundleSize`).
- `MergedBlocksWriter` gets a `BundleSize` field (all `99`/`100` literals removed);
  `LowBoundary` kept, new `LowBoundaryFor(num, size)`.
- Firehose hub `keepFinalBlocks = max(500, 2×bundleSize)`.
- Tools: shared `--merged-blocks-bundle-size` flag (check, print, compare, merge,
  unmerge, upgrade, download-from-firehose, fix tools). Read through
  `firecore.GetMergedBlocksBundleSizeFlag(cmd)`, which validates the value (positive
  multiple of 100) at every call site so a bad size errors up-front instead of
  panicking on a modulo/divide deeper in the process. A `ToolsCmd.PersistentPreRunE`
  can't be used for this — it would shadow the root command's setup hook.
- `tools print merged-blocks` open-range fix: caps the stream at the last available
  merged-blocks file (found via an `O(log n)` exponential-probe + binary-search on
  `FileExists`, not a full store listing) so it stops cleanly instead of erroring on
  the expected-missing next file; also guards an unsigned underflow when a closed
  range ends at block 0.
- **New conversion tool**:
  `firecore tools resize-merged-blocks <src> <dst> <start> <stop> --source-bundle-size=100 --target-bundle-size=1000`.
  Both directions supported; sizes must divide evenly; start/stop must be aligned on
  target boundaries; the source must contain the stop block (the process completes upon
  reading it).
- **Working example**: `devel/bundle1000/` (copy of `devel/standard/` with
  `common-merged-blocks-bundle-size: 1000` and a faster dummy chain). See its README
  for verification commands.

### firehose-ethereum

- `--bundle-size` flag (default 100) on the 5 offline fix tools that walked the store
  assuming 100-block files (`fix-ordinals`, `fix-any-type`, `remove-gas-changes`,
  `fix-withdrawals`, `find-unknown-status`).

## Verified end-to-end (devel/bundle1000, dummy-blockchain)

- Merger produced `0000000000`, `0000001000`, `0000002000` (1000 blocks each).
- `tools check merged-blocks --merged-blocks-bundle-size=1000`: no holes.
- `tools print merged-blocks` reads inside a 1000-file.
- `tools firehose-client 995:1005` streams across the bundle boundary.
- `substreams run --production-mode` (chain-agnostic package, blocks 1–1500) through
  tier1→tier2: tier2 logs show `merged_blocks_bundle_size: 1000` on subrequests,
  completed successfully.
- `resize-merged-blocks` round trip 1000→100→1000, `check merged-blocks` clean both ways.
- Misconfig guard: reading the 1000-store with the default 100 errors with
  `merged blocks file "0000000000" contains block 100, beyond the configured bundle size of 100`.
- Back-compat gate: `devel/standard` (no flag) still produces/reads 100-block files;
  full `go test ./...` green in all four repos.

## Caveats / things to look into before prod

1. **Bundle size does not match the files.** A merged-blocks consumer configured
   smaller than the files (e.g. default 100 over a 200-block store) looks for
   `0000000100`, which does not exist; configured bigger (100-store read at 1000) jumps
   over blocks. Either way the stream silently skips blocks and stalls with unlinkable
   blocks far from the root cause — and the consumer itself only *warns* ("hole in
   merged files", "too many unlinkable blocks"), it does not fail. The advertise/info server now hard-errors at
   startup (`--ignore-advertise-validation` skips it). Merged-blocks stores have no
   holes in normal operation (an empty bundle is still written as an empty file), so it
   works from the listing alone: the gap between the first two file boundaries is the
   real bundle size and must equal the configured one, else it errors with the value to
   set. Only the degenerate single-file store falls back to reading that one file's
   content (no second boundary to measure against).
2. **Fleet-wide consistency per chain.** Merger, firehose, substreams-tier1 and any
   index-builder must all agree on the value for a given chain's store. Multi-machine
   config drift = silent skipping (see #1).
3. **Rollout order for substreams.** Upgrade every tier2 fleet *before* setting a
   non-100 value on any tier1. Old tier2s ignore the new field and read at 100 → their
   jobs stall retrying nonexistent file names. Old tier1 → new tier2 is safe (0 ⇒ 100).
4. **Memory.** The merger and `MergedBlocksWriter` buffer a full bundle in RAM. At
   1000 blocks on chains with big blocks (e.g. 80 MiB), that's tens of GiB. The merger's
   `maxUnlinkableBlocks = 4×bundleSize` and the one-block prune clamp also scale up.
5. **Hub bootstrap needs deeper one-block history.** The hub rounds its lowest kept
   block down to a bundle boundary; with `keepFinalBlocks = 2×bundleSize` it needs up to
   ~3×bundleSize one-block files at startup. `merger-prune-one-block-files-after` clamps
   to the bundle size automatically, but external/aggressive one-block pruning can break
   hub bootstrap.
6. **Live latency for substreams linear handoff.** `GetRecentFinalBlock` rounds down a
   full bundle: production-mode handoff can sit up to ~2×bundleSize behind head. At 1000
   on a slow chain that is a real delay; at 100 nothing changes.
7. **Transform indexes (`common-index-block-sizes`).** Index files were built assuming
   100-aligned boundaries. `BlocksInRange` is parameterized and 1000 is already an
   accepted index size, but index-based skipping over non-100 bundles was NOT exercised
   end-to-end here. Verify or rebuild indexes before enabling on an indexed chain.
8. **go.mod replaces.** The branches in firehose-core, substreams and firehose-ethereum
   carry `replace` directives to local sibling checkouts
   (`../bstream`, `../substreams`, `../firehose-core`). CI will not build them as-is:
   merge bstream first, then bump each downstream and drop the replaces.
9. **LiveBackFiller heuristic.** The default "merged file is written" delay now scales
   (`bundleSize+20`); it stays 120 at size 100. Verify in a prod-like live environment
   at 1000 — the escape hatch `Tier1Config.LiveBackFillerFinalBlockDelay` still exists
   but is not exposed as a firehose-core flag.
10. **`keepFinalBlocks` behavior change is capped, not literal 2×.** To avoid
    regressions at 100, firehose keeps `max(500, 2×size)` and tier1 `max(200, 2×size)`.
    If you want a strict `2×size` (i.e. 200 for firehose at size 100), trim the `max()`.
11. **Downloader/other consumers.** Anything outside these four repos that reads
    merged-blocks with its own hardcoded 100 (custom sinks, external tooling) will need
    the same treatment.
