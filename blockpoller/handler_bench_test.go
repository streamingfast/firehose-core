package blockpoller

import (
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func benchBlock(payloadSize int) *pbbstream.Block {
	payload := make([]byte, payloadSize)
	rand.New(rand.NewSource(1)).Read(payload)

	return &pbbstream.Block{
		Number:    21000000,
		Id:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ParentNum: 20999999,
		ParentId:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		LibNum:    20999900,
		Timestamp: timestamppb.New(time.Unix(1700000000, 123)),
		Payload:   &anypb.Any{TypeUrl: "type.googleapis.com/sf.ethereum.type.v2.Block", Value: payload},
	}
}

// BenchmarkFireBlockHandler measures printing a block line to stdout, redirected to
// /dev/null so the write system call is part of the measure.
func BenchmarkFireBlockHandler(b *testing.B) {
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer devNull.Close()

	stdout := os.Stdout
	os.Stdout = devNull
	defer func() { os.Stdout = stdout }()

	for _, size := range []int{64 * 1024, 1024 * 1024, 16 * 1024 * 1024} {
		blk := benchBlock(size)
		b.Run(fmt.Sprintf("payload=%dKiB", size/1024), func(b *testing.B) {
			handler := NewFireBlockHandler("type.googleapis.com/sf.ethereum.type.v2.Block")

			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				if err := handler.Handle(blk); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
