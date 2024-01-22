package protoregistry

import (
	"testing"

	"github.com/jhump/protoreflect/desc"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestUnmarshal(t *testing.T) {
	acme := readTestProto(t, "testdata/acme")

	type args struct {
		registry *Registry
		typeURL  string
		value    []byte
	}
	tests := []struct {
		name      string
		args      args
		want      func(tt *testing.T, out *dynamic.Message)
		assertion require.ErrorAssertionFunc
	}{
		{
			"chain alone",
			args{
				registry: mustRegistry(t, acme.UnwrapFile()),
				typeURL:  "sf.acme.type.v1.Block",
			},
			func(tt *testing.T, out *dynamic.Message) {
				assert.Equal(tt, "", out.GetFieldByName("hash"))
				assert.Equal(tt, uint64(0), out.GetFieldByName("num"))
			},
			require.NoError,
		},
		{
			"overriding built-in chain with proto path",
			args{
				registry: mustRegistry(t, acme.UnwrapFile(), "testdata/override_acme"),
				typeURL:  "sf.acme.type.v1.Block",
			},
			func(tt *testing.T, out *dynamic.Message) {
				// If you reach this point following a panic in the Go test, the reason there
				// is a panic here is because the override_ethereum.proto file is taking
				// precedence over the ethereum.proto file, which is not what we want.
				assert.Equal(tt, "", out.GetFieldByName("hash_custom"))
				assert.Equal(tt, uint64(0), out.GetFieldByName("num_custom"))
			},
			require.NoError,
		},
		{
			"overridding well-know chain (ethereum) with proto path",
			args{
				registry: mustRegistry(t, acme.UnwrapFile(), "testdata/override_ethereum"),
				typeURL:  "sf.ethereum.type.v2.Block",
				value:    []byte{0x18, 0x0a},
			},
			func(tt *testing.T, out *dynamic.Message) {
				// If you reach this point following a panic in the Go test, the reason there
				// is a panic here is because the override_ethereum.proto file is taking
				// precedence over the ethereum.proto file, which is not what we want.
				assert.Equal(tt, uint64(10), out.GetFieldByName("number_custom"))
			},
			require.NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			any := &anypb.Any{TypeUrl: "type.googleapis.com/" + tt.args.typeURL, Value: tt.args.value}
			out, err := tt.args.registry.Unmarshal(any)
			tt.assertion(t, err)

			tt.want(t, out)
		})
	}
}

func mustRegistry(t *testing.T, chainFileDescriptor protoreflect.FileDescriptor, protoPaths ...string) *Registry {
	t.Helper()

	reg, err := New(chainFileDescriptor, protoPaths...)
	require.NoError(t, err)

	return reg
}

func readTestProto(t *testing.T, file string) *desc.FileDescriptor {
	t.Helper()

	descs, err := parseProtoFiles([]string{file})
	require.NoError(t, err)

	return descs[0]
}
