package fireproto

import (
	pbfirehose "github.com/streamingfast/pbgo/firehose"
	"github.com/streamingfast/protox"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// WalkNonDeterministicFields walks the proto.Message tree rooted at root and calls fn
// for each field tagged with (firehose.non_deterministic) = true.
// fn receives the containing message instance and the field descriptor.
//
// fn is called for every non-deterministic field in the message type regardless of
// whether the field is currently populated in the instance; clearing an unset field
// is a no-op, so this is safe to use with ClearNonDeterministicFields.
//
// Well-known Google types (google.protobuf.*) are not recursed into.
// Self-referential message types are handled via DFS cycle prevention in the walker.
func WalkNonDeterministicFields(root proto.Message, fn func(msg protoreflect.Message, field protoreflect.FieldDescriptor)) {
	for msg, field := range protox.WalkMessageInstanceFields(root.ProtoReflect(), nil) {
		if hasFlag(field, pbfirehose.E_NonDeterministic) {
			fn(msg, field)
		}
	}
}

// ClearNonDeterministicFields clears all fields tagged with (firehose.non_deterministic) = true
// in the message tree rooted at root. Fields are set to their zero value.
func ClearNonDeterministicFields(root proto.Message) {
	WalkNonDeterministicFields(root, func(msg protoreflect.Message, field protoreflect.FieldDescriptor) {
		msg.Clear(field)
	})
}

// FindBlockMessage returns the root Block message of file: the first top-level
// message tagged with (firehose.block) = true, or else the top-level message
// named `Block`, or nil if neither exists.
func FindBlockMessage(file protoreflect.FileDescriptor) protoreflect.MessageDescriptor {
	messages := file.Messages()
	for i := range messages.Len() {
		md := messages.Get(i)
		if v, ok := protox.GetMessageExtensionValue[bool](md, pbfirehose.E_Block, false); ok && v {
			return md
		}
	}

	return messages.ByName("Block")
}

// FindTransactionsField returns the first field of root's message type tagged
// with (firehose.transactions) = true, or nil if none is tagged.
// Only top-level fields are examined; nested messages are not searched, and
// when several fields are tagged the first one in declaration order wins.
func FindTransactionsField(root proto.Message) protoreflect.FieldDescriptor {
	return findTaggedField(root, pbfirehose.E_Transactions)
}

// TransactionType returns the message descriptor of the elements of the
// (firehose.transactions) field of root, or nil if no field is tagged or the
// tagged field is not a repeated message field.
func TransactionType(root proto.Message) protoreflect.MessageDescriptor {
	field := FindTransactionsField(root)
	if field == nil || !field.IsList() {
		return nil
	}

	return field.Message()
}

func findTaggedField(root proto.Message, xt protoreflect.ExtensionType) protoreflect.FieldDescriptor {
	fields := root.ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		if field := fields.Get(i); hasFlag(field, xt) {
			return field
		}
	}

	return nil
}

func hasFlag(field protoreflect.FieldDescriptor, xt protoreflect.ExtensionType) bool {
	v, ok := protox.GetFieldExtensionValue[bool](field, xt, false)
	return ok && v
}
