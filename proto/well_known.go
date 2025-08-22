package proto

import (
	"fmt"

	"github.com/streamingfast/firehose-core/proto/wkp"
	"google.golang.org/protobuf/reflect/protodesc"
)

type WellKnownType struct {
	proto string
}

func RegisterWellKnownFileDescriptors(registry *Registry) error {
	for _, fileDescriptor := range wkp.WellKnownProtos() {
		fd, err := protodesc.NewFile(fileDescriptor, registry.Files)
		if err != nil {
			return fmt.Errorf("creating new file descriptor: %w", err)
		}

		err = registry.RegisterFileDescriptor(fd)
		if err != nil {
			return fmt.Errorf("registering file descriptor: %w", err)
		}

	}
	return nil
}
