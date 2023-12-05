package jsonencoder

import (
	"fmt"
	"os"

	"github.com/streamingfast/firehose-core/protoregistry"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

type Encoder struct {
	e           *jsontext.Encoder
	files       *protoregistry.Files
	marshallers []*json.Marshalers
}

func New(files *protoregistry.Files, opts ...Option) *Encoder {
	e := &Encoder{
		e:     jsontext.NewEncoder(os.Stdout),
		files: files,
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
	return json.MarshalEncode(e.e, in, json.WithMarshalers(json.NewMarshalers(e.marshallers...)))
}

func (e *Encoder) MarshalLegacy(typeURL string, value []byte) error {
	msg, err := e.files.Unmarshall(typeURL, value)
	if err != nil {
		return fmt.Errorf("unmarshalling proto any: %w", err)
	}

	return json.MarshalEncode(e.e, msg, json.WithMarshalers(json.NewMarshalers(e.marshallers...)))
}