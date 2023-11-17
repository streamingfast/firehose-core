package firecore

import (
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/firehose-core/node-manager/mindreader"
	"github.com/streamingfast/logging"
	pbbstream "github.com/streamingfast/pbgo/sf/bstream/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const FirePrefix = "FIRE "
const FirePrefixLen = len(FirePrefix)
const InitLogPrefix = "INIT "
const InitLogPrefixLen = len(InitLogPrefix)
const BlockLogPrefix = "BLOCK "
const BlockLogPrefixLen = len(BlockLogPrefix)

type parseCtx struct {
	readerProtocolVersion string
	protoMessageType      string
}

type ConsoleReader struct {
	lines  chan string
	close  func()
	done   chan interface{}
	logger *zap.Logger
	tracer logging.Tracer
	ctx    *parseCtx
}

func NewConsoleReader(lines chan string, blockEncoder BlockEncoder, logger *zap.Logger, tracer logging.Tracer) (mindreader.ConsolerReader, error) {
	reader := &ConsoleReader{
		lines:  lines,
		close:  func() {},
		done:   make(chan interface{}),
		ctx:    &parseCtx{},
		logger: logger,
		tracer: tracer,
	}
	return reader, nil
}

func (r *ConsoleReader) Done() <-chan interface{} {
	return r.done
}

func (r *ConsoleReader) ReadBlock() (out *pbbstream.Block, err error) {
	out, err = r.next()
	if err != nil {
		return nil, err
	}

	return out, nil
}

func (r *ConsoleReader) next() (out *pbbstream.Block, err error) {

	for line := range r.lines {
		if !strings.HasPrefix(line, "FIRE ") {
			continue
		}

		line = line[FirePrefixLen:]

		switch {
		case strings.HasPrefix(line, InitLogPrefix):
			err = r.ctx.readInit(line[InitLogPrefixLen:])
		case strings.HasPrefix(line, BlockLogPrefix):
			out, err = r.ctx.readBlock(line[BlockLogPrefixLen:])
		default:
			if r.tracer.Enabled() {
				r.logger.Debug("skipping unknown Firehose log line", zap.String("line", line))
			}
			continue
		}

		if err != nil {
			chunks := strings.SplitN(line, " ", 2)
			return nil, fmt.Errorf("%s: %s (line %q)", chunks[0], err, line)
		}

		if out != nil {
			return out, nil
		}
	}

	r.logger.Info("lines channel has been closed")
	close(r.done)
	return nil, io.EOF
}

// Formats
// [block_num:342342342] [block_hash] [parent_num] [parent_hash] [lib:123123123] [timestamp:unix_nano] B64ENCODED_any
func (ctx *parseCtx) readBlock(line string) (out *pbbstream.Block, err error) {
	chunks, err := SplitInBoundedChunks(line, 7)
	if err != nil {
		return nil, fmt.Errorf("splitting block log line: %w", err)
	}

	blockNum, err := strconv.ParseUint(chunks[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing block num %q: %w", chunks[0], err)
	}

	blockHash := chunks[1]

	parentNum, err := strconv.ParseUint(chunks[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing parent num %q: %w", chunks[2], err)
	}

	parentHash := chunks[3]

	libNum, err := strconv.ParseUint(chunks[4], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing lib num %q: %w", chunks[4], err)
	}

	timestampUnixNano, err := strconv.ParseUint(chunks[5], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing timestamp %q: %w", chunks[5], err)
	}

	timestamp := time.Unix(0, int64(timestampUnixNano))

	payload, err := base64.StdEncoding.DecodeString(chunks[6])

	blockPayload := &anypb.Any{}
	if err := proto.Unmarshal(payload, blockPayload); err != nil {
		return nil, fmt.Errorf("unmarshaling block payload: %w", err)
	}

	typeChunks := strings.Split(blockPayload.TypeUrl, "/")
	payloadType := typeChunks[len(typeChunks)-1]
	if payloadType != ctx.protoMessageType {
		return nil, fmt.Errorf("invalid payload type, expected %q, got %q", ctx.protoMessageType, blockPayload.TypeUrl)
	}

	block := &bstream.Block{
		Id:        blockHash,
		Number:    blockNum,
		ParentId:  parentHash,
		ParentNum: parentNum,
		Timestamp: timestamppb.New(timestamp),
		LibNum:    libNum,
		Payload:   blockPayload,
	}

	return block, nil
}

// [READER_PROTOCOL_VERSION] sf.ethereum.type.v2.Block
func (ctx *parseCtx) readInit(line string) error {
	chunks, err := SplitInBoundedChunks(line, 2)
	if err != nil {
		return fmt.Errorf("split: %s", err)
	}

	ctx.readerProtocolVersion = chunks[0]
	ctx.protoMessageType = chunks[1]

	return nil
}

// SplitInBoundedChunks splits the line in `count` chunks and returns the slice `chunks[1:count]` (so exclusive end),
// but will accumulate all trailing chunks within the last (for free-form strings, or JSON objects)
func SplitInBoundedChunks(line string, count int) ([]string, error) {
	chunks := strings.SplitN(line, " ", count)
	if len(chunks) != count {
		return nil, fmt.Errorf("%d fields required but found %d fields for line %q", count, len(chunks), line)
	}

	return chunks, nil
}
