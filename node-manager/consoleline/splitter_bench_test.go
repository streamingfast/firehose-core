package consoleline

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"testing"

	simdbase64 "github.com/emmansun/base64"
)

// benchBlockLine returns a "FIRE BLOCK" line (protocol 3.0) with a random payload of
// payloadSize decoded bytes, newline included.
func benchBlockLine(payloadSize int) []byte {
	payload := make([]byte, payloadSize)
	rand.New(rand.NewSource(1)).Read(payload)
	header := "FIRE BLOCK 21000000 0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 20999999 0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 20999900 1700000000000000000 "
	return []byte(header + base64.StdEncoding.EncodeToString(payload) + "\n")
}

var benchPayloadSizes = []int{64 * 1024, 1024 * 1024, 16 * 1024 * 1024}

// BenchmarkSplitterBlockLine measures reading a block line the way the node output is
// read: in 32 KiB writes, the chunk size of io.Copy.
func BenchmarkSplitterBlockLine(b *testing.B) {
	const chunkSize = 32 * 1024

	for _, size := range benchPayloadSizes {
		line := benchBlockLine(size)
		b.Run(fmt.Sprintf("payload=%dKiB", size/1024), func(b *testing.B) {
			var blocks int
			splitter := NewSplitter(0, func(string) {}, func(*Block) { blocks++ })
			if _, err := splitter.Write([]byte("FIRE INIT 3.0 sf.ethereum.type.v2.Block\n")); err != nil {
				b.Fatal(err)
			}

			b.SetBytes(int64(len(line)))
			b.ReportAllocs()
			for b.Loop() {
				for data := line; len(data) > 0; {
					chunk := data[:min(len(data), chunkSize)]
					data = data[len(chunk):]
					if _, err := splitter.Write(chunk); err != nil {
						b.Fatal(err)
					}
				}
			}
			if blocks != b.N {
				b.Fatalf("expected %d blocks, got %d", b.N, blocks)
			}
		})
	}
}

// BenchmarkBase64DecodeOnly is the floor for BenchmarkSplitterBlockLine: decoding the same
// payload in one call into a preallocated buffer.
func BenchmarkBase64DecodeOnly(b *testing.B) {
	for _, size := range benchPayloadSizes {
		payload := make([]byte, size)
		rand.New(rand.NewSource(1)).Read(payload)
		encoded := []byte(base64.StdEncoding.EncodeToString(payload))
		dst := make([]byte, size)

		b.Run(fmt.Sprintf("payload=%dKiB", size/1024), func(b *testing.B) {
			b.SetBytes(int64(len(encoded)))
			for b.Loop() {
				if _, err := simdbase64.StdEncoding.Decode(dst, encoded); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
