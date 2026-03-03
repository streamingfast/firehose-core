package substreams

import (
	"context"
	"encoding/hex"
	"fmt"
	"runtime"
	"sort"
	"sync"

	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"github.com/streamingfast/substreams/manifest"
	pbsubstreams "github.com/streamingfast/substreams/pb/sf/substreams/v1"
	"go.uber.org/zap"
)

func NewToolsStoreSizeCmd(logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store-size <spkg> --state-store <store-url>",
		Short: "Calculate and report the total size of all stores in a Substreams package",
		Long: `Reads a Substreams package manifest and calculates the on-disk/on-cloud size
of each store module in the package. Supports local file systems (file:// or direct paths)
and Google Cloud Storage (gs://).`,
		Example: `  firecore tools substreams store-size my-package.spkg --state-store gs://my-bucket/substreams-states
  firecore tools substreams store-size substreams.yaml --state-store /data/states`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStoreSize(cmd.Context(), args[0], sflags.MustGetString(cmd, "state-store"), logger)
		},
	}

	cmd.Flags().String("state-store", "", "State store URL (required, supports file:// and gs://)")
	cmd.MarkFlagRequired("state-store")

	return cmd
}

type storeModuleInfo struct {
	Name         string
	Hash         string
	InitialBlock uint64
	Sizes        *StoreSizes
	Err          error
}

func runStoreSize(ctx context.Context, manifestPath string, stateStore string, logger *zap.Logger) error {
	fmt.Println(stylex.Title("Substreams Store Size Analysis"))
	fmt.Println(stylex.Dim(stylex.Separator(80)))
	fmt.Println()

	fmt.Print(stylex.Label("Reading manifest... "))
	manifestReader, err := manifest.NewReader(manifestPath)
	if err != nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("creating manifest reader: %w", err)
	}

	pkgBundle, err := manifestReader.Read()
	if err != nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("reading manifest %q: %w", manifestPath, err)
	}

	if pkgBundle == nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("no package found in manifest")
	}
	fmt.Println(stylex.Success("✓"))

	pkg := pkgBundle.Package

	// Count store modules
	storeModules := 0
	for _, module := range pkg.Modules.Modules {
		if module.GetKindStore() != nil {
			storeModules++
		}
	}

	if storeModules == 0 {
		fmt.Println()
		fmt.Println(stylex.Note("No store modules found in package"))
		return nil
	}

	fmt.Println(stylex.Labelf("Found %d store module(s)", storeModules))
	fmt.Println(stylex.Labelf("State store: %s", stateStore))
	fmt.Println()
	fmt.Println(stylex.Label("Analyzing stores..."))

	querier, err := NewStoreSizeQuerier(stateStore)
	if err != nil {
		return fmt.Errorf("initializing store size querier: %w", err)
	}

	results := processStoresInParallel(ctx, pkg, pkgBundle, querier, logger)

	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})

	displayResults(results)

	return nil
}

func processStoresInParallel(
	ctx context.Context,
	pkg *pbsubstreams.Package,
	pkgBundle *manifest.PackageBundle,
	querier StoreSizeQuerier,
	logger *zap.Logger,
) []storeModuleInfo {
	// Use GOMAXPROCS as worker pool size
	numWorkers := runtime.GOMAXPROCS(0)

	// Create channels for work distribution
	jobs := make(chan *pbsubstreams.Module, len(pkg.Modules.Modules))
	results := make(chan storeModuleInfo, len(pkg.Modules.Modules))

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go worker(ctx, jobs, results, pkg, pkgBundle, querier, logger, &wg)
	}

	// Send jobs
	for _, module := range pkg.Modules.Modules {
		jobs <- module
	}
	close(jobs)

	// Wait for workers and close results
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var allResults []storeModuleInfo
	for result := range results {
		allResults = append(allResults, result)
	}

	return allResults
}

func worker(
	ctx context.Context,
	jobs <-chan *pbsubstreams.Module,
	results chan<- storeModuleInfo,
	pkg *pbsubstreams.Package,
	pkgBundle *manifest.PackageBundle,
	querier StoreSizeQuerier,
	logger *zap.Logger,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	hashes := manifest.NewModuleHashes()

	for module := range jobs {
		// Skip non-store modules
		if module.GetKindStore() == nil {
			continue
		}

		// Calculate module hash
		moduleHash, err := hashes.HashModule(pkg.Modules, module, pkgBundle.Graph)
		if err != nil {
			results <- storeModuleInfo{
				Name: module.Name,
				Err:  fmt.Errorf("hashing module: %w", err),
			}
			continue
		}

		hashStr := hex.EncodeToString(moduleHash)

		sizes, err := querier.GetStoreSizes(ctx, hashStr)
		results <- storeModuleInfo{
			Name:         module.Name,
			Hash:         hashStr,
			InitialBlock: module.InitialBlock,
			Sizes:        sizes,
			Err:          err,
		}
	}
}

func displayResults(results []storeModuleInfo) {
	if len(results) == 0 {
		return
	}

	fmt.Println(stylex.Headerf("Store Sizes:"))
	fmt.Println()

	var totalUncompressed int64
	var errorCount int
	var hasAnyUncompressed bool

	// Find max lengths for column alignment
	maxNameLen := 0
	for _, r := range results {
		if len(r.Name) > maxNameLen {
			maxNameLen = len(r.Name)
		}
	}
	if maxNameLen < 12 { // Minimum for "Module Name" header
		maxNameLen = 12
	}

	// Print table header (simplified - only Live size)
	fmt.Printf("  %s  %s\n",
		stylex.Headerf("%-*s", maxNameLen, "Module Name"),
		stylex.Header("Live (uncompressed)"),
	)
	fmt.Printf("  %s  %s\n",
		stylex.Dim(stylex.Separator(maxNameLen)),
		stylex.Dim(stylex.Separator(19)),
	)

	// Display each store
	for _, result := range results {
		if result.Err != nil {
			errorCount++
			fmt.Printf("  %s  %s\n",
				stylex.Errorf("%-*s", maxNameLen, result.Name),
				stylex.Errorf("Error: %v", result.Err),
			)
			continue
		}

		liveStr := stylex.Dimf("%19s", "N/A")
		if result.Sizes.LiveUncompressed != nil {
			hasAnyUncompressed = true
			totalUncompressed += *result.Sizes.LiveUncompressed
			liveStr = stylex.Valuef("%19s", formatBytes(*result.Sizes.LiveUncompressed))
		}

		fmt.Printf("  %s  %s\n",
			stylex.Valuef("%-*s", maxNameLen, result.Name),
			liveStr,
		)
	}

	// Display summary
	fmt.Println()
	fmt.Println(stylex.Dim(stylex.Separator(maxNameLen + 24)))
	fmt.Println(stylex.Header("Summary:"))

	if hasAnyUncompressed {
		fmt.Printf("  %s %s\n",
			stylex.Label("Total Live (uncompressed):"),
			stylex.Value(formatBytes(totalUncompressed)),
		)
	}
	fmt.Printf("  %s %s\n",
		stylex.Label("Stores Analyzed:          "),
		stylex.Value(fmt.Sprintf("%d", len(results)-errorCount)),
	)
	if errorCount > 0 {
		fmt.Printf("  %s %s\n",
			stylex.Label("Errors:                   "),
			stylex.Error(fmt.Sprintf("%d", errorCount)),
		)
	}
	fmt.Println()
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
