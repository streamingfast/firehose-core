package blockpoller

import (
	"errors"
	"fmt"
	"strconv"
	"testing"

	"go.uber.org/zap"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/bstream/forkable"
	pbbstream "github.com/streamingfast/bstream/types/pb/sf/bstream/v1"
	"github.com/stretchr/testify/assert"
)

func TestForkHandler_run(t *testing.T) {
	tests := []struct {
		name            string
		startBlock      bstream.BlockRef
		blocks          []*TestBlock
		expectFireBlock []*pbbstream.Block
	}{
		{
			name:       "start block 0",
			startBlock: blk("0a", "", 0).AsRef(),
			blocks: []*TestBlock{
				tb("0a", "", 0),
				tb("1a", "0a", 0),
				tb("2a", "1a", 0),
			},
			expectFireBlock: []*pbbstream.Block{
				blk("0a", "", 0),
				blk("1a", "0a", 0),
				blk("2a", "1a", 0),
			},
		},
		{
			name:       "Fork 1",
			startBlock: blk("100a", "99a", 100).AsRef(),
			blocks: []*TestBlock{
				tb("100a", "99a", 100),
				tb("101a", "100a", 100),
				tb("102a", "101a", 100),
				tb("103a", "102a", 100),
				tb("104b", "103b", 100),
				tb("103a", "102a", 100),
				tb("104a", "103a", 100),
				tb("105b", "104b", 100),
				tb("103b", "102b", 100),
				tb("102b", "101a", 100),
				tb("106a", "105a", 100),
				tb("105a", "104a", 100),
			},
			expectFireBlock: []*pbbstream.Block{
				blk("100a", "99a", 100),
				blk("101a", "100a", 100),
				blk("102a", "101a", 100),
				blk("103a", "102a", 100),
				blk("104a", "103a", 100),
				blk("102b", "101a", 100),
				blk("103b", "102b", 100),
				blk("104b", "103b", 100),
				blk("105b", "104b", 100),
				blk("105a", "104a", 100),
				blk("106a", "105a", 100),
			},
		},
		{
			name:       "Fork 2",
			startBlock: blk("100a", "99a", 100).AsRef(),
			blocks: []*TestBlock{
				tb("100a", "99a", 100),
				tb("101a", "100a", 100),
				tb("102a", "101a", 100),
				tb("103a", "102a", 100),
				tb("104b", "103b", 100),
				tb("103a", "102a", 100),
				tb("104a", "103a", 100),
				tb("105b", "104b", 100),
				tb("103b", "102b", 100),
				tb("102a", "101a", 100),
				tb("103a", "104a", 100),
				tb("104a", "105a", 100),
				tb("105a", "104a", 100),
			},
			expectFireBlock: []*pbbstream.Block{
				blk("100a", "99a", 100),
				blk("101a", "100a", 100),
				blk("102a", "101a", 100),
				blk("103a", "102a", 100),
				blk("104a", "103a", 100),
				blk("105a", "104a", 100),
			},
		},
		{
			name:       "with lib advancing",
			startBlock: blk("100a", "99a", 100).AsRef(),
			blocks: []*TestBlock{
				tb("100a", "99a", 100),
				tb("101a", "100a", 100),
				tb("102a", "101a", 100),
				tb("103a", "102a", 101),
				tb("104b", "103b", 101),
				tb("103a", "102a", 101),
				tb("104a", "103a", 101),
				tb("105b", "104b", 101),
				tb("103b", "102b", 101),
				tb("102a", "101a", 101),
				tb("103a", "104a", 101),
				tb("104a", "105a", 101),
				tb("105a", "104a", 101),
			},
			expectFireBlock: []*pbbstream.Block{
				blk("100a", "99a", 100),
				blk("101a", "100a", 100),
				blk("102a", "101a", 100),
				blk("103a", "102a", 100),
				blk("104a", "103a", 100),
				blk("105a", "104a", 100),
			},
		},
		{
			name:       "with skipping blocks",
			startBlock: blk("100a", "99a", 100).AsRef(),
			blocks: []*TestBlock{
				tb("100a", "99a", 100),
				tb("101a", "100a", 100),
				tb("102a", "101a", 100),
				tb("103a", "102a", 101),
				tb("104b", "103b", 101),
				tb("103a", "102a", 101),
				tb("104a", "103a", 101),
				tb("105b", "104b", 101),
				tb("103b", "102b", 101),
				tb("102a", "101a", 101),
				tb("103a", "104a", 101),
				tb("104a", "105a", 101),
				tb("105a", "104a", 101),
			},
			expectFireBlock: []*pbbstream.Block{
				blk("100a", "99a", 100),
				blk("101a", "100a", 100),
				blk("102a", "101a", 100),
				blk("103a", "102a", 100),
				blk("104a", "103a", 100),
				blk("105a", "104a", 100),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			blockFetcher := newTestBlockFetcher(t, tt.blocks)

			blockFire := &TestBlockFire{
				blocks: tt.expectFireBlock,
			}

			f := &BlockPoller{
				blockFetcher:      blockFetcher,
				blockFireFunc:     blockFire.fetchBlockFire(t),
				forkDB:            forkable.NewForkDB(),
				startBlockNumGate: 0,
				logger:            logger,
			}

			err := f.run(tt.startBlock)
			if !errors.Is(err, errCompleteDone) {
				assert.Fail(t, "expected errCompleteDone")
			}
			blockFetcher.check()
			blockFire.check(t)
		})
	}
}

func TestForkHandler_resolveStartBlock(t *testing.T) {
	tests := []struct {
		startBlockNum     uint64
		finalizedBlockNum uint64
		expected          uint64
	}{
		{90, 100, 90},
		{100, 100, 100},
		{110, 100, 100},
	}

	for _, test := range tests {
		t.Run("", func(t *testing.T) {
			assert.Equal(t, test.expected, resolveStartBlock(test.startBlockNum, test.finalizedBlockNum))
		})
	}
}

func TestForkHandler_fireCompleteSegment(t *testing.T) {
	tests := []struct {
		name          string
		blocks        []*forkable.Block
		startBlockNum uint64
		expect        []string
	}{
		{
			name:          "start block less then first block",
			blocks:        []*forkable.Block{forkBlk("100a"), forkBlk("101a"), forkBlk("102a")},
			startBlockNum: 98,
			expect:        []string{"100a", "101a", "102a"},
		},
		{
			name:          "start block is first block",
			blocks:        []*forkable.Block{forkBlk("100a"), forkBlk("101a"), forkBlk("102a")},
			startBlockNum: 100,
			expect:        []string{"100a", "101a", "102a"},
		},
		{
			name:          "start block is middle block",
			blocks:        []*forkable.Block{forkBlk("100a"), forkBlk("101a"), forkBlk("102a")},
			startBlockNum: 101,
			expect:        []string{"101a", "102a"},
		},
		{
			name:          "start block is last block",
			blocks:        []*forkable.Block{forkBlk("100a"), forkBlk("101a"), forkBlk("102a")},
			startBlockNum: 102,
			expect:        []string{"102a"},
		},
		{
			name: "start block is past block", blocks: []*forkable.Block{forkBlk("100a"), forkBlk("101a"), forkBlk("102a")},
			startBlockNum: 104,
			expect:        []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := &BlockPoller{startBlockNumGate: test.startBlockNum, logger: zap.NewNop()}
			receviedIds := []string{}
			f.blockFireFunc = func(p *pbbstream.Block) {
				receviedIds = append(receviedIds, p.Id)
			}

			f.fireCompleteSegment(test.blocks)
			assert.Equal(t, test.expect, receviedIds)
		})
	}

}

func tb(id, prev string, libNum uint64) *TestBlock {
	return &TestBlock{
		expect: blk(id, prev, libNum),
		send:   blk(id, prev, libNum),
	}
}

func blk(id, prev string, libNum uint64) *pbbstream.Block {
	return &pbbstream.Block{
		Number:    blocknum(id),
		Id:        id,
		ParentId:  prev,
		LibNum:    libNum,
		ParentNum: blocknum(prev),
	}
}

func forkBlk(id string) *forkable.Block {
	return &forkable.Block{
		BlockID:  id,
		BlockNum: blocknum(id),
		Object: &block{
			Block: &pbbstream.Block{
				Number: blocknum(id),
				Id:     id,
			},
		},
	}
}

func blocknum(blockID string) uint64 {
	b := blockID
	if len(blockID) < 8 { // shorter version, like 8a for 00000008a
		b = fmt.Sprintf("%09s", blockID)
	}
	bin, err := strconv.ParseUint(b[:8], 10, 64)
	if err != nil {
		panic(err)
	}
	return bin
}
