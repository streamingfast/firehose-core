## Firehose Integrators Tool Kit

This repository contains all the boilerplate that is required to maintain the Go part of the Firehose stack for chain integrators. This repository can be seen as a Integrator Tool Kit for people maintaining Firehose version of a specific chain. It's essentially a chain agnostic shared library that is used to avoid duplication across all projects and ease maintenance work for the various teams. It contain **no** chain specific code and everything that is chain specific must be provided.

> **Note** This repository is **only** useful for maintainers of `firehose-<chain>` repositories and new integrators looking to integrate Firehose into a new chain. If you are a developer using Firehose or Substreams technology, this repository is not for you.

### Philosophy

Firehose maintenance cost comes from two side. First there is the chain integration that needs to be maintained. This is done within the chain's code directly by the chain's core developers. The second side of thing is the maintenance of the Golang part of the Firehose stack.

Each chain creates it's own Firehose Golang repository named `firehose-<chain>` (https://github.com/streamingfast/firehose-acme acts as template for this). Firehose is composed of multiple smaller components that can be run independently and each of them as a set of CLI flags and other configuration.

The initial https://github.com/streamingfast/firehose-acme "template" we had was containing a lot of boilerplate code to properly configure and run the Firehose Golang stack. This means that if we needed to add a new feature which required a new flags or change a flag default value or any kind of improvements, chain integrators that were maintaining their own `firehose-<chain>` repository were in the obligation of tracking changes made https://github.com/streamingfast/firehose-acme and apply those back on their own repository by hand.

This was true also for continuously tracking updating the various small library that form the Firehose stack. With Firehose starting to getting more and more streamlined across different chains, that was a recipe for a maintenance hell for every chain integration.

This repository aims at solving this maintenance burden by acting as a facade for all the Golang code required to have a functional and up to date Firehose stack. This way, we maintain the `firehose-core` project, adding/changing/removing flags, bumping dependencies, adding new features and you as a maintainer of `firehose-<chain>` repository, you simply need to track https://github.com/streamingfast/firehose-core for new releases and bump a single dependency to be up to date with latest changes.

### Changelog

The [CHANGELOG.md](./CHANGELOG.md) of this project will be re-written so that you can copy directly the various entries since your last update straight to your own release notes so that operators that are using your `firehose-<chain>` repository are made aware of deprecation notes, removal, changes and other important element.

### Build & CI

The build and CI files are maintained for now in https://github.com/streamingfast/firehose-acme directly and should be updated manually from time to time from there.

