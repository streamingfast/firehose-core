package substreams

import (
	"context"
	"fmt"
	"strings"

	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"github.com/streamingfast/substreams/manifest"
	pbsubstreams "github.com/streamingfast/substreams/pb/sf/substreams/v1"
)

// ResolveAndLoadSpkg checks which spkg variant exists under stateStore/moduleHash/
// and loads it. Older Substreams RPC versions produced .partial.spkg.zst files which
// require skipping package and module output type validation.
// Returns the package, the full URL that was loaded, whether it is partial, and any error.
func ResolveAndLoadSpkg(ctx context.Context, stateStore, moduleHash string) (*pbsubstreams.Package, string, bool, error) {
	base := strings.TrimSuffix(stateStore, "/") + "/" + moduleHash

	store, err := dstore.NewSimpleStore(base)
	if err != nil {
		return nil, "", false, fmt.Errorf("creating store at %s: %w", base, err)
	}

	fullExists, err := store.FileExists(ctx, "substreams.spkg.zst")
	if err != nil {
		return nil, "", false, fmt.Errorf("checking substreams.spkg.zst: %w", err)
	}
	if fullExists {
		url := base + "/substreams.spkg.zst"
		pkg, err := LoadSpkgFromURL(url, false)
		return pkg, url, false, err
	}

	partialURL := base + "/substreams.partial.spkg.zst"
	pkg, err := LoadSpkgFromURL(partialURL, true)
	return pkg, partialURL, true, err
}

// LoadSpkgFromURL reads and parses the spkg at the given URL. When skipValidation
// is true, module output type and package validation are disabled — required for
// .partial.spkg.zst files produced by older Substreams RPC versions.
func LoadSpkgFromURL(spkgURL string, skipValidation bool) (*pbsubstreams.Package, error) {
	opts := []manifest.Option{manifest.SkipSourceCodeReader()}
	if skipValidation {
		opts = append(opts, manifest.SkipPackageValidationReader(), manifest.SkipModuleOutputTypeValidationReader())
	}
	reader, err := manifest.NewReader(spkgURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating reader for %s: %w", spkgURL, err)
	}
	bundle, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("reading spkg: %w", err)
	}
	return bundle.Package, nil
}

// FindModuleInPackage finds a module by name in a package, with a fallback that
// strips a "<prefix>:<name>" form (e.g. "graph_node:map_events") since some
// output module names carry such a prefix while the package stores them without.
func FindModuleInPackage(pkg *pbsubstreams.Package, name string) (*pbsubstreams.Module, error) {
	if pkg.Modules == nil {
		return nil, fmt.Errorf("package has no modules")
	}

	findByName := func(n string) *pbsubstreams.Module {
		for _, m := range pkg.Modules.Modules {
			if m.Name == n {
				return m
			}
		}
		return nil
	}

	if m := findByName(name); m != nil {
		return m, nil
	}

	if _, stripped, found := strings.Cut(name, ":"); found {
		if m := findByName(stripped); m != nil {
			fmt.Printf("%s\n", stylex.Warnf("Output module %q not found; retrying with stripped name %q", name, stripped))
			return m, nil
		}
	}

	available := make([]string, len(pkg.Modules.Modules))
	for i, m := range pkg.Modules.Modules {
		available[i] = m.Name
	}
	return nil, fmt.Errorf("module %q not found in package (available: %s)", name, strings.Join(available, ", "))
}

// SpkgPackageLabel returns a "name@version" label for the given package, or
// "<unknown>" if no PackageMeta is set.
func SpkgPackageLabel(pkg *pbsubstreams.Package) string {
	if len(pkg.PackageMeta) == 0 {
		return "<unknown>"
	}
	return pkg.PackageMeta[0].Name + "@" + pkg.PackageMeta[0].Version
}
