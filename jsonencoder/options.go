package jsonencoder

import (
	"encoding/hex"
	"fmt"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/mr-tron/base58"
)

type Option func(c *Encoder)

func WithBytesAsBase58() Option {
	return func(c *Encoder) {
		m := json.MarshalFuncV2(func(encoder *jsontext.Encoder, t []byte, options json.Options) error {
			fmt.Println("base58", hex.EncodeToString(t))
			return encoder.WriteToken(jsontext.String(hex.EncodeToString(t)))
		})
		c.marshallers = append(c.marshallers, m)
	}
}

func WithBytesAsHex() Option {
	return func(c *Encoder) {
		m := json.MarshalFuncV2(func(encoder *jsontext.Encoder, t []byte, options json.Options) error {
			fmt.Println("hex", hex.EncodeToString(t))
			return encoder.WriteToken(jsontext.String(base58.Encode(t)))
		})
		c.marshallers = append(c.marshallers, m)
	}
}
