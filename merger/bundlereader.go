// Copyright 2019 dfuse Platform Inc.
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

package merger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/streamingfast/bstream"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

// NewStreamingBundleReader creates an io.Reader that streams one-block-files directly from storage
// without loading them into memory. It opens each file via opener one at a time, strips DBIN headers
// from all files except the first, and pipes the concatenated output to the returned reader.
//
// When validate is true, each one-block file is instead buffered (one file at a time, so memory
// stays bounded to a single block) and checked with validateOneBlockFile before being written out,
// so that corrupted blocks are rejected instead of being propagated to the merged-blocks store.
func NewStreamingBundleReader(ctx context.Context, logger *zap.Logger, oneBlockFiles []*bstream.OneBlockFile, anyOneBlockFile *bstream.OneBlockFile, opener func(context.Context, *bstream.OneBlockFile) (io.ReadCloser, error), validate bool) (io.Reader, error) {
	// Open anyOneBlockFile just to determine the DBIN header length
	headerReader, err := opener(ctx, anyOneBlockFile)
	if err != nil {
		return nil, fmt.Errorf("cannot open one_block_file to get header: %w", err)
	}
	dbinReader, err := bstream.NewDBinBlockReader(headerReader)
	if err != nil {
		headerReader.Close()
		return nil, fmt.Errorf("creating block reader for header: %w", err)
	}
	headerBytes := dbinReader.Header.RawBytes
	headerLen := len(headerBytes)
	headerReader.Close()

	pr, pw := io.Pipe()

	go func() {
		// Write the DBIN header once at the start
		if _, err := pw.Write(headerBytes); err != nil {
			pw.CloseWithError(err)
			return
		}

		for _, obf := range oneBlockFiles {
			select {
			case <-ctx.Done():
				pw.CloseWithError(ctx.Err())
				return
			default:
			}

			r, err := opener(ctx, obf)
			if err != nil {
				pw.CloseWithError(fmt.Errorf("opening one_block_file %s: %w", obf.CanonicalName, err))
				return
			}

			if validate {
				data, err := io.ReadAll(r)
				r.Close()
				if err != nil {
					pw.CloseWithError(fmt.Errorf("reading one_block_file %s: %w", obf.CanonicalName, err))
					return
				}

				if len(data) < headerLen {
					pw.CloseWithError(fmt.Errorf("one_block_file %s is too short (%d bytes) to contain a DBIN header", obf.CanonicalName, len(data)))
					return
				}

				if err := validateOneBlockFile(data); err != nil {
					pw.CloseWithError(fmt.Errorf("refusing to merge corrupted one_block_file %s: %w", obf.CanonicalName, err))
					return
				}

				// Skip the DBIN header on each individual file
				if _, err := pw.Write(data[headerLen:]); err != nil {
					pw.CloseWithError(err)
					return
				}
				continue
			}

			// Skip the DBIN header on each individual file
			if _, err := io.CopyN(io.Discard, r, int64(headerLen)); err != nil {
				r.Close()
				pw.CloseWithError(fmt.Errorf("skipping header in one_block_file %s: %w", obf.CanonicalName, err))
				return
			}

			if _, err := io.Copy(pw, r); err != nil {
				r.Close()
				pw.CloseWithError(fmt.Errorf("streaming one_block_file %s: %w", obf.CanonicalName, err))
				return
			}
			r.Close()
		}
		pw.Close()
	}()

	return pr, nil
}

// validateOneBlockFile ensures the file contains exactly one well-formed block: the DBIN
// framing and the pbbstream.Block envelope must decode, and the block payload must be valid
// protobuf wire-format. Corrupted blocks are caught here instead of being propagated to the
// merged-blocks store, where they would poison every downstream consumer of the bundle
// (index-builder, firehose, substreams, ...).
func validateOneBlockFile(data []byte) error {
	blkReader, err := bstream.NewDBinBlockReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("reading DBIN header: %w", err)
	}

	blk, err := blkReader.Read()
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("decoding block: %w", err)
	}
	if blk == nil {
		return fmt.Errorf("file contains no block")
	}

	if len(blk.Payload.GetValue()) == 0 {
		return fmt.Errorf("block #%d (%s) has an empty payload", blk.Number, blk.Id)
	}

	// Unmarshalling into an empty message walks the whole payload wire-format without
	// requiring knowledge of the chain-specific block type, catching corruption that would
	// otherwise surface as 'proto: cannot parse invalid wire-format data' in consumers.
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(blk.Payload.Value, &emptypb.Empty{}); err != nil {
		return fmt.Errorf("block #%d (%s) payload (%s) is corrupted: %w", blk.Number, blk.Id, blk.Payload.TypeUrl, err)
	}

	if _, err := blkReader.Read(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("block #%d (%s): expected exactly one block in file, found trailing data", blk.Number, blk.Id)
	}

	return nil
}
