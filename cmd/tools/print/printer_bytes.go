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
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/mr-tron/base58"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	fcproto "github.com/streamingfast/firehose-core/proto"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"google.golang.org/protobuf/proto"
)

var _ OutputPrinter = (*BytesOutputPrinter)(nil)

type BytesOutputPrinter struct {
	bytesEncoding string
	registry      *fcproto.Registry
}

func NewBytesOutputPrinter(bytesEncoding string, registry *fcproto.Registry) *BytesOutputPrinter {
	return &BytesOutputPrinter{
		bytesEncoding: bytesEncoding,
		registry:      registry,
	}
}

func (p *BytesOutputPrinter) PrintTo(input any, out io.Writer) error {
	if pbblock, ok := input.(*pbbstream.Block); ok {
		return p.printBytes(pbblock.Payload.Value, out)
	}

	if v, ok := input.(*pbfirehose.Response); ok {
		return p.printBytes(v.Block.Value, out)
	}

	if v, ok := input.(*pbfirehose.SingleBlockResponse); ok {
		return p.printBytes(v.Block.Value, out)
	}

	if v, ok := input.(proto.Message); ok {
		data, err := proto.Marshal(v)
		if err != nil {
			return fmt.Errorf("unable to marshal proto message: %w", err)
		}

		return p.printBytes(data, out)
	}

	return fmt.Errorf("unsupported type %T", input)
}

var base64Encoding = base64.StdEncoding

func (p *BytesOutputPrinter) printBytes(data []byte, out io.Writer) error {
	var err error
	switch p.bytesEncoding {
	case "hex":
		err = p.printBytesHex(data, out)
	case "base58":
		err = p.printBytesBase58(data, out)
	case "base64":
		err = p.printBytesBase64(data, out)
	default:
		return fmt.Errorf("unsupported bytes encoding %q", p.bytesEncoding)
	}
	if err != nil {
		return fmt.Errorf("unable to print bytes: %w", err)
	}

	return writeStringToWriter(out, "")
}

func (p *BytesOutputPrinter) printBytesHex(data []byte, out io.Writer) error {
	encoder := hex.NewEncoder(out)
	_, err := encoder.Write(data)
	return err
}

func (p *BytesOutputPrinter) printBytesBase58(data []byte, out io.Writer) error {
	return writeStringToWriter(out, base58.Encode(data))
}

func (p *BytesOutputPrinter) printBytesBase64(data []byte, out io.Writer) error {
	encoder := base64.NewEncoder(base64Encoding, out)
	// This flushes the base64 encoder but doesn't close the underlying writer, which is
	// what we want here
	defer encoder.Close()

	_, err := encoder.Write(data)
	return err
}
