package firehose

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	firecore "github.com/streamingfast/firehose-core"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type testChainBlock struct {
	*pbbstream.Block
}

func (b testChainBlock) GetFirehoseBlockID() string           { return b.Id }
func (b testChainBlock) GetFirehoseBlockNumber() uint64       { return b.Number }
func (b testChainBlock) GetFirehoseBlockParentID() string     { return b.ParentId }
func (b testChainBlock) GetFirehoseBlockParentNumber() uint64 { return b.ParentNum }
func (b testChainBlock) GetFirehoseBlockTime() time.Time      { return time.Time{} }

func TestDownloadFromFirehoseRejectsUnalignedStartBlock(t *testing.T) {
	cmd := NewToolsDownloadFromFirehoseCmd(&firecore.Chain[testChainBlock]{Tools: &firecore.ToolsConfig[testChainBlock]{}}, zap.NewNop())
	cmd.SetContext(context.Background())
	cmd.Flags().Uint64("merged-blocks-bundle-size", 1000, "")

	// start block 500 with bundle size 1000: writing would produce an
	// incomplete first bundle, the command must refuse it before connecting
	err := cmd.RunE(cmd, []string{"localhost:1", "500:2000", filepath.Join(t.TempDir(), "dst")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be on a merged-blocks boundary")
}
