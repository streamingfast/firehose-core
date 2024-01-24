package json

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/mr-tron/base58"
	"github.com/streamingfast/firehose-core/proto"
	"golang.org/x/exp/slices"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
)

type Marshaller struct {
	marshallers          *json.Marshalers
	includeUnknownFields bool
	registry             *proto.Registry
}

type EncoderOption func(*Marshaller)

func WithoutUnknownFields() EncoderOption {
	return func(e *Marshaller) {
		e.includeUnknownFields = false
	}
}
func New(registry *proto.Registry, includeUnknownFields ...EncoderOption) *Marshaller {
	e := &Marshaller{
		includeUnknownFields: true,
		registry:             registry,
	}

	for _, opt := range includeUnknownFields {
		opt(e)
	}
	e.setMarshallers("")
	return e
}

func (e *Marshaller) Marshal(in any) error {
	err := json.MarshalEncode(jsontext.NewEncoder(os.Stdout), in, json.WithMarshalers(e.marshallers))
	if err != nil {
		return fmt.Errorf("marshalling and encoding block to json: %w", err)
	}
	return nil
}

func (e *Marshaller) MarshalToString(in any) (string, error) {
	buf := bytes.NewBuffer(nil)
	if err := json.MarshalEncode(jsontext.NewEncoder(buf), in, json.WithMarshalers(e.marshallers)); err != nil {
		return "", err
	}
	return buf.String(), nil

}

func (e *Marshaller) anypb(encoder *jsontext.Encoder, t *anypb.Any, options json.Options) error {
	msg, err := e.registry.Unmarshal(t)
	if err != nil {
		return fmt.Errorf("unmarshalling proto any: %w", err)
	}
	e.setMarshallers(t.TypeUrl)
	cnt, err := json.Marshal(msg, json.WithMarshalers(e.marshallers))
	if err != nil {
		return fmt.Errorf("json marshalling proto any: %w", err)
	}
	return encoder.WriteValue(cnt)
}

type kvlist []*kv
type kv struct {
	key   string
	value any
}

func (e *Marshaller) encodeKVList(encoder *jsontext.Encoder, t kvlist, options json.Options) error {
	if err := encoder.WriteToken(jsontext.ObjectStart); err != nil {
		return err
	}
	for _, kv := range t {
		if err := encoder.WriteToken(jsontext.String(kv.key)); err != nil {
			return err
		}

		cnt, err := json.Marshal(kv.value, json.WithMarshalers(e.marshallers))
		if err != nil {
			return fmt.Errorf("json marshalling of value : %w", err)
		}

		if err := encoder.WriteValue(cnt); err != nil {
			return err
		}
	}
	return encoder.WriteToken(jsontext.ObjectEnd)
}

func (e *Marshaller) dynamicpbMessage(encoder *jsontext.Encoder, msg *dynamicpb.Message, options json.Options) error {
	var kvl kvlist

	if e.includeUnknownFields {
		x := msg.GetUnknown()
		fieldNumber, ofType, l := protowire.ConsumeField(x)
		if l > 0 {
			var unknownValue []byte
			unknownValue = x[:l]
			kvl = append(kvl, &kv{
				key:   fmt.Sprintf("__unknown_fields_%d_with_type_%d__", fieldNumber, ofType),
				value: hex.EncodeToString(unknownValue),
			})
		}
	}

	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.IsList() {
			out := make([]any, v.List().Len())
			for i := 0; i < v.List().Len(); i++ {
				out[i] = v.List().Get(i).Interface()
			}
			kvl = append(kvl, &kv{
				key:   string(fd.Name()),
				value: out,
			})
			return true
		}
		kvl = append(kvl, &kv{
			key:   string(fd.Name()),
			value: v.Interface(),
		})

		return true
	})

	slices.SortFunc(kvl, func(a, b *kv) bool {
		return a.key < b.key
	})

	cnt, err := json.Marshal(kvl, json.WithMarshalers(e.marshallers))
	if err != nil {
		return fmt.Errorf("json marshalling proto any: %w", err)
	}
	return encoder.WriteValue(cnt)
}

func (e *Marshaller) base58Bytes(encoder *jsontext.Encoder, t []byte, options json.Options) error {
	return encoder.WriteToken(jsontext.String(base58.Encode(t)))
}

func (e *Marshaller) hexBytes(encoder *jsontext.Encoder, t []byte, options json.Options) error {
	return encoder.WriteToken(jsontext.String(hex.EncodeToString(t)))
}

func (e *Marshaller) setMarshallers(typeURL string) {
	out := []*json.Marshalers{
		json.MarshalFuncV2(e.anypb),
		json.MarshalFuncV2(e.dynamicpbMessage),
		json.MarshalFuncV2(e.encodeKVList),
	}

	if strings.Contains(typeURL, "solana") {
		dynamic.SetDefaultBytesRepresentation(dynamic.BytesAsBase58)
		out = append(out, json.MarshalFuncV2(e.base58Bytes))
		e.marshallers = json.NewMarshalers(out...)
		return
	}

	dynamic.SetDefaultBytesRepresentation(dynamic.BytesAsHex)
	out = append(out, json.MarshalFuncV2(e.hexBytes))
	e.marshallers = json.NewMarshalers(out...)
	return
}
