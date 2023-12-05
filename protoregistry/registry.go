package protoregistry

import (
	"fmt"
	"sync"

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
