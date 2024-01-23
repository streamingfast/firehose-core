package jsonencoder

import (
	"bytes"
	"fmt"
	"os"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

type Encoder struct {
	marshallers *json.Marshalers
}

func New() *Encoder {
	e := &Encoder{}
	e.setMarshallers("")
	return e
}

func (e *Encoder) Marshal(in any) error {
	err := json.MarshalEncode(jsontext.NewEncoder(os.Stdout), in, json.WithMarshalers(e.marshallers))
	if err != nil {
		return fmt.Errorf("marshalling and encoding block to json: %w", err)
	}
	return nil
}

func (e *Encoder) MarshalToString(in any) (string, error) {
	buf := bytes.NewBuffer(nil)
	if err := json.MarshalEncode(jsontext.NewEncoder(buf), in, json.WithMarshalers(e.marshallers)); err != nil {
		return "", err
	}
	return buf.String(), nil

}
