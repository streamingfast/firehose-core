package firehose_reader

import (
	"context"
	"errors"
	"fmt"
	"github.com/mostynb/go-grpc-compression/zstd"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/firehose-core/firehose/client"
	"github.com/streamingfast/firehose-core/node-manager/mindreader"
	pbfirehose "github.com/streamingfast/pbgo/sf/firehose/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/encoding/gzip"
	"os"
	"time"
)

type FirehoseReader struct {
	firehoseClient  pbfirehose.StreamClient
	firehoseStream  pbfirehose.Stream_BlocksClient
	closeFunc       func() error
	callOpts        []grpc.CallOption
	zlogger         *zap.Logger
	cursorStateFile string
	cursor          string
	stats           *firehoseReaderStats
}

func NewFirehoseReader(config FirehoseConfig, zlogger *zap.Logger) (*FirehoseReader, error) {

	firehoseClient, closeFunc, callOpts, err := client.NewFirehoseClient(config.Endpoint, config.Jwt, config.ApiKey, config.InsecureConn, config.PlaintextConn)
	if err != nil {
		return nil, err
	}

	switch config.Compression {
	case "gzip":
		callOpts = append(callOpts, grpc.UseCompressor(gzip.Name))
	case "zstd":
		callOpts = append(callOpts, grpc.UseCompressor(zstd.Name))
	case "none":
	default:
		return nil, fmt.Errorf("invalid compression: %q, must be one of 'gzip', 'zstd' or 'none'", config.Compression)
	}

	res := &FirehoseReader{
		firehoseClient: firehoseClient,
		closeFunc:      closeFunc,
		callOpts:       callOpts,
		zlogger:        zlogger,
		stats:          newFirehoseReaderStats(),
	}

	return res, nil
}

func (f *FirehoseReader) Launch(startBlock, stopBlock uint64, cursorFile string) error {

	cursor, err := os.ReadFile(cursorFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("unable to read cursor file: %w", err)
	}

	if len(cursor) > 0 {
		f.zlogger.Info("found state file, continuing previous run", zap.String("cursor", string(cursor)), zap.String("state_file", cursorFile))
	}

	stream, err := f.firehoseClient.Blocks(context.Background(), &pbfirehose.Request{
		StartBlockNum:   int64(startBlock),
		Cursor:          string(cursor),
		StopBlockNum:    stopBlock,
		FinalBlocksOnly: false,
	}, f.callOpts...)
	if err != nil {
		return fmt.Errorf("failed to request block stream from Firehose: %w", err)
	}

	f.firehoseStream = stream
	f.cursorStateFile = cursorFile
	f.stats.StartPeriodicLogToZap(context.Background(), f.zlogger, 10*time.Second)

	return nil
}

func (f *FirehoseReader) NoopConsoleReader(_ chan string) (mindreader.ConsolerReader, error) {
	return f, nil
}

func (f *FirehoseReader) ReadBlock() (obj *pbbstream.Block, err error) {

	res, err := f.firehoseStream.Recv()
	if err != nil {
		return nil, err
	}

	// We don't write the current cursor here, but the one from the previous block. In case an error happens downstream,
	// we need to ensure that the current block is included after a restart.
	err = f.writeCursor()
	if err != nil {
		return nil, err
	}
	f.cursor = res.Cursor

	BlockReadCount.Inc()
	f.stats.lastBlock = pbbstream.BlockRef{
		Num: res.Metadata.Num,
		Id:  res.Metadata.Id,
	}

	return &pbbstream.Block{
		Number:    res.Metadata.Num,
		Id:        res.Metadata.Id,
		ParentId:  res.Metadata.ParentId,
		Timestamp: res.Metadata.Time,
		LibNum:    res.Metadata.LibNum,
		ParentNum: res.Metadata.ParentNum,
		Payload:   res.Block,
	}, nil
}

func (f *FirehoseReader) Done() <-chan interface{} {
	//TODO implement me
	panic("implement me")
}

func (f *FirehoseReader) Close() error {
	_ = f.writeCursor()
	f.stats.StopPeriodicLogToZap()
	return f.closeFunc()
}

func (f *FirehoseReader) writeCursor() error {
	if f.cursor == "" {
		return nil
	}

	err := os.WriteFile(f.cursorStateFile, []byte(f.cursor), 0644)
	if err != nil {
		return fmt.Errorf("failed to write cursor to state file: %w", err)
	}

	return nil
}
