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
	"strconv"
	"unsafe"

	"github.com/spf13/cobra"
	"github.com/streamingfast/cli/sflags"
	fcproto "github.com/streamingfast/firehose-core/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func GetOutputPrinter(cmd *cobra.Command, chainFileDescriptor protoreflect.FileDescriptor) (OutputPrinter, error) {
	printer := sflags.MustGetString(cmd, "output")
	if printer == "" {
		printer = "jsonl"
	}

	var protoPaths []string
	if sflags.FlagDefined(cmd, "proto-paths") {
		protoPaths = sflags.MustGetStringSlice(cmd, "proto-paths")
	}

	bytesEncoding := "hex"
	if sflags.FlagDefined(cmd, "bytes-encoding") {
		bytesEncoding = sflags.MustGetString(cmd, "bytes-encoding")
	}

	registry, err := fcproto.NewRegistry(chainFileDescriptor, protoPaths...)
	if err != nil {
		return nil, fmt.Errorf("new registry: %w", err)
	}

	if printer == "json" || printer == "jsonl" {
		jsonPrinter, err := NewJSONOutputPrinter(bytesEncoding, printer == "jsonl", registry)
		if err != nil {
			return nil, fmt.Errorf("unable to create json encoder: %w", err)
		}

		return jsonPrinter, nil
	}

	if printer == "protojson" || printer == "protojsonl" {
		indent := ""
		if printer == "protojson" {
			indent = "  "
		}

		return NewProtoJSONOutputPrinter(indent, registry), nil
	}

	if printer == "text" {
		// Supports the `transactions` flag defined on `firecore tools print` sub-command,
		// we should move it to a proper `text` sub-option like `output-text-details` or something
		// like that.
		printTransactions := false
		if sflags.FlagDefined(cmd, "transactions") {
			printTransactions = sflags.MustGetBool(cmd, "transactions")
		}

		return NewTextOutputPrinter(bytesEncoding, registry, printTransactions), nil
	}

	return nil, fmt.Errorf("unsupported output printer %q", printer)
}

//go:generate go-enum -f=$GOFILE --marshal --names --nocase

// ENUM(Text, JSON, JSONL, ProtoJSON, ProtoJSONL)
type PrintOutputMode uint

type OutputPrinter interface {
	PrintTo(message any, w io.Writer) error
}

func writeStringToWriter(w io.Writer, str string) error {
	return writeBytesToWriter(w, unsafe.Slice(unsafe.StringData(str), len(str)))
}

func writeStringFToWriter(w io.Writer, format string, args ...any) error {
	return writeStringToWriter(w, fmt.Sprintf(format, args...))
}

func writeBytesToWriter(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err != nil {
		return err
	}

	if n != len(data) {
		return io.ErrShortWrite
	}

	return nil
}

func ptr[T any](v T) *T {
	return &v
}

func deref[T any](v *T, orDefault T) T {
	if v == nil {
		return orDefault
	}

	return *v
}

func uint64PtrToString(v *uint64, orDefault string) string {
	if v == nil {
		return orDefault
	}

	return strconv.FormatUint(*v, 10)
}
