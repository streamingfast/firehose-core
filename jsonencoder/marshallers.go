package jsonencoder

import (
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/exp/slices"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/mr-tron/base58"
	"github.com/streamingfast/firehose-core/protoregistry"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"
)

func (e *Encoder) anypb(encoder *jsontext.Encoder, t *anypb.Any, options json.Options) error {
	msg, err := protoregistry.Unmarshal(t)
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

func (e *Encoder) encodeKVList(encoder *jsontext.Encoder, t kvlist, options json.Options) error {
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

func (e *Encoder) dynamicpbMessage(encoder *jsontext.Encoder, msg *dynamicpb.Message, options json.Options) error {
	var kvl kvlist

	if e.IncludeUnknownFields {
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

func (e *Encoder) base58Bytes(encoder *jsontext.Encoder, t []byte, options json.Options) error {
	return encoder.WriteToken(jsontext.String(base58.Encode(t)))
}

func (e *Encoder) hexBytes(encoder *jsontext.Encoder, t []byte, options json.Options) error {
	return encoder.WriteToken(jsontext.String(hex.EncodeToString(t)))
}

func (e *Encoder) setMarshallers(typeURL string) {
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
