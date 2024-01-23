package jsonencoder

import (
	"encoding/hex"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/dynamicpb"

	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/jhump/protoreflect/dynamic"
	"github.com/mr-tron/base58"
	"github.com/streamingfast/firehose-core/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"
)

func (e *Encoder) anypb(encoder *jsontext.Encoder, t *anypb.Any, options json.Options) error {
	msg, err := protoregistry.Unmarshal(t)
	if err != nil {
		return fmt.Errorf("unmarshalling proto any: %w", err)
	}
	cnt, err := json.Marshal(msg, json.WithMarshalers(e.getMarshallers(t.TypeUrl)))
	if err != nil {
		return fmt.Errorf("json marshalling proto any: %w", err)
	}
	return encoder.WriteValue(cnt)
}

func (e *Encoder) dynamicpbMessage(encoder *jsontext.Encoder, msg *dynamicpb.Message, options json.Options) error {
	mapMsg := map[string]any{}

	//mapMsg["__unknown_fields__"] = hex.EncodeToString(msg.GetUnknown())

	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.IsList() {
			out := make([]any, v.List().Len())
			for i := 0; i < v.List().Len(); i++ {
				out[i] = v.List().Get(i).Interface()
			}
			mapMsg[string(fd.Name())] = out
			return true
		}
		mapMsg[string(fd.Name())] = v.Interface()
		return true
	})

	cnt, err := json.Marshal(mapMsg, json.WithMarshalers(e.getMarshallers("solana")))
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

func (e *Encoder) getMarshallers(typeURL string) *json.Marshalers {
	out := []*json.Marshalers{
		json.MarshalFuncV2(e.anypb),
		json.MarshalFuncV2(e.dynamicpbMessage),
	}

	if strings.Contains(typeURL, "solana") {
		dynamic.SetDefaultBytesRepresentation(dynamic.BytesAsBase58)
		out = append(out, json.MarshalFuncV2(e.base58Bytes))
		return json.NewMarshalers(out...)
	}

	dynamic.SetDefaultBytesRepresentation(dynamic.BytesAsHex)
	out = append(out, json.MarshalFuncV2(e.hexBytes))
	return json.NewMarshalers(out...)
}
