package firecore

import (
	"github.com/streamingfast/bstream"
)

type GenericBlockEncoder struct {
}

func NewGenericBlockEncoder() *GenericBlockEncoder {
	return &GenericBlockEncoder{}
}

func (g GenericBlockEncoder) Encode(block Block) (blk *bstream.Block, err error) {
	//TODO implement me
	panic("implement me")
}
