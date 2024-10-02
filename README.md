## Firehose multi-chain executor

This repository contains all the base components of [Firehose](https://firehose.streamingfast.io/) and run the software for multiple block chains (Bitcoin, Solana, ...) or be used as a library (firehose-ethereum, firehose-antelope)

## Compiling

* `go install -v ./cmd/firecore`

Or download the latest Release from https://github.com/streamingfast/firehose-core/releases/

## Running directly

* `firehose-core` can run one or many of the following components:
  - reader-node
  - merger
  - relayer
  - firehose
  - substreams-tier1
  - substreams-tier2
  - (reader-node-stdin -- not run by default)

* You can use a config file like this (default: `firehose.yaml`)

```
start:
  args:
  - reader-node
  - merger
  flags:
    reader-node-path: "/usr/local/bin/firesol"
    reader-node-args: ["fetch", "rpc", "http://localhost:8545", "0"]
```

* Run it with `firecore start --config-file=./firehose.yaml` or set an empty value for config-file (`--config-file=`) to use the default values.

### Development

For development purposes, the easiest set up is to install the [dummy-blockchain](https://github.com/streamingfast/dummy-blockchain) and then use the `./devel/standard/start.sh` script we provide in the repository that launches a full fledged `firehose-core` instance backed by this dummy blockchain:

```
# Needed only once, if you don't have the binary locally already
go install github.com/streamingfast/dummy-blockchain@latest

# The -c cleans any previous data, remove to keep data upon restarts
./devel/standard/start.sh -c
```

## Using as a library

For chains that implement "firehose block filters" and extensions like "eth_call", this repository can be used as a library for those implementations, like these:

* [firehose-ethereum](https://github.com/streamingfast/firehose-ethereum)
* [firehose-antelope](https://github.com/pinax-network/firehose-antelope)

### Philosophy

Firehose maintenance cost comes from two sides. First, there is the chain integration that needs to be maintained. This is done within the chain's code directly by the chain's core developers. The second side of things is the maintenance of the Golang part of the Firehose stack.

Each chain creates its own Firehose Golang repository named `firehose-<chain>`. [Firehose-acme repository](https://github.com/streamingfast/firehose-core/firehose-acme) acts as an example of this. Firehose is composed of multiple smaller components that can be run independently and each of them has a set of CLI flags and other configuration parameters.

The initial "Acme" template we had contained a lot of boilerplate code to properly configure and run the Firehose Golang stack. This meant that if we needed to add a new feature that required a new flag or change a flag default value or any kind of improvements, chain integrators that were maintaining their `firehose-<chain>` repository were in the obligation of tracking changes made in `firehose-acme` and apply those back on their repository by hand.

This was true also for continuously tracking updates to the various small libraries that form the Firehose stack. With Firehose starting to get more and more streamlined across different chains, that was a recipe for a maintenance hell for every chain integration.

This repository aims at solving this maintenance burden by acting as a facade for all the Golang code required to have a functional and up-to-date Firehose stack. This way, we maintain the `firehose-core` project, adding/changing/removing flags, bumping dependencies, and adding new features, while you, as a maintainer of `firehose-<chain>` repository, simply need to track `firehose-core` for new releases and bump a single dependency to be up to date with the latest changes.

### Changelog

The [CHANGELOG.md](./CHANGELOG.md) of this project is written in such way so that you can copy-paste recent changes straight into your own release notes so that operators that are using your `firehose-<chain>` repository are made aware of deprecation notes, removal, changes and other important elements.

Maintainers, you should copy/paste content of this content straight to your project. It is written and meant to be copied over to your project. If you were at `firehose-core` version `1.0.0` and are bumping to `1.1.0`, you should copy the content between those 2 version to your own repository, replacing placeholder value `fire{chain}` with your chain's own binary.

The bash command `awk '/## v0.1.11/,/## v0.1.8/' CHANGELOG.md | grep -v '## v0.1.8'` to obtain the content between 2 versions. You can then merged the different `Added`, `Changed`, `Removed` and others into single merged section.

### Update

When bumping `firehose-core` to a breaking version, details of such upgrade will be described in [UPDATE.md](./UPDATE.md). Breaking version can be be noticed currently if the minor version is bumped up, for example going from v0.1.11 to v0.2.0 introduces some breaking changes. Once we will release the very first major version 1, breaking changes will be when going from v0.y.z to v1.0.0.
