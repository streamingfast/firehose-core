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
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	fcjson "github.com/streamingfast/firehose-core/json"
	fcproto "github.com/streamingfast/firehose-core/proto"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/known/anypb"
)

var _ OutputPrinter = (*TextOutputPrinter)(nil)

type TextOutputPrinter struct {
	bytesEncoding     string
	registry          *fcproto.Registry
	printTransactions bool
}

func NewTextOutputPrinter(bytesEncoding string, registry *fcproto.Registry, printTransactions bool) *TextOutputPrinter {
	return &TextOutputPrinter{
		bytesEncoding:     strings.ToLower(bytesEncoding),
		registry:          registry,
		printTransactions: printTransactions,
	}
}

func (p *TextOutputPrinter) PrintTo(input any, out io.Writer) error {
	if pbblock, ok := input.(*pbbstream.Block); ok {
		err := writeStringFToWriter(out, "Block #%d (%s)\n - Parent: #%d (%s)\n  - LIB: #%d\n  - Time: %s\n",
			pbblock.Number,
			pbblock.Id,
			pbblock.ParentNum,
			pbblock.ParentId,
			pbblock.LibNum,
			pbblock.Timestamp.AsTime(),
		)
		if err != nil {
			return fmt.Errorf("writing block: %w", err)
		}

		if p.printTransactions {
			if _, err = out.Write([]byte("warning: transaction printing not supported by bstream block")); err != nil {
				return fmt.Errorf("writing transaction support warning: %w", err)
			}
		}
	}

	if v, ok := input.(*pbfirehose.Response); ok {
		return p.printBlock(v.Block, out)
	}

	if v, ok := input.(*pbfirehose.SingleBlockResponse); ok {
		return p.printBlock(v.Block, out)
	}

	if v, ok := input.(proto.Message); ok {
		return p.printGenericMessage(v, "unhandled message type", out)
	}

	return writeStringFToWriter(out, "%T", input)
}

func (p *TextOutputPrinter) printBlock(anyBlock *anypb.Any, out io.Writer) error {
	block, err := anypb.UnmarshalNew(anyBlock, proto.UnmarshalOptions{Resolver: p.registry})
	if err != nil {
		if errors.Is(err, protoregistry.NotFound) {
			return writeStringFToWriter(out, "Protobuf %s (not found in registry)", getAnyTypeID(anyBlock))
		}

		return fmt.Errorf("unmarshalling block: %w", err)
	}

	var hash, parentHash, libHash *string
	var number, parentNumber, libNumber *uint64

	// FIXME: Add timestamp
	var fieldsExtractor func(message protoreflect.Message)
	fieldsExtractor = func(message protoreflect.Message) {
		fields := message.Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			field := fields.Get(i)
			fieldName := field.Name()

			switch {
			case isField(fieldName, blockHashFields):
				hash = p.extractHashFromField(field, message)
			case isField(fieldName, parentBlockHashFields):
				parentHash = p.extractHashFromField(field, message)
			case isField(fieldName, libBlockHashFields):
				libHash = p.extractHashFromField(field, message)

			case isField(fieldName, blockNumberFields):
				number = p.extractNumberFromField(field, message)
			case isField(fieldName, parentBlockNumberFields):
				parentNumber = p.extractNumberFromField(field, message)
			case isField(fieldName, libBlockNumberFields):
				libNumber = p.extractNumberFromField(field, message)

			case isField(fieldName, blockHeaderFields) && field.Kind() == protoreflect.MessageKind:
				fieldsExtractor(message.Get(field).Message())
			}
		}
	}

	fieldsExtractor(block.ProtoReflect())

	var parts []string
	if number != nil || hash != nil {
		parts = append(parts, fmt.Sprintf("Block #%s (%s)", uint64PtrToString(number, "N/A"), deref(hash, "N/A")))
	}

	if parentNumber != nil || parentHash != nil {
		parentNumberValue := "N/A"
		if parentNumber != nil {
			parentNumberValue = strconv.FormatUint(*parentNumber, 10)
		} else if number != nil {
			parentNumberValue = maybeDeriveParentNumber(block, *number)
		}

		parts = append(parts, fmt.Sprintf("Parent #%s (%s)", parentNumberValue, deref(parentHash, "N/A")))
	}

	if libNumber != nil || libHash != nil {
		parts = append(parts, fmt.Sprintf("LIB #%s (%s)", uint64PtrToString(libNumber, "N/A"), deref(libHash, "N/A")))
	}

	if len(parts) > 0 {
		if _, err := out.Write([]byte(strings.Join(parts, ", "))); err != nil {
			return fmt.Errorf("writing block parts: %w", err)
		}

		return nil
	}

	return p.printGenericMessage(block, "unable to extract any block info", out)
}

func (p *TextOutputPrinter) extractHashFromField(field protoreflect.FieldDescriptor, message protoreflect.Message) *string {
	if field.Kind() == protoreflect.StringKind {
		return ptr(message.Get(field).String())
	}

	if field.Kind() == protoreflect.BytesKind {
		bytes := message.Get(field).Bytes()
		return ptr(fcjson.Encode(p.bytesEncoding, bytes))
	}

	value := message.Get(field)
	if !value.IsValid() {
		return ptr("<nil>")
	}

	return ptr(value.String())
}

func (p *TextOutputPrinter) extractNumberFromField(field protoreflect.FieldDescriptor, message protoreflect.Message) *uint64 {
	kind := field.Kind()
	if kind == protoreflect.Uint64Kind || kind == protoreflect.Uint32Kind || kind == protoreflect.Fixed64Kind || kind == protoreflect.Fixed32Kind {
		return ptr(message.Get(field).Uint())
	}

	if kind == protoreflect.Int64Kind || kind == protoreflect.Int32Kind || kind == protoreflect.Sfixed32Kind || kind == protoreflect.Sfixed64Kind || kind == protoreflect.Sint32Kind || kind == protoreflect.Sint64Kind {
		return ptr(uint64(message.Get(field).Int()))
	}

	return nil
}

func (p *TextOutputPrinter) printGenericMessage(message proto.Message, suffix string, out io.Writer) error {
	format := "Protobuf %s"
	args := []any{message.ProtoReflect().Descriptor().FullName()}

	if suffix != "" {
		format += " (%s)"
		args = append(args, suffix)
	}

	return writeStringFToWriter(out, format, args...)
}

var blockHashFields = []string{"hash", "id", "block_hash", "blockhash"}
var blockNumberFields = []string{
	"number", "block_number", "blocknumber",
	"num", "block_num", "blocknum",
}

var parentBlockNumberFields = []string{
	"parent_number", "parentnumber",
	"parent_block_number", "parentblocknumber",
	"parent_num", "parent_num",
	"parent_block_num", "parent_blocknum",
}
var parentBlockHashFields = []string{
	"parent_hash", "parenthash", "parent_block_hash", "parentblockhash",
	"previous_hash", "previoushash", "previous_block_hash", "previousblockhash",
	"parent_id", "parentid",
}

var libBlockHashFields = []string{
	"final_hash", "finalhash",
	"lib_hash", "libblockhash",
}
var libBlockNumberFields = []string{
	"final_number", "finalnumber",
	"lib_number", "lib_hash",
}

var blockHeaderFields = []string{
	"header", "block_header", "blockheader",
}

func isField(fieldShortName protoreflect.Name, candidates []string) bool {
	return slices.Contains(candidates, strings.ToLower(string(fieldShortName)))
}

func maybeDeriveParentNumber(block protoreflect.ProtoMessage, field uint64) string {
	if strings.Contains(string(block.ProtoReflect().Descriptor().FullName()), "sf.ethereum") {
		if field == 0 {
			return "None"
		}

		return strconv.FormatUint(field-1, 10)
	}

	return "N/A"
}

func getAnyTypeID(value *anypb.Any) string {
	return strings.ReplaceAll(value.GetTypeUrl(), "type.googleapis.com/", "")
}
