# bundle1000 — 1000-blocks merged-blocks example

Same layout as `devel/standard/`, but the whole stack runs with
`common-merged-blocks-bundle-size: 1000`: the merger writes 1000-blocks
merged files and every reader (firehose, substreams-tier1/tier2) reads them.

## Run

```bash
go install github.com/streamingfast/dummy-blockchain@latest
cd devel/bundle1000
./start.sh -c
```

## Verify

The merger writes `0000000000`, `0000001000`, ... (one file per 1000 blocks):

```bash
ls firehose-data/storage/merged-blocks/
```

Print blocks from a 1000-blocks file (the shared tools flag must match the
store):

```bash
../firecore tools print merged-blocks ./firehose-data/storage/merged-blocks 0:10 \
  --merged-blocks-bundle-size=1000
```

Stream across a bundle boundary through the firehose app:

```bash
../firecore tools firehose-client localhost:10015 --plaintext 995:1005
```

Check the store consistency:

```bash
../firecore tools check merged-blocks ./firehose-data/storage/merged-blocks \
  --merged-blocks-bundle-size=1000
```

Convert legacy 100-blocks files (for example produced by `devel/standard`) to
1000-blocks files:

```bash
../firecore tools resize-merged-blocks \
  ../standard/firehose-data/storage/merged-blocks \
  ./resized-merged-blocks \
  0 1000 --source-bundle-size=100 --target-bundle-size=1000
```
