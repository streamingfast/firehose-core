package blockpoller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestFireBlockFinalizer_saveState(t *testing.T) {
	tests := []struct {
		blocks     []*forkable.Block
		forkDBFunc func() *forkable.ForkDB
		expect     string
	}{
		{
			blocks: []*forkable.Block{
				forkBlk("100a"),
			},
			forkDBFunc: func() *forkable.ForkDB {
				fk := forkable.NewForkDB()
				fk.AddLink(bstream.NewBlockRef("97a", 97), "96a", nil)
				fk.AddLink(bstream.NewBlockRef("98a", 98), "97a", nil)
				fk.AddLink(bstream.NewBlockRef("99a", 99), "98a", nil)
				fk.SetLIB(blk("99a", "97a", 95).AsRef(), 98)
				return fk
			},

			expect: `{"Lib":{"id":"98a","num":98,"previous_ref_id":""},"LastFiredBlock":{"id":"100a","num":100,"previous_ref_id":""},"Blocks":[{"id":"100a","num":100,"previous_ref_id":""}]}`,
		},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			dirName, err := os.MkdirTemp("", "fblk")
			require.NoError(t, err)
			defer os.Remove(dirName)

			poller := &BlockPoller{
				stateStorePath: dirName,
				forkDB:         tt.forkDBFunc(),
				logger:         zap.NewNop(),
			}
			require.NoError(t, poller.saveState(tt.blocks))
			cnt, err := os.ReadFile(filepath.Join(dirName, "cursor.json"))
			require.NoError(t, err)
			assert.Equal(t, tt.expect, string(cnt))
		})
	}

}
