package blockpoller

import (
	"context"
	"fmt"
	"testing"
	"time"

	pbbstream "github.com/streamingfast/bstream/types/pb/sf/bstream/v1"
	"github.com/streamingfast/logging"
	"github.com/stretchr/testify/assert"
	"github.com/test-go/testify/require"
	"go.uber.org/zap/zapcore"
)

var logger, tracer = logging.PackageLogger("forkhandler", "github.com/streamingfast/firehose-bitcoin/forkhandler.test")

func init() {
	logging.InstantiateLoggers(logging.WithDefaultLevel(zapcore.DebugLevel))
}

var TestErrCompleteDone = fmt.Errorf("complete done")

type TestBlock struct {
	expect *pbbstream.Block
	send   *pbbstream.Block
}

var _ BlockFetcher = &TestBlockFetcher{}

type TestBlockFetcher struct {
	t         *testing.T
	blocks    []*TestBlock
	idx       uint64
	completed bool
}

func newTestBlockFetcher(t *testing.T, blocks []*TestBlock) *TestBlockFetcher {
	return &TestBlockFetcher{
		t:      t,
		blocks: blocks,
	}
}

func (b *TestBlockFetcher) PollingInterval() time.Duration {
	return 0
}

func (b *TestBlockFetcher) Fetch(_ context.Context, blkNum uint64) (*pbbstream.Block, error) {
	if len(b.blocks) == 0 {
		assert.Fail(b.t, fmt.Sprintf("should not have ffetchired block %d", blkNum))
	}

	if b.idx >= uint64(len(b.blocks)) {
		b.completed = true
		return nil, nil
	}

	if blkNum != b.blocks[b.idx].expect.Number {
		assert.Fail(b.t, fmt.Sprintf("expected to fetch block %d, got %d", b.blocks[b.idx].expect.Number, blkNum))
	}

	blkToSend := b.blocks[b.idx].send
	b.idx++
	return blkToSend, nil
}

func (b *TestBlockFetcher) check(t *testing.T) {
	t.Helper()
	require.Equal(b.t, uint64(len(b.blocks)), b.idx, "we should have fetched all %d blocks, only fired %d blocks", len(b.blocks), b.idx)
}
