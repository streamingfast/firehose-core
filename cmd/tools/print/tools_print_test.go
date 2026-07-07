// Copyright 2021 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package print

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/streamingfast/dstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHighestMergedBlocksBase(t *testing.T) {
	newStore := func(t *testing.T, bases ...uint64) dstore.Store {
		t.Helper()
		store, err := dstore.NewDBinStore("file://" + filepath.Join(t.TempDir(), "merged"))
		require.NoError(t, err)
		for _, base := range bases {
			require.NoError(t, store.WriteObject(context.Background(), fmt.Sprintf("%010d", base), strings.NewReader("")))
		}
		return store
	}

	t.Run("empty store", func(t *testing.T) {
		_, found, err := highestMergedBlocksBase(context.Background(), newStore(t), 0, 100)
		require.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("single file", func(t *testing.T) {
		base, found, err := highestMergedBlocksBase(context.Background(), newStore(t, 0), 0, 100)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, uint64(0), base)
	})

	t.Run("contiguous run 0..900", func(t *testing.T) {
		var bases []uint64
		for b := uint64(0); b <= 900; b += 100 {
			bases = append(bases, b)
		}
		base, found, err := highestMergedBlocksBase(context.Background(), newStore(t, bases...), 0, 100)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, uint64(900), base)
	})

	t.Run("returns an existing base past a hole", func(t *testing.T) {
		// 0,100,200 exist, a hole at 300, then 400: the probe may land past the
		// hole. It must still return a base that actually exists; FileSource
		// then errors on the missing 300 just as it did before.
		store := newStore(t, 0, 100, 200, 400)
		base, found, err := highestMergedBlocksBase(context.Background(), store, 0, 100)
		require.NoError(t, err)
		require.True(t, found)
		exists, err := mergedBlocksFileExists(context.Background(), store, base)
		require.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("bundle size 1000", func(t *testing.T) {
		base, found, err := highestMergedBlocksBase(context.Background(), newStore(t, 0, 1000, 2000), 0, 1000)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, uint64(2000), base)
	})

	t.Run("start beyond first file", func(t *testing.T) {
		base, found, err := highestMergedBlocksBase(context.Background(), newStore(t, 100, 200, 300), 150, 100)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, uint64(300), base)
	})
}

func Test_doesLookLikeStoreURLFile(t *testing.T) {
	type args struct {
		path string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "just block number",
			args: args{"gs://bucket/path/to/file/0000000001"},
			want: true,
		},
		{
			name: "block number + dbin",
			args: args{"gs://bucket/path/to/file/0000000001.dbin"},
			want: true,
		},
		{
			name: "block number + dbin +zst",
			args: args{"gs://bucket/path/to/file/0000000001.dbin.zst"},
			want: true,
		},
		{
			name: "wrong block prefix, alone",
			args: args{"gs://bucket/path/to/file/v2"},
			want: false,
		},
		{
			name: "wrong block prefix + dbing",
			args: args{"gs://bucket/path/to/file/v2.dbin"},
			want: false,
		},
		{
			name: "wrong block prefix + dbin + zst",
			args: args{"gs://bucket/path/to/file/v2.dbin.zst"},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksLikeMergedBlocksFile(tt.args.path); got != tt.want {
				t.Errorf("doesLookLikeStoreURLFile() = %v, want %v", got, tt.want)
			}
		})
	}
}
