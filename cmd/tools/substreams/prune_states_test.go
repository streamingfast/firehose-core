package substreams

import (
	"context"
	"testing"

	"github.com/streamingfast/dstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			name:       "keeps multiples, latest and recent ones",
			files:      files(1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000, 9000, 10000, 11000, 12000, 13000),
			keepEvery:  5000,
			keepRecent: 2000,
			want:       []uint64{1000, 2000, 3000, 4000, 6000, 7000, 8000, 9000},
		},
		{
			name:       "latest always kept even when not a multiple",
			files:      files(1000, 2000, 3000),
			keepEvery:  5000,
			keepRecent: 0,
			want:       []uint64{1000, 2000},
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
			name:       "misaligned initial block snapshots follow their end block",
			files:      files(1000, 2000, 5000, 6000, 10000),
			keepEvery:  5000,
			keepRecent: 0,
			want:       []uint64{1000, 2000, 6000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ends(snapshotsToPrune(tt.files, tt.keepEvery, tt.keepRecent)))
		})
	}
}

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

	modules, err := listModuleSnapshots(context.Background(), store)
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
