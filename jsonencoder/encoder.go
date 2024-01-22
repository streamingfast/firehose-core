package jsonencoder

import (
	"bytes"
	"os"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

type Encoder struct {
}

func New() *Encoder {
	return &Encoder{}
}

func (e *Encoder) Marshal(in any) error {
	return json.MarshalEncode(jsontext.NewEncoder(os.Stdout), in, json.WithMarshalers(e.getMarshallers("")))
}

func (e *Encoder) MarshalToString(in any) (string, error) {
	buf := bytes.NewBuffer(nil)
	if err := json.MarshalEncode(jsontext.NewEncoder(buf), in, json.WithMarshalers(e.getMarshallers(""))); err != nil {
		return "", err
	}
	return buf.String(), nil

}
