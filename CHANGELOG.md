# Change log

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). See [MAINTAINERS.md](./MAINTAINERS.md)
for instructions to keep up to date.

Operators, you should copy/paste content of this content straight to your project. It is written and meant to be copied over to your project.

If you were at `firehose-core` version `1.0.0` and are bumping to `1.1.0`, you should copy the content between those 2 version to your own repository, replacing placeholder value `fire{chain}` with your chain's own binary.

## v0.1.8

### Fixed

* More tolerant retry/timeouts on filesource (prevent "Context Deadline Exceeded")

## v0.1.7

### Operators

> [!IMPORTANT]
> The Substreams service exposed from this version will send progress messages that cannot be decoded by substreams clients prior to v1.1.12.
> Streaming of the actual data will not be affected. Clients will need to be upgraded to properly decode the new progress messages.

### Changed

* Bumped substreams to `v1.1.12` to support the new progress message format. Progression now relates to **stages** instead of modules. You can get stage information using the `substreams info` command starting at version `v1.1.12`.
* Bumped supervisor buffer size to 100Mb
* Added templating option to `reader-node-arguments` arg, specifically {start-block-num} and {stop-block-num}
* Substreams bumped: better "Progress" messages

### Removed

* Removed `--substreams-tier1-request-stats` and `--substreams-tier1-request-stats` (substreams request-stats are now always sent to clients)

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
