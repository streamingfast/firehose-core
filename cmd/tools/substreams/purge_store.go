package substreams

import (
	"context"
	"fmt"
	"io"

	"path"
	"strings"
	"sync"
	"time"

	"github.com/streamingfast/dstore"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// unlimited is what dstore's ListFolders takes to mean "no limit".
const unlimited = -1

// markerObject is a last_used object as the listing returned it. substreams-tier1 overwrites
// the marker on every request it serves, writing that same day as its content, so the object's
// last-write time is the usage date and the listing already carries it.
type markerObject struct {
	name    string
	updated time.Time
}

// purgeStore is the storage the purge works against. Everything goes through dstore, which
// lists folders one level at a time and reads names, sizes and modification times straight out
// of the listings it was already making.
type purgeStore struct {
	store       dstore.Store
	baseURL     string
	scanWorkers int
	logger      *zap.Logger

	// shardListings records whether splitting a folder listing pays on this store, see
	// boundsArePushedDown.
	shardListings bool
}

// boundsArePushedDown reports whether the store hands the bounds of a ranged folder listing to
// the service. Where it does, splitting a listing splits the work; where it does not, dstore
// lists the whole level and filters, so every slice would repeat that same full listing.
func boundsArePushedDown(scheme string) bool {
	switch scheme {
	case "gs", "s3":
		return true
	}
	return false
}

func newPurgeStore(ctx context.Context, storeURL string, cfg *purgeConfig, logger *zap.Logger) (*purgeStore, error) {
	baseURL := strings.TrimSuffix(storeURL, "/")
	store, err := dstore.NewSimpleStore(baseURL)
	if err != nil {
		return nil, fmt.Errorf("creating store for %q: %w", storeURL, err)
	}

	scanWorkers := cfg.scanWorkers
	if scanWorkers < 1 {
		scanWorkers = 1
	}

	shardListings := boundsArePushedDown(store.BaseURL().Scheme)
	logger.Debug("store ready",
		zap.String("store", storeURL),
		zap.String("scheme", store.BaseURL().Scheme),
		zap.Bool("shard_listings", shardListings),
	)

	return &purgeStore{
		store:         store,
		baseURL:       baseURL,
		scanWorkers:   scanWorkers,
		logger:        logger,
		shardListings: shardListings,
	}, nil
}

func (s *purgeStore) Describe() string { return s.baseURL }

func (s *purgeStore) ObjectURL(name string) string { return s.store.ObjectURL(name) }

func (s *purgeStore) Close() error { return nil }

type purgeScope struct {
	name    string
	prefix  string
	network string
}

// Scopes discovers both supported layouts. Direct tier1 folders are represented by
// one empty-network scope, while a shared network root gets one scope per selected network.
func (s *purgeStore) Scopes(ctx context.Context, requestedNetworks []string) ([]purgeScope, error) {
	children, err := s.childNames(ctx, "")
	if err != nil {
		return nil, err
	}

	requested := make(map[string]struct{}, len(requestedNetworks))
	for _, network := range requestedNetworks {
		requested[network] = struct{}{}
	}

	var scopes []purgeScope
	var direct bool
	seenNetworks := make(map[string]struct{})

	for _, child := range children {
		if moduleHashRE.MatchString(child) {
			direct = true
			continue
		}

		grandchildren, err := s.childNames(ctx, child+"/")
		if err != nil {
			continue
		}

		hasModuleHash := false
		hasStatesFolder := false
		for _, grandchild := range grandchildren {
			hasModuleHash = hasModuleHash || moduleHashRE.MatchString(grandchild)
			hasStatesFolder = hasStatesFolder || grandchild == statesFolder
		}

		if hasModuleHash {
			direct = true
		}
		if hasStatesFolder {
			if len(requested) == 0 {
				scopes = append(scopes, purgeScope{
					name:    child,
					prefix:  joinPrefix(child, statesFolder) + "/",
					network: child,
				})
				seenNetworks[child] = struct{}{}
			} else if _, ok := requested[child]; ok {
				scopes = append(scopes, purgeScope{
					name:    child,
					prefix:  joinPrefix(child, statesFolder) + "/",
					network: child,
				})
				seenNetworks[child] = struct{}{}
			}
		}
	}

	if len(requested) > 0 {
		for _, network := range requestedNetworks {
			if _, seen := seenNetworks[network]; seen {
				continue
			}
			scopes = append(scopes, purgeScope{
				name:    network,
				prefix:  joinPrefix(network, statesFolder) + "/",
				network: network,
			})
		}
	}

	if direct {
		scopes = append([]purgeScope{{name: "state store"}}, scopes...)
	}

	return scopes, nil
}

// Networks retains the shared-network discovery view for callers that only need network names.
func (s *purgeStore) Networks(ctx context.Context) ([]string, error) {
	scopes, err := s.Scopes(ctx, nil)
	if err != nil {
		return nil, err
	}

	networks := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if scope.network != "" {
			networks = append(networks, scope.network)
		}
	}
	return networks, nil
}

// childNames lists the immediate sub-folders of a prefix, keeping only their last segment.
//
// On a store that bounds a listing server-side, the listing is split into slices: a folder
// listing is paged one round trip at a time however few folders come back, so the level
// holding a network's module hashes — tens of thousands of them — is otherwise dominated by
// that serial paging. Splitting a level that turns out to be small costs one request per slice
// instead of one in total, a few hundred milliseconds, and there is no way to know which
// levels are large without listing them: a network keeps its hashes either directly under
// substreams-states/ or under a cache tag.
func (s *purgeStore) childNames(ctx context.Context, prefix string) ([]string, error) {
	if !s.shardListings {
		folders, err := s.store.ListFolders(ctx, prefix, unlimited)
		if err != nil {
			return nil, fmt.Errorf("listing %q: %w", prefix, err)
		}
		return folderNames(folders), nil
	}

	return s.shardedChildNames(ctx, prefix)
}

// shardedChildNames lists one slice of the key space per request, in parallel.
func (s *purgeStore) shardedChildNames(ctx context.Context, prefix string) ([]string, error) {
	var mu sync.Mutex
	var names []string

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(s.scanWorkers)

	for _, within := range hexRanges(shardWidth) {
		group.Go(func() error {
			from, to := "", ""
			if within.from != "" {
				from = prefix + within.from
			}
			if within.to != "" {
				to = prefix + within.to
			}

			folders, err := s.store.ListFoldersFromTo(groupCtx, prefix, from, to, unlimited)
			if err != nil {
				return fmt.Errorf("listing %q from %q to %q: %w", prefix, from, to, err)
			}

			mu.Lock()
			defer mu.Unlock()
			names = append(names, folderNames(folders)...)
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, err
	}

	return names, nil
}

func folderNames(folders []string) []string {
	names := make([]string, 0, len(folders))
	for _, folder := range folders {
		names = append(names, path.Base(strings.TrimSuffix(folder, "/")))
	}
	return names
}

// shardWidth is how many leading hexadecimal digits of a module hash name a slice, so the key
// space is cut into 16^shardWidth slices plus an open head and tail.
//
// One digit, 17 slices, measured fastest and steadiest on a network holding 39k module
// folders: 1.6s, against 2.5-5.3s with two digits (257 slices) and 12.8s with three (4097).
// Past a modest number of concurrent listings the requests stop overlapping and only add their
// own cost, and the tail of mostly-empty slices is pure overhead: three digits spends 4097
// requests to return the same 39k folders. Fewer, fatter slices each page a few times, which
// the service does well.
const shardWidth = 1

// keyRange is a slice of the key space one listing is responsible for, expressed as suffixes
// of the prefix being listed.
type keyRange struct {
	from string
	to   string
}

// hexRanges splits the key space into 16^width slices plus an open head and tail, so the
// slices tile it completely. Module hashes are hexadecimal, so they spread evenly over the
// numbered slices while anything else lands in the two open ones.
func hexRanges(width int) []keyRange {
	const digits = "0123456789abcdef"

	bounds := []string{""}
	for i := 0; i < intPow(16, width); i++ {
		bound := make([]byte, width)
		for d, n := width-1, i; d >= 0; d, n = d-1, n/16 {
			bound[d] = digits[n%16]
		}
		bounds = append(bounds, string(bound))
	}
	bounds = append(bounds, "")

	ranges := make([]keyRange, 0, len(bounds)-1)
	for i := 0; i < len(bounds)-1; i++ {
		ranges = append(ranges, keyRange{from: bounds[i], to: bounds[i+1]})
	}

	return ranges
}

func intPow(base, exponent int) int {
	out := 1
	for range exponent {
		out *= base
	}
	return out
}

// ModuleFolders walks a shared network/substreams-states root. Direct state-store
// scopes use moduleFoldersAt directly with an empty prefix.
func (s *purgeStore) ModuleFolders(ctx context.Context, network string) ([]moduleFolder, int, error) {
	statesPrefix := joinPrefix(network, statesFolder) + "/"
	return s.moduleFoldersAt(ctx, statesPrefix, network)
}

// moduleFoldersAt finds module hashes directly below prefix and below one cache-tag
// network is only populated for the shared network layout, where it is useful
// in logs and summaries; direct-layout folder names are relative to the store root.
func (s *purgeStore) moduleFoldersAt(ctx context.Context, prefix, network string) ([]moduleFolder, int, error) {
	children, err := s.childNames(ctx, prefix)
	if err != nil {
		return nil, 0, err
	}
	if len(children) == 0 {
		return nil, 0, nil
	}

	var folders []moduleFolder
	var tags []string
	for _, child := range children {
		if moduleHashRE.MatchString(child) {
			folders = append(folders, moduleFolder{prefix: prefix + child + "/", network: network, hash: child})
			continue
		}
		tags = append(tags, child)
	}

	var mu sync.Mutex
	skipped := 0

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(s.scanWorkers)

	for _, tag := range tags {
		group.Go(func() error {
			tagPrefix := prefix + tag + "/"
			tagChildren, err := s.childNames(groupCtx, tagPrefix)
			if err != nil {
				if groupCtx.Err() != nil {
					return err
				}
				mu.Lock()
				skipped++
				mu.Unlock()
				s.logger.Warn("skipping tag that could not be listed", zap.String("prefix", tagPrefix), zap.Error(err))
				return nil
			}

			mu.Lock()
			defer mu.Unlock()
			for _, child := range tagChildren {
				if !moduleHashRE.MatchString(child) {
					continue
				}
				folders = append(folders, moduleFolder{prefix: tagPrefix + child + "/", network: network, tag: tag, hash: child})
			}
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return nil, 0, err
	}

	return folders, skipped, nil
}

// Markers lists the 'last_used*' objects of a module folder. The prefix is narrow enough that
// the service answers from its index without walking the state files next to them, and the
// modification time comes back with the name.
func (s *purgeStore) Markers(ctx context.Context, folder moduleFolder) ([]markerObject, error) {
	var objects []markerObject
	err := s.store.WalkAttributes(ctx, folder.prefix+lastUsedPrefix, func(entry dstore.ObjectEntry) error {
		objects = append(objects, markerObject{name: entry.Name, updated: entry.LastModified})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing markers of %q: %w", folder.prefix, err)
	}

	return objects, nil
}

func (s *purgeStore) ReadObject(ctx context.Context, name string) ([]byte, error) {
	reader, err := s.store.OpenObject(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("opening %q: %w", name, err)
	}
	defer reader.Close()

	raw, err := io.ReadAll(io.LimitReader(reader, maxMarkerSize))
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", name, err)
	}

	return raw, nil
}

func (s *purgeStore) ListObjects(ctx context.Context, folder moduleFolder, onObject func(name string, size int64) error) error {
	err := s.store.WalkAttributes(ctx, folder.prefix, func(entry dstore.ObjectEntry) error {
		return onObject(entry.Name, entry.Size)
	})
	if err != nil {
		return fmt.Errorf("walking %q: %w", folder.prefix, err)
	}

	return nil
}

func (s *purgeStore) DeleteObject(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := s.store.DeleteObject(ctx, name); err != nil {
		return fmt.Errorf("deleting %q: %w", name, err)
	}

	return nil
}

func joinPrefix(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.Trim(part, "/"); part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "/")
}
