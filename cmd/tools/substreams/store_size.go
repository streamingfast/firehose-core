package substreams

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/substreams/manifest"
	pbsubstreams "github.com/streamingfast/substreams/pb/sf/substreams/v1"
	"go.uber.org/zap"
)

var (
	// Styling for output - colors only enabled if terminal is TTY
	isTTY         = isatty.IsTerminal(os.Stdout.Fd())
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	headerStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	labelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	valueStyle    = lipgloss.NewStyle().Bold(true)
	successStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	noteStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Italic(true)
	separatorChar = "─"
)

func init() {
	// Disable styling if not a TTY
	if !isTTY {
		titleStyle = lipgloss.NewStyle()
		headerStyle = lipgloss.NewStyle()
		labelStyle = lipgloss.NewStyle()
		valueStyle = lipgloss.NewStyle()
		successStyle = lipgloss.NewStyle()
		errorStyle = lipgloss.NewStyle()
		dimStyle = lipgloss.NewStyle()
		noteStyle = lipgloss.NewStyle()
	}
}

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
	// Print header
	fmt.Println(titleStyle.Render("Substreams Store Size Analysis"))
	fmt.Println(dimStyle.Render(strings.Repeat(separatorChar, 80)))
	fmt.Println()

	// Read manifest
	fmt.Print(labelStyle.Render("Reading manifest... "))
	manifestReader, err := manifest.NewReader(manifestPath)
	if err != nil {
		fmt.Println(errorStyle.Render("✗"))
		return fmt.Errorf("creating manifest reader: %w", err)
	}

	pkgBundle, err := manifestReader.Read()
	if err != nil {
		fmt.Println(errorStyle.Render("✗"))
		return fmt.Errorf("reading manifest %q: %w", manifestPath, err)
	}

	if pkgBundle == nil {
		fmt.Println(errorStyle.Render("✗"))
		return fmt.Errorf("no package found in manifest")
	}
	fmt.Println(successStyle.Render("✓"))

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
		fmt.Println(noteStyle.Render("No store modules found in package"))
		return nil
	}

	fmt.Println(labelStyle.Render(fmt.Sprintf("Found %d store module(s)", storeModules)))
	fmt.Println(labelStyle.Render(fmt.Sprintf("State store: %s", stateStore)))
	fmt.Println()
	fmt.Println(labelStyle.Render("Analyzing stores..."))

	// Initialize store size querier
	querier, err := NewStoreSizeQuerier(stateStore)
	if err != nil {
		return fmt.Errorf("initializing store size querier: %w", err)
	}

	// Process stores in parallel using worker pool
	results := processStoresInParallel(ctx, pkg, pkgBundle, querier, logger)

	// Sort results alphabetically by module name
	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})

	// Display results
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

	fmt.Println(headerStyle.Render("Store Sizes:"))
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
		headerStyle.Render(fmt.Sprintf("%-*s", maxNameLen, "Module Name")),
		headerStyle.Render("Live (uncompressed)"),
	)
	fmt.Printf("  %s  %s\n",
		dimStyle.Render(strings.Repeat(separatorChar, maxNameLen)),
		dimStyle.Render(strings.Repeat(separatorChar, 19)),
	)

	// Display each store
	for _, result := range results {
		if result.Err != nil {
			errorCount++
			fmt.Printf("  %s  %s\n",
				errorStyle.Render(fmt.Sprintf("%-*s", maxNameLen, result.Name)),
				errorStyle.Render(fmt.Sprintf("Error: %v", result.Err)),
			)
			continue
		}

		liveStr := dimStyle.Render(fmt.Sprintf("%19s", "N/A"))
		if result.Sizes.LiveUncompressed != nil {
			hasAnyUncompressed = true
			totalUncompressed += *result.Sizes.LiveUncompressed
			liveStr = valueStyle.Render(fmt.Sprintf("%19s", formatBytes(*result.Sizes.LiveUncompressed)))
		}

		fmt.Printf("  %s  %s\n",
			valueStyle.Render(fmt.Sprintf("%-*s", maxNameLen, result.Name)),
			liveStr,
		)
	}

	// Display summary
	fmt.Println()
	fmt.Println(dimStyle.Render(strings.Repeat(separatorChar, maxNameLen+24)))
	fmt.Println(headerStyle.Render("Summary:"))

	if hasAnyUncompressed {
		fmt.Printf("  %s %s\n",
			labelStyle.Render("Total Live (uncompressed):"),
			valueStyle.Render(formatBytes(totalUncompressed)),
		)
	}
	fmt.Printf("  %s %s\n",
		labelStyle.Render("Stores Analyzed:          "),
		valueStyle.Render(fmt.Sprintf("%d", len(results)-errorCount)),
	)
	if errorCount > 0 {
		fmt.Printf("  %s %s\n",
			labelStyle.Render("Errors:                   "),
			errorStyle.Render(fmt.Sprintf("%d", errorCount)),
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
