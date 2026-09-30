package blockpoller

import (
	"bytes"
	stdbase64 "encoding/base64"
	"fmt"
	"testing"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFireBlockHandler_clean(t *testing.T) {
	tests := []struct {
		in     string
		expect string
	}{
		{"type.googleapis.com/sf.bstream.v2.Block", "sf.bstream.v2.Block"},
		{"sf.bstream.v2.Block", "sf.bstream.v2.Block"},
	}

	for _, test := range tests {
		t.Run(test.in, func(t *testing.T) {
			assert.Equal(t, test.expect, clean(test.in))
		})
	}

}

func TestFireBlockHandler_Handle(t *testing.T) {
	expectedLine := func(b *pbbstream.Block) string {
		return fmt.Sprintf(
			"FIRE BLOCK %d %s %d %s %d %d %s\n",
			b.Number,
			b.Id,
			b.ParentNum,
			b.ParentId,
			b.LibNum,
			b.Timestamp.AsTime().UnixNano(),
			stdbase64.StdEncoding.EncodeToString(b.Payload.Value),
		)
	}

	out := &bytes.Buffer{}
	handler := NewFireBlockHandler("type.googleapis.com/sf.ethereum.type.v2.Block")
	handler.out = out

	handler.Init()
	assert.Equal(t, "FIRE INIT 3.0 sf.ethereum.type.v2.Block\n", out.String())

	// Sizes going up and down exercise growing and reusing the line buffer, the last
	// ones are past maxRetainedLineSize.
	for _, size := range []int{0, 1, 2, 3, 4, 5, 64, 1000, 1024 * 1024, 17, 0, maxRetainedLineSize, 10} {
		blk := benchBlock(size)
		out.Reset()

		require.NoError(t, handler.Handle(blk))
		assert.Equal(t, expectedLine(blk), out.String(), "payload size %d", size)
	}

	blk := benchBlock(10)
	blk.Payload.TypeUrl = "type.googleapis.com/sf.solana.type.v1.Block"
	assert.Error(t, handler.Handle(blk))
}
