package substreams

import (
	"context"
	"testing"

	"github.com/streamingfast/dstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestSnapshotsToPrune(t *testing.T) {
	files := func(ends ...uint64) []snapshotFile {
		out := make([]snapshotFile, len(ends))
		for i, end := range ends {
			out[i] = snapshotFile{name: "x", endBlock: end}
		}
		return out
	}
	ends := func(files []snapshotFile) []uint64 {
		var out []uint64
		for _, f := range files {
			out = append(out, f.endBlock)
		}
		return out
	}

	tests := []struct {
		name       string
		files      []snapshotFile
		keepEvery  uint64
		keepRecent uint64
		want       []uint64
	}{
		{
			name:       "keeps first of each window, latest and recent ones",
			files:      files(1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000, 9000, 10000, 11000, 12000, 13000),
			keepEvery:  5000,
			keepRecent: 2000,
			want:       []uint64{2000, 3000, 4000, 6000, 7000, 8000, 9000},
		},
		{
			name:       "latest always kept even when in same window as first",
			files:      files(1000, 2000, 3000),
			keepEvery:  5000,
			keepRecent: 0,
			want:       []uint64{2000},
		},
		{
			name:       "recent floor below zero keeps everything",
			files:      files(1000, 2000, 3000),
			keepEvery:  5000,
			keepRecent: 10000,
			want:       nil,
		},
		{
			name:  "no files",
			files: nil, keepEvery: 5000, keepRecent: 0,
			want: nil,
		},
		{
			name:       "misaligned initial block snapshots align to windows",
			files:      files(1000, 2000, 5000, 6000, 10000),
			keepEvery:  5000,
			keepRecent: 0,
			want:       []uint64{2000, 6000},
		},
		{
			name:       "misaligned coarse snapshots keep one per window",
			files:      files(3000, 103000, 203000, 303000),
			keepEvery:  100000,
			keepRecent: 0,
			want:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ends(snapshotsToPrune(tt.files, tt.keepEvery, tt.keepRecent)))
		})
	}
}

// Folder names here are not module hashes, so discovery finds nothing and the full-walk
// fallback kicks in, the path taken by URLs pointing inside a single module folder.
func TestListModuleSnapshots(t *testing.T) {
	store := dstore.NewMockStore(nil)
	store.Files = map[string][]byte{
		"mainnet/substreams-states/aaaa/states/0000002000-0000000000.kv":      nil,
		"mainnet/substreams-states/aaaa/states/0000001000-0000000000.kv":      nil,
		"mainnet/substreams-states/aaaa/states/0000003000-0000002000.partial": nil,
		"mainnet/substreams-states/aaaa/outputs/0000001000-0000002000.output": nil,
		"mainnet/substreams-states/aaaa/last_used":                            nil,
		"mainnet/substreams-states/bbbb/states/0000001000-0000000500.kv":      nil,
	}

	backend := &purgeStore{store: store, scanWorkers: 4, logger: zap.NewNop()}
	modules, err := listModuleSnapshots(context.Background(), backend, 4)
	require.NoError(t, err)
	require.Len(t, modules, 2)

	assert.Equal(t, "mainnet/substreams-states/aaaa/states", modules[0].folder)
	assert.Equal(t, []snapshotFile{
		{name: "mainnet/substreams-states/aaaa/states/0000001000-0000000000.kv", endBlock: 1000},
		{name: "mainnet/substreams-states/aaaa/states/0000002000-0000000000.kv", endBlock: 2000},
	}, modules[0].files)

	assert.Equal(t, "mainnet/substreams-states/bbbb/states", modules[1].folder)
	assert.Equal(t, []snapshotFile{
		{name: "mainnet/substreams-states/bbbb/states/0000001000-0000000500.kv", endBlock: 1000},
	}, modules[1].files)
}

// Real module hashes take the discovery path: module folders are found the way purge
// finds them, tags included, and only their 'states/' prefix is listed.
func TestListModuleSnapshotsDiscovery(t *testing.T) {
	const h1 = "ddc9230698a79b25c443c73753c9a94e038373c1"
	const h2 = "a119f43d8c72fbd2254fa21aab74cfc5e2f14c2f"

	ctx := context.Background()
	base := "file://" + t.TempDir()

	store, err := dstore.NewSimpleStore(base)
	require.NoError(t, err)

	for _, name := range []string{
		"eth-mainnet/substreams-states/" + h1 + "/states/0000002000-0000001000.kv",
		"eth-mainnet/substreams-states/" + h1 + "/outputs/0000001000-0000002000.output",
		"eth-mainnet/substreams-states/" + h1 + "/last_used.zst",
		"eth-mainnet/substreams-states/mmap-stores/" + h2 + "/states/0000003000-0000001000.kv.zst",
	} {
		require.NoError(t, store.WriteObject(ctx, name, emptyReader()))
	}

	backend, err := newPurgeStore(ctx, base, 4, zap.NewNop())
	require.NoError(t, err)
	defer backend.Close()

	modules, err := listModuleSnapshots(ctx, backend, 4)
	require.NoError(t, err)
	require.Len(t, modules, 2)

	assert.Equal(t, "eth-mainnet/substreams-states/"+h1+"/states", modules[0].folder)
	assert.Equal(t, []snapshotFile{
		{name: "eth-mainnet/substreams-states/" + h1 + "/states/0000002000-0000001000.kv", endBlock: 2000},
	}, modules[0].files)

	assert.Equal(t, "eth-mainnet/substreams-states/mmap-stores/"+h2+"/states", modules[1].folder)
	assert.Equal(t, []snapshotFile{
		{name: "eth-mainnet/substreams-states/mmap-stores/" + h2 + "/states/0000003000-0000001000.kv.zst", endBlock: 3000},
	}, modules[1].files)
}

func TestParseFullKVFilename(t *testing.T) {
	file, ok := parseFullKVFilename("a/states/0000012000-0000000000.kv")
	require.True(t, ok)
	assert.Equal(t, uint64(12000), file.endBlock)

	file, ok = parseFullKVFilename("a/states/0000012000-0000000000.kv.zst")
	require.True(t, ok)
	assert.Equal(t, uint64(12000), file.endBlock)

	_, ok = parseFullKVFilename("a/states/0000012000-0000011000.partial.zst")
	assert.False(t, ok)

	_, ok = parseFullKVFilename("a/states/0000012000-0000011000.partial")
	assert.False(t, ok)

	_, ok = parseFullKVFilename("a/states/0000012000-0000011000.abc.kv")
	assert.False(t, ok)
}
