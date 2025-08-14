// Copyright 2021 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package print

import (
	"fmt"
	"io"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	fcjson "github.com/streamingfast/firehose-core/json"
	fcproto "github.com/streamingfast/firehose-core/proto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

var _ OutputPrinter = (*JSONOutputPrinter)(nil)

type JSONOutputPrinter struct {
	singleLine bool
	registry   *fcproto.Registry
	marshaller *fcjson.Marshaller
}

func NewJSONOutputPrinter(bytesEncoding string, singleLine bool, registry *fcproto.Registry) (OutputPrinter, error) {
	var options []fcjson.MarshallerOption

	if bytesEncoding != "" {
		options = append(options, fcjson.WithBytesEncoding(bytesEncoding))
	}

	return &JSONOutputPrinter{
		singleLine: singleLine,
		registry:   registry,
		marshaller: fcjson.NewMarshaller(registry, options...),
	}, nil
}

func (p *JSONOutputPrinter) PrintTo(input any, w io.Writer) error {
	var encoderOptions []json.Options
	if !p.singleLine {
		encoderOptions = append(encoderOptions, jsontext.WithIndent("  "))
	}

	if block, ok := input.(*pbbstream.Block); ok {
		// Special case for Block, we want to print it as a Block
		unmarshalled, err := anypb.UnmarshalNew(block.Payload, proto.UnmarshalOptions{
			Resolver: p.registry,
		})
		if err == nil {
			input = unmarshalled
		}
	}

	out, err := p.marshaller.MarshalToString(input, encoderOptions...)
	if err != nil {
		return fmt.Errorf("marshalling block to json: %w", err)
	}

	return writeStringToWriter(w, out)
}
