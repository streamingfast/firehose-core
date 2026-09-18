package consoleline

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type collected struct {
	Text  string
	Block *Block
}

func split(t *testing.T, input string, maxLineLength int, decodeBlocks bool, chunkSizes func() int) ([]collected, error) {
	return splitWithBuffer(t, input, maxLineLength, 1024*1024, decodeBlocks, chunkSizes)
}

func splitWithBuffer(t *testing.T, input string, maxLineLength, bufferSize int, decodeBlocks bool, chunkSizes func() int) ([]collected, error) {
	t.Helper()

	var out []collected
	onLine := func(line string) { out = append(out, collected{Text: line}) }
	var onBlock func(*Block)
	if decodeBlocks {
		onBlock = func(block *Block) { out = append(out, collected{Block: block}) }
	}

	splitter := newSplitter(maxLineLength, bufferSize, onLine, onBlock)
	for data := []byte(input); len(data) > 0; {
		chunk := data[:min(len(data), chunkSizes())]
		data = data[len(chunk):]

		n, err := splitter.Write(chunk)
		if err != nil {
			return out, err
		}
		require.Equal(t, len(chunk), n)
	}

	require.NoError(t, splitter.Close())
	return out, nil
}

// expectedSplit is a line at a time implementation of what Splitter does
func expectedSplit(input string, decodeBlocks bool) []collected {
	var out []collected
	headerSpaces := 0

	lines := strings.Split(input, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")

		if decodeBlocks && headerSpaces > 0 && strings.HasPrefix(line, blockLinePrefix) {
			fields := strings.SplitN(line[len(blockLinePrefix):], " ", headerSpaces+1)
			if len(fields) == headerSpaces+1 {
				block := &Block{Header: strings.Join(fields[:headerSpaces], " "), LineLength: len(line)}
				payload, err := base64.StdEncoding.DecodeString(fields[headerSpaces])
				if err != nil {
					block.Err = err
				} else {
					block.Payload = payload
				}

				out = append(out, collected{Block: block})
				continue
			}
		}

		if decodeBlocks && strings.HasPrefix(line, initLinePrefix) {
			headerSpaces = blockHeaderSpaces(line[len(initLinePrefix):])
		}
		out = append(out, collected{Text: line})
	}

	return out
}

func blockLine(num int, payload []byte) string {
	return fmt.Sprintf("FIRE BLOCK %d h%d %d p%d 0 1700000000 %s", num, num, num-1, num-1, base64.StdEncoding.EncodeToString(payload))
}

func randomPayload(r *rand.Rand, size int) []byte {
	payload := make([]byte, size)
	r.Read(payload)
	return payload
}

func TestSplitter_MatchesLineAtATime(t *testing.T) {
	r := rand.New(rand.NewSource(1))

	big := randomPayload(r, 3*1024*1024+12345)
	inputs := map[string]string{
		"logs only":                  "info: starting\n\nwarn: something\r\nlast line without newline",
		"blocks":                     "FIRE INIT 3.0 sf.test.Block\n" + blockLine(1, randomPayload(r, 100)) + "\nlog line\n" + blockLine(2, nil) + "\r\n" + blockLine(3, randomPayload(r, 70000)) + "\n",
		"protocol 3.1":               "FIRE INIT 3.1 sf.test.Block\nFIRE BLOCK 1 1000 h1 0 p0 0 1700000000 " + base64.StdEncoding.EncodeToString([]byte("abcd")) + "\n",
		"block before init":          blockLine(1, []byte("abc")) + "\nFIRE INIT 3.0 sf.test.Block\n" + blockLine(2, []byte("abc")) + "\n",
		"unsupported version":        "FIRE INIT 9.9 sf.test.Block\n" + blockLine(1, []byte("abc")) + "\n",
		"big block":                  "FIRE INIT 3.0 sf.test.Block\n" + blockLine(1, big) + "\n" + blockLine(2, []byte("x")) + "\n",
		"last block without newline": "FIRE INIT 3.0 sf.test.Block\n" + blockLine(1, []byte("hello")),
		"header only":                "FIRE INIT 3.0 sf.test.Block\nFIRE BLOCK 1 h1 0\nFIRE BLOCK 1 h1 0 p0 0 1700000000\n",
		"prefix only":                "FIRE INIT 3.0 sf.test.Block\nFIRE BLOCK\nFIRE BLOCK \nFIRE BLOC\nFIRE BLOCKS 1 2 3 4 5 6 7\n",
		"invalid base64":             "FIRE INIT 3.0 sf.test.Block\nFIRE BLOCK 1 h1 0 p0 0 1700000000 ab$d\nFIRE BLOCK 1 h1 0 p0 0 1700000000 abc\nFIRE BLOCK 1 h1 0 p0 0 1700000000 YQ==YQ==\n" + blockLine(2, []byte("ok")) + "\n",
		"space in payload":           "FIRE INIT 3.0 sf.test.Block\nFIRE BLOCK 1 h1 0 p0 0 1700000000 YWJj ZGVm\n",
	}

	chunkings := map[string]func() int{
		"1 byte":      func() int { return 1 },
		"3 bytes":     func() int { return 3 },
		"32 KiB":      func() int { return 32 * 1024 },
		"random":      func() int { return 1 + r.Intn(5000) },
		"all at once": func() int { return 1 << 30 },
	}

	for inputName, input := range inputs {
		for chunkingName, chunkSizes := range chunkings {
			if inputName == "big block" && (chunkingName == "1 byte" || chunkingName == "3 bytes") {
				continue
			}

			for _, decodeBlocks := range []bool{true, false} {
				bufferSize := 1024 * 1024
				if chunkingName == "random" {
					bufferSize = 16
				}

				t.Run(fmt.Sprintf("%s/%s/decode=%t", inputName, chunkingName, decodeBlocks), func(t *testing.T) {
					got, err := splitWithBuffer(t, input, 1<<30, bufferSize, decodeBlocks, chunkSizes)
					require.NoError(t, err)

					expected := expectedSplit(input, decodeBlocks)
					require.Equal(t, len(expected), len(got))
					for i := range expected {
						assert.Equal(t, expected[i].Text, got[i].Text, "line %d", i)
						if expected[i].Block == nil {
							assert.Nil(t, got[i].Block, "line %d", i)
							continue
						}

						require.NotNil(t, got[i].Block, "line %d", i)
						assert.Equal(t, expected[i].Block.Header, got[i].Block.Header, "line %d", i)
						assert.Equal(t, expected[i].Block.LineLength, got[i].Block.LineLength, "line %d", i)
						assert.Equal(t, expected[i].Block.Err != nil, got[i].Block.Err != nil, "line %d error, expected %v got %v", i, expected[i].Block.Err, got[i].Block.Err)
						assert.True(t, string(expected[i].Block.Payload) == string(got[i].Block.Payload), "line %d payload differs", i)
					}
				})
			}
		}
	}
}

func TestSplitter_MaxLineLength(t *testing.T) {
	oneByte := func() int { return 1 }
	allAtOnce := func() int { return 1 << 30 }

	for _, chunkSizes := range []func() int{oneByte, allAtOnce} {
		got, err := split(t, "12345\n1234\r\n123456\n", 5, false, chunkSizes)
		require.ErrorIs(t, err, ErrLineTooLong)
		assert.Equal(t, []collected{{Text: "12345"}, {Text: "1234"}}, got)

		input := "FIRE INIT 3.0 sf.test.Block\n" + blockLine(1, []byte("abcdef"))
		limit := len(blockLine(1, []byte("abcdef")))

		got, err = split(t, input+"\n", limit, true, chunkSizes)
		require.NoError(t, err)
		require.Len(t, got, 2)

		_, err = split(t, input+"AAAA\n", limit, true, chunkSizes)
		require.ErrorIs(t, err, ErrLineTooLong)
	}
}

func TestBuffer_GrowsAndShrinks(t *testing.T) {
	b := buffer{pieceSize: 200}
	block := func(size int) {
		b.write(make([]byte, size))
		b.reset(true)
	}

	block(1000)
	assert.Len(t, b.pieces, 5)

	// A grown buffer is reused as is
	b.write([]byte("abcd"))
	assert.Equal(t, []byte("abcd"), b.bytes())
	assert.Len(t, b.pieces, 5)
	b.reset(true)

	// Only blocks count, and a block using half of the buffer restarts the count
	for i := 0; i < shrinkAfterBlocks-2; i++ {
		block(499)
	}
	for i := 0; i < 5; i++ {
		b.write(make([]byte, 10))
		b.reset(false)
	}
	block(500)
	assert.Len(t, b.pieces, 5)

	// 1000 -> 600 -> 400 -> 200, never below the normal size
	for _, expected := range []int{3, 2, 1, 1} {
		for i := 0; i < shrinkAfterBlocks; i++ {
			block(10)
		}
		assert.Len(t, b.pieces, expected)
	}
	assert.Equal(t, 200, cap(b.pieces[0]))

	// A buffer that shrank still grows back
	block(700)
	assert.Len(t, b.pieces, 4)
	for i := 0; i < shrinkAfterBlocks; i++ {
		block(399)
	}
	assert.Len(t, b.pieces, 2)
}

func TestSplitter_BufferSize(t *testing.T) {
	onLine := func(string) {}

	assert.Equal(t, DefaultBufferSize, NewSplitter(0, onLine, nil).buffer.pieceSize)
	assert.Equal(t, MinBufferSize, NewSplitter(1024, onLine, nil).buffer.pieceSize)
	assert.Equal(t, 64*1024, NewSplitter(64*1024, onLine, nil).buffer.pieceSize)
	assert.Equal(t, MaxLineLength, NewSplitter(1024, onLine, nil).maxLineLength)
}
