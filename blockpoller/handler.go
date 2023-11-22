package blockpoller

import (
	"encoding/base64"
	"fmt"
	"sync"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
)

type BlockHandler interface {
	Init()
	Fire(blk *pbbstream.Block) error
}

var _ BlockHandler = (*FireBlockHandler)(nil)

type FireBlockHandler struct {
	blockTypeURL string
	init         sync.Once
}

func NewFireBlockHandler(blockTypeURL string) *FireBlockHandler {
	return &FireBlockHandler{
		blockTypeURL: blockTypeURL,
	}
}

func (f *FireBlockHandler) Init() {
	fmt.Println("FIRE INIT 1.0 ", f.blockTypeURL)
}

func (f *FireBlockHandler) Fire(b *pbbstream.Block) error {
	//blockLine := "FIRE BLOCK 18571000 d2836a703a02f3ca2a13f05efe26fc48c6fa0db0d754a49e56b066d3b7d54659 18570999 55de88c909fa368ae1e93b6b8ffb3fbb12e64aefec1d4a1fcc27ae7633de2f81 18570800 1699992393935935000 Ci10eXBlLmdvb2dsZWFwaXMuY29tL3NmLmV0aGVyZXVtLnR5cGUudjIuQmxvY2sSJxIg0oNqcDoC88oqE/Be/ib8SMb6DbDXVKSeVrBm07fVRlkY+L3tCA=="
	if b.Payload.TypeUrl != f.blockTypeURL {
		return fmt.Errorf("block type url %q does not match expected type %q", b.Payload.TypeUrl, f.blockTypeURL)
	}

	blockLine := fmt.Sprintf(
		"FIRE BLOCK %d %s %d %s %d %d %s",
		b.Number,
		b.Id,
		b.ParentNum,
		b.ParentId,
		b.LibNum,
		b.Timestamp.AsTime().UnixNano(),
		base64.StdEncoding.EncodeToString(b.Payload.Value),
	)

	fmt.Println(blockLine)
	return nil

}
