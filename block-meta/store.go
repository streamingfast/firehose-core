package blockmeta

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/logging"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

type Store struct {
	metaDataStore    dstore.Store
	lastWrittenStore dstore.Store

	logger *zap.Logger
	tracer logging.Tracer
}

func NewStore(metaDataStoreURL string, logger *zap.Logger, tracer logging.Tracer) (*Store, error) {
	// We must have overwrite: true (last argument) because we are writing the same file
	// multiple time potentially. Indeed, the filename in some case will be the block number
	// which is not unique, so we must have the ability to overwrite the file.
	metaDataStore, err := dstore.NewStore(metaDataStoreURL, "binpb", "", true)
	if err != nil {
		return nil, fmt.Errorf("new meta data store: %w", err)
	}

	lastWrittenStore, err := dstore.NewStore(metaDataStoreURL, "txt", "", true)
	if err != nil {
		return nil, fmt.Errorf("new meta data store: %w", err)
	}

	return &Store{
		metaDataStore:    metaDataStore,
		lastWrittenStore: lastWrittenStore,
		logger:           logger,
		tracer:           tracer,
	}, nil
}

// GetBlockMetaByNumber returns the block meta for the given block number. If the block number
// cannot be found in the store, it returns ErrBlockNotFound.
func (s *Store) GetBlockMetaByNumber(ctx context.Context, blockNum uint64) (*pbbstream.BlockMeta, error) {
	return s.readBlockMeta(ctx, s.blockMetaByNumFilename(blockNum))
}

// GetBlockMetaByHash returns the block meta for the given block hash. If the block hash
// cannot be found in the store, it returns ErrBlockNotFound.
func (s *Store) GetBlockMetaByHash(ctx context.Context, blockHash string) (*pbbstream.BlockMeta, error) {
	return s.readBlockMeta(ctx, s.blockMetaByHashFilename(blockHash))
}

func (s *Store) WriteBlockMeta(ctx context.Context, blockMeta *pbbstream.BlockMeta) error {
	if len(blockMeta.Id) < 8 {
		return fmt.Errorf("block meta id must be at least 8 characters long, got %q", blockMeta.Id)
	}

	data, err := proto.Marshal(blockMeta)
	if err != nil {
		return fmt.Errorf("marshal block meta: %w", err)
	}

	err = s.metaDataStore.WriteObject(ctx, s.blockMetaByNumFilename(blockMeta.Number), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("write by_number object: %w", err)
	}

	err = s.metaDataStore.WriteObject(ctx, s.blockMetaByHashFilename(blockMeta.Id), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("write by_hash object: %w", err)
	}

	lastWrittenBuffer := bytes.NewBuffer(nil)
	// Operation is infallible, so we don't need to check for errors
	_, _ = lastWrittenBuffer.WriteString(strconv.FormatUint(blockMeta.Number, 10))

	err = s.lastWrittenStore.WriteObject(ctx, s.lastWrittenFilename(), lastWrittenBuffer)
	if err != nil {
		return fmt.Errorf("write last written block num: %w", err)
	}

	return nil
}

func (s *Store) GetLastWrittenBlockNum(ctx context.Context) (uint64, error) {
	reader, err := s.lastWrittenStore.OpenObject(ctx, s.lastWrittenFilename())
	if err != nil {
		if errors.Is(dstore.ErrNotFound, err) {
			return 0, nil
		}

		return 0, fmt.Errorf("open object: %w", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return 0, fmt.Errorf("read object: %w", err)
	}

	return strconv.ParseUint(string(data), 10, 64)
}

func (s *Store) readBlockMeta(ctx context.Context, filename string) (out *pbbstream.BlockMeta, err error) {
	s.logger.Debug("reading block meta", zap.String("filename", filename))
	defer s.logger.Debug("done reading block meta", zap.String("filename", filename), zap.Bool("result_found", out != nil), zap.NamedError("result_err", err))

	reader, err := s.metaDataStore.OpenObject(ctx, filename)
	if err != nil {
		if errors.Is(dstore.ErrNotFound, err) {
			return nil, ErrBlockNotFound
		}

		return nil, fmt.Errorf("open object: %w", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read object: %w", err)
	}

	m := &pbbstream.BlockMeta{}
	return m, proto.Unmarshal(data, m)
}

func (s *Store) blockMetaByNumFilename(blockNum uint64) string {
	return path.Join("by_number", s.partionnedFilename(fmt.Sprintf("%010d", blockNum)))
}

func (s *Store) blockMetaByHashFilename(blockHash string) string {
	return path.Join("by_hash", s.partionnedFilename(blockHash))
}

func (s *Store) partionnedFilename(filename string) string {
	if len(filename) < 2 {
		return filename
	}

	if len(filename) < 4 {
		return path.Join(filename[0:2], filename)
	}

	return path.Join(filename[0:2], filename[2:4], filename)
}

func (s *Store) lastWrittenFilename() string {
	return "last_written"
}
