package substreams

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cloud.google.com/go/storage"
	"github.com/klauspost/compress/zstd"
	"github.com/spf13/cobra"
	"github.com/streamingfast/cli"
	"github.com/streamingfast/cli/sflags"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// substreams-tier1 writes a module's usage marker under this folder of every network.
const statesFolder = "substreams-states"

// last_used.zst holds the plain 'YYYY-MM-DD' of the last request served from that module
// folder, zstd-compressed by dstore. The plan-suffixed variants (last_used_pro.zst, ...)
// track the same thing per billing plan.
const lastUsedPrefix = "last_used"
const lastUsedDateLayout = "2006-01-02"
const defaultPlan = "default"

var moduleHashRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// maxMarkerSize caps what --read-marker-contents downloads: a marker holds a single date.
const maxMarkerSize = 4096

func NewToolsPurgeCmd(logger *zap.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "purge <state-url>",
		Short: "Delete substreams module caches that have not been used recently",
		Long: cli.Dedent(`
			Deletes the content of every module cache folder under <state-url> whose last recorded
			usage is older than the retention of the plan that used it. The command matches the
			exact tier1 layouts <state-url>/<module-hash>/ and
			<state-url>/<tag>/<module-hash>/, plus shared roots using
			<state-url>/<network>/` + statesFolder + `/<tag>/<module-hash>/.

			Usage comes from the 'last_used*.zst' markers substreams-tier1 refreshes on every
			request it serves: 'last_used.zst' belongs to the 'default' plan and
			'last_used_<plan>.zst' to <plan>. A module folder is KEPT as soon as ONE of its
			markers is within its own plan's retention, so a folder last used 15 days ago by a
			'pro' request survives even when the 'free' retention is 3 days. Plans with no
			explicit retention fall back to the 'default' one.

			A module folder carrying no marker at all is never touched unless
			--purge-without-last-used is set.

			Networks (or the exact state-store scope) are processed one at a time and fully
			isolated from each other: a scope that cannot be listed, or whose files refuse to be
			deleted, is reported and the run moves on to the next one. Within a scope, a folder
			that fails is skipped rather than aborting its siblings. The command exits non-zero
			if anything failed.

			With --daemon it keeps running, starting a new pass every --interval (24h by default)
			and rediscovering the scopes each time. A pass that fails is logged and retried at
			the next interval rather than killing the daemon, and --force is then mandatory since
			nobody is there to answer the confirmation prompt.

			The scan never downloads anything: substreams-tier1 overwrites a marker on every
			request it serves, so the object's last-write time IS the usage date and the listing
			already carries it.

			That equivalence only holds as long as nothing else has written the objects. Copying
			a bucket, migrating a store, restoring a backup, or rsyncing a local one without
			preserving timestamps resets every last-write time to the copy date, and the purge
			then believes every module was used the day of the copy and spares all of them.
			--read-marker-contents downloads each marker and reads the date tier1 stored inside
			it, which survives any copy. It applies to every backend, and costs one download per
			marker.

			The stored date is day-granular, so the object's last-write time stays the more
			precise of the two for a retention counted in hours.

			The scan lists the state-store root one folder level at a time, then asks each module
			folder for its 'last_used*' objects with a narrow prefix. Shared network roots using the
			network/` + statesFolder + `/ layout are detected automatically. The millions of
			state and output files under a scope are only ever enumerated once a folder is
			actually condemned, and a condemned folder is then emptied completely unless --keep
			says otherwise.

			Storage goes through dstore, so every store it supports works: 'gs://', 's3://',
			'az://', 'file://' or a bare local path. Names, sizes and modification times all come
			out of the listings dstore was already making, so the scan never pays a request per
			object for any of them.

			A folder listing is paged one round trip at a time however few folders come back, so
			on the stores that bound a listing service-side ('gs://', 's3://') each level is cut
			into slices listed concurrently, which is what keeps the scan fast on a network
			holding tens of thousands of module folders.
		`),
		Example: cli.Dedent(`
			# Everything unused for 30 days in an exact tier1 state store
			firecore tools substreams purge gs://example-bucket/substreams --dry-run

			# Everything unused for 30 days, on two networks in a shared network root
			firecore tools substreams purge gs://example-bucket/substreams \
			  --network eth-mainnet,sol-mainnet --dry-run

			# Per-plan retention over every network found under the root
			firecore tools substreams purge gs://example-bucket/substreams \
			  --retention default=30d,pro=30d,scaling=14d,free=3d

			# Report what is past retention without listing or deleting anything
			firecore tools substreams purge gs://example-bucket/substreams \
			  --network eth-mainnet --scan-only

			# Any other store dstore supports
			firecore tools substreams purge s3://a-bucket/substreams --network eth-mainnet --dry-run

			# Unattended, one pass every 12 hours
			firecore tools substreams purge gs://example-bucket/substreams \
			  --retention default=30d,free=3d --daemon --interval 12h --force
		`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPurge(cmd, args[0], logger)
		},
	}

	cmd.Flags().StringSlice("network", nil, "Networks to purge in a shared network root, as folder names directly under <state-url> (ex: 'eth-mainnet'). Empty means every network found; direct tier1 layouts are always scanned")
	cmd.Flags().StringSlice("retention", []string{defaultPlan + "=30d"}, "Per-plan retention as 'plan=duration' or 'plan:duration' (ex: 'default=30d,pro=30d,scaling=14d,free=3d'). A 'default' entry is required and covers every plan not listed")
	cmd.Flags().StringSlice("keep", nil, "Glob patterns, matched against the file name, that are never deleted from a condemned folder. Empty means a condemned folder is emptied completely")

	cmd.Flags().Bool("read-marker-contents", false, "Read the date stored inside every last_used marker instead of trusting the object's last-write time. Needed when the objects have been copied, migrated or rsynced, which resets that time. Costs one download per marker. WARNING: marker dates are day-granular; sub-day retention may purge early")
	cmd.Flags().Int("scan-workers", 256, "Number of parallel listing operations during the scan phase. The scan is latency-bound, not CPU-bound, so this scales close to linearly")
	cmd.Flags().Int("delete-workers", 250, "Number of parallel delete operations")
	cmd.Flags().BoolP("dry-run", "n", false, "List what would be deleted without deleting anything")
	cmd.Flags().Bool("scan-only", false, "Report which module folders are past their retention and stop there, without listing or deleting their content")
	cmd.Flags().BoolP("force", "f", false, "Skip the confirmation prompt, required by --daemon")
	cmd.Flags().Bool("daemon", false, "Keep running, starting a new pass every --interval instead of exiting after one")
	cmd.Flags().Duration("interval", 24*time.Hour, "Delay between the start of two passes in --daemon mode, a pass that lasts longer than this restarts immediately")
	cmd.Flags().Bool("purge-without-last-used", false, "Also purge module folders that carry no 'last_used*.zst' marker at all")
	cmd.Flags().String("deleted-files-log", "", "If set, every deleted file name is appended to this file")

	return cmd
}

type purgeConfig struct {
	networks  []string
	retention map[string]time.Duration
	keepGlobs []string

	readMarkerContents   bool
	scanWorkers          int
	deleteWorkers        int
	dryRun               bool
	scanOnly             bool
	force                bool
	daemon               bool
	interval             time.Duration
	purgeWithoutLastUsed bool
	deletedFilesLog      string
	now                  time.Time
}

// retentionFor returns the retention of a plan, falling back to the 'default' one for any
// plan the operator did not name explicitly.
func (c *purgeConfig) retentionFor(plan string) time.Duration {
	if d, found := c.retention[plan]; found {
		return d
	}
	return c.retention[defaultPlan]
}

func (c *purgeConfig) isKept(objectName string) bool {
	base := path.Base(objectName)
	for _, glob := range c.keepGlobs {
		if matched, _ := path.Match(glob, base); matched {
			return true
		}
	}
	return false
}

type moduleFolder struct {
	// prefix is the full object prefix of the folder, trailing slash included.
	prefix  string
	network string
	tag     string
	hash    string
}

func (m moduleFolder) String() string {
	if m.network == "" {
		if m.tag == "" {
			return m.hash
		}
		return fmt.Sprintf("%s/%s", m.tag, m.hash)
	}
	if m.tag == "" {
		return fmt.Sprintf("%s/%s", m.network, m.hash)
	}
	return fmt.Sprintf("%s/%s/%s", m.network, m.tag, m.hash)
}

type marker struct {
	plan string
	date time.Time
}

type condemned struct {
	folder  moduleFolder
	markers []marker
}

// networkResult is what a single network run reports back. Everything but fatalErr is a
// count of things that went wrong without stopping the run.
type networkResult struct {
	network       string
	folders       int
	kept          int
	unmarked      int
	scanErrors    int
	scanDuration  time.Duration
	toPurge       int
	skipped       bool
	purgedFolders int
	deletedFiles  int64
	deletedBytes  int64
	failedDeletes int64
	listErrors    int
	fatalErr      error
}

func (r *networkResult) failed() bool {
	return r.fatalErr != nil || r.scanErrors > 0 || r.listErrors > 0 || r.failedDeletes > 0
}

func runPurge(cmd *cobra.Command, storeURL string, logger *zap.Logger) error {
	ctx := cmd.Context()

	retention, err := parseRetention(sflags.MustGetStringSlice(cmd, "retention"))
	if err != nil {
		return err
	}

	cfg := &purgeConfig{
		networks:  sflags.MustGetStringSlice(cmd, "network"),
		retention: retention,
		keepGlobs: sflags.MustGetStringSlice(cmd, "keep"),

		readMarkerContents:   sflags.MustGetBool(cmd, "read-marker-contents"),
		scanWorkers:          sflags.MustGetInt(cmd, "scan-workers"),
		deleteWorkers:        sflags.MustGetInt(cmd, "delete-workers"),
		dryRun:               sflags.MustGetBool(cmd, "dry-run"),
		scanOnly:             sflags.MustGetBool(cmd, "scan-only"),
		force:                sflags.MustGetBool(cmd, "force"),
		daemon:               sflags.MustGetBool(cmd, "daemon"),
		interval:             sflags.MustGetDuration(cmd, "interval"),
		purgeWithoutLastUsed: sflags.MustGetBool(cmd, "purge-without-last-used"),
		deletedFilesLog:      sflags.MustGetString(cmd, "deleted-files-log"),
	}

	for _, glob := range cfg.keepGlobs {
		if _, err := path.Match(glob, "probe"); err != nil {
			return fmt.Errorf("invalid --keep pattern %q: %w", glob, err)
		}
	}
	if cfg.scanWorkers < 1 || cfg.deleteWorkers < 1 {
		return fmt.Errorf("--scan-workers and --delete-workers must be greater than 0")
	}
	if cfg.daemon {
		if cfg.interval <= 0 {
			return fmt.Errorf("--interval must be greater than 0")
		}
		if !cfg.force && !cfg.dryRun && !cfg.scanOnly {
			return fmt.Errorf("--daemon has nobody to answer the confirmation prompt, pass --force to purge unattended")
		}
	}

	cmd.SilenceUsage = true

	store, err := newPurgeStore(ctx, storeURL, cfg.scanWorkers, logger)
	if err != nil {
		if strings.HasPrefix(storeURL, "gs://") {
			fmt.Println(stylex.Warn("make sure you have Google authorization credentials (gcloud auth application-default login)"))
		}
		return err
	}
	defer store.Close()

	var deletedLog *deletedFilesLog
	if cfg.deletedFilesLog != "" {
		if deletedLog, err = newDeletedFilesLog(cfg.deletedFilesLog); err != nil {
			return err
		}
		defer deletedLog.Close()
	}

	for {
		started := time.Now()
		passErr := runPurgePass(ctx, store, cfg, deletedLog, logger)

		if !cfg.daemon {
			return passErr
		}
		if passErr != nil {
			// A daemon that dies on the first bad pass stops purging until somebody notices.
			logger.Error("purge pass failed, retrying at the next interval", zap.Error(passErr))
		}

		if err := waitNextPass(ctx, started, cfg.interval, logger); err != nil {
			return nil // context cancelled, this is a clean shutdown
		}
	}
}

// runPurgePass rediscovers the scopes and purges them one at a time. It is the unit that
// --daemon repeats, so it must leave nothing behind between two calls.
func runPurgePass(ctx context.Context, store *purgeStore, cfg *purgeConfig, deletedLog *deletedFilesLog, logger *zap.Logger) error {
	cfg.now = time.Now()

	scopes, err := store.Scopes(ctx, cfg.networks)
	if err != nil {
		return fmt.Errorf("discovering purge scopes: %w", err)
	}
	logger.Info("discovered purge scopes", zap.Int("count", len(scopes)))
	if len(scopes) == 0 {
		fmt.Println(stylex.Note("No module scope found, nothing to do"))
		return nil
	}

	// One scope at a time: a scope that blows up must not take the others with it, and an
	// operator watching the logs needs to know how far the run got.
	results := make([]*networkResult, 0, len(scopes))
	for i, scope := range scopes {
		if ctx.Err() != nil {
			logger.Warn("run cancelled, stopping before next scope", zap.String("next_scope", scope.name))
			break
		}

		fmt.Println()
		fmt.Println(stylex.Titlef("Scope %s (%d/%d)", scope.name, i+1, len(scopes)))

		result := purgeNetwork(ctx, store, cfg, scope, deletedLog, logger)
		results = append(results, result)

		if result.fatalErr != nil {
			logger.Error("scope could not be purged, moving on to the next one",
				zap.String("scope", result.network),
				zap.Error(result.fatalErr),
			)
			fmt.Println(stylex.Errorf("Scope %s failed: %v", result.network, result.fatalErr))
		}
	}

	return reportPurgeResults(results, cfg)
}

func waitNextPass(ctx context.Context, started time.Time, interval time.Duration, logger *zap.Logger) error {
	remaining := time.Until(started.Add(interval))
	if remaining <= 0 {
		logger.Info("next pass starts immediately, the previous one lasted longer than the interval",
			zap.Duration("elapsed", time.Since(started)),
			zap.Duration("interval", interval),
		)
		return nil
	}

	logger.Info("waiting before next pass", zap.Duration("remaining", remaining.Truncate(time.Second)))
	timer := time.NewTimer(remaining)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func purgeNetwork(ctx context.Context, store *purgeStore, cfg *purgeConfig, purgeScope purgeScope, deletedLog *deletedFilesLog, logger *zap.Logger) *networkResult {
	result := &networkResult{network: purgeScope.name}

	scanStarted := time.Now()

	discoverStarted := time.Now()
	folders, skipped, err := store.moduleFoldersAt(ctx, purgeScope.prefix, purgeScope.network)
	if err != nil {
		result.fatalErr = err
		return result
	}
	result.listErrors += skipped
	result.folders = len(folders)
	logger.Info("module folders discovered",
		zap.String("scope", purgeScope.name),
		zap.Int("count", len(folders)),
		zap.Duration("elapsed", time.Since(discoverStarted)),
	)

	probeStarted := time.Now()
	toPurge := scanModuleFolders(ctx, store, cfg, folders, result, logger)
	logger.Info("module folders probed",
		zap.String("scope", purgeScope.name),
		zap.Int("count", len(folders)),
		zap.Duration("elapsed", time.Since(probeStarted)),
	)

	result.scanDuration = time.Since(scanStarted)
	result.toPurge = len(toPurge)
	logger.Info("scan phase complete",
		zap.String("scope", purgeScope.name),
		zap.Int("module_folders", result.folders),
		zap.Int("to_purge", len(toPurge)),
		zap.Duration("elapsed", result.scanDuration),
	)

	if result.folders == 0 {
		fmt.Println(stylex.Note("  No module folder found"))
		return result
	}

	fmt.Printf("  %s %s\n", stylex.Label("Module folders:           "), stylex.Value(strconv.Itoa(result.folders)))
	fmt.Printf("  %s %s\n", stylex.Label("Still in use:             "), stylex.Value(strconv.Itoa(result.kept)))
	fmt.Printf("  %s %s\n", stylex.Label("No last_used marker:      "), stylex.Value(strconv.Itoa(result.unmarked)))
	fmt.Printf("  %s %s\n", stylex.Label("To purge:                 "), stylex.Value(strconv.Itoa(len(toPurge))))
	if result.scanErrors > 0 {
		fmt.Printf("  %s %s\n", stylex.Label("Kept because unreadable:  "), stylex.Error(strconv.Itoa(result.scanErrors)))
	}
	fmt.Printf("  %s %s\n", stylex.Label("Scan took:                "), stylex.Value(result.scanDuration.Truncate(time.Millisecond).String()))

	if ctx.Err() != nil {
		result.fatalErr = ctx.Err()
		return result
	}
	if len(toPurge) == 0 {
		return result
	}

	if cfg.scanOnly {
		return result
	}

	sort.Slice(toPurge, func(i, j int) bool { return toPurge[i].folder.prefix < toPurge[j].folder.prefix })

	sampleSize := min(len(toPurge), 10)
	fmt.Println()
	fmt.Println(stylex.Header("  Folders to purge (sample):"))
	for _, c := range toPurge[:sampleSize] {
		fmt.Printf("    %s  %s\n", stylex.Value(c.folder.String()), stylex.Dim(describeMarkers(c.markers)))
	}
	if len(toPurge) > sampleSize {
		fmt.Printf("    %s\n", stylex.Dimf("... and %d more", len(toPurge)-sampleSize))
	}
	fmt.Println()

	if !cfg.force && !cfg.dryRun {
		message := fmt.Sprintf("About to delete the content of %d module folder(s) in scope %q. Continue?", len(toPurge), purgeScope.name)
		if confirmed, _ := cli.PromptConfirm(message); !confirmed {
			fmt.Println(stylex.Note("  Skipped by user"))
			result.skipped = true
			return result
		}
	}

	purgeFolders(ctx, store, cfg, toPurge, result, deletedLog, logger)
	return result
}

func reportPurgeResults(results []*networkResult, cfg *purgeConfig) error {
	verb := "Deleted"
	if cfg.dryRun {
		verb = "Would delete"
	}

	var totalScan time.Duration
	var totalFiles, totalBytes, totalFailed int64
	var failedNetworks []string

	fmt.Println()
	fmt.Println(stylex.Title("Purge summary"))
	for _, r := range results {
		status := stylex.Successf("%s %s file(s) (%s) from %d folder(s)", verb, strconv.FormatInt(r.deletedFiles, 10), formatBytes(r.deletedBytes), r.purgedFolders)
		switch {
		case r.fatalErr != nil:
			status = stylex.Errorf("failed: %v", r.fatalErr)
		case r.skipped:
			status = stylex.Note("skipped by user")
		case cfg.scanOnly:
			status = stylex.Successf("%d folder(s) past retention out of %d, scanned in %s", r.toPurge, r.folders, r.scanDuration.Truncate(time.Millisecond))
		}

		fmt.Printf("  %s  %s\n", stylex.Valuef("%-24s", r.network), status)
		if r.scanErrors > 0 || r.listErrors > 0 || r.failedDeletes > 0 {
			fmt.Printf("    %s\n", stylex.Errorf("%d unreadable marker(s), %d listing error(s), %d failed delete(s)", r.scanErrors, r.listErrors, r.failedDeletes))
		}

		totalScan += r.scanDuration
		totalFiles += r.deletedFiles
		totalBytes += r.deletedBytes
		totalFailed += r.failedDeletes
		if r.failed() {
			failedNetworks = append(failedNetworks, r.network)
		}
	}

	fmt.Println()
	fmt.Printf("  %s %s\n", stylex.Label("Total scan time:"), stylex.Value(totalScan.Truncate(time.Millisecond).String()))
	if !cfg.scanOnly {
		fmt.Printf("  %s %s (%s)\n", stylex.Label("Total deleted:  "), stylex.Value(strconv.FormatInt(totalFiles, 10)+" file(s)"), stylex.Value(formatBytes(totalBytes)))
	}

	if len(failedNetworks) > 0 {
		return fmt.Errorf("completed with errors on network(s): %s", strings.Join(failedNetworks, ", "))
	}

	return nil
}

func parseRetention(entries []string) (map[string]time.Duration, error) {
	out := make(map[string]time.Duration, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		plan, rawDuration, found := strings.Cut(entry, "=")
		if !found {
			if plan, rawDuration, found = strings.Cut(entry, ":"); !found {
				return nil, fmt.Errorf("invalid --retention entry %q, expected 'plan=duration'", entry)
			}
		}

		duration, err := parseRetentionDuration(strings.TrimSpace(rawDuration))
		if err != nil {
			return nil, fmt.Errorf("invalid --retention entry %q: %w", entry, err)
		}
		out[strings.TrimSpace(plan)] = duration
	}

	if _, found := out[defaultPlan]; !found {
		return nil, fmt.Errorf("--retention must contain a %q entry, it covers every plan not named explicitly", defaultPlan)
	}

	return out, nil
}

// parseRetentionDuration accepts time.ParseDuration syntax plus a 'd' (day) suffix.
func parseRetentionDuration(in string) (time.Duration, error) {
	if days, found := strings.CutSuffix(in, "d"); found {
		value, err := strconv.ParseFloat(days, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing days in %q: %w", in, err)
		}
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, fmt.Errorf("duration %q must be greater than 0", in)
		}

		duration := time.Duration(value * float64(24*time.Hour))
		if duration <= 0 {
			return 0, fmt.Errorf("duration %q is too small", in)
		}
		return duration, nil
	}

	duration, err := time.ParseDuration(in)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration %q must be greater than 0", in)
	}

	return duration, nil
}

// scanModuleFolders reads the last_used markers of every folder and returns the ones past
// their retention. A folder whose markers cannot be read is kept and counted: one bad object
// must never condemn a folder, nor abort the scan of its siblings.
func scanModuleFolders(ctx context.Context, store *purgeStore, cfg *purgeConfig, folders []moduleFolder, result *networkResult, logger *zap.Logger) []condemned {
	var mu sync.Mutex
	var toPurge []condemned
	var scanned, keptCount, unmarkedCount, errorCount atomic.Int64

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(cfg.scanWorkers)

	for _, folder := range folders {
		group.Go(func() error {
			markers, unreadable, err := readLastUsedMarkers(groupCtx, store, cfg, folder)
			if err != nil {
				if groupCtx.Err() != nil {
					return err
				}
				unreadable++
				logger.Warn("could not list last_used markers", zap.String("folder", folder.String()), zap.Error(err))
			}

			if done := scanned.Add(1); done%500 == 0 {
				logger.Info("scan progress", zap.String("network", folder.network), zap.Int64("scanned", done), zap.Int("total", len(folders)))
			}

			if unreadable > 0 {
				// A marker we cannot read may well be recent: keeping the folder is the only
				// safe answer.
				errorCount.Add(1)
				keptCount.Add(1)
				logger.Warn("keeping folder with unreadable last_used marker",
					zap.String("folder", folder.String()),
					zap.Int("unreadable_markers", unreadable),
				)
				return nil
			}

			if len(markers) == 0 {
				unmarkedCount.Add(1)
				if !cfg.purgeWithoutLastUsed {
					logger.Debug("no last_used marker, keeping folder", zap.String("folder", folder.String()))
					return nil
				}
			}

			if markersKeepFolder(cfg, markers) {
				keptCount.Add(1)
				logger.Debug("folder still in use",
					zap.String("folder", folder.String()),
					zap.String("last_used", describeMarkers(markers)),
				)
				return nil
			}

			mu.Lock()
			toPurge = append(toPurge, condemned{folder: folder, markers: markers})
			mu.Unlock()
			return nil
		})
	}

	// Only a cancelled context can make a scan goroutine fail, and that is reported by the
	// caller checking ctx.
	_ = group.Wait()

	result.kept += int(keptCount.Load())
	result.unmarked += int(unmarkedCount.Load())
	result.scanErrors += int(errorCount.Load())

	return toPurge
}

// markersKeepFolder is the retention rule: a folder survives as soon as ONE of its markers is
// within the retention of the plan that wrote it.
func markersKeepFolder(cfg *purgeConfig, markers []marker) bool {
	for _, m := range markers {
		if cfg.now.Sub(m.date) < cfg.retentionFor(m.plan) {
			return true
		}
	}
	return false
}

// readLastUsedMarkers lists only the 'last_used*' objects of a module folder: the prefix is
// narrow enough that GCS answers from the index without walking the state files next to them.
func readLastUsedMarkers(ctx context.Context, store *purgeStore, cfg *purgeConfig, folder moduleFolder) (markers []marker, unreadable int, err error) {
	objects, err := store.Markers(ctx, folder)
	if err != nil {
		return nil, 0, err
	}

	markers, unreadable = markersOf(ctx, store, cfg, objects)
	return markers, unreadable, nil
}

// markersOf turns listed marker objects into usage dates. By default the object's last-write
// time is the answer and nothing more is fetched; --read-marker-contents downloads each marker
// to read the date it stores instead. A marker that vanished between the listing and the read
// is simply gone, any other read failure is counted so the caller keeps the folder rather than
// condemn it on incomplete information.
func markersOf(ctx context.Context, store *purgeStore, cfg *purgeConfig, objects []markerObject) (markers []marker, unreadable int) {
	markers = make([]marker, 0, len(objects))
	for _, object := range objects {
		date := object.updated
		if cfg.readMarkerContents {
			var err error
			if date, err = readLastUsedDate(ctx, store, object.name); err != nil {
				if isNotExist(err) {
					continue // deleted between the listing and the read
				}
				if ctx.Err() != nil {
					return markers, unreadable
				}
				unreadable++
				continue
			}
		}
		markers = append(markers, marker{plan: planOfMarker(object.name), date: date})
	}

	return markers, unreadable
}

// planOfMarker maps 'last_used.zst' to the default plan and 'last_used_<plan>.zst' to <plan>.
func planOfMarker(objectName string) string {
	base := strings.TrimSuffix(path.Base(objectName), ".zst")
	plan := strings.TrimPrefix(base, lastUsedPrefix)
	plan = strings.TrimPrefix(plan, "_")
	if plan == "" {
		return defaultPlan
	}
	return plan
}

// isNotExist covers both backends: the GCS client has its own sentinel, dstore surfaces the
// os one for local files and its own for the remote stores.
func isNotExist(err error) bool {
	return errors.Is(err, storage.ErrObjectNotExist) || errors.Is(err, dstore.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

func readLastUsedDate(ctx context.Context, store *purgeStore, objectName string) (time.Time, error) {
	raw, err := store.ReadObject(ctx, objectName)
	if err != nil {
		return time.Time{}, err
	}

	if bytes.HasPrefix(raw, zstdMagic) {
		decoder, err := zstd.NewReader(nil)
		if err != nil {
			return time.Time{}, fmt.Errorf("creating zstd decoder: %w", err)
		}
		defer decoder.Close()

		if raw, err = decoder.DecodeAll(raw, nil); err != nil {
			return time.Time{}, fmt.Errorf("decompressing %q: %w", objectName, err)
		}
	}

	date, err := time.Parse(lastUsedDateLayout, strings.TrimSpace(string(raw)))
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing date of %q: %w", objectName, err)
	}

	return date, nil
}

func describeMarkers(markers []marker) string {
	if len(markers) == 0 {
		return "no last_used marker"
	}

	parts := make([]string, 0, len(markers))
	for _, m := range markers {
		parts = append(parts, fmt.Sprintf("%s=%s", m.plan, m.date.Format(lastUsedDateLayout)))
	}
	sort.Strings(parts)

	return strings.Join(parts, " ")
}

type deletedFilesLog struct {
	mutex sync.Mutex
	file  *os.File
}

func newDeletedFilesLog(filename string) (*deletedFilesLog, error) {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening deleted files log: %w", err)
	}
	return &deletedFilesLog{file: file}, nil
}

func (l *deletedFilesLog) Write(objectName string) {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	fmt.Fprintln(l.file, objectName)
}

func (l *deletedFilesLog) Close() error {
	return l.file.Close()
}

type deleteJob struct {
	objectName string
	size       int64
}

// purgeFolders deletes the content of every condemned folder. Nothing in here is allowed to
// abort the network: a folder that cannot be listed and a file that cannot be deleted are both
// counted and stepped over.
func purgeFolders(ctx context.Context, store *purgeStore, cfg *purgeConfig, toPurge []condemned, result *networkResult, deletedLog *deletedFilesLog, logger *zap.Logger) {
	started := time.Now()
	jobs := make(chan deleteJob, 1000)

	var deleted, failed, deletedBytes, purgedFolders, listErrors atomic.Int64
	var workers sync.WaitGroup

	for range cfg.deleteWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				if deletedLog != nil {
					deletedLog.Write(job.objectName)
				}

				if cfg.dryRun {
					fmt.Printf("dry-run: would delete %s\n", store.ObjectURL(job.objectName))
				} else if err := store.DeleteObject(ctx, job.objectName); err != nil {
					failed.Add(1)
					logger.Warn("skipping failed file", zap.String("file", job.objectName), zap.Error(err))
					continue
				}

				deleted.Add(1)
				deletedBytes.Add(job.size)
			}
		}()
	}

	// Listing a condemned folder is the only place the state and output files get enumerated,
	// so it runs in parallel with the deletes rather than upfront.
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(cfg.scanWorkers)

	for i, c := range toPurge {
		group.Go(func() error {
			logger.Info("purging module folder",
				zap.String("folder", c.folder.String()),
				zap.String("last_used", describeMarkers(c.markers)),
				zap.Int("index", i+1),
				zap.Int("total", len(toPurge)),
			)

			err := store.ListObjects(groupCtx, c.folder, func(name string, size int64) error {
				if cfg.isKept(name) {
					return nil
				}
				select {
				case jobs <- deleteJob{objectName: name, size: size}:
					return nil
				case <-groupCtx.Done():
					return groupCtx.Err()
				}
			})
			if err != nil {
				if groupCtx.Err() != nil {
					return err
				}
				listErrors.Add(1)
				logger.Warn("skipping folder that could not be listed", zap.String("folder", c.folder.String()), zap.Error(err))
				return nil
			}

			purgedFolders.Add(1)
			return nil
		})
	}

	_ = group.Wait()
	close(jobs)
	workers.Wait()

	result.purgedFolders += int(purgedFolders.Load())
	result.deletedFiles += deleted.Load()
	result.deletedBytes += deletedBytes.Load()
	result.failedDeletes += failed.Load()
	result.listErrors += int(listErrors.Load())

	verb := "Deleted"
	if cfg.dryRun {
		verb = "Would delete"
	}
	fmt.Printf("  %s %s file(s) (%s) from %d module folder(s) in %s\n",
		stylex.Header(verb),
		stylex.Value(strconv.FormatInt(deleted.Load(), 10)),
		stylex.Value(formatBytes(deletedBytes.Load())),
		purgedFolders.Load(),
		time.Since(started).Truncate(time.Second),
	)
}
