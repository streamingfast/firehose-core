// Package consoleline splits the output of a Firehose instrumented node into lines as it
// is read. "FIRE BLOCK" lines can have their base64 payload decoded while they are read,
// so the encoded text of a block is never held in memory as a whole.
package consoleline

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/emmansun/base64" // benchmarked 2.4x faster than standard encoding/base64
)

const (
	blockLinePrefix = "FIRE BLOCK "
	initLinePrefix  = "FIRE INIT "
)

// Block is a "FIRE BLOCK" line whose payload was decoded while it was read.
type Block struct {
	// Header is the text between "FIRE BLOCK " and the payload, without the space
	// separating it from the payload.
	Header string

	// Payload is the decoded base64 payload, it is nil when Err is set.
	Payload []byte

	// Err is set when the payload is not valid base64.
	Err error

	// LineLength is the length in bytes of the line as it was read, newline excluded.
	LineLength int
}

// Summary returns the line as text with its payload replaced by its decoded size, for the
// consumers that log lines.
func (b *Block) Summary() string {
	return fmt.Sprintf("%s%s <payload: %d bytes decoded>", blockLinePrefix, b.Header, len(b.Payload))
}

// Line is a line read out of the node, either as text or as a decoded block.
type Line struct {
	Text  string
	Block *Block
}

// ErrLineTooLong is returned by Splitter.Write when a line is longer than the maximum
// line length of the Splitter.
var ErrLineTooLong = errors.New("line too long")

type state int

const (
	stateText state = iota
	stateBlockHeader
	stateBlockPayload
)

// Splitter is an io.Writer that splits what is written to it into lines, stripping the
// trailing "\n" or "\r\n", and hands each line to onLine.
//
// When onBlock is set, a "FIRE BLOCK" line is handed to onBlock instead, with its payload
// decoded while it is written. Finding the payload requires the number of header fields,
// which depends on the protocol version announced by the "FIRE INIT" line. Until that line
// is seen, "FIRE BLOCK" lines are handed to onLine as text.
//
// A Splitter is not safe for concurrent use.
type Splitter struct {
	onLine        func(line string)
	onBlock       func(block *Block)
	maxLineLength int

	state      state
	lineLength int
	buffer     buffer

	// Only meaningful while reading a block line
	headerSpacesLeft int
	header           []byte
	pending          [4]byte
	pendingLen       int
	payloadEnded     bool
	pendingCR        bool
	payloadErr       error

	// blockHeaderSpaces is the number of spaces in a "FIRE BLOCK" line before its payload,
	// 0 until a supported "FIRE INIT" line is seen.
	blockHeaderSpaces int

	err error
}

// NewSplitter returns a Splitter handing lines to onLine and, when onBlock is non-nil,
// decoded "FIRE BLOCK" lines to onBlock. Its buffer has a normal size of bufferSize bytes
// (DefaultBufferSize when 0, at least MinBufferSize), grows past it for longer lines and
// shrinks by half, down to it, each time enough blocks in a row used less than half of it. Write fails with ErrLineTooLong on a line longer
// than MaxLineLength bytes.
func NewSplitter(bufferSize int, onLine func(line string), onBlock func(block *Block)) *Splitter {
	if bufferSize > 0 {
		bufferSize = max(bufferSize, MinBufferSize)
	}

	return newSplitter(MaxLineLength, bufferSize, onLine, onBlock)
}

func newSplitter(maxLineLength, bufferSize int, onLine func(line string), onBlock func(block *Block)) *Splitter {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}

	return &Splitter{
		onLine:        onLine,
		onBlock:       onBlock,
		maxLineLength: maxLineLength,
		buffer:        buffer{pieceSize: max(bufferSize, 3)},
	}
}

// Write implements io.Writer. Handlers are called synchronously from Write.
func (s *Splitter) Write(p []byte) (n int, err error) {
	if s.err != nil {
		return 0, s.err
	}

	n = len(p)

	for len(p) > 0 {
		switch s.state {
		case stateText:
			p, err = s.writeText(p)
		case stateBlockHeader:
			p, err = s.writeBlockHeader(p)
		case stateBlockPayload:
			p, err = s.writeBlockPayload(p)
		}

		if err != nil {
			s.err = err
			return 0, err
		}
	}

	return n, nil
}

// Err returns the error that made Write fail, if any.
func (s *Splitter) Err() error {
	return s.err
}

// Close hands the last line to its handler when it did not end with a newline.
func (s *Splitter) Close() error {
	if s.err != nil {
		return s.err
	}

	switch s.state {
	case stateText:
		if s.lineLength > 0 {
			return s.endText()
		}
	case stateBlockHeader:
		return s.endBlockHeaderAsText()
	case stateBlockPayload:
		return s.endBlock()
	}

	return nil
}

// grow accounts for count more bytes in the current line. The line can go one byte over
// the maximum for a trailing carriage return, the exact length is checked when the line ends.
func (s *Splitter) grow(count int) error {
	s.lineLength += count
	if s.lineLength > s.maxLineLength+len("\r") {
		return s.errLineTooLong()
	}

	return nil
}

func (s *Splitter) errLineTooLong() error {
	return fmt.Errorf("%w: more than the maximum of %d bytes", ErrLineTooLong, s.maxLineLength)
}

func (s *Splitter) writeText(p []byte) ([]byte, error) {
	// The first bytes of a line are taken one prefix length at a time so that a
	// "FIRE BLOCK" line is detected before its payload gets buffered as text.
	if s.onBlock != nil && s.blockHeaderSpaces > 0 && s.lineLength < len(blockLinePrefix) {
		segment := p[:min(len(p), len(blockLinePrefix)-s.lineLength)]
		if i := bytes.IndexByte(segment, '\n'); i >= 0 {
			segment = segment[:i]
		}

		if err := s.grow(len(segment)); err != nil {
			return nil, err
		}
		s.buffer.write(segment)
		p = p[len(segment):]

		if s.lineLength == len(blockLinePrefix) && s.buffer.equal(blockLinePrefix) {
			s.buffer.reset(false)
			s.state = stateBlockHeader
			s.headerSpacesLeft = s.blockHeaderSpaces
			return p, nil
		}

		if len(p) > 0 && p[0] == '\n' {
			return p[1:], s.endText()
		}

		return p, nil
	}

	i := bytes.IndexByte(p, '\n')
	segment := p
	if i >= 0 {
		segment = p[:i]
	}

	if err := s.grow(len(segment)); err != nil {
		return nil, err
	}
	s.buffer.write(segment)

	if i < 0 {
		return nil, nil
	}

	return p[i+1:], s.endText()
}

func (s *Splitter) endText() error {
	line := s.buffer.string()
	line = strings.TrimSuffix(line, "\r")
	if len(line) > s.maxLineLength {
		return s.errLineTooLong()
	}

	s.buffer.reset(strings.HasPrefix(line, blockLinePrefix))
	s.lineLength = 0

	if s.onBlock != nil && strings.HasPrefix(line, initLinePrefix) {
		s.blockHeaderSpaces = blockHeaderSpaces(line[len(initLinePrefix):])
	}

	s.onLine(line)
	return nil
}

// blockHeaderSpaces returns the number of spaces before the payload of a "FIRE BLOCK" line
// for the protocol version of a "FIRE INIT" line, or 0 for an unsupported version.
//
// Must stay in sync with the versions supported by firecore.ConsoleReader.
func blockHeaderSpaces(initFields string) int {
	version, _, _ := strings.Cut(initFields, " ")

	switch version {
	case "1.0", "3.0":
		// [block_num] [block_hash] [parent_num] [parent_hash] [lib] [timestamp] [payload]
		return 6
	case "3.1":
		// [block_num] [partial_block_idx] [block_hash] [parent_num] [parent_hash] [lib] [timestamp] [payload]
		return 7
	default:
		return 0
	}
}

func (s *Splitter) writeBlockHeader(p []byte) ([]byte, error) {
	i := bytes.IndexAny(p, " \n")
	if i < 0 {
		if err := s.grow(len(p)); err != nil {
			return nil, err
		}
		s.header = append(s.header, p...)
		return nil, nil
	}

	if err := s.grow(i + 1); err != nil {
		return nil, err
	}

	if p[i] == '\n' {
		s.lineLength-- // The newline is not part of the line
		s.header = append(s.header, p[:i]...)
		return p[i+1:], s.endBlockHeaderAsText()
	}

	s.headerSpacesLeft--
	if s.headerSpacesLeft > 0 {
		s.header = append(s.header, p[:i+1]...)
		return p[i+1:], nil
	}

	s.header = append(s.header, p[:i]...)
	s.state = stateBlockPayload
	return p[i+1:], nil
}

// endBlockHeaderAsText hands a "FIRE BLOCK" line that ended before its payload to onLine,
// so the reader reports it like any other malformed line.
func (s *Splitter) endBlockHeaderAsText() error {
	s.buffer.write([]byte(blockLinePrefix))
	s.buffer.write(s.header)
	s.resetBlock()
	s.state = stateText
	return s.endText()
}

func (s *Splitter) writeBlockPayload(p []byte) ([]byte, error) {
	if s.pendingCR {
		s.pendingCR = false
		if p[0] == '\n' {
			s.lineLength-- // The carriage return is not part of the line
		} else if s.payloadErr == nil {
			s.payloadErr = s.decodeErr("unexpected carriage return")
		}
	}

	i := bytes.IndexByte(p, '\n')
	segment := p
	if i >= 0 {
		segment = p[:i]
	}

	if err := s.grow(len(segment)); err != nil {
		return nil, err
	}

	if i < 0 && len(segment) > 0 && segment[len(segment)-1] == '\r' {
		// Held back until the next write tells if it is followed by the newline
		s.pendingCR = true
		segment = segment[:len(segment)-1]
	} else if i >= 0 && len(segment) > 0 && segment[len(segment)-1] == '\r' {
		s.lineLength-- // The carriage return is not part of the line
		segment = segment[:len(segment)-1]
	}

	s.decode(segment)

	if i < 0 {
		return nil, nil
	}

	return p[i+1:], s.endBlock()
}

// decode decodes base64 data that follows what was already decoded for the current block.
// The input is decoded 4 bytes group at a time, the bytes of an incomplete group are kept
// until the next call.
func (s *Splitter) decode(data []byte) {
	if s.payloadErr != nil || len(data) == 0 {
		return
	}

	if s.payloadEnded {
		s.payloadErr = s.decodeErr("data after padding")
		return
	}

	if s.pendingLen > 0 {
		copied := copy(s.pending[s.pendingLen:], data)
		s.pendingLen += copied
		data = data[copied:]

		if s.pendingLen < 4 {
			return
		}

		s.decodeGroups(s.pending[:])
		s.pendingLen = 0
		if s.payloadErr != nil {
			return
		}
	}

	full := len(data) / 4 * 4
	if full > 0 && s.payloadEnded {
		s.payloadErr = s.decodeErr("data after padding")
		return
	}

	for groups := data[:full]; len(groups) > 0 && s.payloadErr == nil; {
		room := s.buffer.room()
		chunk := groups[:min(len(groups), len(room)/3*4)]

		s.decodeGroups(chunk)
		groups = groups[len(chunk):]
	}

	s.pendingLen = copy(s.pending[:], data[full:])
}

func (s *Splitter) decodeGroups(groups []byte) {
	if s.payloadEnded {
		s.payloadErr = s.decodeErr("data after padding")
		return
	}

	room := s.buffer.room()
	written, err := base64.StdEncoding.Decode(room, groups)
	if err != nil {
		s.payloadErr = s.decodeErr(err.Error())
		return
	}

	s.buffer.advance(written)
	s.payloadEnded = groups[len(groups)-1] == '='
}

func (s *Splitter) decodeErr(reason string) error {
	return fmt.Errorf("decoding payload: illegal base64 data: %s", reason)
}

func (s *Splitter) endBlock() error {
	if s.pendingCR {
		s.lineLength-- // The carriage return is not part of the line
	}
	if s.lineLength > s.maxLineLength {
		return s.errLineTooLong()
	}

	if s.pendingLen > 0 && s.payloadErr == nil {
		s.payloadErr = s.decodeErr("incomplete trailing group")
	}

	block := &Block{
		Header:     string(s.header),
		Err:        s.payloadErr,
		LineLength: s.lineLength,
	}
	if block.Err == nil {
		block.Payload = s.buffer.bytes()
	}

	s.buffer.reset(true)
	s.resetBlock()
	s.lineLength = 0
	s.state = stateText

	s.onBlock(block)
	return nil
}

func (s *Splitter) resetBlock() {
	s.header = s.header[:0]
	s.pendingLen = 0
	s.payloadEnded = false
	s.pendingCR = false
	s.payloadErr = nil
}
