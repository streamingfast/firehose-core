# DESIGN

## REAL-TIME

### On initialization
1) List merged files from the first-streamable-block, until a "hole" is found, 
2) set this as the start-block 

ex: 
* first-streamable-block == 12340
* existing merged files: 12300, 12400, 12900
* the start-block will be set to 12500

### processing loop

#### 1. Polling the one-block-files to feed the Forkable Handler
* List one-block-files and decode their fields based on the filenames only
* skip any one-block-file that is < start-block
* Send these one-block-files in a "Forkable Handler"

#### 2. Accumulating irreversible (final) blocks

* When there are enough linkable one-blocks, the Forkable Handler will let the "irreversible blocks" through, feeding the Bundler one by one in a linear fashion (there should not be any hole there)
* When a one-block object comes through, its payload is read from the store (async, waitGroup()) so it is ready for the next step
 
#### 3. Merging

* When the Bundler receives an irreversible block that passes a boundary (ex: while loading bundle 100-199, we see the block 205)
* It waits for the one-block reading waitgroup
* It writes the merged file
* It deletes the one-block-files that were merged (the final/canonical ones) -- leaving only the forked blocks in the one-block-store
* It deletes any very old one-block-files (based on timestamp and max-forked-blocks-age

### Providing unmerged blocks through GRPC 

* On request, the merger can send the accumulated irreversible blocks in the bundler through GRPC

## OneBlock files naming

{TIMESTAMP}-{BLOCKNUM}-{BLOCKIDSUFFIX}-{PREVIOUSIDSUFFIX}-{SOURCEID}.json.gz

* TIMESTAMP: YYYYMMDDThhmmss.{0|5} where 0 and 5 are the possible values for 500-millisecond increments..
* BLOCKNUM: 0-padded block number
* BLOCKIDSUFFIX: [:8] from block ID
* PREVIOUSIDSUFFIX: [:8] previousId for block
* SOURCEID: freeform string to identify who wrote the file. This is useful if you want multiple extractors writing to the same one-block-file store without concurrency issues. It is not part of the canonical form of the one-block-file.

Example:
* 20170701T122141.0-0000000100-24a07267-e5914b39-extractor-0.json.gz
* 20170701T122141.5-0000000101-dbda3f44-09f6d693-myhostname134.json.gz

 fmt.Sprintf("%s.%01d", t.Format("20060102T150405"), t.Nanosecond()/100000000)


