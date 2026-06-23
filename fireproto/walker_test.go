package fireproto_test

import (
	"testing"

	"github.com/streamingfast/firehose-core/fireproto"
	pbfirehose "github.com/streamingfast/firehose-core/pb/firehose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// buildAnnotatedDescriptors builds dynamic proto descriptors:
//
//	Block { repeated Tx transactions = 1 [(firehose.transactions)=true]; bytes gas = 2 [(firehose.nondeterministic)=true]; }
//	Tx    { string hash = 1; bytes fee = 2 [(firehose.nondeterministic)=true]; }
func buildAnnotatedDescriptors(t *testing.T) (blockType protoreflect.MessageType, txType protoreflect.MessageType) {
	t.Helper()

	txFDP := &descriptorpb.FileDescriptorProto{
		Name:    new("fireproto_test_tx.proto"),
		Syntax:  new("proto3"),
		Package: new("fireproto.test"),
		Options: &descriptorpb.FileOptions{GoPackage: new("fireproto/test;fireprototest")},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: new("Tx"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     new("hash"),
						Number:   new(int32(1)),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						JsonName: new("hash"),
					},
					{
						Name:     new("fee"),
						Number:   new(int32(2)),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_BYTES.Enum(),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						JsonName: new("fee"),
						Options: func() *descriptorpb.FieldOptions {
							opts := &descriptorpb.FieldOptions{}
							proto.SetExtension(opts, pbfirehose.E_Nondeterministic, true)
							return opts
						}(),
					},
				},
			},
		},
	}

	blockFDP := &descriptorpb.FileDescriptorProto{
		Name:       new("fireproto_test_block.proto"),
		Syntax:     new("proto3"),
		Package:    new("fireproto.test"),
		Dependency: []string{"fireproto_test_tx.proto"},
		Options:    &descriptorpb.FileOptions{GoPackage: new("fireproto/test;fireprototest")},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: new("Block"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     new("transactions"),
						Number:   new(int32(1)),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum(),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum(),
						TypeName: new(".fireproto.test.Tx"),
						JsonName: new("transactions"),
						Options: func() *descriptorpb.FieldOptions {
							opts := &descriptorpb.FieldOptions{}
							proto.SetExtension(opts, pbfirehose.E_Transactions, true)
							return opts
						}(),
					},
					{
						Name:     new("gas"),
						Number:   new(int32(2)),
						Type:     descriptorpb.FieldDescriptorProto_TYPE_BYTES.Enum(),
						Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
						JsonName: new("gas"),
						Options: func() *descriptorpb.FieldOptions {
							opts := &descriptorpb.FieldOptions{}
							proto.SetExtension(opts, pbfirehose.E_Nondeterministic, true)
							return opts
						}(),
					},
				},
			},
		},
	}

	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{
		File: []*descriptorpb.FileDescriptorProto{txFDP, blockFDP},
	})
	require.NoError(t, err)

	txDesc, err := files.FindDescriptorByName("fireproto.test.Tx")
	require.NoError(t, err)
	blockDesc, err := files.FindDescriptorByName("fireproto.test.Block")
	require.NoError(t, err)

	return dynamicpb.NewMessageType(blockDesc.(protoreflect.MessageDescriptor)),
		dynamicpb.NewMessageType(txDesc.(protoreflect.MessageDescriptor))
}

func TestFindTransactionsField(t *testing.T) {
	blockType, _ := buildAnnotatedDescriptors(t)
	block := blockType.New().Interface()

	fd := fireproto.FindTransactionsField(block)
	require.NotNil(t, fd)
	assert.Equal(t, protoreflect.Name("transactions"), fd.Name())
}

func TestFindTransactionsField_none(t *testing.T) {
	ts := &timestamppb.Timestamp{Seconds: 1}
	assert.Nil(t, fireproto.FindTransactionsField(ts))
}

func TestWalkNonDeterministicFields(t *testing.T) {
	blockType, txType := buildAnnotatedDescriptors(t)

	block := blockType.New()
	tx1 := txType.New()
	tx1.Set(tx1.Descriptor().Fields().ByName("hash"), protoreflect.ValueOfString("0xabc"))
	tx1.Set(tx1.Descriptor().Fields().ByName("fee"), protoreflect.ValueOfBytes([]byte{1, 2, 3}))
	block.Mutable(block.Descriptor().Fields().ByName("transactions")).List().Append(protoreflect.ValueOfMessage(tx1))
	block.Set(block.Descriptor().Fields().ByName("gas"), protoreflect.ValueOfBytes([]byte{0xff}))

	type result struct{ parent, field string }
	var got []result
	fireproto.WalkNonDeterministicFields(block.Interface(), func(msg protoreflect.Message, field protoreflect.FieldDescriptor) {
		got = append(got, result{string(msg.Descriptor().Name()), string(field.Name())})
	})

	assert.ElementsMatch(t, []result{
		{parent: "Block", field: "gas"},
		{parent: "Tx", field: "fee"},
	}, got)
}

func TestWalkNonDeterministicFields_noAnnotations(t *testing.T) {
	ts := &timestamppb.Timestamp{Seconds: 100}
	var visited []string
	fireproto.WalkNonDeterministicFields(ts, func(msg protoreflect.Message, field protoreflect.FieldDescriptor) {
		visited = append(visited, string(field.Name()))
	})
	assert.Empty(t, visited)
}

func TestClearNonDeterministicFields(t *testing.T) {
	blockType, txType := buildAnnotatedDescriptors(t)

	block := blockType.New()
	tx1 := txType.New()
	tx1.Set(tx1.Descriptor().Fields().ByName("fee"), protoreflect.ValueOfBytes([]byte{1, 2, 3}))
	block.Mutable(block.Descriptor().Fields().ByName("transactions")).List().Append(protoreflect.ValueOfMessage(tx1))
	block.Set(block.Descriptor().Fields().ByName("gas"), protoreflect.ValueOfBytes([]byte{0xff}))

	fireproto.ClearNonDeterministicFields(block.Interface())

	assert.False(t, block.Has(block.Descriptor().Fields().ByName("gas")), "gas should be cleared")
	tx := block.Get(block.Descriptor().Fields().ByName("transactions")).List().Get(0).Message()
	assert.False(t, tx.Has(tx.Descriptor().Fields().ByName("fee")), "fee should be cleared")
	// hash was never set, just verify it's still unset
	assert.False(t, tx.Has(tx.Descriptor().Fields().ByName("hash")))
}
