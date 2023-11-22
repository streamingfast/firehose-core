package blockpoller

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"google.golang.org/protobuf/types/known/anypb"
)

type BlockFireFunc func(b *block) error

type BlockFetcher interface {
	PollingInterval() time.Duration
	Fetch(ctx context.Context, blkNum uint64) (*pbbstream.Block, error)
}

type BlockFinalizer interface {
	Init()
	Fire(blk *pbbstream.Block) error
}

var _ BlockFinalizer = (*FireBlockFinalizer)(nil)

type FireBlockFinalizer struct {
	blockTypeURL string
}

func NewFireBlockFinalizer(blockTypeURL string) *FireBlockFinalizer {
	return &FireBlockFinalizer{
		blockTypeURL: blockTypeURL,
	}
}

func (f *FireBlockFinalizer) Init() {
	fmt.Println("FIRE INIT 1.0 ", f.blockTypeURL)
}

func (f *FireBlockFinalizer) Fire(b *pbbstream.Block) error {
	//blockLine := "FIRE BLOCK 18571000 d2836a703a02f3ca2a13f05efe26fc48c6fa0db0d754a49e56b066d3b7d54659 18570999 55de88c909fa368ae1e93b6b8ffb3fbb12e64aefec1d4a1fcc27ae7633de2f81 18570800 1699992393935935000 Ci10eXBlLmdvb2dsZWFwaXMuY29tL3NmLmV0aGVyZXVtLnR5cGUudjIuQmxvY2sSJxIg0oNqcDoC88oqE/Be/ib8SMb6DbDXVKSeVrBm07fVRlkY+L3tCA=="
	anyBlock, err := anypb.New(b)
	if err != nil {
		return fmt.Errorf("converting block to anypb: %w", err)
	}

	if anyBlock.TypeUrl != f.blockTypeURL {
		return fmt.Errorf("block type url %q does not match expected type %q", anyBlock.TypeUrl, f.blockTypeURL)
	}

	blockLine := fmt.Sprintf(
		"FIRE BLOCK %d %s %d %s %d %d %s",
		b.Number,
		b.Id,
		b.ParentNum,
		b.ParentId,
		b.LibNum,
		b.Timestamp.AsTime().UnixNano(),
		base64.StdEncoding.EncodeToString(anyBlock.Value),
	)

	fmt.Println(blockLine)
	return nil

}
