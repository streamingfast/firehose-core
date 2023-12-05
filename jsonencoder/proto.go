package jsonencoder

import (
	"fmt"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"google.golang.org/protobuf/types/known/anypb"
)

func (e *Encoder) protoAny(encoder *jsontext.Encoder, t *anypb.Any, options json.Options) error {
	msg, err := e.files.Unmarshall(t.TypeUrl, t.Value)
	if err != nil {
		return fmt.Errorf("unmarshalling proto any: %w", err)
	}
	cnt, err := json.Marshal(msg, json.WithMarshalers(json.NewMarshalers(e.marshallers...)))
	if err != nil {
		return fmt.Errorf("json marshalling proto any: %w", err)
	}
	return encoder.WriteValue(cnt)
}
