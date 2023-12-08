# Change log

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### BREAKING CHANGES: https://github.com/streamingfast/bstream/issues/22
* Merger now only writes irreversible blocks in merged blocks
* Merger keeps the non-canonical one-block-files (forked blocks) until `MaxForkedBlockAgeBeforePruning` is passed, doing a pass at most once every `TimeBetweenPruning`
* Main loop will run at most once every `TimeBetweenPolling`

## [v0.0.2]
### Changed
* Merger now deletes one-block-files that it has seen before exactly like the ones that are passed MaxFixableFork, based on DeleteBlocksBefore
* 'Live' option now changed to 'BatchMode' with the inverse behavior (for consistency with our other projects)
* 'SeenBlocksFile' option changed to 'StateFile' since it now contains highestSeenBlock, used to determine next start block.
* When merger is started, in live mode, it tries to get its start block from the state file. If it cannot, it locates starting point as before, by identifying the highest merged-blocks.

### Added
* Config: `OneBlockDeletionThreads` to control how many one-block-files will be deleted in parallel on storage, 10 is a sane default, 1 is the minimum.
* Config: `MaxOneBlockOperationsBatchSize` to control how many files ahead to we read (also how many files we can put in deleting queue at a time.) Should be way more than the number of files that we need to merge in case of forks, 2000 is a sane default, 250 is the minimum

### Removed
* Option DeleteBlocksBefore is now gone, it is the only behavior (not deleting one-block-files makes no sense for a merger)
* Option Progressfilename is now gone. BatchMode now has NO Progression option (not needed now that batch should mostly be done from mindreader...)

### Improved
* Logging of OneBlockFile deletion now only called once per delete batch
* When someone else pushes a merged file, merger now detects it and reads the actual blocks to populate its seenblockscache, as discussed here: https://github.com/streamingfast/firehose-core/merger/issues/1
* Fixed waiting time to actually use TimeBetweenStoreLookups instead of hardcoded value of 1 second when bundle is incomplete

## [v0.0.1]
### Changed
* `--listen-grpc-addr` now is `--grpc-listen-addr`

### Removed
* Removed the `protocol`, merger is not `protocol` agnostic 
* Removed EnableReadinessProbe option in config, it is now the only behavior

### Improved
* Logging was adjust at many places
* context now used in every dstore call for better timeout handling

## 2020-03-21

### Changed

* License changed to Apache 2.0
