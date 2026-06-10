// Package fireproto provides utilities for working with Firehose-annotated
// protobuf messages using custom field extensions defined in the firehose proto
// package (pb/firehose/options.proto).
//
// # Field Annotations
//
// The firehose proto package defines two FieldOptions extensions that can be
// applied to fields in any Block protobuf message:
//
//   - (firehose.transactions) — marks the repeated field that holds the block's
//     transaction list, enabling generic tooling to locate transactions without
//     chain-specific knowledge.
//
//   - (firehose.nondeterministic) — marks fields whose values may differ between
//     nodes (e.g. gas estimates, fee calculations, timing metadata). These fields
//     are candidates for clearing when performing deterministic block comparisons
//     or diffing block content across nodes.
//
// # Usage
//
// Walk all non-deterministic fields in a block:
//
//	fireproto.WalkNonDeterministicFields(block, func(msg protoreflect.Message, field protoreflect.FieldDescriptor) {
//	    fmt.Printf("non-deterministic: %s.%s\n", msg.Descriptor().Name(), field.Name())
//	})
//
// Clear all non-deterministic fields before comparison:
//
//	fireproto.ClearNonDeterministicFields(block)
//
// Find the transactions field on a block:
//
//	fd := fireproto.FindTransactionsField(block)
//	if fd != nil {
//	    txs := block.ProtoReflect().Get(fd).List()
//	}
package fireproto
