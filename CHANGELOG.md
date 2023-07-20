# Change log

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). See [MAINTAINERS.md](./MAINTAINERS.md)
for instructions to keep up to date.

Operators, you should copy/paste content of this content straight to your `firehose-<chain>` project. It is written and meant to be copied over to your project.

If you were at `firehose-core` version `1.0.0` and are bumping to `1.1.0`, you should copy the content between those 2 version to your own repository.

## Next

### Highlights

#### Substreams State Store Selection

The `substreams` server now accepts `X-Sf-Substreams-Cache-Tag` header to select which Substreams state store URL should be used by the request. When performing a Substreams request, the servers will pick the state store based on the header. This enable consumers to stay on the same cache version when the operators needs to bump the data version (reasons for this could be a bug in Substreams software that caused some cached data to be corrupted on invalid).

To benefit from this, operators that have a version currently in their state store URL should move it to the new flag `substreams-tier1-state-store-default-tag` (don't forget to apply to `substreams-tier1-state-store-default-tag`). For example if today you have in your config:

```yaml
start:
  args:
  ...
  flags:
    substreams-tier1-state-url: <some>/<path>/v3
    ...
    substreams-tier2-store-state-url: <some>/<path>/v3
```

You should convert to:

```yaml
start:
  args:
  ...
  flags:
    substreams-tier1-state-url: <some>/<path>
    substreams-tier1-state-url-default-tag: v3
    ...
    substreams-tier2-store-state-url: <some>/<path>
    substreams-tier2-state-url-default-tag: v3
```

### Operators Upgrade

The app `substreams-tier1` and `substreams-tier2` should be upgraded concurrently, but failure to do so will only result in temporary errors until they are at the same version.

### CLI Changes

* Authentication plugin `trust` can now specify an exclusive list of `allowed` headers (all lowercase), ex: `trust://?allowed=x-sf-user-id,x-sf-api-key-id,x-real-ip,x-sf-substreams-cache-tag`

* The `tier2` app no longer has customizable auth plugin (or any Modules), `trust` will always be used, so that `tier` can pass down its headers (e.g. `X-Sf-Substreams-Cache-Tag`).

### Backend changes

* The `tier1` and `tier2` config have a new configuration `StateStoreDefaultTag`, if non-empty, it which will be appended to the value `StateStoreURL` to form the final state store URL. Users will be able to point to a different state store (ex: stay on `/my/store/v2` while default path is now `/my/store/v3`) by providing a `X-Sf-Substreams-Cache-Tag` header (gated by auth module).

## v0.1.2

#### Operator Changes

* Added `fire<chain> tools check forks <forked-blocks-store-url> [--min-depth=<depth>]` that reads forked blocks you have and prints resolved longest forks you have seen. The command works for any chain, here a sample output:

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

* The `fire<chain> tools` commands and sub-commands have better rendering `--help` by hidden not needed global flags with long description.

## v0.1.1

#### Operator Changes

* Added missing `--substreams-tier2-request-stats` request debugging flag.

* Added missing Firehose rate limiting options flags, `--firehose-rate-limit-bucket-size` and `--firehose-rate-limit-bucket-fill-rate` to manage concurrent connection attempts to Firehose, check `fire<chain> start --help` for details.

## v0.1.0

#### Backend Changes

* Fixed Substreams accepted block which was not working properly.
