package protoregistry

import (
	"testing"

	"github.com/davecgh/go-spew/spew"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestUnmarshal(t *testing.T) {
	acme := readTestProto(t, "testdata/acme")

	type args struct {
		typeURL string
		value   []byte
	}
	tests := []struct {
		name       string
		protoPaths []string
		want       func(tt *testing.T, out *dynamicpb.Message)
		assertion  require.ErrorAssertionFunc
		value      []byte
		typeURL    string
	}{
		{
			name:    "chain alone",
			typeURL: "sf.acme.type.v1.Block",
			want: func(tt *testing.T, out *dynamicpb.Message) {
				assert.Equal(tt, "", out.Get(out.Descriptor().Fields().ByName("hash")))
				assert.Equal(tt, uint64(0), out.Get(out.Descriptor().Fields().ByName("num")))
			},
			assertion: require.NoError,
		},
		//{
		//	name:       "overriding built-in chain with proto path",
		//	protoPaths: []string{"testdata/override_acme"},
		//	typeURL:    "sf.acme.type.v1.Block",
		//	want: func(tt *testing.T, out *dynamicpb.Message) {
		//		// If you reach this point following a panic in the Go test, the reason there
		//		// is a panic here is because the override_ethereum.proto file is taking
		//		// precedence over the ethereum.proto file, which is not what we want.
		//		assert.Equal(tt, "", out.GetFieldByName("hash_custom"))
		//		assert.Equal(tt, uint64(0), out.GetFieldByName("num_custom"))
		//	},
		//	assertion: require.NoError,
		//},
		//{
		//	name:       "overridding well-know chain (ethereum) with proto path",
		//	protoPaths: []string{"testdata/override_ethereum"},
		//	typeURL:    "sf.ethereum.type.v2.Block",
		//	value:      []byte{0x18, 0x0a},
		//	want: func(tt *testing.T, out *dynamicpb.Message) {
		//		// If you reach this point following a panic in the Go test, the reason there
		//		// is a panic here is because the override_ethereum.proto file is taking
		//		// precedence over the ethereum.proto file, which is not what we want.
		//		assert.Equal(tt, uint64(10), out.GetFieldByName("number_custom"))
		//	},
		//	assertion: require.NoError,
		//},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mustRegistry(t, acme, tt.protoPaths...)
			a := &anypb.Any{TypeUrl: "type.googleapis.com/" + tt.typeURL, Value: tt.value}
			out, err := Unmarshal(a)
			tt.assertion(t, err)

			tt.want(t, out)
		})
	}
}

func mustRegistry(t *testing.T, chainFileDescriptor protoreflect.FileDescriptor, protoPaths ...string) {
	t.Helper()

	err := RegisterFiles(protoPaths)
	require.NoError(t, err)
	err = RegisterFileDescriptor(chainFileDescriptor)
	require.NoError(t, err)
	spew.Dump(protoregistry.GlobalTypes)
}

func readTestProto(t *testing.T, file string) protoreflect.FileDescriptor {
	t.Helper()

	descs, err := parseProtoFiles([]string{file})
	require.NoError(t, err)

	return descs[0]
}
