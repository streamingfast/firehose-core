package compare

import (
	"testing"
	"time"

	fcproto "github.com/streamingfast/firehose-core/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDiffFieldPaths_identical(t *testing.T) {
	a := timestamppb.New(time.Unix(100, 5))
	b := timestamppb.New(time.Unix(100, 5))
	assert.Empty(t, diffFieldPaths(a, b))
}

func TestDiffFieldPaths_scalar(t *testing.T) {
	a := timestamppb.New(time.Unix(100, 5))
	b := timestamppb.New(time.Unix(100, 6))
	assert.Equal(t, []string{"nanos"}, diffFieldPaths(a, b))
}

func TestDiffFieldPaths_nil(t *testing.T) {
	assert.Equal(t, []string{"<nil message>"}, diffFieldPaths(nil, timestamppb.Now()))
	assert.Empty(t, diffFieldPaths(nil, nil))
}

func TestDiffFieldPaths_unknownField(t *testing.T) {
	a := timestamppb.New(time.Unix(1, 0))
	b := timestamppb.New(time.Unix(1, 0))
	b.ProtoReflect().SetUnknown(appendUnknown(nil, 99, []byte("x")))
	assert.Equal(t, []string{"unknown_field(99)"}, diffFieldPaths(a, b))
}

func TestDiffFieldPaths_nestedAndList(t *testing.T) {
	a := newCosmosBlock(t)
	setNestedString(a, "header", "chain_id", "injective-1")
	appendBytes(a, "txs", []byte{1, 2, 3})
	appendBytes(a, "txs", []byte{4, 5, 6})

	b := proto.Clone(a).(*dynamicpb.Message)
	assert.Empty(t, diffFieldPaths(a, b))

	setNestedString(b, "header", "chain_id", "injective-2")
	assert.Equal(t, []string{"header.chain_id"}, diffFieldPaths(a, b))

	setNestedString(b, "header", "chain_id", "injective-1")
	appendBytes(b, "txs", []byte{7, 8, 9})
	assert.Equal(t, []string{"txs: length 2 != 3"}, diffFieldPaths(a, b))

	b = proto.Clone(a).(*dynamicpb.Message)
	setListBytes(b, "txs", 0, []byte{9, 9, 9})
	assert.Equal(t, []string{"txs[0]"}, diffFieldPaths(a, b))
}

func TestDiffFieldPaths_nestedUnknownField(t *testing.T) {
	a := newCosmosBlock(t)
	setNestedString(a, "header", "chain_id", "injective-1")
	b := proto.Clone(a).(*dynamicpb.Message)

	headerFd := b.Descriptor().Fields().ByName("header")
	header := b.Mutable(headerFd).Message()
	header.SetUnknown(appendUnknown(nil, 12, []byte("extra")))

	assert.Equal(t, []string{"header.unknown_field(12)"}, diffFieldPaths(a, b))
}

func TestCompare_unknownFieldsDifferButJSONMatches(t *testing.T) {
	registry, err := fcproto.NewRegistry(nil)
	require.NoError(t, err)

	a := newCosmosBlock(t)
	setNestedString(a, "header", "chain_id", "injective-1")
	b := proto.Clone(a).(*dynamicpb.Message)
	b.ProtoReflect().SetUnknown(appendUnknown(nil, 12, []byte("extra")))

	require.False(t, proto.Equal(a, b))

	refJSON, curJSON, different, err := Compare(a, b, false, registry, "")
	require.NoError(t, err)
	assert.True(t, different)
	assert.Equal(t, refJSON, curJSON)
	assert.Equal(t, []string{"unknown_field(12)"}, diffFieldPaths(a, b))
}

func TestFormatFieldDiffs_truncates(t *testing.T) {
	paths := make([]string, maxPrintedFieldDiffs+3)
	for i := range paths {
		paths[i] = "x"
	}
	got := formatFieldDiffs(paths)
	require.Len(t, got, maxPrintedFieldDiffs+1)
	assert.Equal(t, "... and 3 more", got[len(got)-1])
}

func newCosmosBlock(t *testing.T) *dynamicpb.Message {
	t.Helper()
	registry, err := fcproto.NewRegistry(nil)
	require.NoError(t, err)
	mt, err := registry.FindMessageByName("sf.cosmos.type.v2.Block")
	require.NoError(t, err)
	return dynamicpb.NewMessage(mt.Descriptor())
}

func setNestedString(msg *dynamicpb.Message, parent, field protoreflect.Name, value string) {
	parentFd := msg.Descriptor().Fields().ByName(parent)
	nested := msg.Mutable(parentFd).Message()
	nested.Set(nested.Descriptor().Fields().ByName(field), protoreflect.ValueOfString(value))
}

func appendBytes(msg *dynamicpb.Message, field protoreflect.Name, value []byte) {
	fd := msg.Descriptor().Fields().ByName(field)
	msg.Mutable(fd).List().Append(protoreflect.ValueOfBytes(value))
}

func setListBytes(msg *dynamicpb.Message, field protoreflect.Name, index int, value []byte) {
	fd := msg.Descriptor().Fields().ByName(field)
	msg.Mutable(fd).List().Set(index, protoreflect.ValueOfBytes(value))
}

func appendUnknown(raw []byte, num protowire.Number, value []byte) []byte {
	raw = protowire.AppendTag(raw, num, protowire.BytesType)
	return protowire.AppendBytes(raw, value)
}
