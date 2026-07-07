package info

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
)

func mergedFileBytes(t *testing.T, from, to uint64) []byte {
	t.Helper()
	buf := bytes.NewBuffer(nil)
	w, err := bstream.NewDBinBlockWriter(buf)
	require.NoError(t, err)
	for i := from; i <= to; i++ {
		parentID := ""
		if i > 0 {
			parentID = fmt.Sprintf("%08x", i-1)
		}
		require.NoError(t, w.Write(&pbbstream.Block{
			Number:   i,
			Id:       fmt.Sprintf("%08x", i),
			ParentId: parentID,
			Payload:  &anypb.Any{TypeUrl: "type.googleapis.com/sf.test.Block"},
		}))
	}
	return buf.Bytes()
}

func TestDetectBundleSizeMismatch(t *testing.T) {
	tests := []struct {
		name                 string
		configured           uint64
		firstStreamableBlock uint64
		// files maps a boundary filename to the [from,to] block range it holds.
		// A nil range means "just register the boundary name" (content unread).
		files      map[string][2]uint64
		emptyFiles []string
		expectErr  bool
	}{
		{
			name:       "matching 100-block store (gap)",
			configured: 100,
			emptyFiles: []string{"0000000000", "0000000100", "0000000200"},
			expectErr:  false,
		},
		{
			name:       "configured bigger than files, gap -> error",
			configured: 1000,
			emptyFiles: []string{"0000000000", "0000000100", "0000000200"},
			expectErr:  true,
		},
		{
			// the reported bug: 200-block store read with the default 100,
			// detected from the listing alone (no file read)
			name:       "configured smaller than files (200-store read at 100) -> error",
			configured: 100,
			emptyFiles: []string{"0000000000", "0000000200", "0000000400"},
			expectErr:  true,
		},
		{
			name:       "configured smaller than files (1000-store read at 100) -> error",
			configured: 100,
			emptyFiles: []string{"0000000000", "0000001000"},
			expectErr:  true,
		},
		{
			name:       "gap not a multiple of configured -> error",
			configured: 300,
			emptyFiles: []string{"0000000000", "0000000200", "0000000400"},
			expectErr:  true,
		},
		{
			name:       "single complete 100-file under 1000 config -> error",
			configured: 1000,
			files:      map[string][2]uint64{"0000000000": {0, 99}},
			expectErr:  true,
		},
		{
			name:       "single 200-file read at 100 -> error",
			configured: 100,
			files:      map[string][2]uint64{"0000000000": {0, 199}},
			expectErr:  true,
		},
		{
			name:       "single complete 1000-file under 1000 config -> no mismatch",
			configured: 1000,
			files:      map[string][2]uint64{"0000000000": {0, 999}},
			expectErr:  false,
		},
		{
			name:       "single file ending off a 100-boundary (tail skip) -> no error",
			configured: 1000,
			files:      map[string][2]uint64{"0000000000": {0, 950}},
			expectErr:  false,
		},
		{
			name:       "empty store",
			configured: 1000,
			expectErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := bstream.DefaultMergedBlocksBundleSize
			bstream.DefaultMergedBlocksBundleSize = tt.configured
			defer func() { bstream.DefaultMergedBlocksBundleSize = old }()

			store := dstore.NewMockStore(nil)
			for _, f := range tt.emptyFiles {
				store.SetFile(f, nil)
			}
			for name, rng := range tt.files {
				store.SetFile(name, mergedFileBytes(t, rng[0], rng[1]))
			}

			err := detectBundleSizeMismatch(context.Background(), store, tt.firstStreamableBlock)
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
