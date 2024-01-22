package protoregistry

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
)

// Generate the flags based on Go code in this project directly, this however
// creates a chicken & egg problem if there is compilation error within the project
// but to fix them we must re-generate it.
//go:generate go run ./generator well_known.go protoregistry

func Register(chainFileDescriptor protoreflect.FileDescriptor, protoPaths ...string) error {

	// Proto paths have the highest precedence, so we register them first
	if len(protoPaths) > 0 {
		if err := RegisterFiles(protoPaths); err != nil {
			return fmt.Errorf("register proto files: %w", err)
		}
	}

	// Chain file descriptor has the second highest precedence, it always
	// override built-in types if defined.
	if chainFileDescriptor != nil {
		err := RegisterFileDescriptor(chainFileDescriptor)
		if err != nil {
			return fmt.Errorf("register chain file descriptor: %w", err)
		}
	}

	// Last are well known types, they have the lowest precedence
	fds, err := GetWellKnownFileDescriptors()
	if err != nil {
		return fmt.Errorf("getting well known file descriptors: %w", err)
	}
	return RegisterFileDescriptors(fds)

}

func RegisterFiles(files []string) error {
	if len(files) == 0 {
		return nil
	}

	fileDescriptors, err := parseProtoFiles(files)
	if err != nil {
		return fmt.Errorf("parsing proto files: %w", err)
	}

	return RegisterFileDescriptors(fileDescriptors)
}
func RegisterFileDescriptors(fds []protoreflect.FileDescriptor) error {
	for _, fd := range fds {
		err := RegisterFileDescriptor(fd)
		if err != nil {
			return fmt.Errorf("registering proto file: %w", err)
		}
	}
	return nil
}
func RegisterFileDescriptor(fd protoreflect.FileDescriptor) error {
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
		return fmt.Errorf("registering proto file: %w", err)
	}
	return nil
}

func Unmarshal(a *anypb.Any) (*dynamicpb.Message, error) {
	messageType, err := protoregistry.GlobalTypes.FindMessageByURL(a.TypeUrl)
	if err != nil {
		return nil, fmt.Errorf("failed to find message type: %v", err)
	}

	message := dynamicpb.NewMessage(messageType.Descriptor())
	err = a.UnmarshalTo(message)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %v", err)
	}

	return nil, fmt.Errorf("no message descriptor in registry for  type url: %s", a.TypeUrl)
}

func cleanTypeURL(in string) string {
	return strings.Replace(in, "type.googleapis.com/", "", 1)
}
