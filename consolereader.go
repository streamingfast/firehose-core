package firecore

import (
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dmetrics"
	"github.com/streamingfast/firehose-core/node-manager/mindreader"
	"github.com/streamingfast/logging"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const FirePrefix = "FIRE "
const FirePrefixLen = len(FirePrefix)
const InitLogPrefix = "INIT "
const InitLogPrefixLen = len(InitLogPrefix)
const BlockLogPrefix = "BLOCK "
const BlockLogPrefixLen = len(BlockLogPrefix)

type ParsingStats struct {
}

type ConsoleReader struct {
	lines     chan string
	done      chan interface{}
	closeOnce sync.Once
	logger    *zap.Logger
	tracer    logging.Tracer

	// Parsing context
	readerProtocolVersion string
	protoMessageType      string
	lastBlock             bstream.BlockRef
	lastParentBlock       bstream.BlockRef
	lastBlockTimestamp    time.Time

	lib uint64

	blockRate *dmetrics.AvgRatePromCounter
}

func NewConsoleReader(lines chan string, blockEncoder BlockEncoder, logger *zap.Logger, tracer logging.Tracer) (mindreader.ConsolerReader, error) {
	reader := newConsoleReader(lines, logger, tracer)

	delayBetweenStats := 30 * time.Second
	if tracer.Enabled() {
		delayBetweenStats = 5 * time.Second
	}

	go func() {
		defer reader.blockRate.Stop()

		for {
			select {
			case <-reader.done:
				return
			case <-time.After(delayBetweenStats):
				reader.printStats()
			}
		}
	}()

	return reader, nil
}

func newConsoleReader(lines chan string, logger *zap.Logger, tracer logging.Tracer) *ConsoleReader {
	return &ConsoleReader{
		lines:  lines,
		done:   make(chan interface{}),
		logger: logger,
		tracer: tracer,

		blockRate: dmetrics.MustNewAvgRateFromPromCounter(ConsoleReaderBlockReadCount, 1*time.Second, 30*time.Second, "blocks"),
	}
}

func (r *ConsoleReader) Done() <-chan interface{} {
	return r.done
}

func (r *ConsoleReader) Close() error {
	r.closeOnce.Do(func() {
		r.blockRate.SyncNow()
		r.printStats()

		r.logger.Info("console reader done")
		close(r.done)
	})

	return nil
}

type blockRefView struct {
	ref bstream.BlockRef
}

func (v blockRefView) String() string {
	if v.ref == nil {
		return "<unset>"
	}

	return v.ref.String()
}

type blockRefViewTimestamp struct {
	ref       bstream.BlockRef
	timestamp time.Time
}

func (v blockRefViewTimestamp) String() string {
	return fmt.Sprintf("%s @ %s", blockRefView{v.ref}, v.timestamp.Local().Format(time.RFC822Z))
}

func (r *ConsoleReader) printStats() {
	r.logger.Info("console reader stats",
		zap.Stringer("block_rate", r.blockRate),
		zap.Stringer("last_block", blockRefViewTimestamp{r.lastBlock, r.lastBlockTimestamp}),
		zap.Stringer("last_parent_block", blockRefView{r.lastParentBlock}),
		zap.Uint64("lib", r.lib),
	)
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
		case strings.HasPrefix(line, BlockLogPrefix):
			out, err = r.readBlock(line[BlockLogPrefixLen:])

		case strings.HasPrefix(line, InitLogPrefix):
			err = r.readInit(line[InitLogPrefixLen:])
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

	r.Close()

	return nil, io.EOF
}

// Formats
// [READER_PROTOCOL_VERSION] sf.ethereum.type.v2.Block
func (r *ConsoleReader) readInit(line string) error {
	chunks, err := splitInBoundedChunks(line, 2)
	if err != nil {
		return fmt.Errorf("split: %s", err)
	}

	r.readerProtocolVersion = chunks[0]

	switch r.readerProtocolVersion {
	// Implementation of RPC poller were set to use 1.0 so we keep support for it for now
	case "1.0", "3.0":
		r.logger.Info("console reader protocol version set", zap.String("version", r.readerProtocolVersion))

	default:
		return fmt.Errorf("major version of Firehose exchange protocol is unsupported (expected: one of [1.0, 3.0], found %s), you are most probably running an incompatible version of the Firehose aware node client/node poller", r.readerProtocolVersion)
	}

	protobufFullyQualifiedName := chunks[1]
	if protobufFullyQualifiedName == "" {
		return fmt.Errorf("protobuf fully qualified name is empty, it must be set to a valid Protobuf fully qualified message type representing your block format")
	}

	r.setProtoMessageType(protobufFullyQualifiedName)

	return nil
}

// Formats
// [block_num:342342342] [block_hash] [parent_num] [parent_hash] [lib:123123123] [timestamp:unix_nano] B64ENCODED_any
func (r *ConsoleReader) readBlock(line string) (out *pbbstream.Block, err error) {
	if r.readerProtocolVersion == "" {
		return nil, fmt.Errorf("reader protocol version not set, did you forget to send the 'FIRE INIT <reader_protocol_version> <protobuf_fully_qualified_type>' line?")
	}

	chunks, err := splitInBoundedChunks(line, 7)
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
	if err != nil {
		return nil, fmt.Errorf("decoding payload %q: %w", chunks[6], err)
	}

	blockPayload := &anypb.Any{
		TypeUrl: r.protoMessageType,
		Value:   payload,
	}

	block := &pbbstream.Block{
		Id:        blockHash,
		Number:    blockNum,
		ParentId:  parentHash,
		ParentNum: parentNum,
		Timestamp: timestamppb.New(timestamp),
		LibNum:    libNum,
		Payload:   blockPayload,
	}

	ConsoleReaderBlockReadCount.Inc()
	r.lastBlock = bstream.NewBlockRef(blockHash, blockNum)
	r.lastParentBlock = bstream.NewBlockRef(parentHash, parentNum)
	r.lastBlockTimestamp = timestamp
	r.lib = libNum

	return block, nil
}

func (r *ConsoleReader) setProtoMessageType(typeURL string) {
	if strings.HasPrefix(typeURL, "type.googleapis.com/") {
		r.protoMessageType = typeURL
		return
	}

	if strings.Contains(typeURL, "/") {
		panic(fmt.Sprintf("invalid type url %q, expecting type.googleapis.com/", typeURL))
	}

	r.protoMessageType = "type.googleapis.com/" + typeURL
}

// splitInBoundedChunks splits the line in `count` chunks and returns the slice `chunks[1:count]` (so exclusive end),
// but will accumulate all trailing chunks within the last (for free-form strings, or JSON objects)
func splitInBoundedChunks(line string, count int) ([]string, error) {
	chunks := strings.SplitN(line, " ", count)
	if len(chunks) != count {
		return nil, fmt.Errorf("%d fields required but found %d fields for line %q", count, len(chunks), line)
	}

	return chunks, nil
}
