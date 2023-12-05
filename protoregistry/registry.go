package protoregistry

import (
	"fmt"
	"strings"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
)

// Generate the flags based on Go code in this project directly, this however
// creates a chicken & egg problem if there is compilation error within the project
// but to fix them we must re-generate it.
//go:generate go run ./generator well_known.go protoregistry

type Registry struct {
	filesDescriptors []*desc.FileDescriptor
}

func New() *Registry {
	f := &Registry{
		filesDescriptors: []*desc.FileDescriptor{},
	}
	return f
}

func (r *Registry) RegisterFiles(files []string) error {
	fileDescriptors, err := parseProtoFiles(files)
	if err != nil {
		return fmt.Errorf("parsing proto files: %w", err)
	}
	r.filesDescriptors = append(r.filesDescriptors, fileDescriptors...)
	return nil
}

func (r *Registry) RegisterFileDescriptor(f *desc.FileDescriptor) {
	r.filesDescriptors = append(r.filesDescriptors, f)
}

func (r *Registry) Unmarshall(typeURL string, value []byte) (*dynamic.Message, error) {
	for _, fd := range r.filesDescriptors {
		md := fd.FindSymbol(cleanTypeURL(typeURL))
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

func (r *Registry) Extends(registry *Registry) {
	r.filesDescriptors = append(r.filesDescriptors, registry.filesDescriptors...)
}

func cleanTypeURL(in string) string {
	return strings.Replace(in, "type.googleapis.com/", "", 1)
}
