// fakereader prints the fake chain's blocks as Firehose "FIRE" lines, the way a firecore
// reader node expects them. It shows how a reader uses the reader cursor
// (--reader-node-cursor); see the README.
//
// Modes:
//   - rewind: the reader keeps no state and can print any block again, like an RPC poller.
//   - stateful: the reader keeps a state that depends on every block before (an indexer
//     writing its own database, say), committed to disk every --commit-interval blocks. It
//     can't go back: after a restart it continues from its last commit.
//
// Protocols:
//   - legacy: the reader records the last block it printed and continues after it on restart,
//     as blockpoller does with its cursor.json.
//   - cursor: the reader starts from firecore's reader cursor (--resume-* from the
//     {resume-*} templates), and prints a "FIRE CURSOR" line after each block. In stateful
//     mode it commits only blocks firecore has stored (--cursor-file).
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/streamingfast/firehose-core/devel/reader-cursor/fakechain"
)

var (
	mode           = flag.String("mode", "rewind", "rewind or stateful")
	protocol       = flag.String("protocol", "cursor", "legacy or cursor")
	startBlock     = flag.Uint64("start-block", 1, "first block of the chain")
	stopBlock      = flag.Uint64("stop-block", 0, "stop after this block (0: never)")
	stateDir       = flag.String("state-dir", "", "the reader's own state (legacy marker, stateful state)")
	rate           = flag.Float64("rate", 0, "blocks per second (0: as fast as firecore reads them)")
	payloadSize    = flag.Int("payload-size", 1024, "filler bytes per block")
	libDistance    = flag.Uint64("lib-distance", 10, "rewind mode: blocks between a block and its LIB")
	commitInterval = flag.Uint64("commit-interval", 500, "stateful mode: blocks between commits")

	cursorFile        = flag.String("cursor-file", "", "cursor protocol: firecore's reader cursor file ({cursor-file})")
	resumeBlock       = flag.String("resume-block", "", "cursor protocol: {resume-block-num}")
	resumeBlockID     = flag.String("resume-block-id", "", "cursor protocol: {resume-block-id}")
	resumeLib         = flag.String("resume-lib", "", "cursor protocol: {resume-lib-num}")
	resumeCursor      = flag.String("resume-cursor", "", "cursor protocol: {resume-cursor}")
	resumeCursorBlock = flag.String("resume-cursor-block", "", "cursor protocol: {resume-cursor-block-num}")

	snapshotAt = flag.Uint64("snapshot-at", 0, "stateful mode: write the state committed at this block, as restoring a snapshot taken there would, and exit")
)

var out = bufio.NewWriterSize(os.Stdout, 1<<20)

func main() {
	flag.Parse()
	log.SetOutput(os.Stderr)
	log.SetPrefix("fakereader: ")
	log.SetFlags(log.Lmicroseconds)

	if *stateDir == "" {
		log.Fatal("--state-dir is required")
	}
	if err := os.MkdirAll(*stateDir, 0o755); err != nil {
		log.Fatal(err)
	}
	if *protocol != "legacy" && *protocol != "cursor" {
		log.Fatalf("unknown protocol %q", *protocol)
	}

	if *snapshotAt != 0 {
		state := fakechain.Genesis
		for num := *startBlock; num <= *snapshotAt; num++ {
			state = fakechain.NextState(state, num)
		}
		if err := writeState(committedState{Height: *snapshotAt, State: hex.EncodeToString(state[:])}); err != nil {
			log.Fatal(err)
		}
		os.Remove(markerPath())
		log.Printf("state restored at %d", *snapshotAt)
		return
	}

	fmt.Fprintf(out, "FIRE INIT 3.0 %s\n", fakechain.BlockType)

	var err error
	switch *mode {
	case "rewind":
		err = runRewind()
	case "stateful":
		err = runStateful()
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		out.Flush()
		log.Fatal(err)
	}
	out.Flush()
}

func optionalUint(name, value string) (uint64, bool) {
	if value == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		log.Fatalf("--%s: %s", name, err)
	}
	return n, true
}

// ---- rewind mode ----

func runRewind() error {
	start := *startBlock

	switch *protocol {
	case "legacy":
		if last, ok, err := readMarker(); err != nil {
			return err
		} else if ok {
			start = last + 1
		}
		log.Printf("rewind/legacy: starting at %d (after the last block printed)", start)

	case "cursor":
		// Printing again what firecore already stored is harmless, and starting at its LIB
		// covers a fork it stored blocks of.
		if lib, ok := optionalUint("resume-lib", *resumeLib); ok {
			start = max(lib+1, *startBlock)
			log.Printf("rewind/cursor: firecore stored up to %s #%s (cursor value %q at #%s), starting at its LIB+1 %d", *resumeBlock, *resumeBlockID, *resumeCursor, *resumeCursorBlock, start)
		} else {
			log.Printf("rewind/cursor: no reader cursor yet, starting at %d", start)
		}
	}

	pacer := newPacer()
	for num := start; *stopBlock == 0 || num <= *stopBlock; num++ {
		pacer.wait()
		lib := *startBlock
		if num > *libDistance+*startBlock {
			lib = num - *libDistance
		}
		content := fakechain.Content{Num: num, Filler: filler(num)}
		if err := printBlock(num, lib, content); err != nil {
			return err
		}

		switch *protocol {
		case "legacy":
			// like blockpoller: the cursor is saved once the block is printed
			if err := writeMarker(num); err != nil {
				return err
			}
		case "cursor":
			// a value the reader wants back: here, where an RPC poller would continue
			printCursor(num, base64.StdEncoding.EncodeToString([]byte("next="+strconv.FormatUint(num+1, 10))))
		}
	}
	return nil
}

// ---- stateful mode ----

type committedState struct {
	Height uint64 `json:"height"`
	State  string `json:"state"`
}

func runStateful() error {
	committed, err := readState()
	if err != nil {
		return err
	}

	height, state := *startBlock-1, fakechain.Genesis
	if committed != nil {
		height = committed.Height
		decoded, err := hex.DecodeString(committed.State)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("bad committed state %q", committed.State)
		}
		copy(state[:], decoded)
	}

	// blocks up to emitAfter were already printed: they are indexed again without printing
	emitAfter := height
	switch *protocol {
	case "legacy":
		if last, ok, err := readMarker(); err != nil {
			return err
		} else if ok {
			emitAfter = max(emitAfter, last)
		}
		log.Printf("stateful/legacy: state committed at %d, last printed %d", height, emitAfter)

	case "cursor":
		if resume, ok := optionalUint("resume-block", *resumeBlock); ok {
			if height > resume {
				return fmt.Errorf("state committed at %d is past firecore's reader cursor %d: blocks %d to %d can't be produced again, restore a snapshot at or before %d", height, resume, resume+1, height, resume)
			}
			emitAfter = resume
		}
		log.Printf("stateful/cursor: state committed at %d, firecore stored up to %d", height, emitAfter)
	}

	pacer := newPacer()
	for num := height + 1; *stopBlock == 0 || num <= *stopBlock; num++ {
		state = fakechain.NextState(state, num)
		stateHex := hex.EncodeToString(state[:])

		if num <= emitAfter {
			if *protocol == "cursor" && num == emitAfter {
				if cursorBlock, ok := optionalUint("resume-cursor-block", *resumeCursorBlock); ok && cursorBlock == num && *resumeCursor != stateHex {
					return fmt.Errorf("state at %d is %s, firecore's reader cursor has %s", num, stateHex, *resumeCursor)
				}
				log.Printf("stateful/cursor: indexed again up to %d, state matches the reader cursor", num)
			}
		} else {
			pacer.wait()
			content := fakechain.Content{Num: num, State: stateHex, Filler: filler(num)}
			if err := printBlock(num, num-1, content); err != nil {
				return err
			}

			switch *protocol {
			case "legacy":
				// the marker is written once the block is printed
				if err := writeMarker(num); err != nil {
					return err
				}
			case "cursor":
				printCursor(num, stateHex)
			}
		}

		if num%*commitInterval == 0 {
			if *protocol == "cursor" && num > emitAfter {
				if err := waitStored(num); err != nil {
					return err
				}
			}
			if err := writeState(committedState{Height: num, State: stateHex}); err != nil {
				return err
			}
		}
	}
	return nil
}

// waitStored waits until firecore's reader cursor is at num: committing the state any later
// would let it get ahead of what firecore stored.
func waitStored(num uint64) error {
	if *cursorFile == "" {
		return fmt.Errorf("--cursor-file is required to commit with the cursor protocol")
	}
	if err := out.Flush(); err != nil {
		return err
	}

	started := time.Now()
	for {
		stored, err := readFirecoreCursor(*cursorFile)
		if err != nil {
			return err
		}
		if stored >= num {
			log.Printf("stateful/cursor: firecore stored %d, committing (waited %s)", num, time.Since(started).Round(time.Millisecond))
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func readFirecoreCursor(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var cursor struct {
		Block struct {
			Num uint64 `json:"num"`
		} `json:"block"`
	}
	if err := json.Unmarshal(data, &cursor); err != nil {
		return 0, fmt.Errorf("decoding %s: %w", path, err)
	}
	return cursor.Block.Num, nil
}

// ---- output ----

func printBlock(num, lib uint64, content fakechain.Content) error {
	parentNum := num - 1
	payload := base64.StdEncoding.EncodeToString(fakechain.Payload(content))
	_, err := fmt.Fprintf(out, "FIRE BLOCK %d %s %d %s %d %d %s\n",
		num, fakechain.ID(num), parentNum, fakechain.ID(parentNum), lib, fakechain.Timestamp(num).UnixNano(), payload)
	if err != nil {
		return err
	}
	return out.Flush()
}

func printCursor(num uint64, value string) {
	fmt.Fprintf(out, "FIRE CURSOR %d %s %s\n", num, fakechain.ID(num), value)
	out.Flush()
}

func filler(num uint64) string {
	if *payloadSize <= 0 {
		return ""
	}
	return strings.Repeat(strconv.FormatUint(num%10, 10), *payloadSize)
}

type pacer struct {
	interval time.Duration
	next     time.Time
}

func newPacer() *pacer {
	if *rate <= 0 {
		return &pacer{}
	}
	return &pacer{interval: time.Duration(float64(time.Second) / *rate), next: time.Now()}
}

func (p *pacer) wait() {
	if p.interval == 0 {
		return
	}
	if d := time.Until(p.next); d > 0 {
		time.Sleep(d)
	}
	p.next = p.next.Add(p.interval)
}

// ---- the reader's own files ----

func markerPath() string { return filepath.Join(*stateDir, "last-printed") }
func statePath() string  { return filepath.Join(*stateDir, "state.json") }

func readMarker() (uint64, bool, error) {
	data, err := os.ReadFile(markerPath())
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return n, err == nil, err
}

func writeMarker(num uint64) error {
	return os.WriteFile(markerPath(), []byte(strconv.FormatUint(num, 10)), 0o644)
}

func readState() (*committedState, error) {
	data, err := os.ReadFile(statePath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state := &committedState{}
	return state, json.Unmarshal(data, state)
}

// writeState commits the state atomically, like a database commit.
func writeState(state committedState) error {
	data, _ := json.Marshal(state)
	tmp := statePath() + ".tmp"
	file, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, statePath())
}
