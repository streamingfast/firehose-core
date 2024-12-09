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

	fcproto "github.com/streamingfast/firehose-core/proto"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var _ OutputPrinter = (*ProtoJSONOutputPrinter)(nil)

type ProtoJSONOutputPrinter struct {
	marshaller protojson.MarshalOptions
}

func NewProtoJSONOutputPrinter(indent string, registry *fcproto.Registry) *ProtoJSONOutputPrinter {
	return &ProtoJSONOutputPrinter{
		marshaller: protojson.MarshalOptions{
			Resolver:          registry,
			Indent:            indent,
			EmitDefaultValues: true,
		},
	}
}

func (p *ProtoJSONOutputPrinter) PrintTo(input any, w io.Writer) error {
	v, ok := input.(proto.Message)
	if !ok {
		return fmt.Errorf("we accept only proto.Message input")
	}

	out, err := p.marshaller.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshalling block to protojson: %w", err)
	}

	return writeBytesToWriter(w, out)
}
