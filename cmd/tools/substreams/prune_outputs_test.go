package substreams

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/streamingfast/dstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestParseOutputFilename(t *testing.T) {
	file, ok := parseOutputFilename("a/outputs/0000001000-0000002000.output")
	require.True(t, ok)
	assert.Equal(t, uint64(2000), file.endBlock)

	file, ok = parseOutputFilename("a/outputs/0000001000-0000002000.output.zst")
	require.True(t, ok)
	assert.Equal(t, uint64(2000), file.endBlock)

	_, ok = parseOutputFilename("a/index/0000001000-0000002000.index")
	assert.False(t, ok)

	_, ok = parseOutputFilename("a/states/0000002000-0000001000.kv")
	assert.False(t, ok)

	_, ok = parseOutputFilename("a/outputs/0000001000-0000002000.output.tmp")
	assert.False(t, ok)
}

func TestIsSpkgObject(t *testing.T) {
	assert.True(t, isSpkgObject("h/substreams.spkg.zst"))
	assert.True(t, isSpkgObject("h/substreams.partial.spkg.zst"))
	assert.True(t, isSpkgObject("h/substreams.spkg"))
	assert.True(t, isSpkgObject("h/substreams.partial.spkg"))
	assert.False(t, isSpkgObject("h/substreams.yaml"))
	assert.False(t, isSpkgObject("h/other.spkg.zst"))
	assert.False(t, isSpkgObject("h/last_used.zst"))
}

func TestOutputsToPrune(t *testing.T) {
	cutoff := time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	old := cutoff.Add(-time.Hour)
	fresh := cutoff.Add(time.Hour)

	files := []outputFile{
		{name: "old-below", endBlock: 1000, modified: old},
		{name: "old-straddling", endBlock: 6000, modified: old},
		{name: "fresh-below", endBlock: 2000, modified: fresh},
		{name: "at-cutoff-below", endBlock: 3000, modified: cutoff},
		{name: "old-at-block", endBlock: 5000, modified: old},
	}

	pruned := outputsToPrune(files, 5000, cutoff)
	var names []string
	for _, file := range pruned {
		names = append(names, file.name)
	}
	assert.Equal(t, []string{"old-below", "old-at-block"}, names)
}

func TestScanOutputFolders(t *testing.T) {
	const plain = "ddc9230698a79b25c443c73753c9a94e038373c1"
	const output = "a119f43d8c72fbd2254fa21aab74cfc5e2f14c2f"
	const tagged = "cccccccccccccccccccccccccccccccccccccccc"

	ctx := context.Background()
	dir := t.TempDir()
	base := "file://" + dir

	store, err := dstore.NewSimpleStore(base)
	require.NoError(t, err)

	old := []string{
		"eth-mainnet/substreams-states/" + plain + "/outputs/0000001000-0000002000.output.zst",
		"eth-mainnet/substreams-states/" + plain + "/outputs/0000008000-0000009000.output.zst",
		"eth-mainnet/substreams-states/" + output + "/outputs/0000001000-0000002000.output.zst",
		"eth-mainnet/substreams-states/mmap-stores/" + tagged + "/outputs/0000002000-0000003000.output.zst",
	}
	fresh := []string{
		"eth-mainnet/substreams-states/" + plain + "/outputs/0000002000-0000003000.output.zst",
		"eth-mainnet/substreams-states/" + plain + "/states/0000003000-0000001000.kv.zst",
		"eth-mainnet/substreams-states/" + plain + "/last_used.zst",
		"eth-mainnet/substreams-states/" + output + "/substreams.spkg.zst",
	}
	for _, name := range append(append([]string{}, old...), fresh...) {
		require.NoError(t, store.WriteObject(ctx, name, emptyReader()))
	}
	for _, name := range old {
		require.NoError(t, os.Chtimes(filepath.Join(dir, filepath.FromSlash(name)), time.Time{}, time.Now().Add(-96*time.Hour)))
	}

	backend, err := newPurgeStore(ctx, base, 4, zap.NewNop())
	require.NoError(t, err)
	defer backend.Close()

	folders, skipped, err := backend.ModuleFolders(ctx, "eth-mainnet")
	require.NoError(t, err)
	require.Zero(t, skipped)
	require.Len(t, folders, 3)

	cfg := pruneOutputsConfig{
		network:            "eth-mainnet",
		truncateBelowBlock: 5000,
		minimumAge:         72 * time.Hour,
		parallelism:        4,
		now:                time.Now(),
	}

	// The output module (spkg present) is kept untouched; elsewhere only files both fully
	// below the block and older than the age go, whatever the cache tag.
	modules, outputModulesKept, scanErrors := scanOutputFolders(ctx, backend, cfg, folders, zap.NewNop())
	require.Zero(t, scanErrors)
	assert.Equal(t, 1, outputModulesKept)
	require.Len(t, modules, 2)

	assert.True(t, strings.HasSuffix(modules[0].folder.prefix, plain+"/"))
	assert.Equal(t, 3, modules[0].total)
	require.Len(t, modules[0].files, 1)
	assert.Equal(t, "eth-mainnet/substreams-states/"+plain+"/outputs/0000001000-0000002000.output.zst", modules[0].files[0].name)

	assert.True(t, strings.HasSuffix(modules[1].folder.prefix, tagged+"/"))
	require.Len(t, modules[1].files, 1)

	// --output-module-minimum-age brings the spkg-carrying module in, with its own age.
	cfg.outputModuleMinimumAge = 48 * time.Hour
	modules, outputModulesKept, scanErrors = scanOutputFolders(ctx, backend, cfg, folders, zap.NewNop())
	require.Zero(t, scanErrors)
	assert.Zero(t, outputModulesKept)
	require.Len(t, modules, 3)

	// An output-module age larger than the files' keeps them again.
	cfg.outputModuleMinimumAge = 200 * time.Hour
	modules, outputModulesKept, _ = scanOutputFolders(ctx, backend, cfg, folders, zap.NewNop())
	assert.Zero(t, outputModulesKept)
	require.Len(t, modules, 2)
}
