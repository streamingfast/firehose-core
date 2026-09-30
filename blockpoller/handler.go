package blockpoller

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/emmansun/base64" // benchmarked 2.4x faster than standard encoding/base64
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
)

// maxRetainedLineSize is the largest line buffer kept between blocks, a bigger block
// allocates its own.
const maxRetainedLineSize = 32 * 1024 * 1024

type BlockHandler interface {
	Init()
	Handle(blk *pbbstream.Block) error
}

var _ BlockHandler = (*FireBlockHandler)(nil)

type FireBlockHandler struct {
	blockTypeURL string
	init         sync.Once

	out  io.Writer
	mu   sync.Mutex
	line []byte
}

func NewFireBlockHandler(blockTypeURL string) *FireBlockHandler {
	return &FireBlockHandler{
		blockTypeURL: clean(blockTypeURL),
		out:          os.Stdout,
	}
}

func (f *FireBlockHandler) Init() {
	fmt.Fprintln(f.out, "FIRE INIT 3.0", f.blockTypeURL)
}

func (f *FireBlockHandler) Handle(b *pbbstream.Block) error {
	typeURL := clean(b.Payload.TypeUrl)
	if typeURL != f.blockTypeURL {
		return fmt.Errorf("block type url %q does not match expected type %q", typeURL, f.blockTypeURL)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	line := appendBlockLine(f.line[:0], b)
	_, err := f.out.Write(line)

	if cap(line) <= maxRetainedLineSize {
		f.line = line
	} else {
		f.line = nil
	}

	return err
}

// appendBlockLine appends to dst the "FIRE BLOCK" line of b, trailing newline included.
func appendBlockLine(dst []byte, b *pbbstream.Block) []byte {
	payload := b.Payload.Value
	encodedLen := base64.StdEncoding.EncodedLen(len(payload))

	dst = append(dst, "FIRE BLOCK "...)
	dst = strconv.AppendUint(dst, b.Number, 10)
	dst = append(dst, ' ')
	dst = append(dst, b.Id...)
	dst = append(dst, ' ')
	dst = strconv.AppendUint(dst, b.ParentNum, 10)
	dst = append(dst, ' ')
	dst = append(dst, b.ParentId...)
	dst = append(dst, ' ')
	dst = strconv.AppendUint(dst, b.LibNum, 10)
	dst = append(dst, ' ')
	dst = strconv.AppendInt(dst, b.Timestamp.AsTime().UnixNano(), 10)
	dst = append(dst, ' ')

	start := len(dst)
	if free := cap(dst) - start; free < encodedLen+1 {
		grown := make([]byte, start, start+encodedLen+1)
		copy(grown, dst)
		dst = grown
	}
	dst = dst[:start+encodedLen]
	base64.StdEncoding.Encode(dst[start:], payload)

	return append(dst, '\n')
}

func clean(in string) string {
	return strings.Replace(in, "type.googleapis.com/", "", 1)
}
