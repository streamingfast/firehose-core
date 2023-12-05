package jsonencoder

import (
	"bytes"
	"os"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/streamingfast/firehose-core/protoregistry"
)

type Encoder struct {
	protoRegistry *protoregistry.Registry
	marshallers   []*json.Marshalers
}

func New(files *protoregistry.Registry, opts ...Option) *Encoder {
	e := &Encoder{
		protoRegistry: files,
	}

	e.marshallers = []*json.Marshalers{
		json.MarshalFuncV2(e.protoAny),
	}

	for _, opt := range opts {
		opt(e)
	}
	return e
}

func (e *Encoder) Marshal(in any) error {
	return json.MarshalEncode(jsontext.NewEncoder(os.Stdout), in, json.WithMarshalers(json.NewMarshalers(e.marshallers...)))
}

func (e *Encoder) MarshalToString(in any) (string, error) {
	buf := bytes.NewBuffer(nil)
	if err := json.MarshalEncode(jsontext.NewEncoder(buf), in, json.WithMarshalers(json.NewMarshalers(e.marshallers...))); err != nil {
		return "", err
	}
	return buf.String(), nil

}
