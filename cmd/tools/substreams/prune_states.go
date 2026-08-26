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

	"github.com/spf13/cobra"
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
		Use:   "prune-states <store-url> --keep-every <blocks>",
		Short: "Delete intermediate store snapshots, keeping one every N blocks",
		Long: `Walks every module folder found under the given store URL and deletes the full store
snapshots (the '<end>-<start>.kv' files under 'states/'), keeping the first snapshot of
each --keep-every-aligned window. Windows start on multiples of --keep-every regardless of
the module's initial block (the first window is simply shorter), so a module snapshotting
at 103000, 203000, ... with --keep-every 100000 keeps one snapshot per 100000-block window
instead of losing everything to misalignment. The most recent snapshot of each module is
always kept, as is every snapshot within --keep-recent blocks of it, so requests in flight
and the live head keep their nearby resume points.

substreams-tier1 rebuilds a store from the last remaining snapshot before the requested
block, so pruning trades disk space for reprocessing time on requests that start in a
pruned range. Partial files are never touched.

Module folders are discovered the same way 'purge' does: direct tier1 layouts
(<url>/<hash> and <url>/<tag>/<hash>) and shared network roots
(<url>/<network>/` + statesFolder + `/<tag>/<hash>) are all recognized, and only each
module's 'states/' prefix is then listed. The URL can also point at a single module
folder or directly at its 'states' folder, in which case the whole tree is walked.`,
		Example: `  firecore tools substreams prune-states gs://my-bucket/substreams-states --keep-every 100000 --dry-run
  firecore tools substreams prune-states /data/states/mainnet/substreams-states/<hash> --keep-every 50000 --keep-recent 200000`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := pruneConfig{
				keepEvery:   sflags.MustGetUint64(cmd, "keep-every"),
				keepRecent:  sflags.MustGetUint64(cmd, "keep-recent"),
				dryRun:      sflags.MustGetBool(cmd, "dry-run"),
				parallelism: sflags.MustGetInt(cmd, "parallelism"),
			}
			return runPruneStates(cmd.Context(), args[0], cfg, logger)
		},
	}

	cmd.Flags().Uint64("keep-every", 0, "Keep the first snapshot of each window of this many blocks, windows aligned on multiples of it (required)")
	cmd.Flags().Uint64("keep-recent", 0, "Also keep every snapshot within this many blocks of a module's most recent one (defaults to --keep-every)")
	cmd.Flags().Bool("dry-run", false, "Only report what would be deleted")
	cmd.Flags().Int("parallelism", 16, "Number of concurrent deletions")
	cmd.MarkFlagRequired("keep-every")

	return cmd
}

type pruneConfig struct {
	keepEvery   uint64
	keepRecent  uint64
	dryRun      bool
	parallelism int
}

type snapshotFile struct {
	name     string
	endBlock uint64
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
	if cfg.keepRecent == 0 {
		cfg.keepRecent = cfg.keepEvery
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
	fmt.Println(stylex.Labelf("Store:       %s", storeURL))
	fmt.Println(stylex.Labelf("Keep every:  %d blocks", cfg.keepEvery))
	fmt.Println(stylex.Labelf("Keep recent: %d blocks", cfg.keepRecent))
	if cfg.dryRun {
		fmt.Println(stylex.Warn("Dry run: nothing will be deleted"))
	}
	fmt.Println()

	fmt.Print(stylex.Label("Listing snapshots... "))
	modules, err := listModuleSnapshots(ctx, store, cfg.parallelism)
	if err != nil {
		fmt.Println(stylex.Error("✗"))
		return fmt.Errorf("listing snapshots: %w", err)
	}
	fmt.Println(stylex.Successf("✓ %d module folder(s)", len(modules)))

	var toDelete []string
	for _, module := range modules {
		condemned := snapshotsToPrune(module.files, cfg.keepEvery, cfg.keepRecent)
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
func listModuleSnapshots(ctx context.Context, store *purgeStore, parallelism int) ([]moduleSnapshots, error) {
	folders, err := discoverModuleFolders(ctx, store)
	if err != nil {
		return nil, err
	}
	if len(folders) == 0 {
		return walkAllSnapshots(ctx, store.store)
	}

	var mu sync.Mutex
	byFolder := map[string]*moduleSnapshots{}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(parallelism)

	for _, folder := range folders {
		group.Go(func() error {
			statesPrefix := folder.prefix + "states/"
			var files []snapshotFile
			err := store.store.Walk(groupCtx, statesPrefix, func(filename string) error {
				if file, ok := parseFullKVFilename(filename); ok {
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
		return nil, err
	}

	return sortedModuleSnapshots(byFolder), nil
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
// them, for URLs pointing inside a single module folder.
func walkAllSnapshots(ctx context.Context, store dstore.Store) ([]moduleSnapshots, error) {
	byFolder := map[string]*moduleSnapshots{}

	err := store.Walk(ctx, "", func(filename string) error {
		file, ok := parseFullKVFilename(filename)
		if !ok {
			return nil
		}
		folder := path.Dir(filename)
		module := byFolder[folder]
		if module == nil {
			module = &moduleSnapshots{folder: folder}
			byFolder[folder] = module
		}
		module.files = append(module.files, file)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return sortedModuleSnapshots(byFolder), nil
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

// snapshotsToPrune returns the files of one module to delete: all but the first snapshot of
// each keepEvery-aligned window ([N*keepEvery, (N+1)*keepEvery)), the most recent one and
// any within keepRecent blocks of it. Windows align on multiples of keepEvery whatever the
// module's initial block is, the first window simply covering fewer blocks, so a module
// snapshotting at 103000, 203000, ... with keepEvery=100000 keeps them all instead of
// requiring exact multiples. `files` must be sorted by end block.
func snapshotsToPrune(files []snapshotFile, keepEvery, keepRecent uint64) []snapshotFile {
	if len(files) == 0 {
		return nil
	}
	latest := files[len(files)-1].endBlock
	var recentFloor uint64
	if latest > keepRecent {
		recentFloor = latest - keepRecent
	}

	var out []snapshotFile
	prevWindow := uint64(math.MaxUint64)
	for _, file := range files[:len(files)-1] {
		window := file.endBlock / keepEvery
		firstInWindow := window != prevWindow
		prevWindow = window
		if firstInWindow {
			continue
		}
		if file.endBlock >= recentFloor {
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
