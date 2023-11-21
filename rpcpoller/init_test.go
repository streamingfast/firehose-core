package forkhandler

import (
	"context"
	"fmt"
	"testing"
	"time"

	pbbstream "github.com/streamingfast/bstream/types/pb/sf/bstream/v1"
	"github.com/streamingfast/logging"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zapcore"
)

var logger, tracer = logging.PackageLogger("forkhandler", "github.com/streamingfast/firehose-bitcoin/forkhandler.test")

func init() {
	logging.InstantiateLoggers(logging.WithDefaultLevel(zapcore.DebugLevel))
}

var errCompleteDone = fmt.Errorf("complete done")

type TestBlock struct {
	expect *pbbstream.Block
	send   *pbbstream.Block
}

var _ BlockFetcher = &TestBlockFetcher{}

type TestBlockFetcher struct {
	t      *testing.T
	blocks []*TestBlock
	idx    uint64
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
		return nil, errCompleteDone
	}

	if blkNum != b.blocks[b.idx].expect.Number {
		assert.Fail(b.t, fmt.Sprintf("expected to fetch block %d, got %d", b.blocks[b.idx].expect.Number, blkNum))
	}

	blkToSend := b.blocks[b.idx].send
	b.idx++
	return blkToSend, nil
}

func (b *TestBlockFetcher) check() {
	assert.Equal(b.t, uint64(len(b.blocks)), b.idx, "we should have fetched all %d blocks, only fired %d blocks", len(b.blocks), b.idx)
}

type TestBlockFire struct {
	blocks []*pbbstream.Block
	idx    uint64
}

func (b *TestBlockFire) check(t *testing.T) {
	assert.Equal(t, uint64(len(b.blocks)), b.idx, "we should have fired all %d blocks, only fired %d blocks", len(b.blocks), b.idx)
}

func (b *TestBlockFire) fetchBlockFire(t *testing.T) BlockFireFunc {
	return func(p *pbbstream.Block) {
		if len(b.blocks) == 0 {
			assert.Fail(t, fmt.Sprintf("should not have fired block %d", p.Number))
		}

		if b.idx >= uint64(len(b.blocks)) {
			assert.Fail(t, fmt.Sprintf("should not have fired block %d", p.Number))
		}

		if p.Number != b.blocks[b.idx].Number || p.Id != b.blocks[b.idx].Id {
			assert.Fail(t, fmt.Sprintf("expected to tryFire block %s, got %s", b.blocks[b.idx].String(), p.String()))
		}
		b.idx++
	}
}
