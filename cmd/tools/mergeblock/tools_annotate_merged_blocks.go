package mergeblock

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/storage"
	"github.com/dustin/go-humanize"
	"github.com/klauspost/compress/zstd"
	"github.com/spf13/cobra"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dbin"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"go.uber.org/zap"
)

// scanScratchBufferSize is the chunk a worker reads a block in. Big enough that a large block
// takes few reads, small enough that a wide --parallelism stays cheap: at the default of 32
// workers these buffers add up to 16 MiB.
const scanScratchBufferSize = 512 * 1024

func NewToolsAnnotateMergedBlocksCmd(rootLog *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "annotate-merged-blocks <gs-store-url>",
		Short: "Record each merged-blocks file's size, block count and first block time in its object metadata",
		Long: cli.Dedent(`
			Reads every merged-blocks file under the store URL, streams it through a zstd
			decoder, and writes three custom metadata entries on the object:

			  datasize   the file's size once decompressed, in bytes
			  itemcount  the number of blocks the file holds
			  timestamp  the time of the file's first block, '2025-10-12 10:23:12' in UTC

			'firecore tools stats-merged-blocks' then reports on a whole store from these
			entries, reading them out of a listing without downloading a single file again.

			No block is ever held: each one is read into a scratch buffer that is reused from
			block to block and from file to file, and dropped, so memory stays flat whatever
			the block size. Of the first block only the head is looked at, far enough to reach
			its timestamp field, and only the block's metadata fields are decoded, its
			chain-specific payload being skipped, so this works on any chain's merged blocks
			without knowing the chain's block type.

			This only works on Google Cloud Storage ('gs://') because it sets object metadata
			on files it does not rewrite.

			The listing already carries each object's existing metadata, so files that already
			carry all three entries are skipped without a single extra request: rerunning over
			a store that is mostly done costs one listing and nothing else. --overwrite
			recomputes them anyway.

			Every file is read and updated under both its generation and its metageneration,
			so a file rewritten by the merger while this runs is reported as failed rather
			than annotated with values taken from the version that is gone.

			Running inside a GCP pod, --grpc is worth trying: it uses the Cloud Storage gRPC
			API, which on GKE takes the DirectPath route to the storage backend and skips the
			Google Front End entirely.

			The store URL takes the same two query parameters dstore reads: '?project=' bills
			the reads to that project on a requester-pays bucket, and '?client_protocol=grpc'
			selects the same transport as --grpc. Requester-pays and DirectPath are mutually
			exclusive, so setting a project turns DirectPath off and routes the gRPC traffic
			through the Google Front End, which is the only side that enforces the billing.
		`),
		Example: cli.Dedent(`
			# Annotate a whole merged-blocks store with 64 concurrent readers
			firecore tools annotate-merged-blocks gs://example-bucket/eth-mainnet/merged --parallelism 64

			# Annotate blocks 1000000 to 2000000 over gRPC, from a GKE pod
			firecore tools annotate-merged-blocks gs://example-bucket/eth-mainnet/merged \
			  --start-block 1000000 --stop-block 2000000 --grpc
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := annotateConfig{
				parallelism: sflags.MustGetInt(cmd, "parallelism"),
				useGRPC:     sflags.MustGetBool(cmd, "grpc"),
				connPool:    sflags.MustGetInt(cmd, "grpc-connection-pool"),
				overwrite:   sflags.MustGetBool(cmd, "overwrite"),
				dryRun:      sflags.MustGetBool(cmd, "dry-run"),
				startBlock:  sflags.MustGetUint64(cmd, "start-block"),
				stopBlock:   sflags.MustGetUint64(cmd, "stop-block"),
			}
			cmd.SilenceUsage = true
			return runAnnotateMergedBlocks(cmd.Context(), args[0], cfg, rootLog)
		},
	}

	cmd.Flags().Int("parallelism", 32, "Number of merged-blocks files read and annotated concurrently")
	cmd.Flags().Bool("grpc", false, "Talk to Cloud Storage over its gRPC API instead of JSON/XML over HTTP")
	cmd.Flags().Int("grpc-connection-pool", 0, "Number of gRPC connections to spread the work over, 0 derives one per 32 of --parallelism")
	cmd.Flags().Bool("overwrite", false, "Recompute files that already carry all three metadata entries")
	cmd.Flags().BoolP("dry-run", "n", false, "Only report how many files would be annotated, reading none of them")
	cmd.Flags().Uint64("start-block", 0, "Only annotate files whose first block is at or above this block")
	cmd.Flags().Uint64("stop-block", 0, "Only annotate files whose first block is below this block, 0 for no upper bound")

	return cmd
}

type annotateConfig struct {
	parallelism int
	useGRPC     bool
	connPool    int
	overwrite   bool
	dryRun      bool
	startBlock  uint64
	stopBlock   uint64
}

type annotateStats struct {
	listed       atomic.Int64
	annotated    atomic.Int64
	skipped      atomic.Int64
	failed       atomic.Int64
	blocks       atomic.Int64
	compressed   atomic.Int64
	uncompressed atomic.Int64
}

func runAnnotateMergedBlocks(ctx context.Context, storeURL string, cfg annotateConfig, logger *zap.Logger) error {
	target, err := parseGSURL(storeURL)
	if err != nil {
		return err
	}
	cfg.useGRPC = cfg.useGRPC || target.grpc

	if cfg.parallelism < 1 {
		cfg.parallelism = 1
	}
	if cfg.stopBlock != 0 && cfg.stopBlock <= cfg.startBlock {
		return fmt.Errorf("--stop-block %d must be above --start-block %d", cfg.stopBlock, cfg.startBlock)
	}

	connectionPool := grpcConnectionPool(cfg.connPool, cfg.parallelism)
	client, err := newGSClient(ctx, target, cfg.useGRPC, connectionPool, logger)
	if err != nil {
		return fmt.Errorf("creating storage client: %w", err)
	}
	defer client.Close()

	bucket := bucketHandle(client, target)

	fmt.Println(stylex.Title("Merged Blocks Annotation"))
	fmt.Println(stylex.Dim(stylex.Separator(80)))
	fmt.Println(stylex.Labelf("Store:       %s", storeURL))
	fmt.Println(stylex.Labelf("Parallelism: %d", cfg.parallelism))
	if cfg.useGRPC {
		fmt.Println(stylex.Labelf("Transport:   gRPC (%d connection(s))", connectionPool))
	} else {
		fmt.Println(stylex.Label("Transport:   HTTP"))
	}
	if cfg.dryRun {
		fmt.Println(stylex.Warn("Dry run: nothing is read and no metadata is written"))
	}
	fmt.Println()

	stats := &annotateStats{}
	started := time.Now()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stopProgress := startAnnotateProgress(ctx, stats, started, logger)

	objects := make(chan mergedBlocksObject, cfg.parallelism*2)

	var workers sync.WaitGroup
	for range cfg.parallelism {
		workers.Add(1)
		go func() {
			defer workers.Done()
			annotateWorker(ctx, bucket, cfg, objects, stats, logger)
		}()
	}

	listErr := walkMergedBlocks(ctx, bucket, target.prefix, cfg.startBlock, cfg.stopBlock,
		[]string{"Name", "Size", "Generation", "Metageneration", "Metadata"},
		func(object mergedBlocksObject) error {
			stats.listed.Add(1)
			select {
			case objects <- object:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	)
	close(objects)
	workers.Wait()
	stopProgress()

	annotatedLabel := "Annotated:  "
	if cfg.dryRun {
		annotatedLabel = "Would annotate:"
	}
	fmt.Println(stylex.Valuef("%s %d file(s), %s block(s)", annotatedLabel, stats.annotated.Load(), humanize.Comma(stats.blocks.Load())))
	fmt.Println(stylex.Valuef("Skipped:     %d file(s) already carrying all three entries", stats.skipped.Load()))
	fmt.Println(stylex.Valuef("Read:        %s compressed, %s uncompressed", humanize.Bytes(uint64(stats.compressed.Load())), humanize.Bytes(uint64(stats.uncompressed.Load()))))
	fmt.Println(stylex.Valuef("Elapsed:     %s", time.Since(started).Round(time.Second)))

	if listErr != nil {
		fmt.Println(stylex.Error("✗ listing interrupted"))
		return fmt.Errorf("listing %s: %w", storeURL, listErr)
	}
	if failed := stats.failed.Load(); failed > 0 {
		fmt.Println(stylex.Errorf("✗ %d file(s) could not be annotated", failed))
		return fmt.Errorf("%d file(s) could not be annotated", failed)
	}
	fmt.Println(stylex.Success("✓"))
	return nil
}

func annotateWorker(ctx context.Context, bucket *storage.BucketHandle, cfg annotateConfig, objects <-chan mergedBlocksObject, stats *annotateStats, logger *zap.Logger) {
	// One decoder per worker, reused across files and told to decode on this goroutine only:
	// the worker pool already provides the parallelism, and a low-memory decoder keeps the
	// window allocation bounded whatever the file size.
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	if err != nil {
		logger.Error("cannot create zstd decoder", zap.Error(err))
		stats.failed.Add(1)
		return
	}
	defer decoder.Close()

	// One scratch buffer per worker, reused for every block of every file it handles.
	scratch := make([]byte, scanScratchBufferSize)

	for object := range objects {
		if ctx.Err() != nil {
			return
		}

		if !cfg.overwrite && isAnnotated(object.attrs.Metadata) {
			stats.skipped.Add(1)
			continue
		}
		if cfg.dryRun {
			stats.annotated.Add(1)
			continue
		}

		if err := annotateOne(ctx, bucket, object, decoder, scratch, stats); err != nil {
			if ctx.Err() != nil {
				return
			}
			stats.failed.Add(1)
			logger.Warn("cannot annotate merged-blocks file", zap.String("file", object.attrs.Name), zap.Error(err))
			continue
		}
		stats.annotated.Add(1)
	}
}

// isAnnotated reports whether a previous run left all three entries on the object; a file
// carrying only some of them is read again so they always describe the same version of it.
func isAnnotated(metadata map[string]string) bool {
	for _, key := range []string{dataSizeMetadataKey, itemCountMetadataKey, timestampMetadataKey} {
		if _, found := metadata[key]; !found {
			return false
		}
	}
	return true
}

func annotateOne(ctx context.Context, bucket *storage.BucketHandle, object mergedBlocksObject, decoder *zstd.Decoder, scratch []byte, stats *annotateStats) error {
	attrs := object.attrs

	// Pin the read to the generation the listing returned, so all three values describe one
	// and the same version of the file.
	reader, err := bucket.Object(attrs.Name).Generation(attrs.Generation).NewReader(ctx)
	if err != nil {
		return fmt.Errorf("opening object: %w", err)
	}
	defer reader.Close()

	var source io.Reader = reader
	if object.compressed {
		if err := decoder.Reset(reader); err != nil {
			return fmt.Errorf("resetting decoder: %w", err)
		}
		// Release the reference to the reader once read, the decoder outlives this file.
		defer decoder.Reset(nil)
		source = decoder
	}

	scan, err := scanMergedBlocksFile(source, scratch)
	if err != nil {
		return err
	}

	stats.compressed.Add(attrs.Size)
	stats.uncompressed.Add(scan.dataSize)
	stats.blocks.Add(scan.blockCount)

	metadata := make(map[string]string, len(attrs.Metadata)+3)
	maps.Copy(metadata, attrs.Metadata)
	maps.Copy(metadata, firecore.MergedBlocksMetadata(scan.dataSize, scan.blockCount, scan.firstBlockTime))

	conditions := storage.Conditions{GenerationMatch: attrs.Generation, MetagenerationMatch: attrs.Metageneration}
	if _, err := bucket.Object(attrs.Name).If(conditions).Update(ctx, storage.ObjectAttrsToUpdate{Metadata: metadata}); err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}
	return nil
}

type mergedBlocksScan struct {
	// dataSize is the number of bytes read off the decompressed stream.
	dataSize       int64
	blockCount     int64
	firstBlockTime time.Time
}

// scanMergedBlocksFile walks the whole dbin stream without ever holding a block: each one is
// framed by its own length, so counting them costs the four length bytes and a read of the
// block into a scratch buffer that is reused from block to block and from file to file. Of the
// first block, only the head is looked at, far enough to reach its timestamp, which keeps a
// large block out of memory just as much as a small one.
func scanMergedBlocksFile(source io.Reader, scratch []byte) (mergedBlocksScan, error) {
	// dbin.Reader buffers nothing, it pulls exactly the bytes it consumes, so the raw stream
	// can be read on from where it stopped.
	header, err := dbin.NewReader(source).ReadHeader()
	if err != nil {
		return mergedBlocksScan{}, fmt.Errorf("reading dbin header: %w", err)
	}
	scan := mergedBlocksScan{dataSize: int64(len(header.RawBytes))}

	for {
		length, err := readMessageLength(source)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if scan.blockCount == 0 {
					return mergedBlocksScan{}, errors.New("file holds no block")
				}
				return scan, nil
			}
			return mergedBlocksScan{}, fmt.Errorf("reading length of block %d: %w", scan.blockCount, err)
		}

		if scan.blockCount == 0 {
			if scan.firstBlockTime, err = readBlockTimestamp(source, length, scratch); err != nil {
				return mergedBlocksScan{}, err
			}
		} else if err := discard(source, length, scratch); err != nil {
			return mergedBlocksScan{}, fmt.Errorf("reading block %d: %w", scan.blockCount, err)
		}

		scan.dataSize += messageFrameSize(length)
		scan.blockCount++
	}
}

// readMessageLength reads one dbin message's four big-endian length bytes, returning io.EOF and
// nothing else when the stream ends cleanly on a message boundary.
func readMessageLength(source io.Reader) (int64, error) {
	var lengthBytes [4]byte
	if _, err := io.ReadFull(source, lengthBytes[:]); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint32(lengthBytes[:])), nil
}

// readBlockTimestamp reads the head of the block, enough to reach its timestamp, then reads
// past the rest of it. Only the block's metadata fields are decoded, the chain-specific payload
// never being looked at, so this works on any chain without knowing the chain's block type.
func readBlockTimestamp(source io.Reader, length int64, scratch []byte) (time.Time, error) {
	head := int64(firecore.BlockTimestampPeekSize)
	if head > length {
		head = length
	}
	if _, err := io.ReadFull(source, scratch[:head]); err != nil {
		return time.Time{}, fmt.Errorf("reading first block: %w", err)
	}

	timestamp, err := firecore.ExtractBlockTimestamp(scratch[:head])
	if err != nil {
		return time.Time{}, fmt.Errorf("reading first block timestamp: %w", err)
	}

	if err := discard(source, length-head, scratch); err != nil {
		return time.Time{}, fmt.Errorf("reading first block: %w", err)
	}
	return timestamp, nil
}

// discard reads length bytes into the scratch buffer and drops them, so the stream advances
// without any of it being kept and without any allocation.
func discard(source io.Reader, length int64, scratch []byte) error {
	for length > 0 {
		chunk := int64(len(scratch))
		if chunk > length {
			chunk = length
		}
		read, err := io.ReadFull(source, scratch[:chunk])
		length -= int64(read)
		if err != nil {
			return err
		}
	}
	return nil
}

// messageFrameSize is what one dbin message takes on the stream: its four length bytes plus
// the message itself.
func messageFrameSize(messageSize int64) int64 { return 4 + messageSize }

func startAnnotateProgress(ctx context.Context, stats *annotateStats, started time.Time, logger *zap.Logger) (stop func()) {
	ticker := time.NewTicker(10 * time.Second)
	quit := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				annotated := stats.annotated.Load()
				logger.Info("annotating merged-blocks files",
					zap.Int64("listed", stats.listed.Load()),
					zap.Int64("annotated", annotated),
					zap.Int64("skipped", stats.skipped.Load()),
					zap.Int64("failed", stats.failed.Load()),
					zap.String("uncompressed", humanize.Bytes(uint64(stats.uncompressed.Load()))),
					zap.Float64("files_per_second", float64(annotated)/time.Since(started).Seconds()),
				)
			}
		}
	}()

	return func() {
		ticker.Stop()
		close(quit)
		<-done
	}
}
