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
	"fmt"
	"io"
	"testing"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/streamingfast/firehose-core/test"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/dstore"
	"github.com/stretchr/testify/require"
)

func TestNewDstore(t *testing.T) {
	store := NewDStoreIO(
		testLogger,
		dstore.NewMockStore(nil),
		dstore.NewMockStore(nil),
		dstore.NewMockStore(nil),
		1,
		0,
		100,
		0,
		false,
	)

	_, ok := store.(ForkAwareIOInterface)
	require.True(t, ok)

	// non-fork-aware
	store = NewDStoreIO(
		testLogger,
		dstore.NewMockStore(nil),
		dstore.NewMockStore(nil),
		nil,
		1,
		0,
		100,
		0,
		false,
	)

	_, ok = store.(ForkAwareIOInterface)
	require.False(t, ok)
}

func newTestDStoreIO(
	oneBlocksStore dstore.Store,
	mergedBlocksStore dstore.Store,
) IOInterface {
	return NewDStoreIO(testLogger, oneBlocksStore, mergedBlocksStore, nil, 0, 0, 100, 0, false)
}

func makeBlockReader(t *testing.T) io.ReadCloser {
	t.Helper()
	out := new(bytes.Buffer)
	w, err := bstream.NewDBinBlockWriter(out)
	require.NoError(t, err)

	tb := &test.Block{Number: 9999}
	anyB, err := anypb.New(tb)
	require.NoError(t, err)
	err = w.Write(&pbbstream.Block{Number: 9999, Payload: anyB})
	require.NoError(t, err)
	return io.NopCloser(out)
}

func TestMergerIO_MergeUploadPerfect(t *testing.T) {
	files := []*bstream.OneBlockFile{
		block100(),
		block101(),
	}
	var mergeLastBase string
	var filesRead []string
	var mergeCounter int

	oneBlockStore := dstore.NewMockStore(nil)
	oneBlockStore.OpenObjectFunc = func(_ context.Context, name string) (io.ReadCloser, error) {
		filesRead = append(filesRead, name)
		return makeBlockReader(t), nil
	}
	mergedBlocksStore := dstore.NewMockStore(func(base string, f io.Reader) error {
		mergeLastBase = base
		mergeCounter++
		io.ReadAll(f)
		return nil
	})

	mio := newTestDStoreIO(oneBlockStore, mergedBlocksStore)

	err := mio.MergeAndStore(context.Background(), 100, files)
	require.NoError(t, err)
	require.Equal(t, 1, mergeCounter)
	require.Equal(t, "0000000100", mergeLastBase)

	// Streaming path: anyOneBlockFile (block100) opened for header, then block100 and block101 streamed.
	require.Equal(t, []string{
		"0000000100-0000000000000100a-0000000000000099a-98-suffix", // header read
		"0000000100-0000000000000100a-0000000000000099a-98-suffix", // stream block100
		"0000000101-0000000000000101a-0000000000000100a-99-suffix", // stream block101
	}, filesRead)
}

func TestMergerIO_MergeUploadFiltered(t *testing.T) {
	files := []*bstream.OneBlockFile{
		block98(),
		block99(),
		block100(),
		block101(),
	}
	var mergeLastBase string
	var filesRead []string
	var mergeCounter int

	oneBlockStore := dstore.NewMockStore(nil)
	oneBlockStore.OpenObjectFunc = func(_ context.Context, name string) (io.ReadCloser, error) {
		filesRead = append(filesRead, name)
		return makeBlockReader(t), nil
	}
	mergedBlocksStore := dstore.NewMockStore(func(base string, f io.Reader) error {
		mergeLastBase = base
		mergeCounter++
		io.ReadAll(f)
		return nil
	})

	mio := newTestDStoreIO(oneBlockStore, mergedBlocksStore)

	err := mio.MergeAndStore(context.Background(), 100, files)
	require.NoError(t, err)
	require.Equal(t, 1, mergeCounter)
	require.Equal(t, "0000000100", mergeLastBase)

	// block98 is anyOneBlockFile (opened for header only), block99 is filtered out,
	// block100 and block101 are in filteredOBF and streamed.
	require.Equal(t, []string{
		"0000000098-0000000000000098a-0000000000000097a-96-suffix", // header read
		"0000000100-0000000000000100a-0000000000000099a-98-suffix", // stream block100
		"0000000101-0000000000000101a-0000000000000100a-99-suffix", // stream block101
	}, filesRead)
}

func TestMergerIO_MergeUploadNoFiles(t *testing.T) {
	mio := newTestDStoreIO(dstore.NewMockStore(nil), dstore.NewMockStore(nil))
	err := mio.MergeAndStore(context.Background(), 114, []*bstream.OneBlockFile{})
	require.Error(t, err)
}

func TestMergerIO_MergeUploadFilteredToZero(t *testing.T) {
	b100 := block102Final100()
	b101 := block103Final101()
	files := []*bstream.OneBlockFile{b100, b101}

	b100.MemoizeData = newTestOneBlockFile(t, "b100", 100, []byte{0x08, 0x01}).MemoizeData
	b101.MemoizeData = newTestOneBlockFile(t, "b101", 101, []byte{0x08, 0x01}).MemoizeData

	oneBlockStore := dstore.NewMockStore(nil)
	oneBlockStore.OpenObjectFunc = func(_ context.Context, name string) (io.ReadCloser, error) {
		for _, obf := range files {
			for fname := range obf.Filenames {
				if fname == name {
					return io.NopCloser(bytes.NewReader(obf.MemoizeData)), nil
				}
			}
		}
		return nil, fmt.Errorf("file not found: %s", name)
	}

	mio := newTestDStoreIO(oneBlockStore, dstore.NewMockStore(nil))
	err := mio.MergeAndStore(context.Background(), 114, files)
	require.NoError(t, err)
}

func TestMergerIO_MergeUploadCorruptBlockWithValidation(t *testing.T) {
	files := []*bstream.OneBlockFile{
		block100(),
		block101(),
	}

	corrupted := newTestOneBlockFile(t, "corrupted", 100, []byte{0xff, 0xff, 0xff})

	oneBlockStore := dstore.NewMockStore(nil)
	oneBlockStore.OpenObjectFunc = func(_ context.Context, name string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(corrupted.MemoizeData)), nil
	}
	mergedBlocksStore := dstore.NewMockStore(func(base string, f io.Reader) error {
		_, err := io.ReadAll(f)
		return err
	})

	mio := NewDStoreIO(testLogger, oneBlockStore, mergedBlocksStore, nil, 0, 0, 100, 0, true)

	err := mio.MergeAndStore(context.Background(), 100, files)
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing to merge corrupted one_block_file")
	require.Contains(t, err.Error(), "invalid wire-format")
}
