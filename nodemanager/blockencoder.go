package nodemanager

import (
	"github.com/streamingfast/bstream"
	firecore "github.com/streamingfast/firehose-core"
)

type GenericBlockEncoder struct {
}

func NewGenericBlockEncoder() *GenericBlockEncoder {
	return &GenericBlockEncoder{}
}

func (g GenericBlockEncoder) Encode(block firecore.Block) (blk *bstream.Block, err error) {
	//TODO implement me
	panic("implement me")
}
