package mergeblock

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"go.uber.org/zap"
)

func NewToolsStatsMergedBlocksCmd(rootLog *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats-merged-blocks <gs-store-url>",
		Short: "Report size, block count and compression of a merged-blocks store, month by month",
		Long: cli.Dedent(`
			Reports on a range of a merged-blocks store: total compressed and uncompressed
			size, block count, compression ratio and bytes per block, broken down by month
			and totalled over the range.

			Nothing is downloaded. Every number comes from the 'datasize', 'itemcount' and
			'timestamp' metadata entries that 'firecore tools annotate-merged-blocks' wrote
			on each object, and those come back with the listing, so the whole report costs
			one listing however large the range is. Run the annotation first: for a file
			missing any of the three entries, only its compressed size is known. Its block
			count and uncompressed size are estimated from the annotated files in the range
			(same blocks per file, same compression ratio), and it belongs to no month. Such
			files get a row of their own and are included in the total.

			The month a file counts towards is the month of its first block, so a file
			straddling a month boundary counts entirely towards the month it starts in.

			This only works on Google Cloud Storage ('gs://'), where the annotation lives.

			The report is labelled with a chain name, which defaults to the second-to-last
			part of the store path, the bucket excluded ('eth-mainnet' for both
			'gs://mybucket/something/eth-mainnet/v1' and 'gs://mybucket/eth-mainnet').
			Set '--chain-name' when that guess is wrong.

			Use '--json' to get the same report as a single JSON object on stdout instead
			of the table.
		`),
		Example: cli.Dedent(`
			# Whole store
			firecore tools stats-merged-blocks gs://example-bucket/eth-mainnet/merged

			# One range
			firecore tools stats-merged-blocks gs://example-bucket/eth-mainnet/merged \
			  --start-block 15000000 --stop-block 16000000

			# Machine-readable
			firecore tools stats-merged-blocks gs://example-bucket/eth-mainnet/merged --json
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := statsConfig{
				useGRPC:    sflags.MustGetBool(cmd, "grpc"),
				startBlock: sflags.MustGetUint64(cmd, "start-block"),
				stopBlock:  sflags.MustGetUint64(cmd, "stop-block"),
				asJSON:     sflags.MustGetBool(cmd, "json"),
				chainName:  sflags.MustGetString(cmd, "chain-name"),
			}
			if cfg.chainName == "" {
				cfg.chainName = defaultChainName(args[0])
			}
			cmd.SilenceUsage = true
			return runStatsMergedBlocks(cmd.Context(), args[0], cfg, rootLog)
		},
	}

	cmd.Flags().Bool("grpc", false, "Talk to Cloud Storage over its gRPC API instead of JSON/XML over HTTP")
	cmd.Flags().Uint64("start-block", 0, "Only count files whose first block is at or above this block")
	cmd.Flags().Uint64("stop-block", 0, "Only count files whose first block is below this block, 0 for no upper bound")
	cmd.Flags().String("chain-name", "", "Name the report is labelled with, defaults to the second-to-last part of the store path, the bucket excluded")
	cmd.Flags().Bool("json", false, "Print the report as a single JSON object instead of a table")

	return cmd
}

type statsConfig struct {
	useGRPC    bool
	startBlock uint64
	stopBlock  uint64
	asJSON     bool
	chainName  string
}

// defaultChainName guesses the chain a store holds from its url, taking the second-to-last
// element of the path: 'gs://mybucket/something/eth-mainnet/v1' is 'eth-mainnet'. A one-element
// path is that element, 'gs://mybucket/eth-mainnet' is 'eth-mainnet'. The bucket is never part
// of the guess, so a url with only a bucket returns "". Never looks at the store itself.
func defaultChainName(storeURL string) string {
	parsed, err := url.Parse(storeURL)
	if err != nil {
		return ""
	}

	var parts []string
	for _, part := range strings.Split(parsed.Path, "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return parts[len(parts)-2]
	}
}

// mergedBlocksTally accumulates one bucket of the report, a month or the whole range.
type mergedBlocksTally struct {
	files        int64
	blocks       int64
	compressed   int64
	uncompressed int64
	// firstBlock is the lowest first-block of the files in the bucket, meaningless while
	// files is 0.
	firstBlock uint64
}

func (t *mergedBlocksTally) add(other mergedBlocksTally) {
	if other.files > 0 && (t.files == 0 || other.firstBlock < t.firstBlock) {
		t.firstBlock = other.firstBlock
	}
	t.files += other.files
	t.blocks += other.blocks
	t.compressed += other.compressed
	t.uncompressed += other.uncompressed
}

// compressionRatio is how many uncompressed bytes each stored byte stands for.
func (t mergedBlocksTally) compressionRatio() float64 {
	if t.compressed == 0 {
		return 0
	}
	return float64(t.uncompressed) / float64(t.compressed)
}

func (t mergedBlocksTally) uncompressedPerBlock() float64 {
	if t.blocks == 0 {
		return 0
	}
	return float64(t.uncompressed) / float64(t.blocks)
}

func (t mergedBlocksTally) compressedPerBlock() float64 {
	if t.blocks == 0 {
		return 0
	}
	return float64(t.compressed) / float64(t.blocks)
}

// statsAccumulator gathers the report one listed file at a time.
type statsAccumulator struct {
	// months and annotated hold the annotated files only.
	months    map[string]*mergedBlocksTally
	annotated mergedBlocksTally
	// unannotated holds the files missing part of the annotation, of which only the file
	// count, compressed size and first block are known: see estimatedUnannotated.
	unannotated mergedBlocksTally
	// seen counts every file listed, annotated or not, and firstBlock/lastBlock are the
	// first blocks of the first and last of them.
	seen       uint64
	firstBlock uint64
	lastBlock  uint64
}

func newStatsAccumulator() *statsAccumulator {
	return &statsAccumulator{months: map[string]*mergedBlocksTally{}}
}

// add counts one listed file and returns false when it carries no complete annotation.
func (a *statsAccumulator) add(object mergedBlocksObject) bool {
	if a.seen == 0 {
		a.firstBlock = object.lowBlockNum
	}
	a.seen++
	a.lastBlock = object.lowBlockNum

	file, ok := readAnnotation(object)
	if !ok {
		a.unannotated.add(mergedBlocksTally{files: 1, compressed: object.attrs.Size, firstBlock: object.lowBlockNum})
		return false
	}

	month := a.months[file.month]
	if month == nil {
		month = &mergedBlocksTally{}
		a.months[file.month] = month
	}
	month.add(file.tally)
	a.annotated.add(file.tally)
	return true
}

// estimatedUnannotated is the unannotated files with their block count and uncompressed size
// estimated from the annotated files: the same blocks per file and the same compression ratio.
// Both stay 0 when no file is annotated, as there is nothing to estimate from.
func (a *statsAccumulator) estimatedUnannotated() mergedBlocksTally {
	estimate := a.unannotated
	if a.annotated.files > 0 {
		estimate.blocks = int64(math.Round(float64(a.annotated.blocks) * float64(estimate.files) / float64(a.annotated.files)))
	}
	estimate.uncompressed = int64(math.Round(a.annotated.compressionRatio() * float64(estimate.compressed)))
	return estimate
}

// total is every listed file, the unannotated ones at their estimate.
func (a *statsAccumulator) total() mergedBlocksTally {
	total := a.annotated
	total.add(a.estimatedUnannotated())
	return total
}

func runStatsMergedBlocks(ctx context.Context, storeURL string, cfg statsConfig, logger *zap.Logger) error {
	target, err := parseGSURL(storeURL)
	if err != nil {
		return err
	}
	cfg.useGRPC = cfg.useGRPC || target.grpc

	if cfg.stopBlock != 0 && cfg.stopBlock <= cfg.startBlock {
		return fmt.Errorf("--stop-block %d must be above --start-block %d", cfg.stopBlock, cfg.startBlock)
	}

	client, err := newGSClient(ctx, target, cfg.useGRPC, 1, logger)
	if err != nil {
		return fmt.Errorf("creating storage client: %w", err)
	}
	defer client.Close()

	if !cfg.asJSON {
		fmt.Println(stylex.Title("Merged Blocks Statistics"))
		fmt.Println(stylex.Dim(stylex.Separator(80)))
		if cfg.chainName != "" {
			fmt.Println(stylex.Labelf("Chain: %s", cfg.chainName))
		}
		fmt.Println(stylex.Labelf("Store: %s", storeURL))
		fmt.Println(stylex.Labelf("Range: %s", describeBlockRange(cfg.startBlock, cfg.stopBlock)))
		fmt.Println()
	}

	acc := newStatsAccumulator()
	started := time.Now()

	err = walkMergedBlocks(ctx, bucketHandle(client, target), target.prefix, cfg.startBlock, cfg.stopBlock,
		[]string{"Name", "Size", "Metadata"},
		func(object mergedBlocksObject) error {
			if !acc.add(object) {
				logger.Debug("file carries no complete annotation", zap.String("file", object.attrs.Name))
			}
			return nil
		},
	)
	if err != nil {
		return fmt.Errorf("listing %s: %w", storeURL, err)
	}

	if cfg.asJSON {
		report := newStatsReport(cfg, acc, time.Since(started))
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}

	if acc.seen == 0 {
		fmt.Println(stylex.Note("No merged-blocks file found in that range"))
		return nil
	}

	printMergedBlocksTable(acc.months, acc.estimatedUnannotated(), acc.total())

	fmt.Println()
	fmt.Println(stylex.Valuef("Blocks seen: %s to %s", humanize.Comma(int64(acc.firstBlock)), humanize.Comma(int64(acc.lastBlock))))
	fmt.Println(stylex.Valuef("Listed in:   %s", time.Since(started).Round(time.Millisecond)))
	if acc.unannotated.files > 0 {
		fmt.Println(stylex.Warnf("%d file(s) carry no complete annotation, their blocks and uncompressed size in the %s row and the total are estimated, run 'firecore tools annotate-merged-blocks' on them",
			acc.unannotated.files, unannotatedRowName))
	}
	return nil
}

// statsReport is the '--json' shape of the report. Sizes are in bytes, blocks and files are
// counts, and the per-block numbers are the same divisions the table shows.
type statsReport struct {
	ChainName      string  `json:"chain_name"`
	StartBlock     uint64  `json:"start_block"`
	StopBlock      uint64  `json:"stop_block"`
	FirstBlockSeen *uint64 `json:"first_block_seen"`
	LastBlockSeen  *uint64 `json:"last_block_seen"`
	// Months cover the annotated files only.
	Months []statsBucket `json:"months"`
	// Total covers every listed file, the unannotated ones at their estimate.
	Total statsBucket `json:"total"`
	// Unannotated is the part of Total from files missing part of the annotation. Its
	// files, compressed bytes and first block are exact, its blocks and uncompressed bytes
	// are estimated from the annotated files' blocks per file and compression ratio.
	Unannotated statsBucket `json:"unannotated"`
	// UnannotatedFiles is Unannotated.Files.
	UnannotatedFiles uint64  `json:"unannotated_files"`
	ListedInSeconds  float64 `json:"listed_in_seconds"`
}

type statsBucket struct {
	Month                     string  `json:"month,omitempty"`
	FirstBlock                uint64  `json:"first_block"`
	Files                     int64   `json:"files"`
	Blocks                    int64   `json:"blocks"`
	CompressedBytes           int64   `json:"compressed_bytes"`
	UncompressedBytes         int64   `json:"uncompressed_bytes"`
	CompressionRatio          float64 `json:"compression_ratio"`
	CompressedBytesPerBlock   float64 `json:"compressed_bytes_per_block"`
	UncompressedBytesPerBlock float64 `json:"uncompressed_bytes_per_block"`
}

func newStatsBucket(month string, tally mergedBlocksTally) statsBucket {
	return statsBucket{
		Month:                     month,
		FirstBlock:                tally.firstBlock,
		Files:                     tally.files,
		Blocks:                    tally.blocks,
		CompressedBytes:           tally.compressed,
		UncompressedBytes:         tally.uncompressed,
		CompressionRatio:          tally.compressionRatio(),
		CompressedBytesPerBlock:   tally.compressedPerBlock(),
		UncompressedBytesPerBlock: tally.uncompressedPerBlock(),
	}
}

func newStatsReport(cfg statsConfig, acc *statsAccumulator, listedIn time.Duration) statsReport {
	report := statsReport{
		ChainName:        cfg.chainName,
		StartBlock:       cfg.startBlock,
		StopBlock:        cfg.stopBlock,
		Months:           []statsBucket{},
		Total:            newStatsBucket("", acc.total()),
		Unannotated:      newStatsBucket("", acc.estimatedUnannotated()),
		UnannotatedFiles: uint64(acc.unannotated.files),
		ListedInSeconds:  listedIn.Seconds(),
	}
	if acc.seen > 0 {
		firstBlock, lastBlock := acc.firstBlock, acc.lastBlock
		report.FirstBlockSeen = &firstBlock
		report.LastBlockSeen = &lastBlock
	}

	names := make([]string, 0, len(acc.months))
	for name := range acc.months {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		report.Months = append(report.Months, newStatsBucket(name, *acc.months[name]))
	}

	return report
}

// annotatedFile is one merged-blocks file's contribution to the report.
type annotatedFile struct {
	month string
	tally mergedBlocksTally
}

// readAnnotation reads back what annotate-merged-blocks wrote. A file missing any of the three
// entries counts towards nothing: a partial annotation would silently understate every total.
func readAnnotation(object mergedBlocksObject) (annotatedFile, bool) {
	dataSize, err := strconv.ParseInt(object.attrs.Metadata[dataSizeMetadataKey], 10, 64)
	if err != nil {
		return annotatedFile{}, false
	}
	blocks, err := strconv.ParseInt(object.attrs.Metadata[itemCountMetadataKey], 10, 64)
	if err != nil {
		return annotatedFile{}, false
	}
	timestamp, err := time.Parse(timestampMetadataLayout, object.attrs.Metadata[timestampMetadataKey])
	if err != nil {
		return annotatedFile{}, false
	}

	return annotatedFile{
		month: timestamp.Format("2006-01"),
		tally: mergedBlocksTally{
			files:        1,
			blocks:       blocks,
			compressed:   object.attrs.Size,
			uncompressed: dataSize,
			firstBlock:   object.lowBlockNum,
		},
	}, true
}

// unannotatedRowName labels the table row of the files missing part of the annotation, the
// asterisk marking its estimated numbers.
const unannotatedRowName = "UNANNOTATED*"

func printMergedBlocksTable(months map[string]*mergedBlocksTally, unannotated, total mergedBlocksTally) {
	names := make([]string, 0, len(months))
	for name := range months {
		names = append(names, name)
	}
	sort.Strings(names)

	table := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(table, "MONTH\tFIRST_BLOCK\tFILES\tBLOCKS\tCOMPRESSED\tUNCOMPRESSED\tRATIO\tCOMP./BLOCK\tUNCOMP./BLOCK\t")
	for _, name := range names {
		printMergedBlocksRow(table, name, *months[name])
	}
	if unannotated.files > 0 {
		printMergedBlocksRow(table, unannotatedRowName, unannotated)
	}
	if len(names) > 1 || unannotated.files > 0 {
		fmt.Fprintln(table, "\t\t\t\t\t\t\t\t\t")
		printMergedBlocksRow(table, "TOTAL", total)
	}
	table.Flush()
}

func printMergedBlocksRow(table *tabwriter.Writer, name string, tally mergedBlocksTally) {
	fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%.2fx\t%s\t%s\t\n",
		name,
		humanize.Comma(int64(tally.firstBlock)),
		humanize.Comma(tally.files),
		humanize.Comma(tally.blocks),
		humanize.IBytes(uint64(tally.compressed)),
		humanize.IBytes(uint64(tally.uncompressed)),
		tally.compressionRatio(),
		humanize.IBytes(uint64(tally.compressedPerBlock())),
		humanize.IBytes(uint64(tally.uncompressedPerBlock())),
	)
}

func describeBlockRange(startBlock, stopBlock uint64) string {
	if startBlock == 0 && stopBlock == 0 {
		return "whole store"
	}
	if stopBlock == 0 {
		return fmt.Sprintf("%s and up", humanize.Comma(int64(startBlock)))
	}
	return fmt.Sprintf("%s to %s (exclusive)", humanize.Comma(int64(startBlock)), humanize.Comma(int64(stopBlock)))
}
