package fireproto_test

import (
	"testing"

	"github.com/streamingfast/firehose-core/fireproto"
	pbfirehose "github.com/streamingfast/pbgo/firehose"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func fieldWith(name string, number int32, typ descriptorpb.FieldDescriptorProto_Type, label descriptorpb.FieldDescriptorProto_Label, typeName string, xt protoreflect.ExtensionType) *descriptorpb.FieldDescriptorProto {
	fd := &descriptorpb.FieldDescriptorProto{
		Name:     new(name),
		Number:   new(number),
		Type:     typ.Enum(),
		Label:    label.Enum(),
		JsonName: new(name),
	}
	if typeName != "" {
		fd.TypeName = new(typeName)
	}
	if xt != nil {
		fd.Options = &descriptorpb.FieldOptions{}
		proto.SetExtension(fd.Options, xt, true)
	}
	return fd
}

const (
	optional = descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	repeated = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
	tString  = descriptorpb.FieldDescriptorProto_TYPE_STRING
	tBytes   = descriptorpb.FieldDescriptorProto_TYPE_BYTES
	tMessage = descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
)

// buildAnnotatedDescriptors builds dynamic proto descriptors:
//
//	Header { bytes hash = 1; bytes timestamp_hint = 2 [(firehose.non_deterministic)=true]; }
//	Tx     { string hash = 1; bytes fee = 2 [(firehose.non_deterministic)=true]; }
//	Block  {
//	  Header header = 1;
//	  repeated Tx transactions = 2 [(firehose.transactions)=true];
//	  bytes gas = 3 [(firehose.non_deterministic)=true];
//	}
func buildAnnotatedDescriptors(t *testing.T) (blockType, txType protoreflect.MessageType) {
	t.Helper()

	file := &descriptorpb.FileDescriptorProto{
		Name:    new("fireproto_test.proto"),
		Syntax:  new("proto3"),
		Package: new("fireproto.test"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: new("Header"),
				Field: []*descriptorpb.FieldDescriptorProto{
					fieldWith("hash", 1, tBytes, optional, "", nil),
					fieldWith("timestamp_hint", 2, tBytes, optional, "", pbfirehose.E_NonDeterministic),
				},
			},
			{
				Name: new("Tx"),
				Field: []*descriptorpb.FieldDescriptorProto{
					fieldWith("hash", 1, tString, optional, "", nil),
					fieldWith("fee", 2, tBytes, optional, "", pbfirehose.E_NonDeterministic),
				},
			},
			{
				Name: new("Block"),
				Field: []*descriptorpb.FieldDescriptorProto{
					fieldWith("header", 1, tMessage, optional, ".fireproto.test.Header", nil),
					fieldWith("transactions", 2, tMessage, repeated, ".fireproto.test.Tx", pbfirehose.E_Transactions),
					fieldWith("gas", 3, tBytes, optional, "", pbfirehose.E_NonDeterministic),
				},
			},
		},
	}

	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{file}})
	require.NoError(t, err)

	blockDesc, err := files.FindDescriptorByName("fireproto.test.Block")
	require.NoError(t, err)
	txDesc, err := files.FindDescriptorByName("fireproto.test.Tx")
	require.NoError(t, err)

	return dynamicpb.NewMessageType(blockDesc.(protoreflect.MessageDescriptor)),
		dynamicpb.NewMessageType(txDesc.(protoreflect.MessageDescriptor))
}

func TestFindTransactionsField(t *testing.T) {
	blockType, _ := buildAnnotatedDescriptors(t)

	fd := fireproto.FindTransactionsField(blockType.New().Interface())
	require.NotNil(t, fd)
	assert.Equal(t, protoreflect.Name("transactions"), fd.Name())
}

func TestFindTransactionsField_none(t *testing.T) {
	assert.Nil(t, fireproto.FindTransactionsField(&timestamppb.Timestamp{Seconds: 1}))
}

func TestTransactionType(t *testing.T) {
	blockType, txType := buildAnnotatedDescriptors(t)

	md := fireproto.TransactionType(blockType.New().Interface())
	require.NotNil(t, md)
	assert.Equal(t, txType.Descriptor().FullName(), md.FullName())
}

func TestTransactionType_none(t *testing.T) {
	assert.Nil(t, fireproto.TransactionType(&timestamppb.Timestamp{Seconds: 1}))
}

func TestTransactionType_notAMessageList(t *testing.T) {
	file := &descriptorpb.FileDescriptorProto{
		Name:    new("fireproto_badblock.proto"),
		Syntax:  new("proto3"),
		Package: new("fireproto.bad"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: new("Block"),
			Field: []*descriptorpb.FieldDescriptorProto{
				fieldWith("transactions", 1, tString, repeated, "", pbfirehose.E_Transactions),
			},
		}},
	}
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{file}})
	require.NoError(t, err)
	desc, err := files.FindDescriptorByName("fireproto.bad.Block")
	require.NoError(t, err)

	block := dynamicpb.NewMessageType(desc.(protoreflect.MessageDescriptor)).New().Interface()
	require.NotNil(t, fireproto.FindTransactionsField(block))
	assert.Nil(t, fireproto.TransactionType(block))
}

func populatedBlock(blockType, txType protoreflect.MessageType) protoreflect.Message {
	block := blockType.New()
	bf := block.Descriptor().Fields()

	header := block.Mutable(bf.ByName("header")).Message()
	header.Set(header.Descriptor().Fields().ByName("hash"), protoreflect.ValueOfBytes([]byte{9}))
	header.Set(header.Descriptor().Fields().ByName("timestamp_hint"), protoreflect.ValueOfBytes([]byte{7}))

	tx := txType.New()
	tx.Set(tx.Descriptor().Fields().ByName("hash"), protoreflect.ValueOfString("0xabc"))
	tx.Set(tx.Descriptor().Fields().ByName("fee"), protoreflect.ValueOfBytes([]byte{1, 2, 3}))
	block.Mutable(bf.ByName("transactions")).List().Append(protoreflect.ValueOfMessage(tx))
	block.Set(bf.ByName("gas"), protoreflect.ValueOfBytes([]byte{0xff}))

	return block
}

func TestWalkNonDeterministicFields(t *testing.T) {
	blockType, txType := buildAnnotatedDescriptors(t)
	block := populatedBlock(blockType, txType)

	type result struct{ parent, field string }
	var got []result
	fireproto.WalkNonDeterministicFields(block.Interface(), func(msg protoreflect.Message, field protoreflect.FieldDescriptor) {
		got = append(got, result{string(msg.Descriptor().Name()), string(field.Name())})
	})

	assert.ElementsMatch(t, []result{
		{parent: "Block", field: "gas"},
		{parent: "Header", field: "timestamp_hint"},
		{parent: "Tx", field: "fee"},
	}, got)
}

func TestWalkNonDeterministicFields_noAnnotations(t *testing.T) {
	var visited []string
	fireproto.WalkNonDeterministicFields(&timestamppb.Timestamp{Seconds: 100}, func(_ protoreflect.Message, field protoreflect.FieldDescriptor) {
		visited = append(visited, string(field.Name()))
	})
	assert.Empty(t, visited)
}

func TestClearNonDeterministicFields(t *testing.T) {
	blockType, txType := buildAnnotatedDescriptors(t)
	block := populatedBlock(blockType, txType)
	bf := block.Descriptor().Fields()

	fireproto.ClearNonDeterministicFields(block.Interface())

	assert.False(t, block.Has(bf.ByName("gas")), "gas should be cleared")

	header := block.Get(bf.ByName("header")).Message()
	assert.False(t, header.Has(header.Descriptor().Fields().ByName("timestamp_hint")), "timestamp_hint should be cleared")
	assert.True(t, header.Has(header.Descriptor().Fields().ByName("hash")), "deterministic header field kept")

	tx := block.Get(bf.ByName("transactions")).List().Get(0).Message()
	assert.False(t, tx.Has(tx.Descriptor().Fields().ByName("fee")), "fee should be cleared")
	assert.True(t, tx.Has(tx.Descriptor().Fields().ByName("hash")), "deterministic tx field kept")
}

func buildFile(t *testing.T, messages ...*descriptorpb.DescriptorProto) protoreflect.FileDescriptor {
	t.Helper()

	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{{
		Name:        new("fireproto_blockmsg.proto"),
		Syntax:      new("proto3"),
		Package:     new("fireproto.blockmsg"),
		MessageType: messages,
	}}})
	require.NoError(t, err)

	fd, err := files.FindFileByPath("fireproto_blockmsg.proto")
	require.NoError(t, err)
	return fd
}

func TestFindBlockMessage_annotated(t *testing.T) {
	opts := &descriptorpb.MessageOptions{}
	proto.SetExtension(opts, pbfirehose.E_Block, true)

	fd := buildFile(t,
		&descriptorpb.DescriptorProto{Name: new("Block")},
		&descriptorpb.DescriptorProto{Name: new("Root"), Options: opts},
	)

	md := fireproto.FindBlockMessage(fd)
	require.NotNil(t, md)
	assert.Equal(t, protoreflect.Name("Root"), md.Name())
}

func TestFindBlockMessage_fallbackName(t *testing.T) {
	fd := buildFile(t,
		&descriptorpb.DescriptorProto{Name: new("Other")},
		&descriptorpb.DescriptorProto{Name: new("Block")},
	)

	md := fireproto.FindBlockMessage(fd)
	require.NotNil(t, md)
	assert.Equal(t, protoreflect.Name("Block"), md.Name())
}

func TestFindBlockMessage_none(t *testing.T) {
	fd := buildFile(t, &descriptorpb.DescriptorProto{Name: new("Other")})
	assert.Nil(t, fireproto.FindBlockMessage(fd))
}
