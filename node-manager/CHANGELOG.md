# Change log

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## Unreleased

### Removed
* No more 'BatchMode' option, we get wanted behavior only by setting MergeThresholdBlockAge:
    - '0' -> do not automatically merge, ever
    - '1' -> always merge
    - (any other duration) -> only merge bundle when all its blocks are older than this duration
* No more 'tracker' to auto-merge based on current LIB (that feature requires too much setup)
* No more option 'FailOnNonContinuousBlocks'  -> (was not actually implemented anyway)

## 2021 (released throughout..)

### Added
* New Feature: `auto-merge` the mindreader will switch between producing merged-blocks and one-block files depending on the existence of merged files in destination store and on the age of the blocks. It will also never overwrite destination files (unless BatchMode is set)
* New Feature: when producing merged files, a partial file will be produced on shutdown. If the next block to appear on next startup is the expected one, it will load the partial file to continue producing a merged-blocks file.
* New option 'BatchMode' forces the mindreader to produce merged-blocks all the time (without checking block age or existence of merged files in block store) and to overwrite any existing merged-blocks files.
* New option MergeThresholdBlockAge: defines the age at which a block is considered old enough to be included in a merged-block-file directly (without any risk of forking).

### Fixed
* auto-merged block files are now written locally first, then sent asynchronously to the destination storage. They are sent in order (no threads). This makes it more resilient.

### Removed
* `discardAfterStopBlock`: this option did not give any value, especially now that the mindreader can switch between producing merged blocks and one-block files
* `merge_upload_directly`: that feature is now automatically enabled (see new `auto-merge` feature), the `BatchMode` option can force that behavior now.


## [v0.0.1] - 2020-06-22

### Fixed:
* nodeos log levels are now properly decoded when going through zap logging engine instead of showing up as DEBUG
* mindreader does not get stuck anymore when trying to find an unexisting snapshot (it fails rapidly instead)

### Changed
* ContinuousChecker is not enabled by default now, use FailOnNonContinuousBlocks
* BREAKING: AutoRestoreLatest(bool) option becomes AutoRestoreSource (`backup`, `snapshot`)
* Nodeos unexpectedly shutting down now triggers a Shutdown of the whole app
* ShutdownDelay is now actually implemented for any action that will make nodeos unavailable (snapshot, volume_snapshot, backup, maintenance, shutdown..). It will report the app as "not ready" for that period before actually affecting the service.

### Added
* Options.DiscardAfterStopBlock, if not true, one-block-files will now be produced with the remaining blocks when MergeUploadDirectly is set. This way, they are not lost if you intend to restart without mergeUploadDirectly run a merger on these blocks later.
* App `nodeos_mindreader_stdin`, with a small subset of the features, a kind of "dumb mode" that only does the "mindreader" job (producing block files, relaying blocks through GRPC) on none of the "manager" job.
* Options.AutoSnapshotHostnameMatch(string) will only apply auto-snapshot parameters if os.Hostname() returns this string
* Options.AutoBackupHostnameMatch(string) will only apply auto-backup parameters if os.Hostname() returns this string
* Add FailOnNonContinuousBlocks Option to use continuousChecker or not
* Possibility to auto-restore from latest snapshot (useful for BP), deleting appropriate files to make it work and continue
* NumberOfSnapshotsToKeep flag to maintain a small list of snapshots -> If non-zero, it deletes older snapshot.

## 2020-03-21

### Changed

* License changed to Apache 2.0
