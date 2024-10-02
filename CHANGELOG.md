# Change log

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). See [MAINTAINERS.md](./MAINTAINERS.md)
for instructions to keep up to date.

Operators, you should copy/paste content of this content straight to your project. It is written and meant to be copied over to your project.

If you were at `firehose-core` version `1.0.0` and are bumping to `1.1.0`, you should copy the content between those 2 version to your own repository, replacing placeholder value `fire{chain}` with your chain's own binary.

## Unreleased

* [Operator] Node Manager HTTP `/v1/resume` call now accepts `extra-env=<key>=<value>&extra-env=<keyN>=<valueN>` enabling to override environment variables for the next restart **only**. Use `curl -XPOST "http://localhost:10011/v1/resume?sync=true&extra-env=NODE_DEBUG=true"` (change `localhost:10011` accordingly to your setup).

  > This is **not** persistent upon restart!

## v1.6.4

### Substreams fixes

* Fixed an(other) issue where multiple stores running on the same stage with different initialBlocks will fail to proress (and hang)

## v1.6.3

### Substreams fixes

* Fix "cannot resolve 'old cursor' from files in passthrough mode" error on some requests with an old cursor
* Fix handling of 'special case' substreams module with only "params" as its input: should not skip this execution (used in graph-node for head tracking)
  -> empty files in module cache with hash `d3b1920483180cbcd2fd10abcabbee431146f4c8` should be deleted for consistency
* Fix bug where some invalid cursors may be sent (with 'LIB' being above the block being sent) and add safeguard/loggin if the bug appears again
* Fix panic in the whole tier2 process when stores go above the size limit while being read from "kvops" cached changes

### Core fixes

* fix: reader-node-stdin not shutting down after receiving an EOF

## v1.6.2

### Core

* [Operator] The flag `--advertise-block-id-encoding` now accepts shorter form: `hex`, `base64`, etc. The older longer form `BLOCK_ID_ENCODING_HEX` is still supported but we suggested using the shorter form from now on.

### Substreams v1.10.2

> **Note** Since a bug that affected substreams with "skipping blocks" was corrected in this release, any previously produced substreams cache should be considered as possibly corrupted and be eventually replaced

* Substreams: fix bad handling of modules with multiple inputs when only one of them is filtered, resulting in bad outputs in production-mode.
* Substreams: fix stalling on some substreams with stores and mappers with different start block numbers on the same stage
* Substreams: fix 'development mode' and LIVE mode executing some modules that should be skipped

## v1.6.1

* Bump substreams to v1.10.0: Version 1.10.0 adds a new `EndpointInfo/Info` endpoint, introduces a 3-minute default execution timeout per block, updates metering metrics with a deprecation warning, enhances `substreams init` commands, and improves wasm module caching and Prometheus tool flexibility. Full changelog: https://github.com/streamingfast/substreams/releases/tag/v1.10.0
* Metering update: more detailed metering with addition of new metrics. *DEPRECATION WARNING*: `bytes_read` and `bytes_written` metrics will be removed in the future, please use the new metrics for metering instead

## v1.6.0

* Add `sf.firehose.v2.EndpointInfo/Info` service on Firehose and `sf.substreams.rpc.v2.EndpointInfo/Info` to Substreams endpoints. This involves the following new flags:
  - `advertise-chain-name` Canonical name of the chain according to https://thegraph.com/docs/en/developing/supported-networks/ (required, unless it is in the "well-known" list)
  - `advertise-chain-aliases` Alternate names for that chain (optional)
  - `advertise-block-features` List of features describing the blocks (optional)
  - `advertise-block-id-encoding` Encoding format of the block ID [BLOCK_ID_ENCODING_BASE58, BLOCK_ID_ENCODING_BASE64, BLOCK_ID_ENCODING_BASE64URL, BLOCK_ID_ENCODING_HEX, BLOCK_ID_ENCODING_0X_HEX] (required, unless the block type is in the "well-known" list)
  - `ignore-advertise-validation` Runtime checks of chain name/features/encoding against the genesis block will no longer cause server to wait or fail.

* Add a well-known list of chains (hard-coded in `wellknown/chains.go` to help automatically determine the 'advertise' flag values). Users are encouraged to propose Pull Requests to add more chains to the list.
* The new info endpoint adds a mandatory fetching of the first streamable block on startup, with a failure if no block can be fetched after 3 minutes and you are running `firehose` or `substreams-tier1` service.
  It validates the following on a well-known chain:
    - if the first-streamable-block Num/ID match the genesis block of a known chain, e.g. `matic`, it will refuse another value for `advertise-chain-name` than `matic` or one of its aliases (`polygon`)
    - If the first-streamable-block does not match any known chain, it will require the `advertise-chain-name` to be non-empty
    - If the first-streamable-block type is unknown (i.e. not ethereum, solana, near, cosmos, bitcoin...), it will require the user to provide `advertise-chain-name` as well as `advertise-block-id-encoding`

* Substreams: add `--common-tmp-dir` flag and activate local caching of pre-compiled WASM modules through wazero feature
* Substreams: revert module hash calculation from `v1.5.5`, when using a non-zero firstStreamableBlock. Hashes will now be the same even if the chain's first streamable block affects the initialBlock of a module.
* Substreams: add `--substreams-block-execution-timeout` flag (default 3 minutes) to prevent requests stalling
* Metering update: more detailed metering with addition of new metrics (`live_uncompressed_read_bytes`, `live_uncompressed_read_forked_bytes`, `file_uncompressed_read_bytes`, `file_uncompressed_read_forked_bytes`, `file_compressed_read_forked_bytes`, `file_compressed_read_bytes`).  *DEPRECATION WARNING*: `bytes_read` and `bytes_written` metrics will be removed in the future, please use the new metrics for metering instead.

## v1.5.7

* Bump substreams to v1.9.3: fix high CPU usage on tier1 caused by a bad error handling

## v1.5.6

* Bump substreams to v1.9.2: Prevent Noop handler from sending outputs with 'Stalled' step in cursor (which breaks substreams-sink-kv)
* add `--reader-node-line-buffer-size` flag and bump default value from 100M to 200M to go over crazy block 278208000 on Solana

## v1.5.5

* added well known type for starknet and cosmos
* Fixed a bug in substreams where chains with non-zero first-streamable-block would cause some substreams to hang. Solution changes the 'cached' hashes for those substreams.

## v1.5.4

### Substreams bumped to v1.9.0

#### Important Substreams BUG FIX

* Fix a bug introduced in v1.6.0 that could result in corrupted store "state" file if all
  the "outputs" were already cached for a module in a given segment (rare occurence)
* We recommend clearing your substreams cache after this upgrade and re-processing or
  validating your data if you use stores.

#### Added

* Expose a new intrinsic to modules: `skip_empty_output`, which causes the module output to be skipped if it has zero bytes. (Watch out, a protobuf object with all its default values will have zero bytes)
* Improve schedule order (faster time to first block) for substreams with multiple stages when starting mid-chain

## v1.5.3

* fix "hub" not recovering on certain disconnections in relayer/firehose/substreams (scenarios requiring full restart)

## v1.5.2

### Substreams changes

* Added substreams back-filler to populate cache for live requests when the blocks become final
* Fixed: truncate very long details on error messages to prevent them from disappearing when behind a (misbehaving) load-balancer

## v1.5.1

* Bootstrapping from live blocks improved for chains with very slow blocks or with very fast blocks (affects relayer, firehose and substreams tier1)
* Substreams fixed slow response close to HEAD in production-mode

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

* Substreams clients now enable gzip compression over the network (already supported by servers).

* Substreams binary type can now be optionally composed of runtime extensions by appending a `+<extension>,[<extesions...>]` at the end of the binary type. Extensions are `key[=value]` that are runtime specifics.

  > [!NOTE]
  > If you were a library author and parsing generic Substreams manifest(s), you will now need to handle that possibility in the binary type. If you were reading the field without any processing, you don't have to change nothing.

## v1.4.2

* Fix parsing of flag 'common-index-block-sizes' from yaml config file

### Substreams bumped to v1.6.2

* execout: preload only one file instead of two, log if undeleted caches found
* execout: add environment variable SUBSTREAMS_DISABLE_PRELOAD_EXEC_FILES to disable file preloading

## v1.4.1

### Substreams bumped to v1.6.1

* Revert sanity check to support the special case of a substreams with only 'params' as input. This allows a chain-agnostic event to be sent, along with the clock.
* Fix error handling when resolved start-block == stop-block and stop-block is defined as non-zero

## v1.4.0

### Substreams bumped to v1.6.0

> **Note** Upgrading will require changing the tier1 and tier2 versions concurrently, as the internal protocol has changed.

* *Index Modules* and *Block Filter* now supported. See https://github.com/streamingfast/substreams-foundational-modules for an example implementation
* Various scheduling and performance improvements
* env variable `SUBSTREAMS_WORKERS_RAMPUP_TIME` changed from `4s` to `0`. Set it to `4s` to keep previous behavior
* `otelcol://` tracing protocol no longer supported

## v1.3.9

### Substreams

* Allow stores to write to stores with out-of-order ordinals (they will be reordered at the end of the module execution for each block)
* Fix issue in substreams-tier2 causing some files to be written to the wrong place sometimes under load, resulting in some hanging requests

## v1.3.8

* The `tools download-from-firehose` now respects it's documentation when doing `--help`, correct invocation now is `firecore tools download-from-firehose <endpoint> <start>:<end> <output_folder>`.

* The `firecore tools download-from-firehose` has been improved to work with new Firehose `sf.firehose.v2.BlockMetadata` field, if the server sends this new field, the tool is going to work on any chain. If the server's you are reaching is not recent enough, the tool fallbacks to the previous logic. All StreamingFast endpoints should serves be compatible.

* Firehose response (both single block and stream) now include the `sf.firehose.v2.BlockMetadata` field. This new field contains the chain agnostic fields we hold about any block of any chain.

## v1.3.7

### Fixed

* Fixed possible race condition in the blockPoller
* Fix relayer waiting too long to fail when reconnecting to a single source (especially on slow chains). It will now fail right away if it receives an unlinkable block and has a single source configured.
* Fixed skipped block handling and performance issues on blockPoller

### Changed

* The `--block-type` flag got renamed to `--substreams-tier1-block-type`. Specifying it will make substreams-tier1 skip the block type discovery (from files or live stream) on startup, getting ready faster.

### Added

* Logs now print the "x-deployment-id" header on firehose connections (used to propagate subgraph deployment ids from graph-node and help debugging)

## v1.3.6

* bump substreams to v1.5.5 with fix in wazero to prevent process freezing on certain substreams
* bump go-generics to v3.4.0

## v1.3.5

### Substreams fixes

* fix a possible panic() when an request is interrupted during the file loading phase of a squashing operation.
* fix a rare possibility of stalling if only some fullkv stores caches were deleted, but further segments were still present.
* fix stats counters for store operations time

## v1.3.4

* add `DefaultBlockType` into `firehose.Chain` struct, enabling default block type setting for known chain

### substreams

* bumped to v1.5.3
* add `--block-type` flag that can be specified when creating substreams tier1. If not specified, tier1 will auto-detect block type from source.
* fix memory leak on substreams execution (by bumping wazero dependency)
* prevent substreams-tier1 stopping if blocktype auto-detection times out
* fix missing error handling when writing output data to files. This could result in tier1 request just "hanging" waiting for the file never produced by tier2.
* fix handling of dstore error in tier1 'execout walker' causing stalling issues on S3 or on unexpected storage errors
* increase number of retries on storage when writing states or execouts (5 -> 10)
* prevent slow squashing when loading each segment from full KV store (can happen when a stage contains multiple stores)

## v1.3.3

### substreams

* Fix a context leak causing tier1 responses to slow down progressively

## v1.3.2

### substreams

* fix another panic on substreams-tier2 service
* fix thread leak in metering affecting substreams
* revert a substreams scheduler optimisation that causes slow restarts when close to head
* add substreams_tier2_active_requests and substreams_tier2_request_counter prometheus metrics

## v1.3.1

* fix panic on substreams-tier2 service

## v1.3.0

### Substreams

* Substreams bumped to @v1.5.0: See https://github.com/streamingfast/substreams/releases/tag/v1.5.0 for details.

#### Chain-agnostic tier2

* A single substreams-tier2 instance can now serve requests for multiple chains or networks. All network-specific parameters are now passed from Tier1 to Tier2 in the internal ProcessRange request.
* This allows you to better use your computing resources by pooling all the networks together.

> [!IMPORTANT]
> Since the `tier2` services will now get the network information from the `tier1` request, you must make sure that the file paths and network addresses will be the same for both tiers.
> ex: if `--common-merged-blocks-store-url=/data/merged` is set on tier1, make sure the merged blocks are also available from tier2 under the path `/data/merged`.
> The flags `--substreams-state-store-url`, `--substreams-state-store-default-tag` and `--common-merged-blocks-store-url` are now ignored on tier2. The flag `--common-first-streamable-block` should be set to 0 to accommodate every chain.

> [!TIP]
> The cached 'partial' files no longer contain the "trace ID" in their filename, preventing accumulation of "unsquashed" partial store files. The system will delete files under '{modulehash}/state' named in this format`{blocknumber}-{blocknumber}.{hexadecimal}.partial.zst` when it runs into them.

#### Performance improvements

* All module outputs are now cached. (previously, only the last module was cached, along with the "store snapshots", to allow parallel processing).
* Tier2 will now read back mapper outputs (if they exist) to prevent running them again. Additionally, it will not read back the full blocks if its inputs can be satisfied from existing cached mapper outputs.
* Tier2 will skip processing completely if it's processing the last stage and the `output_module` is a mapper that has already been processed (ex: when multiple requests are indexing the same data at the same time)
* Tier2 will skip processing completely if it's processing a stage where all the stores and outputs have been processed and cached.
* Scheduler modification: a stage now waits for the previous stage to have completed the same segment before running, to take advantage of the cached intermediate layers.
* Improved file listing performance for Google Storage backends by 25%

> [!TIP]
* Concurrent requests on the same module hashes may benefit from the other requests' work to a certain extent (up to 75%) -- The very first request does most of the work for the other ones.

> [!TIP]
> More caches will increase disk usage and there is no automatic removal of old module caches. The operator is responsible for deleting old module caches.

> [!TIP]
> The cached 'partial' files no longer contain the "trace ID" in their filename, preventing accumulation of "unsquashed" partial store files.
> The system will delete files under '{modulehash}/state' named in this format`{blocknumber}-{blocknumber}.{hexadecimal}.partial.zst` when it runs into them.

#### Metrics

* Readiness metric for Substreams tier1 app is now named `substreams_tier1` (was mistakenly called `firehose` before).
* Added back readiness metric for Substreams tiere app (named `substreams_tier2`).
* Added metric `substreams_tier1_active_worker_requests` which gives the number of active Substreams worker requests a tier1 app is currently doing against tier2 nodes.
* Added metric `substreams_tier1_worker_request_counter` which gives the total Substreams worker requests a tier1 app made against tier2 nodes.

### Flags

* Added `--merger-delete-threads` to customize the number of threads the merger will use to delete files. It's recommended to increase this when using Ceph as S3 storage provider to 25 or higher (due to performance issues with deletes the merger might otherwise not be able to delete one-block files fast enough).
* Added `--substreams-tier2-max-concurrent-requests` to limit the number of concurrent requests to the tier2 substreams service.

* If relayer is started with a single source, it will have reduced tolerance for missing blocks. This is to prevent the relayer from falling behind when the source is not producing blocks.

## v1.2.5

* Fixed `tools check merged-blocks` default range when `-r <range>` is not provided to now be `[0, +∞]` (was previously `[HEAD, +∞]`).

* Fixed `tools check merged-blocks` to be able to run without a block range provided.

* Added API Key based authentication to `tools firehose-client` and `tools firehose-single-block-client`, specify the value through environment variable `FIREHOSE_API_KEY` (you can use flag `--api-key-env-var` to change variable's name to something else than `FIREHOSE_API_KEY`).

* Fixed `tools check merged-blocks` examples using block range (range should be specified as `[<start>]?:[<end>]`).

* Added `--substreams-tier2-max-concurrent-requests` to limit the number of concurrent requests to the tier2 Substreams service.

### Library `firehose-core`

* Added API Key authentication to `client.NewFirehoseFetchClient` and `client.NewFirehoseClient`.

  > [!NOTE]
  > If you were using `github.com/streamingfast/firehose-core/firehose/client.NewFirehoseFetchClient` or `github.com/streamingfast/firehose-core/firehose/client.NewFirehoseStreamClient`, this will be a minor breaking change, refer to [upgrade notes](./UPDATE.md#v125) for details if it affects you.

## v1.2.4

### Substreams improvements

* Performance: prevent reprocessing jobs when there is only a mapper in production mode and everything is already cached
* Performance: prevent "UpdateStats" from running too often and stalling other operations when running with a high parallel jobs count
* Performance: fixed bug in scheduler ramp-up function sometimes waiting before raising the number of workers
* Added the output module's hash to the "incoming request" log

### Reader node and Beacon blocks
* The `reader-node-bootstrap-url` gained the ability to be bootstrapped from a `bash` script.

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
    clearly documented here.

* The `reader-node-bootstrap-url` gained the ability to be bootstrapped from a pre-made archive file ending with `tar.zst` or `tar.zstd`.

* The `reader-node-bootstrap-data-url` is now added automatically if `firecore.Chain#ReaderNodeBootstrapperFactory` is `non-nil`.

  If the bootstrap URL ends with `tar.zst` or `tar.zstd`, the archive is read and extracted into the
	`reader-node-data-dir` location. The archive is expected to contain the full content of the 'reader-node-data-dir'
	and is expanded as is.

* Added `Beacon` to known list of Block model.

## v1.2.3

* Fix marshalling of blocks to JSON in tools like `firehose-client` and `print merged-blocks`

## v1.2.2

### Auth and metering

* Add missing metering events for `sf.firehose.v2.Fetch/Block` responses.
* Changed default polling interval in 'continuous authentication' from 10s to 60s, added 'interval' query param to URL.

### Substreams

* Fixed bug in scheduler ramp-up function sometimes waiting before raising the number of workers
* Fixed load-balancing from tier1 to tier2 when using dns:/// (round-robin policy was not set correctly)
* Added `trace_id` in grpc authentication calls
* Bumped connect-go library to new "connectrpc.com/connect" location

## v1.2.1

### Fixed

* Fixed `tools firehose-client` which was broken because of bad flag handling

### Added

* Added `--api-key-env-var` flag to firehose-clients, which allows you to pass your API Key from an environment variable (HTTP header `x-api-key`) instead of a JWT (`Authorization: bearer`), where supported.

## v1.2.0

* Poller is now fetching blocks in an optimized way, it will fetch several blocks at once and then process them.

* Poller is now handling skipped blocks, it will fetch the next blocks until it find a none skipped block.

* Poller now has default retry value of infinite.

* Compare tool is now using dynamic protobuf unmarshaler, it will be able to compare any block type.

* Print tool is now using dynamic protobuf unmarshaler, it will be able to print any block type.

* Print tool is encoding bytes in base64 by default, it can be changed to hex or base58 by using parameter `bytes-encoding`.

* Added 'x-trace-id' header to auth requests when using --common-auth-plugin=grpc

* Fixed Substreams scheduler sometimes taking a long time to spawn more than a single worker.

* Added ACCEPT_SOLANA_LEGACY_BLOCK_FORMAT env var to allow special tweak operations

## v1.1.3

* Removed useless chainLatestFinalizeBlock from blockPoller initialization

## v1.1.2

* Added `Arweave` to known list of Block model.

## v1.1.1

* Added `FORCE_FINALITY_AFTER_BLOCKS` environment variable to override block finality information at the reader/poller level. This allows an operator to pretend that finality is still progressing, N blocks behind HEAD, in the case where a beacon chain fails to do so and is intended as a workaround for deprecated chains like Goerli.

## v1.1.0

* Updated `substreams` and `dgrpc` to latest versions to reduce logging.

* Tools printing Firehose `Block` model to JSON now have `--proto-paths` take higher precedence over well-known types and even the chain itself, the order is `--proto-paths` > `chain` > `well-known` (so `well-known` is lookup last).

* The `tools print one-block` now works correctly on blocks generated by omni-chain `firecore` binary.

* Tools printing Firehose `Block` model to JSON now have `--proto-paths` take higher precedence over well-known types and even the chain itself, the order is `--proto-paths` > `chain` > `well-known` (so `well-known` is lookup last).

* The `tools print one-block` now works correctly on blocks generated by omni-chain `firecore` binary.

* The various health endpoint now sets `Content-Type: application/json` header prior sending back their response to the client.

* The `firehose`, `substreams-tier1` and `substream-tier2` health endpoint now respects the `common-system-shutdown-signal-delay` configuration value meaning that the health endpoint will return `false` now if `SIGINT` has been received but we are still in the shutdown unready period defined by the config value. If you use some sort of load balancer, you should make sure they are configured to use the health endpoint and you should `common-system-shutdown-signal-delay` to something like `15s`.

* The `firecore.ConsoleReader` gained the ability to print stats as it ingest blocks.

* The `firecore.ConsoleReader` has been made stricter by ensuring Firehose chain exchange protocol is respected.

* Changed `reader` logger back to `reader-node` to fit with the app's name which is `reader-node`.

* Fix `-c ""` not working properly when no arguments are present when invoking `start` command.

* Fix `tools compare-blocks` that would fail on new format.

* Fix `substreams` to correctly delete `.partial` files when serving a request that is not on a boundary.

* Add Antelope types to the blockchain's known types.

## v1.0.0

This is a major release.

### Operators

> [!IMPORTANT]
> When upgrading your stack to firehose-core v1.0.0, be sure to upgrade all components simultaneously because the block encapsulation format has changed.
> Blocks that are merged using the new merger will not be readable by previous versions.

### Added

* New binary `firecore` which can run all firehose components (`reader`, `reader-stdin`, `merger`, `relayer`, `firehose`, `substreams-tier1|2`) in a chain-agnostic way. This is not mandatory (it can still be used as a library) but strongly suggested when possible.

* Current Limitations on Ethereum:
  - The firecore `firehose` app does not support transforms (filters, header-only --for graph-node compatibility--) so you will want to continue running this app from `fireeth`
  - The firecore `substreams` apps do not support eth_calls so you will want to continue running them from `fireeth`
  - The firecore `reader` does not support the block format output by the current geth firehose instrumentation, so you will want to continue running it from `fireeth`

* New BlockPoller library to facilitate the implementation of rpc-poller-based chains, taking care of managing reorgs

* Considering that firehose-core is chain-agnostic, it's not aware of the different of the different block types. To be able to use tools around block decoding/printing,
  there are two ways to provide the type definition:
  1. the 'protoregistry' package contains well-known block type definitions (ethereum, near, solana, bitcoin...), you won't need to provide anything in those cases.
  2. for other types, you can provide additional protobuf files using `--proto-path` flag

### Changed

* Merged blocks storage format has been changed. Current blocks will continue to be decoded, but new merged blocks will not be readable by previous software versions.
* The code from the following repositories have been merged into this repo. They will soon be archived.
  * github.com/streamingfast/node-manager
  * github.com/streamingfast/merger
  * github.com/streamingfast/relayer
  * github.com/streamingfast/firehose
  * github.com/streamingfast/index-builder

## v0.2.4

* Fixed SF_TRACING feature (regression broke the ability to specify a tracing endpoint)
* Firehose connections rate-limiting will now force a delay of between 1 and 4 seconds (random value)  before refusing a connection when under heavy load
* Fixed substreams GRPC/Connect error codes not propagating correctly

## v0.2.3

### Fixed

 * fixed typo in `check-merged-blocks` preventing its proper display of missing ranges

## v0.2.2

### Added

* Firehose logs now include auth information (userID, keyID, realIP) along with blocks + egress bytes sent.

### Fixed

* Filesource validation of block order in merged-blocks now works correctly when using indexes in firehose `Blocks` queries

### Removed

* Flag `substreams-rpc-endpoints` removed, this was present by mistake and unused actually.
* Flag `substreams-rpc-cache-store-url` removed, this was present by mistake and unused actually.
* Flag `substreams-rpc-cache-chunk-size` removed, this was present by mistake and unused actually.

## v0.2.1

### Operators

> [!IMPORTANT]
> We have had reports of older versions of this software creating corrupted merged-blocks-files (with duplicate or extra out-of-bound blocks)
> This release adds additional validation of merged-blocks to prevent serving duplicate blocks from the firehose or substreams service.
> This may cause service outage if you have produced those blocks or downloaded them from another party who was affected by this bug.

* Find the affected files by running the following command (can be run multiple times in parallel, over smaller ranges)

```
tools check merged-blocks-batch <merged-blocks-store> <start> <stop>
```

* If you see any affected range, produce fixed merged-blocks files with the following command, on each range:

```
tools fix-bloated-merged-blocks <merged-blocks-store> <output-store> <start>:<stop>
```

* Copy the merged-blocks files created in output-store over to the your merged-blocks-store, replacing the corrupted files.

### Removed
* Removed the `--dedupe-blocks` flag on `tools download-from-firehose` as it can create confusion and more issues.

### Fixed
* Bumped `bstream`: the `filesource` will now refuse to read blocks from a merged-files if they are not ordered or if there are any duplicate.
* The command `tools download-from-firehose` will now fail if it is being served blocks "out of order", to prevent any corrupted merged-blocks from being created.
* The command `tools print merged-blocks` did not print the whole merged-blocks file, the arguments were confusing: now it will parse <start_block> as a uint64.
* The command `tools unmerge-blocks` did not cover the whole given range, now fixed

### Added
* Added the command `tools fix-bloated-merged-blocks` to try to fix merged-blocks that contain duplicates and blocks outside of their range.
* Command `tools print one-block and merged-blocks` now supports a new `--output-format` `jsonl` format.
Bytes data can now printed as hex or base58 string instead of base64 string.

### Changed
* Changed `tools check merged-blocks-batch` argument syntax: the output-to-store is now optional.

## v0.2.0

### Fixed

* Fixed a few false positives on `tools check merged-blocks-batch`
* Fixed `tools print merged-blocks` to print correctly a single block if specified.

### Removed

* **Breaking** The `reader-node-log-to-zap` flag has been removed. This was a source of confusion for operators reporting Firehose on <Chain> bugs because the node's logs where merged within normal Firehose on <Chain> logs and it was not super obvious.

  Now, logs from the node will be printed to `stdout` unformatted exactly like presented by the chain. Filtering of such logs must now be delegated to the node's implementation and how it deals depends on the node's binary. Refer to it to determine how you can tweak the logging verbosity emitted by the node.

### Added

* Added support `-o jsonl` in `tools print merged-blocks` and `tools print one-block`.
* Added support for block range in `tools print merged-blocks`.

  > [!NOTE]
  > For now the range is restricted to a single "merged-blocks" file!

* Added retry loop for merger when walking one block files. Some use-cases where the bundle reader was sending files too fast and the merger was not waiting to accumulate enough files to start bundling merged files
* Added `--dedupe-blocks` flag on `tools download-from-firehose` to ensure no duplicate blocks end up in download merged-blocks (should not be needed in normal operations)

## v0.1.12

* Added `tools check merged-blocks-batch` to simplify checking blocks continuity in batched mode, writing results to a store
* Bumped substreams to `v1.1.20` with a fix for some minor bug fixes related to start block processing

## v0.1.11

* Bumped `substreams` to `v1.1.18` with a regression fix for when a Substreams has a start block in the reversible segment.

## v0.1.10

### Added

The `--common-auth-plugin` got back the ability to use `secret://<expected_secret>?[user_id=<user_id>]&[api_key_id=<api_key_id>]` in which case request are authenticated based on the `Authorization: Bearer <actual_secret>` and continue only if `<actual_secret> == <expected_secret>`.

### Changed
* Bumped `substreams` to `v1.1.17` with provider new metrics `substreams_active_requests` and `substreams_counter`

## v0.1.9

### Changed

* Bumped `susbtreams` to `v1.1.14` to fix bugs with start blocks, where Substreams would fail if the start block was before the first block of the chain, or if the start block was a block that was not yet produced by the chain.
* Improved error message when referenced config file is not found, removed hard-coded mention of `fireacme`.

## v0.1.8

### Fixed

* More tolerant retry/timeouts on filesource (prevent "Context Deadline Exceeded")

## v0.1.7

### Operators

> [!IMPORTANT]
> The Substreams service exposed from this version will send progress messages that cannot be decoded by Substreams clients prior to v1.1.12.
> Streaming of the actual data will not be affected. Clients will need to be upgraded to properly decode the new progress messages.

### Changed

* Bumped substreams to `v1.1.12` to support the new progress message format. Progression now relates to **stages** instead of modules. You can get stage information using the `substreams info` command starting at version `v1.1.12`.
* Bumped supervisor buffer size to 100Mb
* Substreams bumped: better "Progress" messages

### Added

* Added new templating option to `reader-node-arguments`, specifically `{start-block-num}` (maps to configuration value `reader-node-start-block-num`) and `{stop-block-num}` (maps to value of configuration value `reader-node-stop-block-num`)

### Changed

* The `reader-node` is now able to read Firehose node protocol line up to 100 MiB in raw size (previously the limit was 50 MiB).

### Removed

* Removed `--substreams-tier1-request-stats` and `--substreams-tier1-request-stats` (Substreams request-stats are now always sent to clients)

## v0.1.6

### Fixed

* Fixed bug where `null` dmetering plugin was not able to be registered.

## v0.1.5

### Fixed

* Fixed dmetering bug where events where dropped, when channel got saturated

### Changed

* `fire{chain} tools check forks` now sorts forks by block number from ascending order (so that line you see is the current highest fork).
* `fire{chain} tools check forks --after-block` can now be used to show only forks after a certain block number.
* bump `firehose`, `dmetering` and `bstream` dependencies in order to get latest fixes to meter live blocks.

## v0.1.4

This release bumps Substreams to [v1.1.10](https://github.com/streamingfast/substreams/releases/tag/v1.1.10).

### Fixed

* Fixed jobs that would hang when flags `--substreams-state-bundle-size` and `--substreams-tier1-subrequests-size` had different values. The latter flag has been completely **removed**, subrequests will be bound to the state bundle size.

### Added

* Added support for *continuous authentication* via the grpc auth plugin (allowing cutoff triggered by the auth system).

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

* Authentication plugin `trust` can now specify an exclusive list of `allowed` headers (all lowercase), ex: `trust://?allowed=x-sf-user-id,x-sf-api-key-id,x-real-ip,x-sf-substreams-cache-tag`

* The `tier2` app no longer uses the `common-auth-plugin`, `trust` will always be used, so that `tier1` can pass down its headers (ex: `X-Sf-Substreams-Cache-Tag`).

## v0.1.2

#### Operator Changes

* Added `fire{chain} tools check forks <forked-blocks-store-url> [--min-depth=<depth>]` that reads forked blocks you have and prints resolved longest forks you have seen. The command works for any chain, here a sample output:

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

* The `fire{chain} tools` commands and sub-commands have better rendering `--help` by hidden not needed global flags with long description.

## v0.1.1

#### Operator Changes

* Added missing `--substreams-tier2-request-stats` request debugging flag.

* Added missing Firehose rate limiting options flags, `--firehose-rate-limit-bucket-size` and `--firehose-rate-limit-bucket-fill-rate` to manage concurrent connection attempts to Firehose, check `fire{chain} start --help` for details.

## v0.1.0

#### Backend Changes

* Fixed Substreams accepted block which was not working properly.
