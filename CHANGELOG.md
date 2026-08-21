# Change log

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). See [MAINTAINERS.md](./MAINTAINERS.md)
for instructions to keep up to date.

Operators, you should copy/paste content of this content straight to your project. It is written and meant to be copied over to your project.

If you were at `firehose-core` version `1.0.0` and are bumping to `1.1.0`, you should copy the content between those 2 version to your own repository, replacing placeholder value `fire{chain}` with your chain's own binary.

## v1.18.0

### Added

- Added flag `--validate-blocks` (`-b`) on `firecore tools check merged-blocks`, ensures that individual block sequence is checked (reading the content of the merged blocks, instead of just checking by file name). Flag is implied by `--print-stats` and `--print-full`

- Bumped `firehose-networks` to [v0.2.3](https://github.com/streamingfast/firehose-networks/releases/tag/v0.2.3): Added Ethereum Hoodi testnet (`hoodi`, `eip155:560048`)  StreamingFast Firehose and Substreams endpoints (`hoodi.eth.streamingfast.io:443`).

- Bumped `substreams` to [v1.21.1-0.20260820161654-1cffa6c10a8d](https://github.com/streamingfast/substreams/compare/v1.21.0...1cffa6c10a8d):

  - Server: `substreams-tier1` emits a periodic `substreams request progress` log per request (after 1 minute, then every 5 minutes) meant to answer "why is my substreams slow?" while the request is still running: phase, per-stage module and job progress, external call cost, last job error, and time spent blocked writing to the consumer. It ends with a short `hints` list naming the likely bottleneck when one is detected. Rates and deltas are suffixed `_5m` and cover a fixed trailing 5 minutes whatever the emission interval is; cadence is tunable with `SUBSTREAMS_PROGRESS_LOG_FIRST_DELAY` and `SUBSTREAMS_PROGRESS_LOG_INTERVAL`.

  - Server: `substreams-tier2` reports its progress every 10 seconds while a block is being processed, instead of only once the block completes, and `ExternalCallMetric` gained `failed_count`, `in_flight_count`, `oldest_in_flight_ms` and `oldest_in_flight_block`. An `eth_call` retrying against an unreachable endpoint is a single wasm extension call that can last minutes: it used to be completely invisible to tier1 until the segment timed out.

  - Server: `substreams-tier1` now restarts when its block hub can no longer link incoming live blocks, the same fix as the `firehose` app one below.

  - Server: the `incoming Substreams Blocks request` log of `substreams-tier1` now carries a `parallelism` object (requested, granted by the authentication layer, effective count and which of the two capped it, plan tier, stage layer executors) plus `parallel_segment_count` and `stage_count`. A client asking for 300 parallel workers and getting 15 previously left no trace of the negotiation in the logs.

  - Server: the same worker accounting is now also reported once the request ends and while it runs. The `substreams request stats` log of `substreams-tier1` gained a `workers` object (`requested`, `granted`, `effective`, `peak`, `pool_exhausted_count`, `pool_rampup_deferred_count`): a high `pool_exhausted_count` with a `peak` well below `effective` means the shared tier2 worker pool ran dry, a case previously only visible at debug level. The periodic `substreams request progress` log gained its own `workers` object (`requested`, `granted`, `effective`, `running`, `idle`, and `pool_exhausted_5m` when jobs failed to get a worker over the window), with a hint naming the three ceilings that can cause it — the tier2 fleet being full, the organization's worker quota, or the server's per-session worker cap — since the pool reports a single error for all three.

  - Server: `substreams-tier1` now tells clients how much of a stage is actually usable rather than merely produced. `ModulesProgress.stages` entries gained `ready_up_to_exclusive` (field 3), the chain block number up to which the stage is immediately usable, and `squash_wait_segment_count` (field 4), the number of segments whose partial exists but has not been squashed in yet. `completed_ranges` counts a segment as soon as its partial is produced, so a stage could render 100% covered with substantial work outstanding — and since squashing runs on tier1 it schedules no job and advances no `processed_blocks`, leaving the request looking frozen at 100% with a rate of zero. `SessionInit` gained `segment_block_count` (field 11), the width in blocks of one parallel segment, without which a client cannot turn `squash_wait_segment_count` into blocks.

  - Server: `substreams-tier1` no longer hangs when a request carries a cursor the block hub declines (unknown hash). Instead of falling back to a file source that waits for merged-blocks to cover that block number, it immediately sends an undo-to-LIB. Same class of problem as the `firehose` app fix above.

  - **Operators** `--substreams-tier2-hosted-store-registry-address` is removed: `substreams-tier1` now resolves hosted foundational stores itself and sends the concrete endpoints to `substreams-tier2`, so tier2 never dials the control plane. Remove the flag from your `substreams-tier2` configuration before upgrading, otherwise the process refuses to start on an unknown flag. `--substreams-tier1-hosted-store-registry-address` and `--substreams-tier1-foundational-stores-config-path` are unchanged.

  - Server: foundational-store endpoint resolution moved to the public `dregistry` plugin chain (JSON map → control-plane `sf.registry.v1` → identifier passthrough) instead of an inline private client. The `--substreams-tier1-foundational-stores-config-path` and hosted store registry address flags are unchanged; they are composed into that chain internally. `substreams-tier1` resolves identifiers once per request and sends concrete endpoints to `substreams-tier2`, so workers no longer dial the control plane. Store dials now honor the registry's TLS flag instead of guessing from a `:443` suffix, each identifier lookup times out after 10 seconds so a hung control-plane RPC cannot stall request setup, and resolution success/failure is recorded on the request progress log and as Prometheus metrics.

  - Server: added `sf.substreams.rpc.v4.Estimator/Estimate` on `substreams-tier1` (gRPC and Connect, path `/sf.substreams.rpc.v4.Estimator/Estimate`), a streaming RPC reporting what a Substreams request would cost before running it, without ever sending the data. Given a package, an output module, a block range and a sampling percentage (default 1%, capped at 10%), it reports how many blocks the real request would have to process — stage multiplier included, already-cached segments excluded — and the estimated uncompressed egress for the range. The egress figure is measured rather than guessed: the sample is actually executed on `substreams-tier2` workers, then only the *size* of the resulting output cache is read back, from the object store's metadata when it is there, so the data itself is never downloaded. A module graph with stores is only estimated over a range whose store snapshots the endpoint already holds; anything else is refused, naming the part of the range that could be estimated instead, since building the missing stores is the very cost being reported. **Operators**: the endpoint is served by `substreams-tier1` with no new flag, and it consumes `substreams-tier2` worker capacity while a sample runs — a sample is a real, if small, production-mode request.

  - Server: `substreams-tier2` records the uncompressed size of each execution output file it writes as `datasize` object metadata, and how many items it holds as `itemcount`, so what a segment represents can be known without downloading it. The item count is the number of `BlockScopedData` messages a consumer of that segment receives, which on a module gated by a block index is far below the number of blocks the segment covers. Skipped on object stores where setting metadata means rewriting the object (S3), which falls back to reading the file when the figures are needed.

  - CLI: `substreams estimate` now asks the endpoint for the estimate through the new RPC instead of sampling from the client, with `--sample-percentage` (max 10) selecting how much of the range the endpoint runs. The previous client-side sampling is still available as `substreams estimate-local`, for endpoints that do not serve the new RPC.

  - CLI: `substreams auth` opens a browser to pick an organization and an API key instead of requiring a pasted token; the paste flow remains under `--paste`.

  - Server: new `context` WASM host module, giving modules a `context::clock(output_ptr)` intrinsic callable at any point during execution, writing the block clock as an encoded `sf.substreams.v1.Clock` (a `{ptr, len}` pair at `output_ptr`, the same convention the `state` getters use). Available on the `wasmtime` and `wazero` runtimes, not on the JavaScript/v8 one. Until now the clock was only reachable by declaring `source: sf.substreams.v1.Clock` as a module input. `context` joins `env`, `state` and `logger` as a namespace WASM extensions cannot register into.

### Changed

- The help output of every command except the root command and `start` no longer prints the global flags in full, they are replaced by a single line pointing at the new `--gh` flag:

  ```
  Global Flags:
        --gh   Show global flags help, 10 global flags hidden
  ```

  Use `fire{chain} tools print one-block --gh` (or `--global-help`) to display them, either on its own or together with `-h`/`--help`. Long global flag descriptions were pushing a command's own flags out of sight, mostly under `fire{chain} tools ...`.

- Flags inherited from an intermediate command are now printed in their own help section instead of being mixed into `Global Flags`, so `fire{chain} tools ...` commands show a `Tools Flags` section with `--output`, `--bytes-encoding`, `--proto-paths` and `--merged-blocks-bundle-size`.

### Deprecated

- `firecore.HideGlobalFlagsOnChildCmd` is now a no-op and prints a deprecation warning when called, remove the call from your chain. Global flags are all hidden behind `--gh` now, hiding a hand-picked subset of them is no longer useful.

### Fixed

- Bumped `bstream`: a Firehose or Substreams request over a range containing an unreadable merged-blocks file no longer hangs. The goroutine reading a merged-blocks file shuts the stream down on failure without ever closing that file's blocks channel, and the consuming loop was a plain `range` over it, so depending on which of the two won the race the request could stall forever instead of returning the error. Found on a store holding a zero-byte merged-blocks file (no DBIN header at all, written by a merger predating the streaming bundle writer); such a file still fails, now deterministically, with `unable to create block reader: unable to read file header: EOF`. A merged-blocks file holding a DBIN header and no block stays valid: it is what the merger writes for a bundle range containing no block on chains that skip block numbers.

- `firecore tools firehose-client --print-clock-only` no longer hangs once the stream is done. It printed every block, then waited forever on the response-ordering goroutine that only the block-decoding path ever starts. `--print-cursor-only` already returned early; the clock path now does too.

- The merger no longer crashes at startup when the most recent merged-blocks bundles hold no block. On chains that skip block numbers a whole bundle range can contain nothing, and the merger writes a merged-blocks file with a header and no block so boundaries stay contiguous. Reading the last block back out of such a file returned no block and was then dereferenced. Startup now walks back to the last bundle that actually has a block. A file without even a DBIN header is still reported as broken.

- The merger's `head_block_time_drift` metric no longer moves backwards. The merger reports its head block time from two places: on startup it publishes the last block of the last merged bundle as a reference point, and while running it reports the one-block files it bundles, read asynchronously. Those writes were unordered, so a stale block time could land after a fresher one and hold the drift high until the next block was bundled. The metric now ignores block times older than the one already reported.

- A Firehose `Blocks` request whose cursor points above this instance's head now fails with `Unavailable` instead of sitting silent until the merged-blocks bundle covering that block number is written (about twenty minutes on a chain bundling 100 blocks). We may simply be lagging while another instance already serves that block, so the client should retry rather than discard its cursor. A cursor no source can resolve stays `InvalidArgument`.


- The reader running in test mode (`--reader-node-test-mode`) now shuts down promptly: pending block comparisons against the production endpoint are cancelled when the process starts terminating, instead of draining the whole buffered blocks channel one remote fetch (and its retry backoff) at a time. Comparisons are still completed when the termination comes from reaching `--reader-node-stop-block-num`. The comparator's gRPC connection is also closed on the way out.

- The `firehose` app now restarts when its block hub can no longer link incoming live blocks, instead of hanging every request at a frozen head indefinitely. A live-source gap whose one-block files were already merged away can never be linked, and `head_block_number`/`finalized_block_number` keep tracking the live source, so the process looked healthy throughout.

- Restarting a supervised node process no longer deadlocks the whole superviser. `Start` waited for a previous process still stopping while holding the lock that also guards `IsRunning`, `LastExitCode` and `Stopped`, so a process that never reached a final state — one ignoring `SIGTERM`, or whose children keep its stdout/stderr open — froze every one of those calls forever. Waiting no longer holds that lock, and `Start` now gives up after 30s with an error instead of blocking indefinitely.

### Security

- Bumped `golang.org/x/mod` to `v0.40.0`, fixing CVE-2026-56864 and CVE-2026-56865 (both HIGH), reported against the binary image by vulnerability scanners.

## v1.17.0

### Added

- New `--substreams-tier2-segment-stall-timeout` flag (default `10m`) on `substreams-tier2`: a segment is now killed when it stops processing blocks for that long, the deadline resetting on every block processed. See the `substreams` bump below for why this replaces the fixed execution budget as the primary guard.

- `rpc.Clients` now tracks a provider name per client. New `AddNamed(client, name)` registers a client under an explicit name, `Add(client)` keeps working unchanged and auto-names the client `client-<index>`, and `Names()` returns all provider names in pool order.

- `rpc.Clients.Reset()` brings the rolling strategy back to the pool's declared order, so the next call goes to the primary provider again. Without it, a `StickyRollingStrategy` that rolled to a fallback after a single transient error stayed on that fallback until the process restarted. `StartSorting` now uses it instead of resetting the strategy itself.

### Changed

- **Operators** the default of `--substreams-tier2-segment-execution-timeout` moves from `1h` to `4h`. It is now only an absolute backstop, the new `--substreams-tier2-segment-stall-timeout` being the guard that actually kills wedged segments. If you pinned the flag to a value of your own, raise it or drop the override, otherwise expensive-but-healthy segments keep being killed.

- Bumped `substreams` to [v1.21.0](https://github.com/streamingfast/substreams/releases/tag/v1.21.0):

  - Server: tier2 now aborts a segment when it **stops making progress** rather than when it exceeds a fixed time budget. The new stall timeout (10 minutes by default, `--substreams-tier2-segment-stall-timeout`) resets on every block processed, and the pre-existing segment execution timeout (`--substreams-tier2-segment-execution-timeout`) is kept only as an absolute backstop, its default raised from 60 minutes to 4 hours.

    The old fixed budget was fatal to expensive-but-healthy workloads: a segment making thousands of RPC calls per block could take slightly longer than 60 minutes while still advancing block by block, get killed, and — since a killed segment is never cached — have its retry redo the same work and hit the same wall. Such a request could never complete, no matter how many times it reconnected. A stalled segment is still killed promptly, and since a single block is already bounded by the block execution timeout (`--substreams-block-execution-timeout`, 3 minutes by default), the stall timeout cannot be tripped by one legitimately slow block.

    The `request active for a long time` log gained a `since_last_progress` field, and a segment killed for stalling now reports `request stalled, no block progress` instead of `request active for too long`.

  - Server: new `substreams_undo_signal_distance_blocks` prometheus histogram, observing how many blocks each `BlockUndoSignal` sent to clients reverts, labeled by `source` (`reorg` when a fork is seen while streaming, `cursor_resolution` when the cursor of an incoming request points to a block that was reorged out). Its `_count` gives the total number of undo signals sent; subtracting the `le="5"` bucket from it gives the number of large ones. Undo signals reverting more than 5 blocks are also logged as a warning with `trace_id`, `head`, `revert_up_to`, `distance` and, on the `cursor_resolution` path, the client `cursor`.

  - Server: tier2 no longer logs `tls: first record does not look like a TLS handshake` errors from plain-HTTP health probes / load balancers hitting a TLS port (same suppress list as the existing EOF and connection-reset handshake noise).

  - Server: a tier1 shutdown happening while a request was still in its parallel backprocessing phase was reported to the client as `Internal` instead of `Unavailable`, so clients did not see the reconnect signal during a tier1 rollout.

- `rpc.WithClients`/`rpc.WithClientsContext` now attribute each failed attempt to its provider, the returned error reads `provider "<name>": <error>`, and each roll to another provider is logged at `warn` level with the `from_provider`/`to_provider` names. A given `from -> to` roll only warns again once 30s elapsed since the last time it did (the ones in between are logged at `debug` level), so a permanently down provider does not warn on every single call.

- `tools substreams logs connection`: explicitly passing `--logs=false` now skips the final `Logs` section altogether, printing neither the prompt nor the Cloud Logging console link. Omitting the flag keeps the previous behavior (prompt, falling back on the console link).

### Fixed

- `rpc.Sort` no longer reads the clients pool without holding the lock, which was a data race against `Add`/`AddNamed` and against the client selection in `WithClientsContext`, since `StartSorting` runs `Sort` on its own goroutine. It now sorts a snapshot taken under the lock, keeping the per-client network fetches lock-free.

- A client added to the pool while `rpc.Sort` was running is no longer dropped. `Sort` snapshots the pool, fetches a sort value per client over the network, then writes the pool back, and that write used to overwrite anything appended in between. It now redoes the round instead of publishing, so the new client is sorted along with the rest.

## v1.16.1

### Added

- New `finalized_block_number{app}` metric, reported by `reader-node`, `reader-node-stdin`, `reader-node-firehose`, `relayer`, `firehose`, `merger` and `block-indexer`. Paired with the existing `head_block_number{app}`, it lets dashboards show how far behind finality each app's head is, as `head_block_number - finalized_block_number`. The `merger` and `block-indexer` only ever process final blocks, so they report the same value for both.

- `tools substreams logs connection`: new final `Logs` section which asks whether the full logs should be printed (defaults to yes when you just hit enter) and, when declined or when running non-interactively, shows a Cloud Logging console link scoped to the request's own time window instead.

- `tools substreams logs connection`: new `--logs` flag printing every log line of the request (tier1 and tier2 alike) in the final `Logs` section instead of the console link, rendered as `<time> <severity> <logger> <message>` with the remaining fields listed one `<key>: <value>` per line under it, numeric duration fields (`duration`, `parallel_duration`, `time_to_first_data`, ...) being shown as human durations. Use `--logs-limit` (default `500`, `0` disables) to control how many of the most recent lines are printed.

### Changed

- Bump `substreams` to [v1.20.3](https://github.com/streamingfast/substreams/releases/tag/v1.20.3).

- Bumped `bstream` for `forkable.WithFinalizedBlockNumMetric`, the option backing the new `finalized_block_number` metric on the `relayer`.

### Security

- Bumped `google.golang.org/grpc` to `v1.82.1`, fixing GHSA-hrxh-6v49-42gf (HIGH, CVSS 8.8), an uncaught exception reachable from the network.

## v1.16.0

### Added

- New `--common-merged-blocks-bundle-size` flag (default `100`, must be a positive multiple of 100) setting the number of blocks per merged-blocks file for the whole process: the merger writes bundles of that size and every reader (firehose, substreams-tier1, cursor resolution, single-block fetch) expects files of that size. substreams-tier1 forwards the value to tier2 on each subrequest, so a shared tier2 fleet can serve chains with different bundle sizes (upgrade tier2s first). The value must match the files actually present in the store; see `devel/bundle1000/` for a working 1000-blocks example.
- New `fire{chain} tools resize-merged-blocks <source> <destination> <start> <stop> --source-bundle-size=100 --target-bundle-size=1000` command rewriting a merged-blocks store to a different bundle size (both up- and down-sizing, sizes must divide evenly, start/stop must be aligned on target boundaries).
- Tools: new shared `--merged-blocks-bundle-size` flag (default `100`) on `tools` subcommands reading or writing merged blocks (`check merged-blocks`, `print merged-blocks`, `compare-blocks`, `merge-blocks`, `unmerge-blocks`, `upgrade-merged-blocks`, `download-from-firehose`, `fix-bloated-merged-blocks`, ...). The value is validated up-front (must be a positive multiple of 100) so an invalid size fails with a clear error instead of a later panic.
- New `--substreams-tier1-store-size-limit` flag (default `0` = 1GiB) overriding the store size limit (in bytes) for tier2 stores; the value is forwarded from tier1 to tier2 on each subrequest. Replaces the removed `SUBSTREAMS_STORE_SIZE_LIMIT` environment variable, which no longer needs to be set on tier2.
- New `--substreams-stores-backend` flag (default `memory`) on `substreams-tier1` and `substreams-tier2`, selecting the FullKV store KV backend: `memory` (Go heap) or `mmap` (bbolt-backed, opt-in — reclaimable under memory pressure, see the `substreams` bump below).
- New `--substreams-stores-scratch-space` flag (default `{sf-data-dir}/substreams/stores-scratch`) for the local scratch directory used by store KV backends (e.g. the `mmap` bbolt files). Must be on a local NVMe SSD when using `mmap`.
- New `--merger-prune-one-block-files-after` flag (default `100`) controlling how many blocks below the last merged block one-block files are kept before deletion. Raise it so a relayer/firehose that briefly falls behind can still find the one-block files needed to bridge the gap and relink, instead of getting stuck in a reconnect loop or dying with `cannot link block after reconnection, restart required`. Clamped to a minimum of `100` (the bundle size) to preserve the safety margin against deleting not-yet-merged files.

### Changed

- Bumped [substreams@v1.20.1](https://github.com/streamingfast/substreams/releases/tag/v1.20.1)
  - Server: new opt-in mmap (bbolt-backed) store backend, selectable via `--substreams-stores-backend=mmap` (default `memory`). It keeps FullKV store data in a memory-mapped file so cold pages are reclaimable by the kernel under memory pressure, instead of pinning everything on the non-reclaimable Go heap — this addresses production OOMs with large or highly concurrent stores. The in-memory backend remains the default and is byte-for-byte unchanged; mmap is validated to produce identical output. **The scratch-space directory holding the bbolt files (`--substreams-stores-scratch-space`) must live on a local NVMe SSD**: the store is continuously read and written through the mmap and the kernel pages it straight to that file, so a slow or network-backed disk (EBS-class, NFS) turns store operations into an I/O bottleneck and negates the benefit.
  - Server: store quicksave/quickload now run up to 8 stores concurrently instead of one at a time, and quicksave streams the store lazily and unsorted (one KV entry at a time, no key sort/allocation) instead of buffering the whole serialized store, lowering peak memory and save time for large stores. On-disk format is unchanged; quickload is order-independent.
  - Server: tier1 store loading at request start now loads up to 8 stores concurrently (size probe + download/decode) instead of one at a time.
  - Server: cached deterministic errors now expire. Each error file carries a write timestamp in its name (`errors.<block>.<hash>.<unix>`), and on read tier1 discards any error older than `SUBSTREAMS_DETERMINISTIC_ERROR_MAX_AGE` (Go duration, default `1h`) and retries execution. Legacy error files without a timestamp are deleted on read.
  - Server: canceled store loads now abort promptly instead of reading the entire (multi-GB) store file into the heap before returning, so a canceled/disconnected request stops hydrating immediately and no longer transiently pins gigabytes of store data.
  - Server: a Blocks request whose start block resolves (from a cursor) to exactly the exclusive stop block now completes cleanly instead of returning `InvalidArgument: start block and stop block are the same`. This previously surfaced as a fatal, non-retryable error to clients that reconnected with a cursor sitting on the last block of the range after a transient disconnect. Raw (cursor-less) requests with `start == stop` still return the InvalidArgument as before.
  - Server: client disconnects (`context canceled`) on a tier1 Blocks stream are no longer logged as `WARNING`/`ERROR` with `code Unknown`; they now resolve to `codes.Canceled` and are logged at Debug/Info as intended.
  - Server: store quicksave now also triggers on client disconnect (context canceled), not only on graceful server shutdown, so a reconnecting client can resume without reprocessing. Only applies to production-mode requests.
  - Server: quicksave block count now counts settled blocks (normal or last-partial) instead of skipping partials entirely, so quicksave arms correctly on flash-block chains; the minimum sent-block threshold before a quicksave triggers was raised from 25 to 50.
  - Server: the `substreams request stats` log now includes the last block sent to the client (`last_sent_block_num`, `last_sent_block_id`, `last_sent_block_time`), and quicksave/quickload logs now report their duration (`save_duration` / `load_duration`).
  - Server: `tier1` calls to hosted foundational stores now forward the `x-organization-id` identity header (alongside the existing trusted headers), so a store's internal trust-based listener can authorize the request without an end-user JWT. This fixes `Unauthenticated: required authorization token not found` errors when reading from hosted foundational stores resolved via the control-plane registry.
  - Server: foundational store calls that fail with authentication errors, organization id mismatch, or prolonged unreachability now bubble up to the user as a non-deterministic (uncached) error instead of retrying until the global deadline. Transient unavailability is still retried (~30s) to absorb blips and rolling restarts.
  - Server: on a linear resume from a cursor, tier1 now emits the client session (trace id) and starts keepalives *before* streaming the quicksave files into the stores, instead of after. Decoding a large store from a cold/remote quicksave can take a long time and previously ran with zero output, risking a gateway/client idle-timeout before the trace id ever arrived; the store files are opened (existence-checked) first, then streamed once the session is sent.
- Bumped `dstore` to include the S3 request-checksum fix: sends the required checksum on S3 uploads when the bucket/endpoint mandates it, avoiding upload failures against providers that require request checksums.
- The firehose and substreams-tier1 hubs now keep `max(500|200, 2 x merged-blocks bundle size)` final blocks in memory so the joining source can hand off from a merged-blocks file boundary.
- A merged-blocks reader now fails fast with a clear error when a file contains blocks beyond the configured bundle size (e.g. reading a 1000-blocks store with the default of 100).

### Fixed

- `tools print merged-blocks <store>` no longer truncates its output when a merged-blocks file is missing (open range with no explicit stop, or a bounded range extending past the last available file): with the bumped `bstream` dependency it now prints every available block first, instead of discarding in-flight files on an async shutdown. On an open range it caps the stream at the last available merged-blocks file (found with an `O(log n)` existence probe rather than a full store listing) so it stops cleanly instead of erroring on the expected-missing next file. Also fixes an off-by-one that stopped one block early on a closed range, and a potential unsigned underflow when a closed range ends at block 0.
- Merger: fixed a lock leak, a dead consecutive-errors circuit breaker, a skip-loop boundary off-by-one, a streaming bundle-reader goroutine leak, and pruners that ignored shutdown.
- Relayer: the head metrics (`head_block_number`, `head_block_time_drift`, `head_block_relative_time`) are now seeded from the hub's head once it is ready (bootstrapped from one-block files), instead of only after a live block flows through. On chains that produce blocks on demand, these metrics no longer stay absent after a restart until the next block is mined.

## v1.15.0

### Changed

- Bumped to [substreams@v1.19.0](https://github.com/streamingfast/substreams/releases/tag/v1.19.0)
  - Server: `tier1` forkable hub now logs under the `tier1` logger instead of the generic `bstream` package logger, so `processing block` (and related hub) log lines are correctly attributed to the component (requires bstream `hub.WithLogger`).
  - Server: per-block execution timeouts (`--substreams-block-execution-timeout`) are no longer silently swallowed when a WASM host-function panic (e.g. wasmtime) coincides with the deadline. Previously, `recoverExecutionPanic` would return `nil` instead of `CodeDeadlineExceeded`, causing the offending block to be skipped and the stream to complete successfully.
  - added more metrics to identify time spent squashing
  - Server: the tier1 job scheduler no longer slows down on very large reprocessings (100_000s of segments). Both `NextJob` and `AllStoresCompleted` used to rescan the whole completed-segment prefix on every scheduling event, making job selection O(segments²) over a run; they now advance a forward-only cursor and are O(1) amortized.
  - Server: `UpdateStats` (progress reporting) now builds each stage's ranges in a single sort-free pass instead of one map+sort per stage every second.
  - Server: removed per-message overhead in the scheduler event loop — the debug-state env var is read once at startup instead of on every message, and the per-message debug log no longer builds its fields when debug logging is disabled.
  - Server: the cached-output streaming buffer now appends and checks for flushing under a single lock per block.

## v1.14.6

### Added

- `tools wkp descriptors [output-file]`: new command that exports all well-known blockchain protobuf descriptors as a self-contained, serialized `google.protobuf.FileDescriptorSet` (binary wire format). The set includes every transitive import (google/protobuf/* well-known types included) so consumers can build a descriptor registry with no external resolution. Output is deterministic (stable topological + alphabetical ordering) enabling "is it up to date?" CI checks via a regenerate-and-diff workflow. Use `-` as `output-file` to write to stdout; the default output name is `well-known-descriptors.binpb`.
- `proto/generator`: switched from the BSR Reflection v1beta1 API to the BSR HTTP descriptor endpoint (`/descriptor/<ref>?source_info=true`). Regenerated WKP files will now embed `source_code_info` (proto field/message comments), enabling documentation renderers and tooling that reads comment annotations. Authentication via `BUFBUILD_AUTH_TOKEN` is now optional for public modules (a warning is emitted when the token is absent).

### Fixed

- Removed vulnerable `github.com/docker/docker` dependency (GHSA-x744-4wpc-v9h2, GHSA-x86f-5xw2-fm2r, GHSA-rg2x-37c3-w2rh). Upgraded `testcontainers-go` to v0.42.0 (which uses `github.com/moby/moby/api` instead) and updated the single import in `relayer/relayer_e2e_test.go` from `github.com/docker/docker/api/types/container` to `github.com/moby/moby/api/types/container`.

### Changed

- `index-builder`: block payload unmarshalling errors now include the block number, block ID and payload type (previously a bare `proto: cannot parse invalid wire-format data` with no way to locate the offending block/bundle).
- Bumped `dstore`: S3 store now suppresses the SDK's checksum validation warnings (sets `DisableLogOutputChecksumValidationSkipped` to `true`) and updates the AWS S3 SDK to a newer version.
- Substreams: the tier1 job scheduler no longer slows down on very large reprocessings (100_000s of segments). `NextJob` and `AllStoresCompleted` used to rescan the whole completed-segment prefix on every scheduling event (O(segments²) over a run) and now advance a forward-only cursor (O(1) amortized). Progress reporting (`UpdateStats`) builds each stage's ranges in a single sort-free pass, the scheduler event loop drops per-message overhead (debug-state env var read once at startup, debug log fields built only when debug logging is enabled), and the cached-output streaming buffer appends and checks for flushing under a single lock per block.
- Substreams: `SUBSTREAMS_STORE_SIZE_LIMIT` is now passed from tier1 to tier2; when set on tier1 it overrides the tier2 env var value.
- `tools substreams logs connection`: the Request section now always shows the `Cursor:` field, displaying `None` when no cursor was provided.

### Added

- Firehose: new `--firehose-discard-partial-blocks` flag (default `false`). When enabled, partial (flash) blocks coming from the live source are dropped before reaching the forkable hub, so the hub head only ever holds real blocks. Useful on chains with flash/partial blocks where the firehose app flaps (`cannot link block after reconnection, restart required`): partial blocks are never written to the one-block store, so after a live-source reconnection the hub cannot re-link a partial head and shuts down. This is a mitigation/experiment knob — it disables flash-block serving on that firehose instance.
- Reader: two prometheus gauges to watch how close blocks read out of the node are to the `reader-node-line-buffer-size` hard limit: `reader_node_max_read_block_size_bytes` (high-water mark of the largest line/block read) and `reader_node_line_buffer_size_bytes` (the configured limit).
- `tools substreams logs reexec`: new `--production-mode` flag to override the execution mode of the re-exec'ed request; when not provided, keeps the original request's mode, `--production-mode` forces production mode, `--production-mode=false` forces development mode.

### Fixed

- Firehose: fix `sf.firehose.v2.Fetch/Block` hanging until merged bundle is created when requesting the first streamable block on a freshly started chain. The single-block handler used a strict `>` comparison against the hub's lowest retained block, so a request for exactly that block (the first streamable block at startup) skipped the live hub and fell through to the merged-blocks store, where it waited indefinitely for a merged bundle that had not been flushed yet. The comparison is now `>=`, so the lowest retained block is served from the hub.
- Substreams: fix some edge cases with partial blocks that would prevent proper detection of invalid partials that need to be undone, or causing spurious UNDO events.
- Substreams: fix `Sinker.requestActiveStartBlock` not being set when the handler implements `SinkerSessionInitHandler`, which previously caused `ProgressMessageLastContiguousBlock` to be incorrect for production-mode mapper stages.
- Substreams: detect reorgs in executed partial blocks even when the transaction hashes are identical. Previously a recomputed block whose only difference was its state (same, equally-ordered transactions) was not detected as replaced, so no reorg was triggered; more block fields are now validated to catch this.
- Logging: `processing block` (and other bstream forkable hub/forkable lines) are now logged under the owning component's logger (`relayer`, `firehose`, `merger`, ...) instead of all appearing under the generic `bstream` logger, making it possible to tell which component emitted each line. Requires bstream `hub.WithLogger`.
- `rpc`: `WithClientsContext` no longer holds the clients lock for the duration of the fetch callback, only while selecting a client. Holding it across the call serialized all concurrent callers, which made the block poller's parallel prefetching (`blockFetchBatchSize > 1`) run sequentially. The poller's in-flight fetch flag is now an `atomic.Bool`, fixing a data race on the parallel-fetch path.

### Security

- Docker: the runtime image now runs `apt-get upgrade` and clears the apt cache during build, pulling in available OS security patches (fixes `CVE-2026-45447` in `openssl`).

## v1.14.5

### Changed

- `reader-node-firehose`: if the persisted cursor in the state file points to a block older than `--reader-node-start-block-num`, the cursor is now discarded (with a warning log) and the syncer restarts from the configured start block. Previously the stale cursor was always honored.
- Bumped `golang.org/x/net` to `v0.55.0` and `golang.org/x/crypto` to `v0.52.0` (along with `x/sys`, `x/term` and `x/text`) to pick up the latest security fixes.

### Fixed

- Substreams: fix tier1 not forwarding the subrequest secret key to tier2 in the live backfiller, which could cause backfill jobs to fail authentication against tier2 when the tier2 secret key was configured.
- Substreams: per-block execution timeouts are now surfaced as a `DeadlineExceeded` gRPC error instead of being silently swallowed (and the affected block dropped). Previously a deadline-exceeded panic during block execution could be caught by the generic context-cancelled handler, so the timeout was hidden and the block silently skipped.
- Substreams: stop the `progressBlockRate` janitor goroutine when closing the sink stats, fixing a goroutine leak.
- `payment-gateway`: fix a nil-pointer panic in session `Release` when `sessionInfo` is `nil`.

### Added

- `tools relayer write-one-blocks` New command to write one-block-files directly from the relayer. Can write partial blocks too, for comparing.
- `tools check one-blocks`: New command that walks one-block files in streaming mode and reports issues inline as they are detected: available block ranges (printed as `✅ Available blocks in range [#X to #Y]`), missing block ranges (printed as `❌ Missing blocks in range [#X to #Y]`), forks (multiple distinct IDs at the same height), and parent-chain continuity breaks. Available and missing ranges are printed interleaved so the full picture of which blocks exist and which are absent is immediately visible. Uses the finalized block number (`LibNum`) embedded in each file to prune internal state so it does not grow infinitely. Progress is reported with an automatic ladder (every 10K below 100K processed, every 100K below 500K, every 500K below 1M, then every 1M); pass `--progress-each N` to override with a fixed cadence. The final summary ends with a colour-coded `Status : ok` (green) or `Status : broken` (red) line.
- Substreams: added more metrics to identify time spent squashing

## v1.14.4

### Removed from docker image

- The 'grpc_health_probe' binary is no longer included in the docker image. You can use the HTTP '/healthz' endpoints instead or use your own GRPC poller.

### Added

- `merger` and `relayer` now expose an HTTP `/healthz` endpoint on a dedicated port via the new `--merger-http-healthz-addr` (default `:10013`) and `--relayer-http-healthz-addr` (default `:10018`) flags. Set the flag to an empty string to disable. The endpoint returns HTTP 200 when the service is ready and 503 otherwise (including during the `common-system-shutdown-signal-delay` graceful-shutdown window).
- Config file now supports a `global:` section for setting persistent (global) flags such as `shift-ports`, `log-format`, `log-to-file`, etc. These flags can also still be set under the command-specific section (e.g. `start.flags`), but `global:` is more intuitive for flags that apply regardless of command.
- `tools compare-blocks`: A single block number (e.g. `2713`) is now accepted as the range argument, automatically expanding to the 100-block bundle that contains that block (e.g. `2700:2799`).
- Substreams **Index optimisation**: Optimized `ClockDistributor` to skip blocks earlier and faster when using block filter.
- Substreams: add substreams_tier2_max_concurrent_requests and substreams_tier1_active_requests_hard_limit metrics to prometheus

### Fixed

- `tools substreams store-size`: Fix `N/A` shown for all stores. For local state stores, the compressed file size is now displayed as a fallback when no uncompressed metadata is available. For GCS state stores, the compressed size is now returned even when files lack `datasize` metadata, and both `.kv` and `.kv.zst` file extensions are now accepted. The "Live (uncompressed)" column is renamed to "Live Size" and now shows "Not found" when no state exists for a module. The computed module hash is shown in the table for easy manual GCS path verification. A warning is shown when no state data is found for any module, hinting at a possible wrong `--state-store` URL.
- Substreams: Fix server-side bug that would cause Blocks request to fail after a few retries with 'load full store (...) load store stream: opening file for streaming: not found' when depending on a store that is being merged slowly

### Changed

- `tools compare-blocks`: `--diff` flag is now a string flag instead of a boolean. `--diff` or `--diff=inline` prints inline diffs using `diffx`; `--diff=editor` opens `$DIFF_EDITOR` (falls back to `diff -u`); `--diff=<cmd>` uses that command as the diff editor (e.g. `--diff=vimdiff`). The separate `--diff-editor` flag has been removed.
- `tools compare-blocks`: diff output uses `diffx` for richer unified diffs with line numbers, ANSI color, and inline character-level highlighting.

## v1.14.3

### Fixed

* `MergedBlocksWriter.WriteBundle`: close the read end of the internal pipe and wait for the producer goroutine to finish before returning. Previously, if the underlying `Store.WriteObject` returned without consuming the reader (e.g. an S3 destination skipping an existing object when `overwrite` is disabled), the producer goroutine would block forever on its next pipe write, leaking the goroutine and pinning the bundle's blocks in memory. This affected `tools download-from-firehose` against S3 destinations that already contained any of the requested bundles.

## v1.14.2

* Library `dsession` bumped to latest version which brings:
  * Bug fixes around session handling at scale.

* Library `dstore` bumped to latest version `v0.2.3` which brings these changes:
  * GCS store: disable gRPC DirectPath when both a project is set and `client_protocol=grpc` is used, preventing connection issues in that configuration.
  * S3 store: share a single HTTP transport across all store clones for proper HTTP/2 multiplexing, replacing the previous per-clone transport that broke connection sharing.

## v1.14.1

### Updated

* Library `dstore` bumped to latest version which brings these changes:
  * GCS store: opt-in gRPC transport via `client_protocol=grpc` query parameter (e.g. `gs://bucket/path?client_protocol=grpc`). Defaults to the existing HTTP client; the gRPC client is selected only when this parameter is explicitly set.
  * S3 store: `storageClass` query parameter is deprecated in favour of `storage_class`; a warning is logged when the old form is used. `storage_class` query parameter as the canonical snake_case name for `storageClass`.
  * S3 store: each store clone now gets its own transport for failure isolation; adds `ResponseHeaderTimeout` to prevent hung requests and configures HTTP/2 health checks via `x/net/http2`; default connection pool sizes are reduced.

### Fixed

* Substreams: Fix server-side bug that would prevent forkableHub from correctly updating metrics when receiving partial or out-of-order blocks

## v1.14.0

### Added

* Added `--shift-ports` global flag that shifts all Firehose service port numbers by a given offset, useful for running multiple instances on the same machine without port conflicts. Both server listen addresses and internal client connection addresses are shifted so wiring stays consistent. Infrastructure ports (Prometheus metrics, pprof, log-level-switcher) are also shifted. Example: `fire{chain} start --shift-ports 100` shifts all ports by +100.
* Added '--merger-max-merging-threads' (defaults: 4) so that the merger can merge blocks in parallel (still using way less RAM than previous one-block-preloading method)
* S3 store: configurable HTTP connection pool via `DSTORE_S3_MAX_IDLE_CONNS`, `DSTORE_S3_MAX_IDLE_CONNS_PER_HOST`, `DSTORE_S3_IDLE_CONN_TIMEOUT` env vars

### Changed

* Removed parallel preloading of one-block-files to reduce RAM usage when merging big blocks.

> [!NOTE]
> With this change, HEAD block timestamp is now updated maximum every 5 seconds instead of at every block, by reading the first 500 bytes of the last one-block-file.

### Fixed

* S3 store: fixed goroutine leak caused by connection pool exhaustion on single-host S3 stores (e.g. MinIO); HTTP body is now explicitly drained and closed, and the transport is configured with `MaxIdleConnsPerHost=100` by default

## v1.13.3

* Fix substreams support for requests with 'application/grpc-web*' content-type (old connectweb library)

## v1.13.2

* Add 'mindreader stats' info log every 30secs with performance metrics

## v1.13.1

### Added

* Add `firecore tools networks list` command to display registered networks from The Graph Networks Registry with their Firehose and Substreams endpoints. Supports `--name-only` flag for listing only network IDs and `--only` flag for filtering networks using a regular expression.
* Add `substreams-tier2-authenticator` flag to specify the authenticator to use for tier2 requests. Can be 'trust://' (default, same as previous behavior) or 'secret://<key>'
* Add `substreams-tier1-subrequests-secret-key` flag to specify the secret key to use for tier1 subrequests authentication when using 'secret://' authenticator on tier2
* Add `reader-node-grpc-secret-key` flag to specify the secret key to use for reader node gRPC authentication
* Add `?secret=...` parsing to `relayer-source`s
* Add Prometheus metrics for reader test mode: track blocks compared, success/failure counts, and success/failure percentages for easy monitoring at interval stats.

### Changed

* Refactor reader test mode Prometheus metrics to fix incorrect success/failure percentage calculation caused by unaccounted blocks. Renamed `blocks_matched_total` -> `blocks_compared_matched_total` and `blocks_mismatched_total` -> `blocks_compared_mismatched_total`. The `blocks_compared_total` metric now counts only blocks that were fully compared (matched + mismatched). Added three new metrics: `blocks_seen_total` (all attempted blocks), `blocks_reorg_total` (skipped due to re-org/ID mismatch), and `blocks_fetch_failure_total` (failed to fetch from production). Invariants: `blocks_seen == blocks_reorg + blocks_fetch_failure + blocks_compared` and `blocks_compared == blocks_compared_matched + blocks_compared_mismatched`.

### Fixed

* Fix substreams/firehose endpoints detection of supported compression: do not fail on 'algo;q=x.y' syntax
* Fix substreams tier2 jobs behind load balancer: will now retry forever on 'Unavailable: no healthy upstream' errors
* Fix relayer failing to get back to live if reader blocks are unlinkable after a long period, and merger has removed one-blocks: it will now shutdown in that case, so it can be restarted.

## v1.13.0

### Substreams Performance (RPC V4, New blocks from last partial)

* **RPC V4 protocol with `BlockScopedDatas` batching**: Multiple `BlockScopedData` messages are now batched into a single `BlockScopedDatas` response, reducing gRPC round-trips and message framing overhead during backfill. Clients automatically fall back V4 → V3 → V2 when connecting to older servers, so no flag changes are required.
* **S2 compression is now the default**: Replaces gzip as the default compression algorithm, providing ~3-5x faster compression/decompression with comparable ratios. The client automatically negotiates compression with the server.
* **VTProtobuf fast serialization**: Both client and server now use [vtprotobuf](https://github.com/planetscale/vtprotobuf) for protobuf marshaling/unmarshaling, providing ~2-3x faster serialization with reduced memory allocations.
* **Server-side message buffering**: Configurable via `--substreams-tier1-output-buffer-size` flag (default: 100 blocks) or `MESSAGE_BUFFER_MAX_DATA_SIZE` environment variable (default: 10MB).
* **Improved Connect/gRPC protocol selection**: Server now efficiently routes requests to the appropriate handler based on content-type, improving performance by ~15% for pure gRPC clients.
* **New blocks from last partial**: "Last partial blocks" are now accepted interchangeably with `new` blocks, allowing faster full blocks for requests that do not ask for partial blocks.

### Added

* Add `firecore tools substreams logs connections <user_id>` command to query Cloud Logging and show Substreams connections for an organization. Correlates incoming requests with stats by trace ID and presents a summary table showing active, closed, and error connections with details like network, source IP, module, duration, and blocks processed.

### Removed

* Remove alpha partial blocks support in firehose service (only exposed via substreams)

## v1.12.8

* Substreams: Improved 'partial blocks': support new pbbstream's "LastPartial" field, fix 'undo' scenarios for stores
* Substreams: Improved 'partial blocks': support new pbbstream's "LastPartial" field, fix 'undo' scenarios for stores
* Reduce RAM usage with partial blocks (relayer, substreams, firehose)
* Prevent panic if transactionTrace.receipt is nil in LogFilter (even if it is not a normal scenario)

## v1.12.7

* Bump Golang to build to 1.25

### Substreams

* Fix issue where a retry on dstore while writing a fullKV would corrupt the file, making it unreadable. Fix prevents this and also now deletes affected files when they are detected
* Fix bug where so request could get stuck forever (until the clients drops or server restarts).
* Fix issue where transient HTTP/2 stream errors (e.g., `INTERNAL_ERROR`) from `dstore` were being treated as fatal errors instead of being retried. These transient network errors are now detected and retried with exponential backoff.

## v1.12.6

* Added bucketed prometheus metrics `head_block_relative_time_sum` to help investigate latency and pipeline performance:
  - "app=firehose_output" and "app=substreams_output" that shows latency between outputing live blocks and their blocktime.
  - "app=relayer" for latency at relayer's input
  - "app=reader-node" for latency at reader's input
* Bump base64 library to use a much faster one in reader
* dstore: bumped google storage lib to v1.59.1 to fix a bug in their multi-range downloader, in case it affects us

### Substreams

* Fixed underflow in 'FailedPrecondition desc = request needs to process a total of x blocks' error when running from 'substreams run' with a start-block in the future.

## v1.12.5

### Substreams fixes

* Fix issue where "live backfiller" would not create segments after reconnecting with a cursor starting from a previous quicksave, causing delays in future reconnection
* Prevent "panic" when log messages are too large: instead, they will be truncated with a 'some logs were truncated' message.
* Raise max individual log message size from 128k to 512k
* Raise max log message size for a full block from 128k to 5MiB
* Reduce log level from Warn to Debug when we fail to get or set the store size (for backends that don't support it)

### Substreams Partial blocks (experimental)

* Removed PartialsData message and brought back this data inside the good old BlockScopedData
* added the following fields to BlockScopedData:
  - bool `is_partial` to indicate if this block is a partial block. The following two fields are only present when `is_partial==true`
  - optional bool `is_last_partial` to indicate if this is the last partial of a given block (with correct block hash)
  - optional uint32 `partial_index` to indicate the index of this partial block within the full block
* renamed `partial_blocks_only` flag to `partial_blocks` on substreams Blocks request
* removed `include_partial_blocks` flag from substreams Blocks request

### AWS Store

* Migrated from AWS SDK for Go v1 to v2 (`github.com/aws/aws-sdk-go` → `github.com/aws/aws-sdk-go-v2`)

### Azure store

* Added support for "workload identity credentials" in Azure. Order of preference is:
  - If `AZURE_STORAGE_KEY` is set, use shared key credential (previous behavior)
	- Otherwise, use DefaultAzureCredential which supports:
	  - Managed Identity (for Azure resources)
	  - Service Principal (via AZURE_CLIENT_ID, AZURE_CLIENT_SECRET, AZURE_TENANT_ID)
	  - Azure CLI credentials
	  - Visual Studio Code credentials

## v1.12.4

### Partial blocks

Added experimental support for partial blocks (e.g. Flashblocks on Base)

See https://docs.substreams.dev/reference-material/chains-and-endpoints/flashblocks for details about how they work in Substreams.

* `SUBSTREAMS_BIGGEST_PARTIAL_BLOCK_INDEX` environment variable now specifies the index to use when bundling the "last partial block" from the full block. (default: 10, for Base)
* Added flag `--include-partial-blocks` on `tools firehose-client`

### Bugfixes

* Substreams: fixed issue where "live backfiller" would not create segments after reconnecting with a cursor starting from a previous quicksave, causing delays in future reconnection

## v1.12.3

* Fixed substreams regression in v1.12.2 where some jobs would not get scheduled correctly, resulting in failure with the mssage  `get size of store "...": opening file: not found`.

## v1.12.2

> [!IMPORTANT]
> This version contains a bug in scheduling of substreams stages which can cause some requests to fail with the message `get size of store "...": opening file: not found`.
> Operators are advised to upgrade to v1.12.3 as soon as possible.

### CLI

* `firecore tools firehose-client` and `firecore tools firehose-single-block-client` now accepts Network Registry ID or aliases directly.
* fixed `firecore tools download-from-firehose` cursor handling to avoid erroneous "this endpoint is serving blocks out of order" issues.

### Substreams

* Fix egress bytes calculation when running in noop or dev mode with specified output debug modules.
* Add support to foundation store v2 protocol.
* Reduced memory usage when loading large stores
* Added opt-in memory limits related to loading FullKV stores, gated by environment variables:
  - "SUBSTREAMS_STORE_SIZE_LIMIT_PER_REQUEST" (default allows 5GiB: `5368709120`): limit size of all loaded stores for a single request, in bytes. Set to a numeric value in bytes.
  - "SUBSTREAMS_ENFORCE_STORE_SIZE_LIMIT_PER_REQUEST" (default false): if set to `true`, enforce the limit above instead of just logging a warning
  - "SUBSTREAMS_TOTAL_STORE_SIZE_LIMIT_PERCENT" (default: 75): limit the size in-memory of all loaded stores concurrently on the instance, in percentage of usable memory (cgroup or system total -- regardless of free or available)
  - "SUBSTREAMS_ENFORCE_TOTAL_STORE_SIZE_LIMIT" (default: false): if set to `true`, enforce the limit above instead of just logging a warning
* Fixed an edge case where substreams with modules depending on stores that start on the future would fail and incorrectly report an error about "tier2 version being incompatible"

### Firehose

* Added `firehose_session_denied_counter` with `reason` label, increment each time a session is refused with the reason why it was refused.

## v1.12.1

### Substreams

* Fix a panic (nil pointer) when skipping blocks via indexes on stores on tier2
* Add store size to substreams starts
* Add store and foundational-store list to incoming request stats

## v1.12.0

### Substreams v1.17.0

#### New sf.substreams.rpc.v3.Stream/Blocks endpoint added

* This new endpoint removes the need for complex "mangling" of the package on the client side.
* Instead of expecting `sf.substreams.v1.Modules` (with the client having to apply parameters, network, etc.), the `sf.substreams.rpc.v3.Request` now expects:
  - a `sf.substreams.v1.Package`.
  - a `map<string, string>` of `params`
  - the `network` string
  which will all be applied to the package server-side.
* It returns the same object as the v2 endpoint, i.e. a stream of `sf.substreams.rpc.v2.Response`
* It is added on top of the existing 'v2' endpoint, both being active at the same time.
* To enable it, operators will simply need to ensure that their routing allows the `/sf.substreams.rpc.v3.Stream/*` path.
* Cached spkg on the server will now contain protobuf definitions, simplifying debugging of user requests.
* Emitted metrics for requests can now be `sf.substreams.rpc.v3/Blocks` instead of always `sf.substreams.rpc.v2/Blocks`, make sure that your metering endpoint can support it.

Note: recent substreams clients will support both endpoints, first trying the v3 and automatically falling back to v2 if they hit a "404 Not Found" or "Not Implemented" error.

#### Bug fixes

* Fixed a bug with BlockFilter: a skipped module would send BlockScopedData (in dev or near HEAD, to follow progress) with an empty module name, breaking some sinks. Module name was present if requesting a module dependent on that skipped module. Now the module name is always included.

## v1.11.3

* Improved panic message when reader node encounter a block whose finality is bigger than the block itself to include `lib_num`, `block_num`, `distance`, and `max_distance` for easier debugging.

* Updated `firehose-networks` dependency to `v0.2.2` (latest).

* Fixed `common-one-block-store-url` flag not expanding environment variables in all apps.

## v1.11.2

### Substreams v1.16.6

* Updated Wasmtime runtime from v30.0.0 to v36.0.0, bringing performance improvements, inlining support, Component Model async implementation, and enhanced security features.
* Added WASM bindgen shims support for Wasmtime runtime to handle WASM modules with WASM bindgen imports (when Substreams Module binary is defined as type `wasm/rust-v1+wasm-bindgen-shims`).
* Added support for foundational-store (in wasmtime and wazero).
* Added foundational-store grpc client to substreams engine.
* Fixed module caching to properly handle modules with different runtime extensions.

## v1.11.1

### Substreams

#### Metering

* 'paymentgateway' metering plugin renamed to `tgm`,  now supports the `indexer-api-key` parameter.

#### Session (stream + workers management)

* Concurrent streams and workers limits are now handled under the new session plugin, available under `common-session-plugin` argument.

* The following flags were removed, now handled by that session plugin
  - `substreams-tier1-global-worker-pool-address`
  - `substreams-tier1-global-request-pool-address`
  - `substreams-tier1-global-worker-pool-keep-alive-delay`
  - `substreams-tier1-global-request-pool-keep-alive-delay`
  - `substreams-tier1-default-max-request-per-use`
  - `substreams-tier1-default-minimal-request-life-time-second`

* To use thegraph.market as a session plugin, use:
  `--common-session-plugin=tgm://session.thegraph.market:443?indexer-api-key={your-api-key}` (requires specific indexer API key)
  see https://github.com/streamingfast/tgm-gateway/tree/develop/session for details on the various flags

* To use simple local session management, use:
  `--common-session-plugin=local://?max_sessions=30&max_sessions_per_user=3&max_workers_per_user=10&max_workers_per_session=10`
  see https://github.com/streamingfast/dsession/tree/main/local for details on those flags

* Note: The 'max_sessions' parameter from the `common-session-plugin` is now also used to limit the number of firehose streams.

* If you were using a custom GRPC implementation for `--substreams-tier1-global-worker-pool-address` and `--substreams-tier1-global-request-pool-address` (ex: localhost:9010),
  simply use this value for the session plugin: `--common-session-plugin=tgm://localhost:9010?plaintext=true`, it is compatible.

#### Stability

* Fix a slow memory leak around metering plugin on tier2
* Add a maximum execution time for a full tier2 segment. By default, this is 60 minutes. It will fail with `rpc error: code = DeadlineExceeded desc = request active for too long`.
  It can be configured from the --substreams-tier2-segment-execution-timeout flag
* Fix `subscription channel at max capacity` error: when the LIVE channel is full (ex: slow module execution or slow client reader), the request will be continued from merged files instead of failing, and gracefully recover if performance is restored.
* Improve log message for 'request active for a long time', adding stats.

## v1.11.0

### CLI

* Improved how `firecore tools --output=protojson` and `firecore tools --output=json` renders `pbbstream.Block` type now printing the underlying chain's specific block.

### Substreams (v1.16.4)

#### Tier1 thread / memory leak

* Fix thread leak on filereader.

* If `--advertise-chain-name` is sey, `substreams-tier1` app will now infer default `--substreams-tier1-block-type` value by using chain's name and extracting chain's block type Protobuf package id, which will fix some cases where `substreams-tier1` waits for 100 blocks before starting up.

#### Authentication changes

People using their own authentication layer will need to consider these changes before upgrading!

* Renamed config headers that come from authentication layer:
  - `x-sf-user-id` renamed to `x-user-id` (from dauth module)
  - `x-sf-api-key-id` renamed to `x-api-key-id` (from dauth module)
  - `x-sf-meta` renamed to `x-meta` (from dauth module)
  - `x-sf-substreams-parallel-jobs` renamed to `x-substreams-parallel-workers`
* Allow decreasing `x-substreams-parallel-workers` through an HTTP headers (auth layer determines higher bound)
* Detect value for the 'stage layer parallel executor max count' based on the `x-plan-tier` header (removed `x-sf-substreams-stage-layer-parallel-executor-max-count` handling)

#### New authentication plugin

* Added `tgm://auth.thegraph.market?indexer-api-key=<API_KEY>&reissue-jwt-max-age-secs=600` plugin that allows an indexer to use The Graph Market as the authentication source.
  An API key with special "indexer" feature is needed to allow repeated calls to the API without rate limiting (for Key-based authentication and reissuance of "untrusted long-lived JWTs").

## v1.10.2

### Substreams (v1.6.2)

* **Added** mechanism to immediately cancel pending requests that are doing an 'external call' (ex: eth_call) on a given block when it gets forked out (UNDO because of a reorg).
* **Fixed** handling of invalid module kind: prevent heavy logging from recovered panic
* Error considered deterministic which will cache the error forever are now suffixed with `<original message> (deterministic error)`.

## v1.10.1

### Substreams

* [OPERATORS] Tier2 servers must be upgraded BEFORE tier1 servers
* tier2 servers will now stream outputs for the 'first segment', to speed up time to first block
* Progress notifications will only be sent every 500ms for the first minute, then reduce rate up to every 5 seconds (can be overridden per request)
* Return 'processed blocks' counter to client at the end of the request
* Added `dev_output_modules` to protobuf request (if present, in dev mode, only send the output of the modules listed)
* Added `progress_messages_interval_ms` to protobuf request (if present, overrides the rate of progress messages to that many milliseconds)

## v1.10.0

[Broken release, do not use]

## v1.9.12

* This release is a hotfix for a thread leak leading to a slow memory leak.

## v1.9.11

### Substreams improvements v1.15.8

Rework the execout File read/write to improve memory efficiency:

* This reduces the RAM usage necessary to read and stream data to the user on tier1,
  as well as to read the existing execouts on tier2 jobs (in multi-stage scenario)

* The cached execouts need to be rewritten to take advantage of this, since their data is currently not ordered:
  the system will automatically load and rewrite existing execout when they are used.

* Code changes include:
  - new FileReader / FileWriter that "read as you go" or "write as you go"
  - No more 'KV' map attached to the File
  - Split the IndexWriter away from its dependencies on execoutMappers.
  - Clock distributor now also reads "as you go", using a small "one-block-cache"

* Removed `SUBSTREAMS_OUTPUT_SIZE_LIMIT_PER_SEGMENT` env var (since this is not a RAM issue anymore)
* Add `uncompressed_egress_bytes` field to `substreams request stats` log message

### Various

* (dstore) Add storageClass query parameter for s3:// urls on stores (@fschoell)
* Update the firehose-beacon proto to include the new Electra spec in the 'well-known' protobuf definitions (@fschoell)
* Use The Graph's Network Registry to recognize chains by genesis blocks and fill the 'advertise' server on substreams/firehose

## v1.9.10

### Substreams improvements v1.15.7

* Tier2 jobs now write mapper outputs "as they progress", preventing memory usage spikes when saving them to disk.
* Tier2 jobs now limit writing and loading mapper output files to a maximum size of 8GiB by default.
* Tier2 jobs now release existingExecOuts memory as blocks progress
* Speed up DeleteByPrefix operations on all tiers (5x perf improvement on some heavy substreams)
* Added`SUBSTREAMS_OUTPUT_SIZE_LIMIT_PER_SEGMENT` (int) environment variable to control this new limit.
* Added `SUBSTREAMS_STORE_SIZE_LIMIT` (uint64) env var to allow overwriting the default 1GiB value
* Added `SUBSTREAMS_PRINT_STACK` (bool) env var to enable printing full stack traces when caught panic occurs
* Added `SUBSTREAMS_DEBUG_API_ADDR` (string) environment variable to expose a "debug API" HTTP interface that allows blocking connections, running GC, listing or canceling active requests.
* Prevent a deterministic failure on a module definition (mode, valueType, updatePolicy) from persisting when the issue is fixed in the substreams.yaml https://github.com/streamingfast/substreams/issues/621
* Metering events on tier2 now bundled at the end of the job (prevents sending metering events for failing jobs)
* Added metering for: "processed_blocks" (block * number of stages where execution happened) and "egress_bytes"

## v1.9.9

### Substreams performance improvements v1.15.4

* (RAM+CPU) dedupe execution of modules with same hash but different name when computing dependency graph. (#619)
* (RAM) prevent memory usage burst on tier2 when writing mapper by streaming protobuf items to writer
* Tier1 requests will no longer error out with "service currently overloaded" because tier2 servers are ramping up

### New 'firehose' reader

* Add `reader-node-firehose` which creates one-blocks by consuming blocks from an already existing Firehose endpoint. This can be used to set up an indexer stack without having to run an instrumented blockchain node, or getting redundancy from another firehose provider.

### Other

* Bumped grpc-go lib to 1.72.0
* Now building `amd64` and `arm64` Docker images on push & release.

## v1.9.8

* Flag `--reader-node-arguments` now accepts to expand `{first-streamable-block}` with the value defined by config flag `--common-first-streamable-block`.

* Flag `--reader-node-arguments` will now expand environment variables if present within the string.

## v1.9.7

* Bump substreams to v1.15.2
* fix the 'quicksave' feature on substreams (incorrect block hash on quicksave)

## v1.9.6

### Substreams (v1.15.1)

* Save deterministic failures in WASM in the module cache (under a file named `errors.0123456789.zst` at the failed block number), so further requests depending on this module at the same block can return the error immediately without re-executing the module.

## v1.9.5

### Substreams

* Fix a panic when a module times out on tier2 while being executed from cached outputs
* Add environment variables to control retry behavior, "SUBSTREAMS_WORKER_MAX_RETRIES" (default 10) and "SUBSTREAMS_WORKER_MAX_TIMEOUT_RETRIES" (default 2), changing from previous defaults (720 and 3)
  The worker_max_timeout_retries is the number of retries specifically applied to block execution timing out (ex: because of external calls)
* The mechanism to slow down processing segments "ahead of blocks being sent to user" has been disabled on "noop-mode" requests, since these requests are used to pre-cache data and should not be slowed down.
* The "number of segments ahead" in this mechanism has been increased from `>number of parallel workers>` to `<number of parallel workers> * 1.5`
* Tier2 now returns GRPC error codes for `DeadlineExceeded` when it times out, and `ResourceExhausted` when a request is rejected due to overload
* Tier1 now correctly reports tier2 job outcomes in the `substreams request stats`
* Added jitter in "retry" logic to prevent all workers from retrying at the same time when tier2 are overloaded

## v1.9.4

### Substreams (v1.14.5)

* Bugfix for panics on some requests

## v1.9.3

### Substreams (v1.14.4)

* Properly reject requests with a stop-block below the "resolved" StartBlock (caused by module initialBlocks or a chain's firstStreamableBlock)
* Added the `resolved-start-block` to the `substreams request stats` log

## v1.9.2

### Substreams (v1.14.3)

#### Bugfixes

* Fixed `runtime error: slice bounds out of range` error on heavy memory usage with wasmtime engin

* Added a validation on a module for the existence of 'triggering' inputs: the server will now fail with a clear error message
  when the only available inputs are stores used with mode 'get' (not 'deltas'), instead of silenlty skipping the module on every block.

#### Performance

* Added a mechanism for 'production-mode' requests where the tier1 will not schedule tier2 jobs over { max_parallel_subrequests } segments above the current block being streamed to the user.
  This will ensure that a user slowly reading blocks 1, 2, 3... will not trigger a flood of tier2 jobs for higher blocks, let's say 300_000_000, that might never get read.

#### Service lifecycle

* Improved connection draining on shutdown: Now waits for the end of the 'shutdown-delay' before draining and refusing new connections, then waits for 'quicksaves' and successful signaling of clients, up to a max of 30 sec.

#### Logging / errors

* Added information about the number of blocks that need to be processed for a given request in the `sf.substreams.rpc.v2.SessionInit` message
* Added an optional field `limit_processed_blocks` to the `sf.substreams.rpc.v2.Request`. When set to a non-zero value, the server will reject a request that would process more blocks than the given value with the `FailedPrecondition` GRPC error code.
* Improved error messages when a module execution is timing out on a block (ex: due to a slow external call) and now return a `DeadlineExceeded` Connect/GRPC error code instead of a Internal. Removed 'panic' from wording.
* In 'substreams request stats' log, add fields: `remote_jobs_completed`, `remote_blocks_processed` and `total_uncompressed_read_bytes`

## v1.9.1

### Substreams

* Fix another `cannot resolve 'old cursor' from files in passthrough mode -- not implemented` bug when receiving a request in production-mode with a cursor that is below the "linear handoff" block

## v1.9.0

### Substreams

* Rust modules will now be executed with `wasmtime` by default instead of `wazero`.
  - Prevents the whole server from stalling in certain memory-intensive operations in wazero.
  - Speed improvement: cuts the execution time in half in some circumstances.
  - Wazero is still used for modules with `wbindgen` and modules compiled with `tinygo`.
  - Set env var `SUBSTREAMS_WASM_RUNTIME=wazero` to revert to previous behavior.

* Implement "QuickSave" feature to save the state of "live running" substreams stores when shutting down, and then resume processing from that point if the cursor matches.
  - Added flag `substreams-tier1-quicksave-store` to enable quicksave when non-empty
    (requires `--common-system-shutdown-signal-delay` to be set to a long enough value to save the in-flight stores)

### Misc

* The `firecore tools print one-block` is now able to print from a file directly.

* Added `firecore tools relayer stream <endpoint> [(+<count>|<stopBlock>)]` to connect to a relayer component through gRPC and stream data out, output controlled by `tools --output` flag.

## v1.8.0

### Substreams

#### Capacity Management

* Integrated the `GlobalRequestPool` service in the `Tier1App` to manage global requests pooling.
* Integrated the `GlobalWorkerPool` service in the `Tier1App` to manage global worker pooling.

* Added flag `substreams-tier1-global-worker-pool-address`, the address of the global worker pool to use for the substreams tier1. (disabled if empty)
* Added flag `substreams-tier1-global-worker-pool-keep-alive-delay` delay between two keep alive call to the global worker pool. Default is 25s")
* Added flag `substreams-tier1-global-request-pool-keep-alive-delay` delay between two keep alive call to the global worker pool for request. Default is 25s
* Added flag `substreams-tier1-default-max-request-per-user` default max request per user, this will be use of the global worker pool is not reachable. Default is 5
* Added flag `substreams-tier1-default-minimal-request-life-time-second` default minimal request life time, this will be use of the global worker pool is not reachable. . Default is 180

* Limit parallel execution of a stage's layer: Previously, the engine was executing modules in a stage's layer all in parallel.
  We now change that behavior, development mode will from now on execute every sequentially and when in production mode will
  limit parallelism to 2 (hard-coded) for now.
  The auth plugin can control that value dynamically by providing a trusted header `X-Sf-Substreams-Stage-Layer-Parallel-Executor-Max-Count`.

#### Performance

* Add shared cache for tier1 execution near HEAD, to prevent multiple tier1 instances from reprocessing the same module on the same block when it comes in (ex: foundational modules)
* Improved fetching of state caches on tier1 requests to speed up "time to first data"

* Fixed a regression since "v1.7.3" where the SkipEmptyOutput instruction was ignored in substreams mappers

### Tools

* make 'compare-blocks' command support one-blocks stores as well as merged-blocks

## v1.7.4

- Bump `substreams` lib to `v1.12.3`
  - Improved logging of requests beginning/end
  - Improved `noop` mode (now sends less data)

## v1.7.3

- Bump `substreams` lib to `v1.12.2`
  - fix panic when using an index that allows `skip_empty_output`

## v1.7.2

- Fixed `substreams-tier2` not setting itself ready correctly on startup since `v1.7.0`.

- Added support for `--output=bytes` mode which prints the chain's specific Protobuf block as bytes, the encoding for the bytes string printed is determined by `--bytes-encoding`, uses `hex` by default.

- Added back `-o` as shortand for `--output` in `firecore tools ...` sub-commands.

## v1.7.1

- Add back `grpc.health.v1.Health` service to `firehose` and `substreams-tier1` services (regression in 1.7.0)
- Give precedence to the tracing header `X-Cloud-Trace-Context` over `Traceparent` to prevent user systems' trace IDs from leaking passed a GCP load-balancer

## v1.7.0

### Reader

- Reader Node Manager HTTP API now accepts `POST http://localhost:10011/v1/restart<?sync=true>` to restart the underlying reader node binary sub-process. This is a alias for `/v1/reload`.

### Tools

- Enhanced `firecore tools print merged-blocks` with various small quality of life improvements:
  - Now accepts a block range instead of a single start block.
  - Passing a single block as the block range will print this single block alone.
  - Block range is now optional, defaulting to run until there is no more files to read.
  - It's possible to pass a merged blocks file directly, with or without an optional range.

### Firehose

> [!IMPORTANT]
> This release will reject firehose connections from clients that don't support GZIP or ZSTD compression. Use `--firehose-enforce-compression=false` to keep previous behavior, then check the logs for `incoming Substreams Blocks request` logs with the value `compressed: false` to track users who are not using compressed HTTP connections.

> [!IMPORTANT]
> This release removes the old `sf.firehose.v1` protocol (replaced by `sf.firehose.v2` in 2022, this should not affect any reasonably recent client).

- Add support for ConnectWeb firehose requests.
- Always use gzip compression on firehose requests for clients that support it (instead of always answering with the same compression as the request).

### Substreams

- The `substreams-tier1` app now has two new configuration flags named respectively `substreams-tier1-active-requests-soft-limit` and `substreams-tier1-active-requests-hard-limit`
  helping better load balance active requests across a pool of `tier1` instances.

  The `substreams-tier1-active-requests-soft-limit` limits the number of client active requests that a tier1 accepts before starting
  to be report itself as 'unready' within the health check endpoint. A limit of 0 or less means no limit.

  This is useful to load balance active requests more easily across a pool of tier1 instance. When the instance reaches the soft
  limit, it will start to be unready from the load balancer standpoint. The load balancer in return will remove it from the list
  of available instances, and new connections will be routed to remaining clients, spreading the load.

      The `substreams-tier1-active-requests-hard-limit` limits the number of client active requests that a tier1 accepts before

  rejecting incoming gRPC requests with 'Unavailable' code and setting itself as unready. A limit of 0 or less means no limit.

  This is useful to prevent the tier1 from being overwhelmed by too many requests, most client auto-reconnects on 'Unavailable' code
  so they should end up on another tier1 instance, assuming you have proper auto-scaling of the number of instances available.

- The `substreams-tier1` app now exposes a new Prometheus metric `substreams_tier1_rejected_request_counter` that tracks rejected
  requests. The counter is labelled by the gRPC/ConnectRPC returned code (`ok` and `canceled` are not considered rejected requests).

- The `substreams-tier2` app now exposes a new Prometheus metric `substreams_tier2_rejected_request_counter` that tracks rejected
  requests. The counter is labelled by the gRPC/ConnectRPC returned code (`ok` and `canceled` are not considered rejected requests).

- Properly accept and compress responses with `gzip` for browser HTTP clients using ConnectWeb with `Accept-Encoding` header
- Allow setting subscription channel max capacity via `SOURCE_CHAN_SIZE` env var (default: 100)

## v1.6.9

### Substreams

- Fix an issue preventing proper detection of gzip compression when multiple headers are set (ex: python grpc client)
- Add support for zstd compression on server
- Fix an issue preventing some tier2 requests on last-stage from correctly generating stores. This could lead to some missing "backfilling" jobs and slower time to first block on reconnection.
- Fix a thread leak on cursor resolution resulting in bad counter for active connections

## v1.6.8

> [!NOTE]
> This release will reject substreams connections from clients that don't support GZIP compression. Use `--substreams-tier1-enforce-compression=false` to keep previous behavior, then check the logs for `incoming Substreams Blocks request` logs with the value `compressed: false` to track users who are not using compressed HTTP connections.

- Substreams: add `--substreams-tier1-enforce-compression` to reject connections from clients that do not support GZIP compression
- Substreams performance: reduced the number of `mallocs` (patching some third-party libraries)
- Substreams performance: removed heavy tracing (that wasn't exposed to the client)
- Fixed `reader-node-line-buffer-size` flag that was not being respected in `reader-node-stdin` app
- Well-known chains: change genesis block for near-mainnet from 9820214 to 9820210
- BlockPoller library: reworked logic to support more flexible balancing strategy

## v1.6.7

- `firehose-grpc-listen-addr` and `substreams-tier1-grpc-listen-addr` flags now accepts comma-separated addresses (allows listening as plaintext and snakeoil-ssl at the same time or on specific ip addresses)
- removed old `RegisterServiceExtension` implementation (not used anywhere anymore)
- rpc-poller lib: fix fetching the first block on an endpoint (was not following the cursor, failing unnecessarily on non-archive nodes)

## v1.6.6

- Bump `substreams` and `dmetering` to latest version adding the `outputModuleHash` to metering sender.

## v1.6.5

### Substreams fixes

> **Note** All caches for stores using the updatePolicy `set_sum` (added in substreams v1.7.0) and modules that depend on them will need to be deleted, since they may contain bad data.

- Fix bad data in stores using `set_sum` policy: squashing of store segments incorrectly "summed" some values that should have been "set" if the last event for a key on this segment was a "sum"
- Fix small bug making some requests in development-mode slow to start (when starting close to the module initialBlock with a store that doesn't start on a boundary)

### Others

- [Operator] Node Manager HTTP `/v1/resume` call now accepts `extra-env=<key>=<value>&extra-env=<keyN>=<valueN>` enabling to override environment variables for the next restart **only**. Use `curl -XPOST "http://localhost:10011/v1/resume?sync=true&extra-env=NODE_DEBUG=true"` (change `localhost:10011` accordingly to your setup).

  > This is **not** persistent upon restart!

- [Metering] Revert undesired Firehose metric `Endpoint` changes, the correct new value used is `sf.firehose.v2.Firehose/Blocks` (had been mistakenly set to `sf.firehose.v2.Firehose/Block` between version v1.6.1 and v1.6.4 inclusively).

## v1.6.4

### Substreams fixes

- Fixed an(other) issue where multiple stores running on the same stage with different initialBlocks will fail to proress (and hang)

## v1.6.3

### Substreams fixes

- Fix "cannot resolve 'old cursor' from files in passthrough mode" error on some requests with an old cursor
- Fix handling of 'special case' substreams module with only "params" as its input: should not skip this execution (used in graph-node for head tracking)
  -> empty files in module cache with hash `d3b1920483180cbcd2fd10abcabbee431146f4c8` should be deleted for consistency
- Fix bug where some invalid cursors may be sent (with 'LIB' being above the block being sent) and add safeguard/loggin if the bug appears again
- Fix panic in the whole tier2 process when stores go above the size limit while being read from "kvops" cached changes

### Core fixes

- fix: reader-node-stdin not shutting down after receiving an EOF

## v1.6.2

### Core

- [Operator] The flag `--advertise-block-id-encoding` now accepts shorter form: `hex`, `base64`, etc. The older longer form `BLOCK_ID_ENCODING_HEX` is still supported but we suggested using the shorter form from now on.

### Substreams v1.10.2

> **Note** Since a bug that affected substreams with "skipping blocks" was corrected in this release, any previously produced substreams cache should be considered as possibly corrupted and be eventually replaced

- Substreams: fix bad handling of modules with multiple inputs when only one of them is filtered, resulting in bad outputs in production-mode.
- Substreams: fix stalling on some substreams with stores and mappers with different start block numbers on the same stage
- Substreams: fix 'development mode' and LIVE mode executing some modules that should be skipped

## v1.6.1

- Bump substreams to v1.10.0: Version 1.10.0 adds a new `EndpointInfo/Info` endpoint, introduces a 3-minute default execution timeout per block, updates metering metrics with a deprecation warning, enhances `substreams init` commands, and improves wasm module caching and Prometheus tool flexibility. Full changelog: <https://github.com/streamingfast/substreams/releases/tag/v1.10.0>
- Metering update: more detailed metering with addition of new metrics. _DEPRECATION WARNING_: `bytes_read` and `bytes_written` metrics will be removed in the future, please use the new metrics for metering instead

## v1.6.0

- Add `sf.firehose.v2.EndpointInfo/Info` service on Firehose and `sf.substreams.rpc.v2.EndpointInfo/Info` to Substreams endpoints. This involves the following new flags:

  - `advertise-chain-name` Canonical name of the chain according to <https://thegraph.com/docs/en/developing/supported-networks/> (required, unless it is in the "well-known" list)
  - `advertise-chain-aliases` Alternate names for that chain (optional)
  - `advertise-block-features` List of features describing the blocks (optional)
  - `advertise-block-id-encoding` Encoding format of the block ID [BLOCK_ID_ENCODING_BASE58, BLOCK_ID_ENCODING_BASE64, BLOCK_ID_ENCODING_BASE64URL, BLOCK_ID_ENCODING_HEX, BLOCK_ID_ENCODING_0X_HEX] (required, unless the block type is in the "well-known" list)
  - `ignore-advertise-validation` Runtime checks of chain name/features/encoding against the genesis block will no longer cause server to wait or fail.

- Add a well-known list of chains (hard-coded in `wellknown/chains.go` to help automatically determine the 'advertise' flag values). Users are encouraged to propose Pull Requests to add more chains to the list.
- The new info endpoint adds a mandatory fetching of the first streamable block on startup, with a failure if no block can be fetched after 3 minutes and you are running `firehose` or `substreams-tier1` service.
  It validates the following on a well-known chain:

  - if the first-streamable-block Num/ID match the genesis block of a known chain, e.g. `matic`, it will refuse another value for `advertise-chain-name` than `matic` or one of its aliases (`polygon`)
  - If the first-streamable-block does not match any known chain, it will require the `advertise-chain-name` to be non-empty
  - If the first-streamable-block type is unknown (i.e. not ethereum, solana, near, cosmos, bitcoin...), it will require the user to provide `advertise-chain-name` as well as `advertise-block-id-encoding`

- Substreams: add `--common-tmp-dir` flag and activate local caching of pre-compiled WASM modules through wazero feature
- Substreams: revert module hash calculation from `v1.5.5`, when using a non-zero firstStreamableBlock. Hashes will now be the same even if the chain's first streamable block affects the initialBlock of a module.
- Substreams: add `--substreams-block-execution-timeout` flag (default 3 minutes) to prevent requests stalling
- Metering update: more detailed metering with addition of new metrics (`live_uncompressed_read_bytes`, `live_uncompressed_read_forked_bytes`, `file_uncompressed_read_bytes`, `file_uncompressed_read_forked_bytes`, `file_compressed_read_forked_bytes`, `file_compressed_read_bytes`). _DEPRECATION WARNING_: `bytes_read` and `bytes_written` metrics will be removed in the future, please use the new metrics for metering instead.

## v1.5.7

- Bump substreams to v1.9.3: fix high CPU usage on tier1 caused by a bad error handling

## v1.5.6

- Bump substreams to v1.9.2: Prevent Noop handler from sending outputs with 'Stalled' step in cursor (which breaks substreams-sink-kv)
- add `--reader-node-line-buffer-size` flag and bump default value from 100M to 200M to go over crazy block 278208000 on Solana

## v1.5.5

- added well known type for starknet and cosmos
- Fixed a bug in substreams where chains with non-zero first-streamable-block would cause some substreams to hang. Solution changes the 'cached' hashes for those substreams.

## v1.5.4

### Substreams bumped to v1.9.0

#### Important Substreams BUG FIX

- Fix a bug introduced in v1.6.0 that could result in corrupted store "state" file if all
  the "outputs" were already cached for a module in a given segment (rare occurence)
- We recommend clearing your substreams cache after this upgrade and re-processing or
  validating your data if you use stores.

#### Added

- Expose a new intrinsic to modules: `skip_empty_output`, which causes the module output to be skipped if it has zero bytes. (Watch out, a protobuf object with all its default values will have zero bytes)
- Improve schedule order (faster time to first block) for substreams with multiple stages when starting mid-chain

## v1.5.3

- fix "hub" not recovering on certain disconnections in relayer/firehose/substreams (scenarios requiring full restart)

## v1.5.2

### Substreams changes

- Added substreams back-filler to populate cache for live requests when the blocks become final
- Fixed: truncate very long details on error messages to prevent them from disappearing when behind a (misbehaving) load-balancer

## v1.5.1

- Bootstrapping from live blocks improved for chains with very slow blocks or with very fast blocks (affects relayer, firehose and substreams tier1)
- Substreams fixed slow response close to HEAD in production-mode

## v1.5.0

### Highlights

- Substreams engine is now able run Rust code that depends on `solana_program` in Solana land to decode and `alloy/ether-rs` in Ethereum land

#### How to use `solana_program` or `alloy`/`ether-rs`

Those libraries when used in a `wasm32-unknown-unknown` context creates in a bunch of [wasmbindgen](https://rustwasm.github.io/wasm-bindgen/) imports in the resulting Substreams Rust code, imports that led to runtime errors because Substreams engine didn't know about those special imports until today.

The Substreams engine is now able to "shims" those `wasmbindgen` imports enabling you to run code that depends libraries like `solana_program` and `alloy/ether-rs` which are known to pull those `wasmbindgen` imports. This is going to work as long as you do not actually call those special imports. Normal usage of those libraries don't accidentally call those methods normally. If they are called, the WASM module will fail at runtime and stall the Substreams module from going forward.

To enable this feature, you need to explicitly opt-in by appending a `+wasm-bindgen-shims` at the end of the binary's type in your Substreams manifest:

```yaml
binaries:
  default:
    type: wasm/rust-v1
    file: <some_file>
```

to become

```yaml
binaries:
  default:
    type: wasm/rust-v1+wasm-bindgen-shims
    file: <some_file>
```

### Others

- Substreams clients now enable gzip compression over the network (already supported by servers).

- Substreams binary type can now be optionally composed of runtime extensions by appending a `+<extension>,[<extesions...>]` at the end of the binary type. Extensions are `key[=value]` that are runtime specifics.

  > [!NOTE]
  > If you were a library author and parsing generic Substreams manifest(s), you will now need to handle that possibility in the binary type. If you were reading the field without any processing, you don't have to change nothing.

## v1.4.2

- Fix parsing of flag 'common-index-block-sizes' from yaml config file

### Substreams bumped to v1.6.2

- execout: preload only one file instead of two, log if undeleted caches found
- execout: add environment variable SUBSTREAMS_DISABLE_PRELOAD_EXEC_FILES to disable file preloading

## v1.4.1

### Substreams bumped to v1.6.1

- Revert sanity check to support the special case of a substreams with only 'params' as input. This allows a chain-agnostic event to be sent, along with the clock.
- Fix error handling when resolved start-block == stop-block and stop-block is defined as non-zero

## v1.4.0

### Substreams bumped to v1.6.0

> **Note** Upgrading will require changing the tier1 and tier2 versions concurrently, as the internal protocol has changed.

- _Index Modules_ and _Block Filter_ now supported. See <https://github.com/streamingfast/substreams-foundational-modules> for an example implementation
- Various scheduling and performance improvements
- env variable `SUBSTREAMS_WORKERS_RAMPUP_TIME` changed from `4s` to `0`. Set it to `4s` to keep previous behavior
- `otelcol://` tracing protocol no longer supported

## v1.3.9

### Substreams

- Allow stores to write to stores with out-of-order ordinals (they will be reordered at the end of the module execution for each block)
- Fix issue in substreams-tier2 causing some files to be written to the wrong place sometimes under load, resulting in some hanging requests

## v1.3.8

- The `tools download-from-firehose` now respects it's documentation when doing `--help`, correct invocation now is `firecore tools download-from-firehose <endpoint> <start>:<end> <output_folder>`.

- The `firecore tools download-from-firehose` has been improved to work with new Firehose `sf.firehose.v2.BlockMetadata` field, if the server sends this new field, the tool is going to work on any chain. If the server's you are reaching is not recent enough, the tool fallbacks to the previous logic. All StreamingFast endpoints should serves be compatible.

- Firehose response (both single block and stream) now include the `sf.firehose.v2.BlockMetadata` field. This new field contains the chain agnostic fields we hold about any block of any chain.

## v1.3.7

### Fixed

- Fixed possible race condition in the blockPoller
- Fix relayer waiting too long to fail when reconnecting to a single source (especially on slow chains). It will now fail right away if it receives an unlinkable block and has a single source configured.
- Fixed skipped block handling and performance issues on blockPoller

### Changed

- The `--block-type` flag got renamed to `--substreams-tier1-block-type`. Specifying it will make substreams-tier1 skip the block type discovery (from files or live stream) on startup, getting ready faster.

### Added

- Logs now print the "x-deployment-id" header on firehose connections (used to propagate subgraph deployment ids from graph-node and help debugging)

## v1.3.6

- bump substreams to v1.5.5 with fix in wazero to prevent process freezing on certain substreams
- bump go-generics to v3.4.0

## v1.3.5

### Substreams fixes

- fix a possible panic() when an request is interrupted during the file loading phase of a squashing operation.
- fix a rare possibility of stalling if only some fullkv stores caches were deleted, but further segments were still present.
- fix stats counters for store operations time

## v1.3.4

- add `DefaultBlockType` into `firehose.Chain` struct, enabling default block type setting for known chain

### substreams

- bumped to v1.5.3
- add `--block-type` flag that can be specified when creating substreams tier1. If not specified, tier1 will auto-detect block type from source.
- fix memory leak on substreams execution (by bumping wazero dependency)
- prevent substreams-tier1 stopping if blocktype auto-detection times out
- fix missing error handling when writing output data to files. This could result in tier1 request just "hanging" waiting for the file never produced by tier2.
- fix handling of dstore error in tier1 'execout walker' causing stalling issues on S3 or on unexpected storage errors
- increase number of retries on storage when writing states or execouts (5 -> 10)
- prevent slow squashing when loading each segment from full KV store (can happen when a stage contains multiple stores)

## v1.3.3

### substreams

- Fix a context leak causing tier1 responses to slow down progressively

## v1.3.2

### substreams

- fix another panic on substreams-tier2 service
- fix thread leak in metering affecting substreams
- revert a substreams scheduler optimisation that causes slow restarts when close to head
- add substreams_tier2_active_requests and substreams_tier2_request_counter prometheus metrics

## v1.3.1

- fix panic on substreams-tier2 service

## v1.3.0

### Substreams

- Substreams bumped to @v1.5.0: See <https://github.com/streamingfast/substreams/releases/tag/v1.5.0> for details.

#### Chain-agnostic tier2

- A single substreams-tier2 instance can now serve requests for multiple chains or networks. All network-specific parameters are now passed from Tier1 to Tier2 in the internal ProcessRange request.
- This allows you to better use your computing resources by pooling all the networks together.

> [!IMPORTANT]
> Since the `tier2` services will now get the network information from the `tier1` request, you must make sure that the file paths and network addresses will be the same for both tiers.
> ex: if `--common-merged-blocks-store-url=/data/merged` is set on tier1, make sure the merged blocks are also available from tier2 under the path `/data/merged`.
> The flags `--substreams-state-store-url`, `--substreams-state-store-default-tag` and `--common-merged-blocks-store-url` are now ignored on tier2. The flag `--common-first-streamable-block` should be set to 0 to accommodate every chain.

> [!TIP]
> The cached 'partial' files no longer contain the "trace ID" in their filename, preventing accumulation of "unsquashed" partial store files. The system will delete files under '{modulehash}/state' named in this format`{blocknumber}-{blocknumber}.{hexadecimal}.partial.zst` when it runs into them.

#### Performance improvements

- All module outputs are now cached. (previously, only the last module was cached, along with the "store snapshots", to allow parallel processing).
- Tier2 will now read back mapper outputs (if they exist) to prevent running them again. Additionally, it will not read back the full blocks if its inputs can be satisfied from existing cached mapper outputs.
- Tier2 will skip processing completely if it's processing the last stage and the `output_module` is a mapper that has already been processed (ex: when multiple requests are indexing the same data at the same time)
- Tier2 will skip processing completely if it's processing a stage where all the stores and outputs have been processed and cached.
- Scheduler modification: a stage now waits for the previous stage to have completed the same segment before running, to take advantage of the cached intermediate layers.
- Improved file listing performance for Google Storage backends by 25%

> [!TIP]

- Concurrent requests on the same module hashes may benefit from the other requests' work to a certain extent (up to 75%) -- The very first request does most of the work for the other ones.

> [!TIP]
> More caches will increase disk usage and there is no automatic removal of old module caches. The operator is responsible for deleting old module caches.

> [!TIP]
> The cached 'partial' files no longer contain the "trace ID" in their filename, preventing accumulation of "unsquashed" partial store files.
> The system will delete files under '{modulehash}/state' named in this format`{blocknumber}-{blocknumber}.{hexadecimal}.partial.zst` when it runs into them.

#### Metrics

- Readiness metric for Substreams tier1 app is now named `substreams_tier1` (was mistakenly called `firehose` before).
- Added back readiness metric for Substreams tiere app (named `substreams_tier2`).
- Added metric `substreams_tier1_active_worker_requests` which gives the number of active Substreams worker requests a tier1 app is currently doing against tier2 nodes.
- Added metric `substreams_tier1_worker_request_counter` which gives the total Substreams worker requests a tier1 app made against tier2 nodes.

### Flags

- Added `--merger-delete-threads` to customize the number of threads the merger will use to delete files. It's recommended to increase this when using Ceph as S3 storage provider to 25 or higher (due to performance issues with deletes the merger might otherwise not be able to delete one-block files fast enough).
- Added `--substreams-tier2-max-concurrent-requests` to limit the number of concurrent requests to the tier2 substreams service.

- If relayer is started with a single source, it will have reduced tolerance for missing blocks. This is to prevent the relayer from falling behind when the source is not producing blocks.

## v1.2.5

- Fixed `tools check merged-blocks` default range when `-r <range>` is not provided to now be `[0, +∞]` (was previously `[HEAD, +∞]`).

- Fixed `tools check merged-blocks` to be able to run without a block range provided.

- Added API Key based authentication to `tools firehose-client` and `tools firehose-single-block-client`, specify the value through environment variable `FIREHOSE_API_KEY` (you can use flag `--api-key-env-var` to change variable's name to something else than `FIREHOSE_API_KEY`).

- Fixed `tools check merged-blocks` examples using block range (range should be specified as `[<start>]?:[<end>]`).

- Added `--substreams-tier2-max-concurrent-requests` to limit the number of concurrent requests to the tier2 Substreams service.

### Library `firehose-core`

- Added API Key authentication to `client.NewFirehoseFetchClient` and `client.NewFirehoseClient`.

  > [!NOTE]
  > If you were using `github.com/streamingfast/firehose-core/firehose/client.NewFirehoseFetchClient` or `github.com/streamingfast/firehose-core/firehose/client.NewFirehoseStreamClient`, this will be a minor breaking change, refer to [upgrade notes](./UPDATE.md#v125) for details if it affects you.

## v1.2.4

### Substreams improvements

- Performance: prevent reprocessing jobs when there is only a mapper in production mode and everything is already cached
- Performance: prevent "UpdateStats" from running too often and stalling other operations when running with a high parallel jobs count
- Performance: fixed bug in scheduler ramp-up function sometimes waiting before raising the number of workers
- Added the output module's hash to the "incoming request" log

### Reader node and Beacon blocks

- The `reader-node-bootstrap-url` gained the ability to be bootstrapped from a `bash` script.

If the bootstrap URL is of the form `bash:///<path/to/script>?<parameters>`, the bash script at
`<path/to/script>` will be executed. The script is going to receive in environment variables the resolved
reader node variables in the form of `READER_NODE_<VARIABLE_NAME>`. The fully resolved node arguments
(from `reader-node-arguments`) are passed as args to the bash script. The query parameters accepted are:

- `arg=<value>` | Pass as extra argument to the script, prepended to the list of resolved node arguments
- `env=<key>%3d<value>` | Pass as extra environment variable as `<key>=<value>` with key being upper-cased (multiple(s) allowed)
- `env_<key>=<value>` | Pass as extra environment variable as `<key>=<value>` with key being upper-cased (multiple(s) allowed)
- `cwd=<path>` | Change the working directory to `<path>` before running the script
- `interpreter=<path>` | Use `<path>` as the interpreter to run the script
- `interpreter_arg=<arg>` | Pass `<interpreter_arg>` as arguments to the interpreter before the script path (multiple(s) allowed)

  > [!NOTE]
  > The `bash:///` script support is currently experimental and might change in upcoming releases, the behavior changes will be
  > clearly documented here.

- The `reader-node-bootstrap-url` gained the ability to be bootstrapped from a pre-made archive file ending with `tar.zst` or `tar.zstd`.

- The `reader-node-bootstrap-data-url` is now added automatically if `firecore.Chain#ReaderNodeBootstrapperFactory` is `non-nil`.

  If the bootstrap URL ends with `tar.zst` or `tar.zstd`, the archive is read and extracted into the
  `reader-node-data-dir` location. The archive is expected to contain the full content of the 'reader-node-data-dir'
  and is expanded as is.

- Added `Beacon` to known list of Block model.

## v1.2.3

- Fix marshalling of blocks to JSON in tools like `firehose-client` and `print merged-blocks`

## v1.2.2

### Auth and metering

- Add missing metering events for `sf.firehose.v2.Fetch/Block` responses.
- Changed default polling interval in 'continuous authentication' from 10s to 60s, added 'interval' query param to URL.

### Substreams

- Fixed bug in scheduler ramp-up function sometimes waiting before raising the number of workers
- Fixed load-balancing from tier1 to tier2 when using dns:/// (round-robin policy was not set correctly)
- Added `trace_id` in grpc authentication calls
- Bumped connect-go library to new "connectrpc.com/connect" location

## v1.2.1

### Fixed

- Fixed `tools firehose-client` which was broken because of bad flag handling

### Added

- Added `--api-key-env-var` flag to firehose-clients, which allows you to pass your API Key from an environment variable (HTTP header `x-api-key`) instead of a JWT (`Authorization: bearer`), where supported.

## v1.2.0

- Poller is now fetching blocks in an optimized way, it will fetch several blocks at once and then process them.

- Poller is now handling skipped blocks, it will fetch the next blocks until it find a none skipped block.

- Poller now has default retry value of infinite.

- Compare tool is now using dynamic protobuf unmarshaler, it will be able to compare any block type.

- Print tool is now using dynamic protobuf unmarshaler, it will be able to print any block type.

- Print tool is encoding bytes in base64 by default, it can be changed to hex or base58 by using parameter `bytes-encoding`.

- Added 'x-trace-id' header to auth requests when using --common-auth-plugin=grpc

- Fixed Substreams scheduler sometimes taking a long time to spawn more than a single worker.

- Added ACCEPT_SOLANA_LEGACY_BLOCK_FORMAT env var to allow special tweak operations

## v1.1.3

- Removed useless chainLatestFinalizeBlock from blockPoller initialization

## v1.1.2

- Added `Arweave` to known list of Block model.

## v1.1.1

- Added `FORCE_FINALITY_AFTER_BLOCKS` environment variable to override block finality information at the reader/poller level. This allows an operator to pretend that finality is still progressing, N blocks behind HEAD, in the case where a beacon chain fails to do so and is intended as a workaround for deprecated chains like Goerli.

## v1.1.0

- Updated `substreams` and `dgrpc` to latest versions to reduce logging.

- Tools printing Firehose `Block` model to JSON now have `--proto-paths` take higher precedence over well-known types and even the chain itself, the order is `--proto-paths` > `chain` > `well-known` (so `well-known` is lookup last).

- The `tools print one-block` now works correctly on blocks generated by omni-chain `firecore` binary.

- Tools printing Firehose `Block` model to JSON now have `--proto-paths` take higher precedence over well-known types and even the chain itself, the order is `--proto-paths` > `chain` > `well-known` (so `well-known` is lookup last).

- The `tools print one-block` now works correctly on blocks generated by omni-chain `firecore` binary.

- The various health endpoint now sets `Content-Type: application/json` header prior sending back their response to the client.

- The `firehose`, `substreams-tier1` and `substream-tier2` health endpoint now respects the `common-system-shutdown-signal-delay` configuration value meaning that the health endpoint will return `false` now if `SIGINT` has been received but we are still in the shutdown unready period defined by the config value. If you use some sort of load balancer, you should make sure they are configured to use the health endpoint and you should `common-system-shutdown-signal-delay` to something like `15s`.

- The `firecore.ConsoleReader` gained the ability to print stats as it ingest blocks.

- The `firecore.ConsoleReader` has been made stricter by ensuring Firehose chain exchange protocol is respected.

- Changed `reader` logger back to `reader-node` to fit with the app's name which is `reader-node`.

- Fix `-c ""` not working properly when no arguments are present when invoking `start` command.

- Fix `tools compare-blocks` that would fail on new format.

- Fix `substreams` to correctly delete `.partial` files when serving a request that is not on a boundary.

- Add Antelope types to the blockchain's known types.

## v1.0.0

This is a major release.

### Operators

> [!IMPORTANT]
> When upgrading your stack to firehose-core v1.0.0, be sure to upgrade all components simultaneously because the block encapsulation format has changed.
> Blocks that are merged using the new merger will not be readable by previous versions.

### Added

- New binary `firecore` which can run all firehose components (`reader`, `reader-stdin`, `merger`, `relayer`, `firehose`, `substreams-tier1|2`) in a chain-agnostic way. This is not mandatory (it can still be used as a library) but strongly suggested when possible.

- Current Limitations on Ethereum:

  - The firecore `firehose` app does not support transforms (filters, header-only --for graph-node compatibility--) so you will want to continue running this app from `fireeth`
  - The firecore `substreams` apps do not support eth_calls so you will want to continue running them from `fireeth`
  - The firecore `reader` does not support the block format output by the current geth firehose instrumentation, so you will want to continue running it from `fireeth`

- New BlockPoller library to facilitate the implementation of rpc-poller-based chains, taking care of managing reorgs

- Considering that firehose-core is chain-agnostic, it's not aware of the different of the different block types. To be able to use tools around block decoding/printing,
  there are two ways to provide the type definition:
  1. the 'protoregistry' package contains well-known block type definitions (ethereum, near, solana, bitcoin...), you won't need to provide anything in those cases.
  2. for other types, you can provide additional protobuf files using `--proto-path` flag

### Changed

- Merged blocks storage format has been changed. Current blocks will continue to be decoded, but new merged blocks will not be readable by previous software versions.
- The code from the following repositories have been merged into this repo. They will soon be archived.
  - github.com/streamingfast/node-manager
  - github.com/streamingfast/merger
  - github.com/streamingfast/relayer
  - github.com/streamingfast/firehose
  - github.com/streamingfast/index-builder

## v0.2.4

- Fixed SF_TRACING feature (regression broke the ability to specify a tracing endpoint)
- Firehose connections rate-limiting will now force a delay of between 1 and 4 seconds (random value) before refusing a connection when under heavy load
- Fixed substreams GRPC/Connect error codes not propagating correctly

## v0.2.3

### Fixed

- fixed typo in `check-merged-blocks` preventing its proper display of missing ranges

## v0.2.2

### Added

- Firehose logs now include auth information (userID, keyID, realIP) along with blocks + egress bytes sent.

### Fixed

- Filesource validation of block order in merged-blocks now works correctly when using indexes in firehose `Blocks` queries

### Removed

- Flag `substreams-rpc-endpoints` removed, this was present by mistake and unused actually.
- Flag `substreams-rpc-cache-store-url` removed, this was present by mistake and unused actually.
- Flag `substreams-rpc-cache-chunk-size` removed, this was present by mistake and unused actually.

## v0.2.1

### Operators

> [!IMPORTANT]
> We have had reports of older versions of this software creating corrupted merged-blocks-files (with duplicate or extra out-of-bound blocks)
> This release adds additional validation of merged-blocks to prevent serving duplicate blocks from the firehose or substreams service.
> This may cause service outage if you have produced those blocks or downloaded them from another party who was affected by this bug.

- Find the affected files by running the following command (can be run multiple times in parallel, over smaller ranges)

```
tools check merged-blocks-batch <merged-blocks-store> <start> <stop>
```

- If you see any affected range, produce fixed merged-blocks files with the following command, on each range:

```
tools fix-bloated-merged-blocks <merged-blocks-store> <output-store> <start>:<stop>
```

- Copy the merged-blocks files created in output-store over to the your merged-blocks-store, replacing the corrupted files.

### Removed

- Removed the `--dedupe-blocks` flag on `tools download-from-firehose` as it can create confusion and more issues.

### Fixed

- Bumped `bstream`: the `filesource` will now refuse to read blocks from a merged-files if they are not ordered or if there are any duplicate.
- The command `tools download-from-firehose` will now fail if it is being served blocks "out of order", to prevent any corrupted merged-blocks from being created.
- The command `tools print merged-blocks` did not print the whole merged-blocks file, the arguments were confusing: now it will parse <start_block> as a uint64.
- The command `tools unmerge-blocks` did not cover the whole given range, now fixed

### Added

- Added the command `tools fix-bloated-merged-blocks` to try to fix merged-blocks that contain duplicates and blocks outside of their range.
- Command `tools print one-block and merged-blocks` now supports a new `--output-format` `jsonl` format.
  Bytes data can now printed as hex or base58 string instead of base64 string.

### Changed

- Changed `tools check merged-blocks-batch` argument syntax: the output-to-store is now optional.

## v0.2.0

### Fixed

- Fixed a few false positives on `tools check merged-blocks-batch`
- Fixed `tools print merged-blocks` to print correctly a single block if specified.

### Removed

- **Breaking** The `reader-node-log-to-zap` flag has been removed. This was a source of confusion for operators reporting Firehose on <Chain> bugs because the node's logs where merged within normal Firehose on <Chain> logs and it was not super obvious.

  Now, logs from the node will be printed to `stdout` unformatted exactly like presented by the chain. Filtering of such logs must now be delegated to the node's implementation and how it deals depends on the node's binary. Refer to it to determine how you can tweak the logging verbosity emitted by the node.

### Added

- Added support `-o jsonl` in `tools print merged-blocks` and `tools print one-block`.
- Added support for block range in `tools print merged-blocks`.

  > [!NOTE]
  > For now the range is restricted to a single "merged-blocks" file!

- Added retry loop for merger when walking one block files. Some use-cases where the bundle reader was sending files too fast and the merger was not waiting to accumulate enough files to start bundling merged files
- Added `--dedupe-blocks` flag on `tools download-from-firehose` to ensure no duplicate blocks end up in download merged-blocks (should not be needed in normal operations)

## v0.1.12

- Added `tools check merged-blocks-batch` to simplify checking blocks continuity in batched mode, writing results to a store
- Bumped substreams to `v1.1.20` with a fix for some minor bug fixes related to start block processing

## v0.1.11

- Bumped `substreams` to `v1.1.18` with a regression fix for when a Substreams has a start block in the reversible segment.

## v0.1.10

### Added

The `--common-auth-plugin` got back the ability to use `secret://<expected_secret>?[user_id=<user_id>]&[api_key_id=<api_key_id>]` in which case request are authenticated based on the `Authorization: Bearer <actual_secret>` and continue only if `<actual_secret> == <expected_secret>`.

### Changed

- Bumped `substreams` to `v1.1.17` with provider new metrics `substreams_active_requests` and `substreams_counter`

## v0.1.9

### Changed

- Bumped `susbtreams` to `v1.1.14` to fix bugs with start blocks, where Substreams would fail if the start block was before the first block of the chain, or if the start block was a block that was not yet produced by the chain.
- Improved error message when referenced config file is not found, removed hard-coded mention of `fireacme`.

## v0.1.8

### Fixed

- More tolerant retry/timeouts on filesource (prevent "Context Deadline Exceeded")

## v0.1.7

### Operators

> [!IMPORTANT]
> The Substreams service exposed from this version will send progress messages that cannot be decoded by Substreams clients prior to v1.1.12.
> Streaming of the actual data will not be affected. Clients will need to be upgraded to properly decode the new progress messages.

### Changed

- Bumped substreams to `v1.1.12` to support the new progress message format. Progression now relates to **stages** instead of modules. You can get stage information using the `substreams info` command starting at version `v1.1.12`.
- Bumped supervisor buffer size to 100Mb
- Substreams bumped: better "Progress" messages

### Added

- Added new templating option to `reader-node-arguments`, specifically `{start-block-num}` (maps to configuration value `reader-node-start-block-num`) and `{stop-block-num}` (maps to value of configuration value `reader-node-stop-block-num`)

### Changed

- The `reader-node` is now able to read Firehose node protocol line up to 100 MiB in raw size (previously the limit was 50 MiB).

### Removed

- Removed `--substreams-tier1-request-stats` and `--substreams-tier1-request-stats` (Substreams request-stats are now always sent to clients)

## v0.1.6

### Fixed

- Fixed bug where `null` dmetering plugin was not able to be registered.

## v0.1.5

### Fixed

- Fixed dmetering bug where events where dropped, when channel got saturated

### Changed

- `fire{chain} tools check forks` now sorts forks by block number from ascending order (so that line you see is the current highest fork).
- `fire{chain} tools check forks --after-block` can now be used to show only forks after a certain block number.
- bump `firehose`, `dmetering` and `bstream` dependencies in order to get latest fixes to meter live blocks.

## v0.1.4

This release bumps Substreams to [v1.1.10](https://github.com/streamingfast/substreams/releases/tag/v1.1.10).

### Fixed

- Fixed jobs that would hang when flags `--substreams-state-bundle-size` and `--substreams-tier1-subrequests-size` had different values. The latter flag has been completely **removed**, subrequests will be bound to the state bundle size.

### Added

- Added support for _continuous authentication_ via the grpc auth plugin (allowing cutoff triggered by the auth system).

## v0.1.3

This release bumps Substreams to [v1.1.9](https://github.com/streamingfast/substreams/releases/tag/v1.1.9).

### Highlights

#### Substreams Scheduler Improvements for Parallel Processing

The `substreams` scheduler has been improved to reduce the number of required jobs for parallel processing. This affects `backprocessing` (preparing the states of modules up to a "start-block") and `forward processing` (preparing the states and the outputs to speed up streaming in production-mode).

Jobs on `tier2` workers are now divided in "stages", each stage generating the partial states for all the modules that have the same dependencies. A `substreams` that has a single store won't be affected, but one that has 3 top-level stores, which used to run 3 jobs for every segment now only runs a single job per segment to get all the states ready.

#### Substreams State Store Selection

The `substreams` server now accepts `X-Sf-Substreams-Cache-Tag` header to select which Substreams state store URL should be used by the request. When performing a Substreams request, the servers will optionally pick the state store based on the header. This enable consumers to stay on the same cache version when the operators needs to bump the data version (reasons for this could be a bug in Substreams software that caused some cached data to be corrupted on invalid).

To benefit from this, operators that have a version currently in their state store URL should move the version part from `--substreams-state-store-url` to the new flag `--substreams-state-store-default-tag`. For example if today you have in your config:

```yaml
start:
  ...
  flags:
    substreams-state-store-url: /<some>/<path>/v3
```

You should convert to:

```yaml
start:
  ...
  flags:
    substreams-state-store-url: /<some>/<path>
    substreams-state-store-default-tag: v3
```

### Operators Upgrade

The app `substreams-tier1` and `substreams-tier2` should be upgraded concurrently. Some calls will fail while versions are misaligned.

### Backend Changes

- Authentication plugin `trust` can now specify an exclusive list of `allowed` headers (all lowercase), ex: `trust://?allowed=x-sf-user-id,x-sf-api-key-id,x-real-ip,x-sf-substreams-cache-tag`

- The `tier2` app no longer uses the `common-auth-plugin`, `trust` will always be used, so that `tier1` can pass down its headers (ex: `X-Sf-Substreams-Cache-Tag`).

## v0.1.2

#### Operator Changes

- Added `fire{chain} tools check forks <forked-blocks-store-url> [--min-depth=<depth>]` that reads forked blocks you have and prints resolved longest forks you have seen. The command works for any chain, here a sample output:

  ```log
  ...

  Fork Depth 3
  #45236230 [ea33194e0a9bb1d8 <= 164aa1b9c8a02af0 (on chain)]
  #45236231 [f7d2dc3fbdd0699c <= ea33194e0a9bb1d8]
      #45236232 [ed588cca9b1db391 <= f7d2dc3fbdd0699c]

  Fork Depth 2
  #45236023 [b6b1c68c30b61166 <= 60083a796a079409 (on chain)]
  #45236024 [6d64aec1aece4a43 <= b6b1c68c30b61166]

  ...
  ```

- The `fire{chain} tools` commands and sub-commands have better rendering `--help` by hidden not needed global flags with long description.

## v0.1.1

#### Operator Changes

- Added missing `--substreams-tier2-request-stats` request debugging flag.

- Added missing Firehose rate limiting options flags, `--firehose-rate-limit-bucket-size` and `--firehose-rate-limit-bucket-fill-rate` to manage concurrent connection attempts to Firehose, check `fire{chain} start --help` for details.

## v0.1.0

#### Backend Changes

- Fixed Substreams accepted block which was not working properly.
