package protoregistry

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"

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

// New creates a new Registry first populated with the well-known types
// and then with the proto files passed as arguments. This means the
// precendence of the proto files is higher than the well-known types.
func New(chainFileDescriptor protoreflect.FileDescriptor, protoPaths ...string) (*Registry, error) {
	f := NewEmpty()

	// Proto paths have the highest precedence, so we register them first
	if len(protoPaths) > 0 {
		if err := f.RegisterFiles(protoPaths); err != nil {
			return nil, fmt.Errorf("register proto files: %w", err)
		}
	}

	// Chain file descriptor has the second highest precedence, it always
	// override built-in types if defined.
	if chainFileDescriptor != nil {
		chainFileDesc, err := desc.WrapFile(chainFileDescriptor)
		if err != nil {
			return nil, fmt.Errorf("wrap file descriptor: %w", err)
		}

		f.filesDescriptors = append(f.filesDescriptors, chainFileDesc)
	}

	// Last are well known types, they have the lowest precedence
	f.Extends(WellKnownRegistry)

	return f, nil
}

func NewEmpty() *Registry {
	f := &Registry{
		filesDescriptors: []*desc.FileDescriptor{},
	}
	return f
}

func (r *Registry) RegisterFiles(files []string) error {
	if len(files) == 0 {
		return nil
	}

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

func (r *Registry) Unmarshal(t *anypb.Any) (*dynamic.Message, error) {
	for _, fd := range r.filesDescriptors {
		md := fd.FindSymbol(cleanTypeURL(t.TypeUrl))
		if md != nil {
			dynMsg := dynamic.NewMessageFactoryWithDefaults().NewDynamicMessage(md.(*desc.MessageDescriptor))
			if err := dynMsg.Unmarshal(t.Value); err != nil {
				return nil, fmt.Errorf("unmarshalling proto: %w", err)
			}
			return dynMsg, nil
		}
	}
	return nil, fmt.Errorf("no message descriptor in registry for  type url: %s", t.TypeUrl)
}

func (r *Registry) Extends(registry *Registry) {
	r.filesDescriptors = append(r.filesDescriptors, registry.filesDescriptors...)
}

func cleanTypeURL(in string) string {
	return strings.Replace(in, "type.googleapis.com/", "", 1)
}
