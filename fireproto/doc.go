// Package fireproto provides utilities for working with Firehose-annotated
// protobuf messages, using the custom field options defined in
// `firehose/options.proto` of the `streamingfast/proto` repository (protobuf package `firehose`, Go bindings in `pbgo`).
//
// The annotations give generic tooling (diffing, indexing, validation) semantic
// knowledge about a chain's Block message without any chain-specific code.
//
// # Field Annotations
//
// Annotations are defined as extensions of protobuf options:
//
//   - (firehose.block), a message option, marks the root Block message of a
//     file. It is optional: a top-level message named `Block` is the fallback,
//     see [FindBlockMessage].
//   - (firehose.transactions), a field option, marks the repeated message field
//     holding the block's transactions. The transaction type is the message type
//     of that field, see [TransactionType].
//   - (firehose.non_deterministic), a field option, marks fields, at any depth,
//     whose value may differ between nodes (a failure reason string, timing
//     metadata, ...). They can be cleared to compare blocks produced by
//     different nodes.
//
// Example:
//
//	import "firehose/options.proto";
//
//	message Block {
//	  option (firehose.block) = true; // optional, `Block` name is the fallback
//
//	  BlockHeader header = 1;
//	  repeated Transaction transactions = 2 [(firehose.transactions) = true];
//	}
//
//	message Transaction {
//	  bytes hash = 1;
//	  string failure_reason = 2 [(firehose.non_deterministic) = true];
//	}
//
// # Usage
//
// Locate the root Block message of a file:
//
//	blockDesc := fireproto.FindBlockMessage(file)
//
// Locate the transactions field of a block and the transaction type:
//
//	txs := fireproto.FindTransactionsField(block)
//	txType := fireproto.TransactionType(block)
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
package fireproto
