package jsonencoder

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/jhump/protoreflect/dynamic"

	"github.com/mr-tron/base58"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"google.golang.org/protobuf/types/known/anypb"
)

func (e *Encoder) anypb(encoder *jsontext.Encoder, t *anypb.Any, options json.Options) error {
	msg, err := e.protoRegistry.Unmarshall(t)
	if err != nil {
		return fmt.Errorf("unmarshalling proto any: %w", err)
	}
	setBytesEncoder(t.TypeUrl)
	cnt, err := json.Marshal(msg, json.WithMarshalers(json.NewMarshalers(e.marshallers...)))
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

func setBytesEncoder(typeURL string) {
	if strings.Contains(typeURL, "solana") {
		dynamic.SetDefaultBytesRepresentation(dynamic.BytesAsBase58)
		return
	}
	dynamic.SetDefaultBytesRepresentation(dynamic.BytesAsHex)
}
