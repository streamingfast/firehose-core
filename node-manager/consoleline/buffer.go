package consoleline

import "strings"

const (
	// MaxLineLength is the largest line in bytes the Firehose stack can handle. Its base64
	// payload decodes to at most 3/4 of it, which keeps the block and 1 MiB for the message
	// around it within the 2 GiB responses of dgrpc.MaxResponseSize.
	MaxLineLength = (2*1024*1024*1024 - 1024*1024) / 3 * 4

	// InitialBufferSize is the size in bytes a Splitter buffer starts at, and grows by when a
	// line does not fit.
	InitialBufferSize = 100 * 1024 * 1024

	// shrinkAfterBlocks is how many blocks in a row must use less than half of a grown buffer
	// before it goes back to InitialBufferSize.
	shrinkAfterBlocks = 100
)

// buffer holds the line being read in pieces of pieceSize bytes, allocated as the line grows
// so that growing never copies what was already written. The content is copied once into an
// exactly sized result when the line ends. Pieces are kept for the next lines, and dropped
// back to a single one once shrinkAfterBlocks blocks in a row used less than half of them.
type buffer struct {
	pieceSize int
	pieces    [][]byte
	current   int // index of the piece being written
	size      int

	smallBlocks int
}

// room returns the unused part of the current piece, at least 3 bytes long (one decoded
// base64 group), moving to the next piece when needed. Bytes written in it must be committed
// with advance.
func (b *buffer) room() []byte {
	if len(b.pieces) == 0 {
		b.pieces = append(b.pieces, make([]byte, 0, b.pieceSize))
	}

	piece := b.pieces[b.current]
	if cap(piece)-len(piece) < 3 {
		b.current++
		if b.current == len(b.pieces) {
			b.pieces = append(b.pieces, make([]byte, 0, b.pieceSize))
		}
		piece = b.pieces[b.current]
	}

	return piece[len(piece):cap(piece)]
}

func (b *buffer) advance(n int) {
	piece := &b.pieces[b.current]
	*piece = (*piece)[:len(*piece)+n]
	b.size += n
}

func (b *buffer) write(p []byte) {
	for len(p) > 0 {
		n := copy(b.room(), p)
		b.advance(n)
		p = p[n:]
	}
}

func (b *buffer) used() [][]byte {
	if len(b.pieces) == 0 {
		return nil
	}
	return b.pieces[:b.current+1]
}

func (b *buffer) equal(s string) bool {
	if b.size != len(s) {
		return false
	}

	offset := 0
	for _, piece := range b.used() {
		if string(piece) != s[offset:offset+len(piece)] {
			return false
		}
		offset += len(piece)
	}

	return true
}

func (b *buffer) bytes() []byte {
	out := make([]byte, 0, b.size)
	for _, piece := range b.used() {
		out = append(out, piece...)
	}

	return out
}

func (b *buffer) string() string {
	used := b.used()
	if len(used) == 1 {
		return string(used[0])
	}

	var out strings.Builder
	out.Grow(b.size)
	for _, piece := range used {
		out.Write(piece)
	}

	return out.String()
}

// reset empties the buffer for the next line. isBlock tells if the line was a block, blocks
// using less than half of a grown buffer eventually shrink it.
func (b *buffer) reset(isBlock bool) {
	lineSize := b.size

	for i := range b.used() {
		b.pieces[i] = b.pieces[i][:0]
	}
	b.current = 0
	b.size = 0

	if !isBlock || len(b.pieces) <= 1 {
		return
	}

	if lineSize >= len(b.pieces)*b.pieceSize/2 {
		b.smallBlocks = 0
		return
	}

	b.smallBlocks++
	if b.smallBlocks >= shrinkAfterBlocks {
		clear(b.pieces[1:])
		b.pieces = b.pieces[:1]
		b.smallBlocks = 0
	}
}
