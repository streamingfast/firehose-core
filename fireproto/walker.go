package fireproto

import (
	pbfirehose "github.com/streamingfast/firehose-core/pb/firehose"
	"github.com/streamingfast/protox"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// WalkNonDeterministicFields walks the proto.Message tree rooted at root and calls fn
// for each field tagged with (firehose.nondeterministic) = true.
// fn receives the containing message instance and the field descriptor.
func WalkNonDeterministicFields(root proto.Message, fn func(msg protoreflect.Message, field protoreflect.FieldDescriptor)) {
	for msg, field := range protox.WalkMessageInstanceFields(root.ProtoReflect(), nil) {
		if isNonDeterministic(field) {
			fn(msg, field)
		}
	}
}

// ClearNonDeterministicFields clears all fields tagged with (firehose.nondeterministic) = true
// in the message tree rooted at root. Fields are set to their zero value.
func ClearNonDeterministicFields(root proto.Message) {
	WalkNonDeterministicFields(root, func(msg protoreflect.Message, field protoreflect.FieldDescriptor) {
		msg.Clear(field)
	})
}

// FindTransactionsField returns the first field descriptor in root's message type
// tagged with (firehose.transactions) = true, or nil if none is tagged.
func FindTransactionsField(root proto.Message) protoreflect.FieldDescriptor {
	fields := root.ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		field := fields.Get(i)
		if v, ok := protox.GetFieldExtensionValue[bool](field, pbfirehose.E_Transactions, false); ok && v {
			return field
		}
	}
	return nil
}

func isNonDeterministic(field protoreflect.FieldDescriptor) bool {
	v, ok := protox.GetFieldExtensionValue[bool](field, pbfirehose.E_Nondeterministic, false)
	return ok && v
}
