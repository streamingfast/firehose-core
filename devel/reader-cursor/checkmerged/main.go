// checkstore verifies the merged blocks a test run left: every block from the first to the
// last is there once, its id and parent are the fake chain's, and (stateful chain) its state
// follows from the block before.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/devel/reader-cursor/fakechain"
)

var (
	mergedStore = flag.String("merged-blocks", "", "merged blocks store URL")
	firstBlock  = flag.Uint64("first-block", 1, "first block expected")
	minLast     = flag.Uint64("min-last-block", 0, "fail if the merged blocks end before this block")
	stateful    = flag.Bool("stateful", false, "check the stateful chain's state")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	store, err := dstore.NewDBinStore(*mergedStore)
	if err != nil {
		return err
	}

	var bundles []string
	if err := store.Walk(ctx, "", func(filename string) error {
		bundles = append(bundles, filename)
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(bundles)
	if len(bundles) == 0 {
		return fmt.Errorf("no merged blocks")
	}

	expected := *firstBlock
	state := fakechain.Genesis
	for num := uint64(1); num < *firstBlock; num++ {
		state = fakechain.NextState(state, num)
	}

	for _, bundle := range bundles {
		reader, err := store.OpenObject(ctx, bundle)
		if err != nil {
			return err
		}
		blocks, err := bstream.NewDBinBlockReader(reader)
		if err != nil {
			return fmt.Errorf("bundle %s: %w", bundle, err)
		}

		for {
			block, err := blocks.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("bundle %s: %w", bundle, err)
			}
			if block.Number < expected {
				continue
			}
			if block.Number != expected {
				return fmt.Errorf("gap: expected block %d, bundle %s has %d next (%d blocks missing)", expected, bundle, block.Number, block.Number-expected)
			}
			if err := checkBlock(block, &state); err != nil {
				return err
			}
			expected++
		}
		reader.Close()
	}

	last := expected - 1
	if last < *minLast {
		return fmt.Errorf("merged blocks end at %d, before %d (the merger stalled?)", last, *minLast)
	}
	fmt.Printf("OK: blocks %d to %d, no gap, ids and parents match", *firstBlock, last)
	if *stateful {
		fmt.Print(", every state follows from the block before")
	}
	fmt.Println()
	return nil
}

func checkBlock(block *pbbstream.Block, state *[32]byte) error {
	num := block.Number
	if block.Id != fakechain.ID(num) || block.ParentId != fakechain.ID(num-1) || block.ParentNum != num-1 {
		return fmt.Errorf("block %d: id %s parent %d %s, expected %s parent %s", num, block.Id, block.ParentNum, block.ParentId, fakechain.ID(num), fakechain.ID(num-1))
	}
	if !*stateful {
		return nil
	}

	content, err := fakechain.DecodePayload(block.Payload.Value)
	if err != nil {
		return fmt.Errorf("block %d: %w", num, err)
	}
	*state = fakechain.NextState(*state, num)
	if content.State != hex.EncodeToString(state[:]) {
		return fmt.Errorf("block %d: state %s, expected %s", num, content.State, hex.EncodeToString(state[:]))
	}
	return nil
}
