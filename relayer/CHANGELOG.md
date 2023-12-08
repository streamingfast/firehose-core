# Change log

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Uncommitted]

### Removed
* `MaxDrift` config option removed
* `InitTime` config option removed

### Changed
* "max-drift" mechanism modified to now detect "block hole", by expecting highest 'received' block never to be higher than highest 'sent' block + 1 (out of the forkable).
* upon detection of a "block hole", instead of shutting down, the process will simply restart joining from block files from where it left off.
* Now prints the whole config on start


### Added
* add SourceRequestBurst to config, allows requesting a burst to each of the source (useful when connecting to another relayer)

## [v0.0.1] 2020-06-23

### Changed
* maxDriftTolerance feature now disabled if set to 0
* now returns headinfo and Ready state before max-source-latency is passed
* add `min-start-offset` instead of default=200
* `--listen-grpc-addr` now is `--grpc-listen-addr`

## 2020-03-21

### Changed

* License changed to Apache 2.0
