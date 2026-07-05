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
	"context"
	"fmt"
	"io"

	"github.com/streamingfast/bstream"
	"go.uber.org/zap"
)

// NewStreamingBundleReader creates an io.ReadCloser that streams one-block-files directly from storage
// without loading them into memory. It opens each file via opener one at a time, strips DBIN headers
// from all files except the first, and pipes the concatenated output to the returned reader.
//
// The caller must Close the returned reader: if the consumer stops reading before EOF (e.g. a
// store WriteObject timing out or erroring), closing unblocks the feeding goroutine so it can
// release its open one-block reader and exit.
func NewStreamingBundleReader(ctx context.Context, logger *zap.Logger, oneBlockFiles []*bstream.OneBlockFile, anyOneBlockFile *bstream.OneBlockFile, opener func(context.Context, *bstream.OneBlockFile) (io.ReadCloser, error)) (io.ReadCloser, error) {
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
