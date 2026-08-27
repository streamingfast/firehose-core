package substreams

import (
	"context"
	"errors"
	"fmt"
	"math"
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

// fullKVFileRegex matches a full store snapshot name, `<exclusive end block>-<initial block>.kv`,
// as written by substreams-tier1 under a module's `states/` folder, with or without the
// compression suffix the state store appends.
var fullKVFileRegex = regexp.MustCompile(`^(\d{10})-(\d{10})\.kv(?:\.zst|\.gz)?$`)

func NewToolsPruneStatesCmd(logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prune-states <store-url> --keep-every <blocks> --truncate-below-block <block>",
		Short: "Delete intermediate store snapshots, keeping one every N blocks",
		Long: `Walks every module folder found under the given store URL and deletes the full store
snapshots (the '<end>-<start>.kv' files under 'states/'), keeping the first snapshot of
each --keep-every-aligned window. Windows start on multiples of --keep-every regardless of
the module's initial block (the first window is simply shorter), so a module snapshotting
at 103000, 203000, ... with --keep-every 100000 keeps one snapshot per 100000-block window
instead of losing everything to misalignment.

Only snapshots whose end block is at or below --truncate-below-block are thinned:
everything above that block is kept untouched, so requests in flight and the live head
keep their nearby resume points. The most recent snapshot of each module is always kept.
--minimum-age, if set, additionally spares any snapshot last modified more recently than
that (the modification time comes straight out of the listing, nothing is downloaded).

substreams-tier1 rebuilds a store from the last remaining snapshot before the requested
block, so pruning trades disk space for reprocessing time on requests that start in a
pruned range. Partial files are never touched, and a module folder carrying a
DO_NOT_PRUNE file at its root (next to the last_used markers) is never touched at all.

Module folders are discovered the same way 'purge' does: direct tier1 layouts
(<store-url>/<hash> and <store-url>/<tag>/<hash>) and shared network roots
(<store-url>/<network>/` + statesFolder + `/<tag>/<hash>) are all recognized, and only each
module's 'states/' prefix is then listed. The URL can also point at a single module
folder or directly at its 'states' folder, in which case the whole tree is walked.`,
		Example: `  firecore tools substreams prune-states gs://my-bucket/substreams-states --keep-every 100000 --truncate-below-block 12000000 --dry-run
  firecore tools substreams prune-states /data/states/mainnet/substreams-states/<hash> --keep-every 50000 --truncate-below-block 12000000 --minimum-age 3d`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var minimumAge time.Duration
			if raw := sflags.MustGetString(cmd, "minimum-age"); raw != "" && raw != "0" {
				var err error
				if minimumAge, err = parseRetentionDuration(raw); err != nil {
					return fmt.Errorf("invalid --minimum-age: %w", err)
				}
			}

			cfg := pruneConfig{
				keepEvery:          sflags.MustGetUint64(cmd, "keep-every"),
				truncateBelowBlock: sflags.MustGetUint64(cmd, "truncate-below-block"),
				minimumAge:         minimumAge,
				dryRun:             sflags.MustGetBool(cmd, "dry-run"),
				force:              sflags.MustGetBool(cmd, "force"),
				parallelism:        sflags.MustGetInt(cmd, "parallelism"),
				now:                time.Now(),
			}
			cmd.SilenceUsage = true
			return runPruneStates(cmd.Context(), args[0], cfg, logger)
		},
	}

	cmd.Flags().Uint64("keep-every", 0, "Keep the first snapshot of each window of this many blocks, windows aligned on multiples of it (required)")
	cmd.Flags().Uint64("truncate-below-block", 0, "Only thin snapshots whose end block is at or below this block, everything above is kept untouched (required)")
	cmd.Flags().String("minimum-age", "", "Only delete snapshots last modified longer than this ago, ex: '3d', '72h'. Unset deletes whatever the age")
	cmd.Flags().BoolP("dry-run", "n", false, "Only report what would be deleted")
	cmd.Flags().BoolP("force", "f", false, "Skip the confirmation prompt")
	cmd.Flags().Int("parallelism", 16, "Number of concurrent deletions")
	cmd.MarkFlagRequired("keep-every")
	cmd.MarkFlagRequired("truncate-below-block")

	return cmd
}

type pruneConfig struct {
	keepEvery          uint64
	truncateBelowBlock uint64
	// minimumAge additionally spares snapshots modified more recently than this; zero
	// disables the age check.
	minimumAge  time.Duration
	dryRun      bool
	force       bool
	parallelism int
	now         time.Time
}

type snapshotFile struct {
	name     string
	endBlock uint64
	modified time.Time
}

// moduleSnapshots is every full snapshot of one module folder, sorted by end block.
type moduleSnapshots struct {
	folder string
	files  []snapshotFile
}

func runPruneStates(ctx context.Context, storeURL string, cfg pruneConfig, logger *zap.Logger) error {
	if cfg.keepEvery == 0 {
		return errors.New("--keep-every must be greater than 0")
	}
	if cfg.truncateBelowBlock == 0 {
		return errors.New("--truncate-below-block must be greater than 0")
	}
	if cfg.parallelism < 1 {
		cfg.parallelism = 1
	}

	store, err := newPurgeStore(ctx, storeURL, cfg.parallelism, logger)
	if err != nil {
		return err
	}
	defer store.Close()

	fmt.Println(stylex.Title("Substreams Store Snapshots Pruning"))
	fmt.Println(stylex.Dim(stylex.Separator(80)))
	fmt.Println(stylex.Labelf("Store:                %s", storeURL))
	fmt.Println(stylex.Labelf("Keep every:           %d blocks", cfg.keepEvery))
	fmt.Println(stylex.Labelf("Truncate below block: %d", cfg.truncateBelowBlock))
	if cfg.minimumAge > 0 {
		fmt.Println(stylex.Labelf("Minimum age:          %s", cfg.minimumAge))
	}
	if cfg.dryRun {
		fmt.Println(stylex.Warn("Dry run: nothing will be deleted"))
	}
	fmt.Println()

	fmt.Print(stylex.Label("Listing snapshots... "))
	modules, protected, err := listModuleSnapshots(ctx, store, cfg.parallelism)
	if err != nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("listing snapshots: %w", err)
	}
	fmt.Println(stylex.Successf("✓ %d module folder(s)", len(modules)))
	if protected > 0 {
		fmt.Println(stylex.Notef("%d module folder(s) protected by DO_NOT_PRUNE", protected))
	}

	cutoff := cfg.now.Add(-cfg.minimumAge)

	var toDelete []string
	for _, module := range modules {
		condemned := snapshotsToPrune(module.files, cfg.keepEvery, cfg.truncateBelowBlock, cutoff)
		if len(condemned) == 0 {
			continue
		}
		fmt.Println(stylex.Valuef("%s: %d of %d snapshot(s)", module.folder, len(condemned), len(module.files)))
		for _, file := range condemned {
			logger.Debug("pruning snapshot", zap.String("file", file.name))
			toDelete = append(toDelete, file.name)
		}
	}

	if len(toDelete) == 0 {
		fmt.Println()
		fmt.Println(stylex.Note("Nothing to prune"))
		return nil
	}

	fmt.Println()
	if cfg.dryRun {
		fmt.Println(stylex.Notef("Would delete %d snapshot(s)", len(toDelete)))
		return nil
	}

	if !cfg.force {
		message := fmt.Sprintf("About to delete %d snapshot(s). Continue?", len(toDelete))
		if confirmed, _ := cli.PromptConfirm(message); !confirmed {
			fmt.Println(stylex.Note("Skipped by user"))
			return nil
		}
	}

	fmt.Print(stylex.Labelf("Deleting %d snapshot(s)... ", len(toDelete)))
	if err := deleteAll(ctx, store.store, toDelete, cfg.parallelism); err != nil {
		fmt.Println(stylex.Error("✗"))
		return err
	}
	fmt.Println(stylex.Success("✓"))
	return nil
}

// listModuleSnapshots discovers module folders the way the purge does — direct tier1
// layouts, cache tags and shared network/substreams-states/ roots alike — then lists only
// each module's 'states/' prefix, never enumerating the output files next to it. A URL
// pointing inside a single module folder (at the folder or at its 'states' one) exposes
// no module hash to discover, and the whole tree is walked instead.
func listModuleSnapshots(ctx context.Context, store *purgeStore, parallelism int) ([]moduleSnapshots, int, error) {
	folders, err := discoverModuleFolders(ctx, store)
	if err != nil {
		return nil, 0, err
	}
	if len(folders) == 0 {
		return walkAllSnapshots(ctx, store.store)
	}

	var mu sync.Mutex
	var protectedCount atomic.Int64
	byFolder := map[string]*moduleSnapshots{}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(parallelism)

	for _, folder := range folders {
		group.Go(func() error {
			protected, err := store.IsProtected(groupCtx, folder)
			if err != nil {
				return err
			}
			if protected {
				protectedCount.Add(1)
				return nil
			}

			statesPrefix := folder.prefix + "states/"
			var files []snapshotFile
			err = store.store.WalkAttributes(groupCtx, statesPrefix, func(entry dstore.ObjectEntry) error {
				if file, ok := parseFullKVFilename(entry.Name); ok {
					file.modified = entry.LastModified
					files = append(files, file)
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("walking %q: %w", statesPrefix, err)
			}
			if len(files) == 0 {
				return nil
			}

			mu.Lock()
			defer mu.Unlock()
			name := strings.TrimSuffix(statesPrefix, "/")
			byFolder[name] = &moduleSnapshots{folder: name, files: files}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, 0, err
	}

	return sortedModuleSnapshots(byFolder), int(protectedCount.Load()), nil
}

// discoverModuleFolders finds every module folder of every scope, the same discovery the
// purge runs: direct tier1 layouts, shared network roots and cache tags included.
func discoverModuleFolders(ctx context.Context, store *purgeStore) ([]moduleFolder, error) {
	scopes, err := store.Scopes(ctx, nil)
	if err != nil {
		return nil, err
	}

	var folders []moduleFolder
	for _, scope := range scopes {
		found, _, err := store.moduleFoldersAt(ctx, scope.prefix, scope.network)
		if err != nil {
			return nil, err
		}
		folders = append(folders, found...)
	}
	return folders, nil
}

// walkAllSnapshots walks the whole store and groups full snapshots by the folder holding
// them, for URLs pointing inside a single module folder. A DO_NOT_PRUNE marker seen during
// the walk protects the snapshots of its 'states' sibling folder.
func walkAllSnapshots(ctx context.Context, store dstore.Store) ([]moduleSnapshots, int, error) {
	byFolder := map[string]*moduleSnapshots{}
	protectedDirs := map[string]bool{}

	err := store.WalkAttributes(ctx, "", func(entry dstore.ObjectEntry) error {
		if strings.HasPrefix(path.Base(entry.Name), doNotPruneMarker) {
			protectedDirs[path.Dir(entry.Name)] = true
			return nil
		}
		file, ok := parseFullKVFilename(entry.Name)
		if !ok {
			return nil
		}
		file.modified = entry.LastModified
		folder := path.Dir(entry.Name)
		module := byFolder[folder]
		if module == nil {
			module = &moduleSnapshots{folder: folder}
			byFolder[folder] = module
		}
		module.files = append(module.files, file)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	protected := 0
	for folder := range byFolder {
		if protectedDirs[path.Dir(folder)] {
			delete(byFolder, folder)
			protected++
		}
	}

	return sortedModuleSnapshots(byFolder), protected, nil
}

func sortedModuleSnapshots(byFolder map[string]*moduleSnapshots) []moduleSnapshots {
	out := make([]moduleSnapshots, 0, len(byFolder))
	for _, module := range byFolder {
		sort.Slice(module.files, func(i, j int) bool { return module.files[i].endBlock < module.files[j].endBlock })
		out = append(out, *module)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].folder < out[j].folder })
	return out
}

func parseFullKVFilename(filename string) (snapshotFile, bool) {
	match := fullKVFileRegex.FindStringSubmatch(path.Base(filename))
	if match == nil {
		return snapshotFile{}, false
	}
	endBlock, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil {
		return snapshotFile{}, false
	}
	return snapshotFile{name: filename, endBlock: endBlock}, true
}

// snapshotsToPrune returns the files of one module to delete: all but the first snapshot
// of each keepEvery-aligned window ([N*keepEvery, (N+1)*keepEvery)), the most recent one,
// anything past truncateBelowBlock and anything modified at or after cutoff. Windows align
// on multiples of keepEvery whatever the module's initial block is, the first window
// simply covering fewer blocks, so a module snapshotting at 103000, 203000, ... with
// keepEvery=100000 keeps them all instead of requiring exact multiples. `files` must be
// sorted by end block.
func snapshotsToPrune(files []snapshotFile, keepEvery, truncateBelowBlock uint64, cutoff time.Time) []snapshotFile {
	if len(files) == 0 {
		return nil
	}

	var out []snapshotFile
	prevWindow := uint64(math.MaxUint64)
	for _, file := range files[:len(files)-1] {
		if file.endBlock > truncateBelowBlock {
			break // sorted, so everything from here on is past the bound
		}
		window := file.endBlock / keepEvery
		firstInWindow := window != prevWindow
		prevWindow = window
		if firstInWindow {
			continue
		}
		if !file.modified.Before(cutoff) {
			continue
		}
		out = append(out, file)
	}
	return out
}

func deleteAll(ctx context.Context, store dstore.Store, files []string, parallelism int) error {
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(parallelism)

	var mu sync.Mutex
	var failed []string
	for _, file := range files {
		group.Go(func() error {
			if err := store.DeleteObject(groupCtx, file); err != nil && !errors.Is(err, dstore.ErrNotFound) {
				if groupCtx.Err() != nil {
					return groupCtx.Err()
				}
				mu.Lock()
				failed = append(failed, fmt.Sprintf("%s: %s", file, err))
				mu.Unlock()
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d deletion(s) failed:\n  %s", len(failed), strings.Join(failed, "\n  "))
	}
	return nil
}
