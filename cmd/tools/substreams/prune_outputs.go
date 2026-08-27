package substreams

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// outputFileRegex matches an execution output file name, `<start block>-<exclusive end
// block>.output`, as written by substreams under a module's `outputs/` folder, with or
// without the compression suffix the store appends.
var outputFileRegex = regexp.MustCompile(`^(\d{10})-(\d{10})\.output(?:\.zst|\.gz)?$`)

// spkgBaseNames are the package files substreams-tier1 writes at the root of the output
// module's cache folder, and only there: an spkg marks a module that was requested
// directly, not an intermediate dependency. tier1 writes them as 'substreams.spkg' or
// 'substreams.partial.spkg' through a compressed store, which appends its own suffix.
var spkgBaseNames = map[string]bool{
	"substreams.spkg":         true,
	"substreams.partial.spkg": true,
}

func isSpkgObject(name string) bool {
	base := path.Base(name)
	base = strings.TrimSuffix(base, ".zst")
	base = strings.TrimSuffix(base, ".gz")
	return spkgBaseNames[base]
}

func NewToolsPruneOutputsCmd(logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune-outputs <store-url> --truncate-below-block <block> --minimum-age <age>",
		Short: "Delete module output files below a block and older than an age",
		Long: cli.Dedent(`
			Deletes execution output files (the '<start>-<end>.output' files under a module's
			'outputs/' folder) of every module found under the store URL. A file is deleted only
			when BOTH conditions hold: its block range ends at or below --truncate-below-block,
			AND its last-modified time (read straight out of the listing, nothing is downloaded)
			is older than --minimum-age. Nothing outside 'outputs/' folders is ever touched.

			The URL may point at a network folder (<store>/<network>), at its ` + statesFolder + `
			folder, at a cache tag under it (.../` + statesFolder + `/v1), or directly at a single
			module folder; cache tags are discovered the same way 'purge' does.

			A module folder carrying a package file ('substreams.spkg.zst' or
			'substreams.partial.spkg.zst') was requested directly as an output module, not as an
			intermediate dependency, and its outputs are skipped by default since they are what
			serves those requests. --output-module-minimum-age, if set, prunes them too, using
			that (typically longer) age instead of --minimum-age.

			A module folder carrying a DO_NOT_PRUNE file at its root (next to the last_used
			markers) is never touched at all.
		`),
		Example: cli.Dedent(`
			# Outputs fully below block 12345678 and untouched for 3 days, on eth-mainnet
			firecore tools substreams prune-outputs gs://example-bucket/substreams/eth-mainnet \
			  --truncate-below-block 12345678 --minimum-age 3d --dry-run

			# Also prune directly-queried output modules, with a longer safety age
			firecore tools substreams prune-outputs gs://example-bucket/substreams/eth-mainnet \
			  --truncate-below-block 12345678 --minimum-age 3d \
			  --output-module-minimum-age 30d
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			minimumAge, err := parseRetentionDuration(sflags.MustGetString(cmd, "minimum-age"))
			if err != nil {
				return fmt.Errorf("invalid --minimum-age: %w", err)
			}

			var outputModuleMinimumAge time.Duration
			if raw := sflags.MustGetString(cmd, "output-module-minimum-age"); raw != "" && raw != "0" {
				if outputModuleMinimumAge, err = parseRetentionDuration(raw); err != nil {
					return fmt.Errorf("invalid --output-module-minimum-age: %w", err)
				}
			}

			cfg := pruneOutputsConfig{
				truncateBelowBlock:     sflags.MustGetUint64(cmd, "truncate-below-block"),
				minimumAge:             minimumAge,
				outputModuleMinimumAge: outputModuleMinimumAge,
				dryRun:                 sflags.MustGetBool(cmd, "dry-run"),
				force:                  sflags.MustGetBool(cmd, "force"),
				parallelism:            sflags.MustGetInt(cmd, "parallelism"),
				now:                    time.Now(),
			}
			cmd.SilenceUsage = true
			return runPruneOutputs(cmd.Context(), args[0], cfg, logger)
		},
	}

	cmd.Flags().Uint64("truncate-below-block", 0, "Only delete output files whose block range ends at or below this block (required)")
	cmd.Flags().String("minimum-age", "", "Only delete output files last modified longer than this ago, ex: '3d', '72h' (required)")
	cmd.Flags().String("output-module-minimum-age", "", "If set, also prune modules carrying an spkg (directly-queried output modules), using this age instead of --minimum-age. Unset or '0' keeps them untouched")
	cmd.Flags().BoolP("dry-run", "n", false, "Only report what would be deleted")
	cmd.Flags().BoolP("force", "f", false, "Skip the confirmation prompt")
	cmd.Flags().Int("parallelism", 64, "Number of concurrent listing and deletion operations")
	cmd.MarkFlagRequired("truncate-below-block")
	cmd.MarkFlagRequired("minimum-age")

	return cmd
}

type pruneOutputsConfig struct {
	truncateBelowBlock uint64
	minimumAge         time.Duration
	// outputModuleMinimumAge replaces minimumAge on modules carrying an spkg; zero keeps
	// those modules untouched entirely.
	outputModuleMinimumAge time.Duration
	dryRun                 bool
	force                  bool
	parallelism            int
	now                    time.Time
}

type outputFile struct {
	name     string
	size     int64
	endBlock uint64
	modified time.Time
}

// prunedModule is one module folder with output files to delete.
type prunedModule struct {
	folder moduleFolder
	files  []outputFile
	total  int
}

func runPruneOutputs(ctx context.Context, storeURL string, cfg pruneOutputsConfig, logger *zap.Logger) error {
	if cfg.parallelism < 1 {
		cfg.parallelism = 1
	}

	store, err := newPurgeStore(ctx, storeURL, cfg.parallelism, logger)
	if err != nil {
		return err
	}
	defer store.Close()

	fmt.Println(stylex.Title("Substreams Outputs Pruning"))
	fmt.Println(stylex.Dim(stylex.Separator(80)))
	fmt.Println(stylex.Labelf("Store:                %s", storeURL))
	fmt.Println(stylex.Labelf("Truncate below block: %d", cfg.truncateBelowBlock))
	fmt.Println(stylex.Labelf("Minimum age:          %s", cfg.minimumAge))
	if cfg.outputModuleMinimumAge > 0 {
		fmt.Println(stylex.Labelf("Output modules:       pruned when older than %s", cfg.outputModuleMinimumAge))
	} else {
		fmt.Println(stylex.Label("Output modules:       kept (folders carrying an spkg)"))
	}
	if cfg.dryRun {
		fmt.Println(stylex.Warn("Dry run: nothing will be deleted"))
	}
	fmt.Println()

	fmt.Print(stylex.Label("Discovering module folders... "))
	folders, skippedListings, err := store.DiscoverModuleFolders(ctx)
	if err != nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("discovering module folders: %w", err)
	}
	fmt.Println(stylex.Successf("✓ %d module folder(s)", len(folders)))
	if skippedListings > 0 {
		fmt.Println(stylex.Warnf("%d cache tag(s) could not be listed and were skipped", skippedListings))
	}
	if len(folders) == 0 {
		fmt.Println(stylex.Note("Nothing to prune"))
		return nil
	}

	modules, outputModulesKept, protected, scanErrors := scanOutputFolders(ctx, store, cfg, folders, logger)

	var toDelete []string
	var totalBytes int64
	for _, module := range modules {
		var bytes int64
		for _, file := range module.files {
			logger.Debug("pruning output file", zap.String("file", file.name))
			toDelete = append(toDelete, file.name)
			bytes += file.size
		}
		totalBytes += bytes
		fmt.Println(stylex.Valuef("%s: %d of %d output file(s) (%s)", module.folder.String(), len(module.files), module.total, formatBytes(bytes)))
	}

	fmt.Println()
	if outputModulesKept > 0 {
		fmt.Println(stylex.Notef("%d output module(s) (spkg present) kept untouched", outputModulesKept))
	}
	if protected > 0 {
		fmt.Println(stylex.Notef("%d module folder(s) protected by DO_NOT_PRUNE", protected))
	}
	if scanErrors > 0 {
		fmt.Println(stylex.Warnf("%d module folder(s) could not be scanned and were kept", scanErrors))
	}

	if len(toDelete) == 0 {
		fmt.Println(stylex.Note("Nothing to prune"))
		return scanErrorsErr(scanErrors)
	}

	if cfg.dryRun {
		fmt.Println(stylex.Notef("Would delete %d output file(s) (%s) from %d module folder(s)", len(toDelete), formatBytes(totalBytes), len(modules)))
		return scanErrorsErr(scanErrors)
	}

	if !cfg.force {
		message := fmt.Sprintf("About to delete %d output file(s) (%s) from %d module folder(s). Continue?", len(toDelete), formatBytes(totalBytes), len(modules))
		if confirmed, _ := cli.PromptConfirm(message); !confirmed {
			fmt.Println(stylex.Note("Skipped by user"))
			return nil
		}
	}

	fmt.Print(stylex.Labelf("Deleting %d output file(s) (%s)... ", len(toDelete), formatBytes(totalBytes)))
	if err := deleteAll(ctx, store.store, toDelete, cfg.parallelism); err != nil {
		fmt.Println(stylex.Error("✗"))
		return err
	}
	fmt.Println(stylex.Success("✓"))
	return scanErrorsErr(scanErrors)
}

func scanErrorsErr(scanErrors int) error {
	if scanErrors > 0 {
		return fmt.Errorf("%d module folder(s) could not be scanned", scanErrors)
	}
	return nil
}

// scanOutputFolders lists each module's spkg marker and outputs, returning the modules
// with files to delete, sorted by folder. A folder that cannot be scanned is kept and
// counted: incomplete information must never condemn a file.
func scanOutputFolders(ctx context.Context, store *purgeStore, cfg pruneOutputsConfig, folders []moduleFolder, logger *zap.Logger) (modules []prunedModule, outputModulesKept, protected, scanErrors int) {
	var mu sync.Mutex
	var keptCount, protectedCount, errorCount atomic.Int64

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(cfg.parallelism)

	for _, folder := range folders {
		group.Go(func() error {
			isProtected, err := store.IsProtected(groupCtx, folder)
			if err != nil {
				if groupCtx.Err() != nil {
					return err
				}
				errorCount.Add(1)
				logger.Warn("keeping folder whose DO_NOT_PRUNE marker could not be checked", zap.String("folder", folder.String()), zap.Error(err))
				return nil
			}
			if isProtected {
				protectedCount.Add(1)
				logger.Debug("folder protected by DO_NOT_PRUNE", zap.String("folder", folder.String()))
				return nil
			}

			hasSpkg, err := store.HasSpkg(groupCtx, folder)
			if err != nil {
				if groupCtx.Err() != nil {
					return err
				}
				errorCount.Add(1)
				logger.Warn("keeping folder whose spkg marker could not be checked", zap.String("folder", folder.String()), zap.Error(err))
				return nil
			}

			minimumAge := cfg.minimumAge
			if hasSpkg {
				if cfg.outputModuleMinimumAge == 0 {
					keptCount.Add(1)
					logger.Debug("keeping output module (spkg present)", zap.String("folder", folder.String()))
					return nil
				}
				minimumAge = cfg.outputModuleMinimumAge
			}

			var files []outputFile
			err = store.WalkOutputs(groupCtx, folder, func(entry dstore.ObjectEntry) error {
				file, ok := parseOutputFilename(entry.Name)
				if !ok {
					return nil
				}
				file.size = entry.Size
				file.modified = entry.LastModified
				files = append(files, file)
				return nil
			})
			if err != nil {
				if groupCtx.Err() != nil {
					return err
				}
				errorCount.Add(1)
				logger.Warn("keeping folder whose outputs could not be listed", zap.String("folder", folder.String()), zap.Error(err))
				return nil
			}

			condemned := outputsToPrune(files, cfg.truncateBelowBlock, cfg.now.Add(-minimumAge))
			if len(condemned) == 0 {
				return nil
			}

			mu.Lock()
			defer mu.Unlock()
			modules = append(modules, prunedModule{folder: folder, files: condemned, total: len(files)})
			return nil
		})
	}

	// Only a cancelled context can make a scan goroutine fail; the deletion phase then
	// fails on that same context.
	_ = group.Wait()

	sort.Slice(modules, func(i, j int) bool { return modules[i].folder.prefix < modules[j].folder.prefix })
	return modules, int(keptCount.Load()), int(protectedCount.Load()), int(errorCount.Load())
}

// outputsToPrune keeps a file as soon as its range reaches past truncateBelowBlock or it
// was modified at or after cutoff: both conditions must condemn it.
func outputsToPrune(files []outputFile, truncateBelowBlock uint64, cutoff time.Time) []outputFile {
	var out []outputFile
	for _, file := range files {
		if file.endBlock > truncateBelowBlock {
			continue
		}
		if !file.modified.Before(cutoff) {
			continue
		}
		out = append(out, file)
	}
	return out
}

func parseOutputFilename(filename string) (outputFile, bool) {
	match := outputFileRegex.FindStringSubmatch(path.Base(filename))
	if match == nil {
		return outputFile{}, false
	}
	endBlock, err := strconv.ParseUint(match[2], 10, 64)
	if err != nil {
		return outputFile{}, false
	}
	return outputFile{name: filename, endBlock: endBlock}, true
}
