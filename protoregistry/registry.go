package protoregistry

import (
	"fmt"
	"sync"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"

	"github.com/jhump/protoreflect/dynamic"

	"github.com/jhump/protoreflect/desc"
)

// GlobalFiles is a global registry of file descriptors.
var GlobalFiles *Files = new(Files)

type Files struct {
	sync.RWMutex
	filesDescriptors []*desc.FileDescriptor
}

func New() *Files {
	return &Files{
		filesDescriptors: []*desc.FileDescriptor{},
	}
}

func (r *Files) RegisterFiles(files []string) error {
	fileDescriptors, err := parseProtoFiles(files)
	if err != nil {
		return fmt.Errorf("parsing proto files: %w", err)
	}
	r.filesDescriptors = append(r.filesDescriptors, fileDescriptors...)
	return nil
}

func (r *Files) Unmarshall(typeURL string, value []byte) (*dynamic.Message, error) {
	for _, fd := range r.filesDescriptors {
		md := fd.FindSymbol(typeURL)
		if md != nil {
			dynMsg := dynamic.NewMessageFactoryWithDefaults().NewDynamicMessage(md.(*desc.MessageDescriptor))
			if err := dynMsg.Unmarshal(value); err != nil {
				return nil, fmt.Errorf("unmarshalling proto: %w", err)
			}
			return dynMsg, nil
		}
	}
	return nil, fmt.Errorf("no message descriptor in registry for  type url: %s", typeURL)
}

func (r *Files) UnmarshallLegacy(protocol pbbstream.Protocol, value []byte) (*dynamic.Message, error) {
	return r.Unmarshall(legacyKindsToProtoType(protocol), value)
}

func legacyKindsToProtoType(protocol pbbstream.Protocol) string {
	switch protocol {
	case pbbstream.Protocol_EOS:
		return "sf.antelope.type.v1.Block"
	case pbbstream.Protocol_ETH:
		return "sf.ethereum.type.v2.Block"
	case pbbstream.Protocol_SOLANA:
		return "sf.solana.type.v1.Block"
	case pbbstream.Protocol_NEAR:
		return "sf.near.type.v1.Block"
	case pbbstream.Protocol_COSMOS:
		return "sf.cosmos.type.v1.Block"
	}
	panic("unaligned protocol")
}
