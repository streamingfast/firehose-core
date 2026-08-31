package substreams

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/streamingfast/dstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestJoinPrefix(t *testing.T) {
	assert.Equal(t, "eth-mainnet/substreams-states", joinPrefix("eth-mainnet", statesFolder))
	assert.Equal(t, "root/eth-mainnet/substreams-states", joinPrefix("root", "eth-mainnet", statesFolder))
	assert.Equal(t, "eth-mainnet/substreams-states", joinPrefix("", "eth-mainnet/", "/"+statesFolder))
}

// Exercise the store against a real dstore-backed local store, the portable path every
// non-GCS backend takes.
func TestPurgeStoreModuleFolders(t *testing.T) {
	const h1 = "ddc9230698a79b25c443c73753c9a94e038373c1"
	const h2 = "a119f43d8c72fbd2254fa21aab74cfc5e2f14c2f"

	ctx := context.Background()
	base := "file://" + t.TempDir()

	store, err := dstore.NewSimpleStore(base)
	require.NoError(t, err)

	for _, name := range []string{
		"eth-mainnet/substreams-states/" + h1 + "/last_used.zst",
		"eth-mainnet/substreams-states/" + h1 + "/outputs/0000123000-0000124000.output.zst",
		"eth-mainnet/substreams-states/" + h1 + "/substreams.spkg.zst",
		"eth-mainnet/substreams-states/mmap-stores/" + h2 + "/last_used_pro.zst",
		"eth-mainnet/substreams-states/mmap-stores/" + h2 + "/outputs/0000123000-0000124000.output.zst",
		"eth-mainnet/substreams-states/stray-file.zst",
		"sol-mainnet/substreams-states/" + h1 + "/last_used.zst",
	} {
		require.NoError(t, store.WriteObject(ctx, name, emptyReader()))
	}

	backend, err := newPurgeStore(ctx, base, 1, zap.NewNop())
	require.NoError(t, err)
	defer backend.Close()

	networks, err := backend.Networks(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"eth-mainnet", "sol-mainnet"}, networks)

	scopes, err := backend.Scopes(ctx, nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []purgeScope{
		{name: "eth-mainnet", prefix: "eth-mainnet/substreams-states/", network: "eth-mainnet"},
		{name: "sol-mainnet", prefix: "sol-mainnet/substreams-states/", network: "sol-mainnet"},
	}, scopes)

	scopes, err = backend.Scopes(ctx, []string{"sol-mainnet"})
	require.NoError(t, err)
	assert.Equal(t, []purgeScope{{name: "sol-mainnet", prefix: "sol-mainnet/substreams-states/", network: "sol-mainnet"}}, scopes)

	folders, skipped, err := backend.ModuleFolders(ctx, "eth-mainnet")
	require.NoError(t, err)
	assert.Zero(t, skipped)
	assert.ElementsMatch(t, []moduleFolder{
		{prefix: "eth-mainnet/substreams-states/" + h1 + "/", network: "eth-mainnet", hash: h1},
		{prefix: "eth-mainnet/substreams-states/mmap-stores/" + h2 + "/", network: "eth-mainnet", tag: "mmap-stores", hash: h2},
	}, folders)

	// Markers are remembered by the walk, and only the ones at the root of the folder count.
	markers, err := backend.Markers(ctx, folders[0])
	require.NoError(t, err)
	require.Len(t, markers, 1)
	assert.Equal(t, defaultPlan, planOfMarker(markers[0].name))
	assert.False(t, markers[0].updated.IsZero())

	tagged := folders[0]
	for _, folder := range folders {
		if folder.tag != "" {
			tagged = folder
		}
	}
	markers, err = backend.Markers(ctx, tagged)
	require.NoError(t, err)
	require.Len(t, markers, 1)
	assert.Equal(t, "pro", planOfMarker(markers[0].name))

	// The whole folder is enumerated for deletion, with the sizes dstore reports.
	var listed []string
	require.NoError(t, backend.ListObjects(ctx, folders[0], func(name string, size int64) error {
		listed = append(listed, name)
		assert.GreaterOrEqual(t, size, int64(0))
		return nil
	}))
	assert.Len(t, listed, 3)
}

func TestPurgeStoreDirectStateStore(t *testing.T) {
	ctx := context.Background()
	base := "file://" + t.TempDir()

	store, err := dstore.NewSimpleStore(base)
	require.NoError(t, err)

	const untaggedHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const taggedHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	require.NoError(t, store.WriteObject(ctx, untaggedHash+"/last_used.zst", emptyReader()))
	require.NoError(t, store.WriteObject(ctx, "v1/"+taggedHash+"/last_used.zst", emptyReader()))

	backend, err := newPurgeStore(ctx, base, 1, zap.NewNop())
	require.NoError(t, err)
	defer backend.Close()

	networks, err := backend.Networks(ctx)
	require.NoError(t, err)
	assert.Empty(t, networks)

	scopes, err := backend.Scopes(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []purgeScope{{name: "state store"}}, scopes)

	folders, skipped, err := backend.moduleFoldersAt(ctx, "", "")
	require.NoError(t, err)
	assert.Zero(t, skipped)
	assert.ElementsMatch(t, []moduleFolder{
		{prefix: untaggedHash + "/", hash: untaggedHash},
		{prefix: "v1/" + taggedHash + "/", tag: "v1", hash: taggedHash},
	}, folders)
}

func TestPurgeStoreTaggedOnlyStateStore(t *testing.T) {
	ctx := context.Background()
	base := "file://" + t.TempDir()

	store, err := dstore.NewSimpleStore(base)
	require.NoError(t, err)

	const hash = "cccccccccccccccccccccccccccccccccccccccc"
	require.NoError(t, store.WriteObject(ctx, "v1/"+hash+"/last_used.zst", emptyReader()))

	backend, err := newPurgeStore(ctx, base, 1, zap.NewNop())
	require.NoError(t, err)
	defer backend.Close()

	networks, err := backend.Networks(ctx)
	require.NoError(t, err)
	assert.Empty(t, networks)

	scopes, err := backend.Scopes(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, []purgeScope{{name: "state store"}}, scopes)

	folders, skipped, err := backend.moduleFoldersAt(ctx, "", "")
	require.NoError(t, err)
	assert.Zero(t, skipped)
	assert.Equal(t, []moduleFolder{{prefix: "v1/" + hash + "/", tag: "v1", hash: hash}}, folders)
}

func emptyReader() *strings.Reader { return strings.NewReader("") }

// The slices must tile the whole key space, or a sharded listing would silently lose folders.
func TestHexRanges(t *testing.T) {
	for _, width := range []int{1, 2} {
		t.Run(fmt.Sprintf("width %d", width), func(t *testing.T) {
			ranges := hexRanges(width)
			assert.Len(t, ranges, intPow(16, width)+1)

			assert.Equal(t, "", ranges[0].from, "the first slice must be open at the bottom")
			assert.Equal(t, "", ranges[len(ranges)-1].to, "the last slice must be open at the top")
			for i := 0; i < len(ranges)-1; i++ {
				assert.Equal(t, ranges[i].to, ranges[i+1].from, "slices must be contiguous")
			}
		})
	}

	assert.Equal(t, keyRange{from: "", to: "00"}, hexRanges(2)[0])
	assert.Equal(t, keyRange{from: "ff", to: ""}, hexRanges(2)[256])
}

// A level past the threshold is listed in slices, and must come back whole.
func TestPurgeStoreShardedListing(t *testing.T) {
	ctx := context.Background()
	base := "file://" + t.TempDir()

	store, err := dstore.NewSimpleStore(base)
	require.NoError(t, err)

	// More folders than a single slice holds, named like the module hashes they stand in for.
	const count = 300
	expected := make([]string, 0, count)
	for i := 0; i < count; i++ {
		hash := fmt.Sprintf("%040x", i)
		expected = append(expected, hash)
		require.NoError(t, store.WriteObject(ctx, "eth-mainnet/substreams-states/"+hash+"/last_used.zst", strings.NewReader("")))
	}

	backend, err := newPurgeStore(ctx, base, 32, zap.NewNop())
	require.NoError(t, err)
	defer backend.Close()

	// A local store filters the bounds itself, so the purge lists it in one go; force the
	// split to exercise it here.
	backend.shardListings = true

	names, err := backend.childNames(ctx, "eth-mainnet/substreams-states/")
	require.NoError(t, err)
	assert.ElementsMatch(t, expected, names, "the sharded listing must return every folder exactly once")

	folders, skipped, err := backend.ModuleFolders(ctx, "eth-mainnet")
	require.NoError(t, err)
	assert.Zero(t, skipped)
	assert.Len(t, folders, count)
}

// Splitting a listing only pays where the service applies the bounds; everywhere else dstore
// lists the whole level and filters, so each slice would repeat that same full listing.
func TestBoundsArePushedDown(t *testing.T) {
	assert.True(t, boundsArePushedDown("gs"))
	assert.True(t, boundsArePushedDown("s3"))
	assert.False(t, boundsArePushedDown("az"))
	assert.False(t, boundsArePushedDown("file"))
	assert.False(t, boundsArePushedDown(""))
	assert.False(t, boundsArePushedDown("memory"))
}
