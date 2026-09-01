package mergeblock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/storage"
	"github.com/dustin/go-humanize"
	"github.com/klauspost/compress/zstd"
	"github.com/spf13/cobra"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"go.uber.org/zap"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/api/option/internaloption"
)

// dataSizeMetadataKey is the custom object metadata entry this tool writes, holding the
// decimal number of bytes the merged-blocks file holds once decompressed.
const dataSizeMetadataKey = "datasize"

// mergedBlocksFileRE matches a merged-blocks object base name, '0000012300.dbin.zst', with
// the compression suffix optional: a plain '.dbin' file needs no read at all, its object
// size is already the uncompressed size.
var mergedBlocksFileRE = regexp.MustCompile(`^(\d{10})\.dbin(\.zst)?$`)

func NewToolsAnnotateMergedBlocksCmd(rootLog *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "annotate-merged-blocks <gs-store-url>",
		Short: "Write each merged-blocks file's uncompressed size into its 'datasize' object metadata",
		Long: cli.Dedent(`
			Reads every merged-blocks file under the store URL, streams it through a zstd
			decoder into a byte counter, and writes that count as the object's 'datasize'
			custom metadata entry. Nothing is buffered: the decompressed bytes are counted
			and dropped, so memory stays flat whatever the file size.

			This only works on Google Cloud Storage ('gs://') because it sets object metadata
			on files it does not rewrite.

			The listing already carries each object's existing metadata, so files that were
			annotated by a previous run are skipped without a single extra request: rerunning
			over a store that is mostly done costs one listing and nothing else. --overwrite
			recomputes them anyway.

			Every file is read and updated under both its generation and its metageneration,
			so a file rewritten by the merger while this runs is reported as failed rather
			than annotated with a size taken from the version that is gone.

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
	cmd.Flags().Bool("overwrite", false, "Recompute files that already carry a 'datasize' metadata entry")
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

	client, err := newGSClient(ctx, target, cfg, logger)
	if err != nil {
		return fmt.Errorf("creating storage client: %w", err)
	}
	defer client.Close()

	bucket := client.Bucket(target.bucket)
	if target.userProject != "" {
		bucket = bucket.UserProject(target.userProject)
	}
	prefix := target.prefix

	fmt.Println(stylex.Title("Merged Blocks Data Size Annotation"))
	fmt.Println(stylex.Dim(stylex.Separator(80)))
	fmt.Println(stylex.Labelf("Store:       %s", storeURL))
	fmt.Println(stylex.Labelf("Parallelism: %d", cfg.parallelism))
	if cfg.useGRPC {
		fmt.Println(stylex.Labelf("Transport:   gRPC (%d connection(s))", grpcConnectionPool(cfg)))
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

	objects := make(chan *storage.ObjectAttrs, cfg.parallelism*2)

	var workers sync.WaitGroup
	for range cfg.parallelism {
		workers.Add(1)
		go func() {
			defer workers.Done()
			annotateWorker(ctx, bucket, prefix, cfg, objects, stats, logger)
		}()
	}

	listErr := listMergedBlocks(ctx, bucket, prefix, cfg, stats, objects)
	close(objects)
	workers.Wait()
	stopProgress()

	elapsed := time.Since(started)
	annotatedLabel := "Annotated:  "
	if cfg.dryRun {
		annotatedLabel = "Would annotate:"
	}
	fmt.Println(stylex.Valuef("%s %d file(s)", annotatedLabel, stats.annotated.Load()))
	fmt.Println(stylex.Valuef("Skipped:     %d file(s) already carrying '%s'", stats.skipped.Load(), dataSizeMetadataKey))
	fmt.Println(stylex.Valuef("Read:        %s compressed, %s uncompressed", humanize.Bytes(uint64(stats.compressed.Load())), humanize.Bytes(uint64(stats.uncompressed.Load()))))
	fmt.Println(stylex.Valuef("Elapsed:     %s", elapsed.Round(time.Second)))

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

// listMergedBlocks feeds the workers, reading names, sizes, generations and existing metadata
// straight out of the listing so an already-annotated file costs nothing beyond its listing row.
func listMergedBlocks(ctx context.Context, bucket *storage.BucketHandle, prefix string, cfg annotateConfig, stats *annotateStats, objects chan<- *storage.ObjectAttrs) error {
	query := &storage.Query{Prefix: prefix, Delimiter: "/"}
	if cfg.startBlock > 0 {
		query.StartOffset = prefix + fmt.Sprintf("%010d", cfg.startBlock)
	}
	if cfg.stopBlock > 0 {
		query.EndOffset = prefix + fmt.Sprintf("%010d", cfg.stopBlock)
	}
	if err := query.SetAttrSelection([]string{"Name", "Size", "Generation", "Metageneration", "Metadata"}); err != nil {
		return fmt.Errorf("selecting listing attributes: %w", err)
	}

	it := bucket.Objects(ctx, query)
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return err
		}

		stats.listed.Add(1)
		select {
		case objects <- attrs:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func annotateWorker(ctx context.Context, bucket *storage.BucketHandle, prefix string, cfg annotateConfig, objects <-chan *storage.ObjectAttrs, stats *annotateStats, logger *zap.Logger) {
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

	for attrs := range objects {
		if ctx.Err() != nil {
			return
		}

		base := strings.TrimPrefix(attrs.Name, prefix)
		match := mergedBlocksFileRE.FindStringSubmatch(base)
		if match == nil {
			continue
		}
		if !cfg.overwrite {
			if _, found := attrs.Metadata[dataSizeMetadataKey]; found {
				stats.skipped.Add(1)
				continue
			}
		}
		if cfg.dryRun {
			stats.annotated.Add(1)
			continue
		}

		if err := annotateOne(ctx, bucket, attrs, match[2] == ".zst", decoder, stats); err != nil {
			if ctx.Err() != nil {
				return
			}
			stats.failed.Add(1)
			logger.Warn("cannot annotate merged-blocks file", zap.String("file", attrs.Name), zap.Error(err))
			continue
		}
		stats.annotated.Add(1)
	}
}

func annotateOne(ctx context.Context, bucket *storage.BucketHandle, attrs *storage.ObjectAttrs, compressed bool, decoder *zstd.Decoder, stats *annotateStats) error {
	dataSize := attrs.Size
	if compressed {
		size, err := uncompressedSize(ctx, bucket.Object(attrs.Name).Generation(attrs.Generation), decoder)
		if err != nil {
			return err
		}
		dataSize = size
		stats.compressed.Add(attrs.Size)
	}
	stats.uncompressed.Add(dataSize)

	metadata := make(map[string]string, len(attrs.Metadata)+1)
	maps.Copy(metadata, attrs.Metadata)
	metadata[dataSizeMetadataKey] = strconv.FormatInt(dataSize, 10)

	conditions := storage.Conditions{GenerationMatch: attrs.Generation, MetagenerationMatch: attrs.Metageneration}
	_, err := bucket.Object(attrs.Name).If(conditions).Update(ctx, storage.ObjectAttrsToUpdate{Metadata: metadata})
	if err != nil {
		return fmt.Errorf("writing metadata: %w", err)
	}
	return nil
}

// uncompressedSize streams the object through the decoder and counts what comes out. The
// decoded bytes go to io.Discard as they are produced, so a file of any size costs only the
// decoder's window.
func uncompressedSize(ctx context.Context, object *storage.ObjectHandle, decoder *zstd.Decoder) (int64, error) {
	reader, err := object.NewReader(ctx)
	if err != nil {
		return 0, fmt.Errorf("opening object: %w", err)
	}
	defer reader.Close()

	return countDecompressed(decoder, reader)
}

// countDecompressed decodes everything the reader holds and returns how many bytes came out,
// dropping them as they are produced.
func countDecompressed(decoder *zstd.Decoder, reader io.Reader) (int64, error) {
	if err := decoder.Reset(reader); err != nil {
		return 0, fmt.Errorf("resetting decoder: %w", err)
	}
	// Release the reference to the reader once counted, the decoder outlives this file.
	defer decoder.Reset(nil)

	size, err := io.Copy(io.Discard, decoder)
	if err != nil {
		return 0, fmt.Errorf("decompressing: %w", err)
	}
	return size, nil
}

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
				elapsed := time.Since(started)
				annotated := stats.annotated.Load()
				logger.Info("annotating merged-blocks files",
					zap.Int64("listed", stats.listed.Load()),
					zap.Int64("annotated", annotated),
					zap.Int64("skipped", stats.skipped.Load()),
					zap.Int64("failed", stats.failed.Load()),
					zap.String("uncompressed", humanize.Bytes(uint64(stats.uncompressed.Load()))),
					zap.Float64("files_per_second", float64(annotated)/elapsed.Seconds()),
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

func newGSClient(ctx context.Context, target gsTarget, cfg annotateConfig, logger *zap.Logger) (*storage.Client, error) {
	if !cfg.useGRPC {
		return storage.NewClient(ctx)
	}

	opts := []option.ClientOption{option.WithGRPCConnectionPool(grpcConnectionPool(cfg))}
	if target.userProject != "" {
		// DirectPath goes straight to the storage backend, which does not honour the
		// x-goog-user-project gRPC metadata header requester-pays billing needs. Turning it
		// off keeps gRPC while routing through the Google Front End, which does enforce it.
		opts = append(opts, internaloption.EnableDirectPath(false))
		logger.Warn("DirectPath disabled: it does not carry the requester-pays project, which this store url sets",
			zap.String("project", target.userProject),
		)
	}
	return storage.NewGRPCClient(ctx, opts...)
}

// grpcConnectionPool spreads the readers over several gRPC connections: a single HTTP/2
// connection multiplexes every stream over one TCP socket, which caps throughput well before
// a wide worker pool does.
func grpcConnectionPool(cfg annotateConfig) int {
	if cfg.connPool > 0 {
		return cfg.connPool
	}
	pool := (cfg.parallelism + 31) / 32
	if pool < 1 {
		return 1
	}
	if pool > 16 {
		return 16
	}
	return pool
}

// gsTarget is what the store URL says: where to look, plus the two query parameters dstore
// reads off a 'gs://' URL, so the same URL means the same thing here as it does anywhere else
// in the stack.
type gsTarget struct {
	bucket string
	prefix string
	// userProject bills reads to that project, for a requester-pays bucket ('?project=').
	userProject string
	// grpc is '?client_protocol=grpc', which selects the same transport as --grpc.
	grpc bool
}

func parseGSURL(storeURL string) (gsTarget, error) {
	parsed, err := url.Parse(storeURL)
	if err != nil {
		return gsTarget{}, fmt.Errorf("invalid store url %q: %w", storeURL, err)
	}
	if parsed.Scheme != "gs" {
		return gsTarget{}, fmt.Errorf("store url %q must be a Google Cloud Storage url (gs://bucket/path), this tool writes object metadata which only that backend supports", storeURL)
	}
	if parsed.Host == "" {
		return gsTarget{}, fmt.Errorf("store url %q has no bucket", storeURL)
	}

	prefix := strings.Trim(parsed.Path, "/")
	if prefix != "" {
		prefix += "/"
	}

	query := parsed.Query()
	return gsTarget{
		bucket:      parsed.Host,
		prefix:      prefix,
		userProject: query.Get("project"),
		grpc:        query.Get("client_protocol") == "grpc",
	}, nil
}
