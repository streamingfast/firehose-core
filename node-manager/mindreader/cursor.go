package mindreader

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"go.uber.org/zap"
)

// ReaderCursorFileName is the name of the reader cursor file, in the reader node's working
// directory.
const ReaderCursorFileName = "reader-cursor.json"

// ReaderCursor is the last block the reader node stored in its one-block store, every block it
// read before that one being stored too. A reader restarts from it instead of from what it
// printed last: blocks it printed after the cursor may never have been stored.
type ReaderCursor struct {
	Block  CursorBlockRef `json:"block"`
	LibNum uint64         `json:"lib_num"`

	// Value is the reader's own cursor ("FIRE CURSOR" line) for the last stored block that had
	// one, at or before Block.
	Value *CursorValue `json:"value,omitempty"`
}

type CursorBlockRef struct {
	Num uint64 `json:"num"`
	ID  string `json:"id"`
}

type CursorValue struct {
	Block CursorBlockRef `json:"block"`
	Value string         `json:"value"`
}

func ReadReaderCursor(path string) (*ReaderCursor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading reader cursor: %w", err)
	}

	cursor := &ReaderCursor{}
	if err := json.Unmarshal(data, cursor); err != nil {
		return nil, fmt.Errorf("decoding reader cursor %q: %w", path, err)
	}
	return cursor, nil
}

// CursorTracker follows blocks from the archiver to the one-block store and keeps the reader
// cursor file at the last block stored without a gap before it. Blocks are queued in the
// order the reader printed them, and uploads complete in any order.
type CursorTracker struct {
	path   string
	logger *zap.Logger

	mu      sync.Mutex
	queue   []*trackedBlock
	byFile  map[string][]*trackedBlock // a block printed twice is queued twice under one file
	byID    map[string][]*trackedBlock
	values  map[string]pendingValue // reader cursor values whose block isn't queued yet, by block id
	current *ReaderCursor
	// the last stored blocks, so that a cursor value arriving after its block was stored
	// still applies
	recent      map[string]uint64
	recentOrder []string
	closed      bool

	// the file is written by one goroutine, with the latest cursor each time: uploads
	// complete faster than files can be synced
	dirty   chan struct{}
	done    chan struct{}
	writeMu sync.Mutex
}

type trackedBlock struct {
	ref      CursorBlockRef
	libNum   uint64
	value    *string
	uploaded bool
}

type pendingValue struct {
	num   uint64
	value string
}

// recentStoredBlocks is how many stored blocks a late "FIRE CURSOR" line can still apply to,
// and how many values for blocks not queued yet are kept before the older ones are dropped
const recentStoredBlocks = 1000

func NewCursorTracker(workingDirectory string, logger *zap.Logger) (*CursorTracker, error) {
	path := filepath.Join(workingDirectory, ReaderCursorFileName)
	current, err := ReadReaderCursor(path)
	if err != nil {
		return nil, err
	}

	logger.Info("reader cursor loaded", zap.String("path", path), zap.Any("cursor", current))
	t := &CursorTracker{
		path:    path,
		logger:  logger,
		byFile:  map[string][]*trackedBlock{},
		byID:    map[string][]*trackedBlock{},
		values:  map[string]pendingValue{},
		current: current,
		recent:  map[string]uint64{},
		dirty:   make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	go func() {
		defer close(t.done)
		for range t.dirty {
			t.Flush()
		}
	}()
	return t, nil
}

func (t *CursorTracker) Path() string {
	return t.path
}

// Queued records a block written to the local one-block store under file name file.
func (t *CursorTracker) Queued(file string, block *pbbstream.Block) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked := &trackedBlock{
		ref:    CursorBlockRef{Num: block.Number, ID: block.Id},
		libNum: block.LibNum,
	}
	if pending, ok := t.values[block.Id]; ok {
		tracked.value = &pending.value
		delete(t.values, block.Id)
	}
	// values for blocks that are never queued (below the start block, say) don't pile up
	if len(t.values) > recentStoredBlocks {
		for id, pending := range t.values {
			if pending.num < block.Number {
				delete(t.values, id)
			}
		}
	}

	t.queue = append(t.queue, tracked)
	t.byFile[file] = append(t.byFile[file], tracked)
	t.byID[block.Id] = append(t.byID[block.Id], tracked)
}

// Uploaded records that file is in the one-block store. Files the tracker didn't queue (left
// in the local store by a previous run) are ignored.
func (t *CursorTracker) Uploaded(file string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.byFile[file]
	if !ok {
		return
	}
	for _, block := range tracked {
		block.uploaded = true
	}
	delete(t.byFile, file)

	advanced := false
	for len(t.queue) > 0 && t.queue[0].uploaded {
		head := t.queue[0]
		t.queue[0] = nil
		t.queue = t.queue[1:]
		t.dequeued(head)

		next := &ReaderCursor{Block: head.ref, LibNum: head.libNum}
		if t.current != nil {
			next.Value = t.current.Value
		}
		if head.value != nil {
			next.Value = &CursorValue{Block: head.ref, Value: *head.value}
		}
		t.current = next
		t.remember(head.ref)
		advanced = true
	}

	if advanced {
		t.write()
	}
}

// SetValue records the reader's own cursor for a block ("FIRE CURSOR" line). It is written
// once that block is stored.
func (t *CursorTracker) SetValue(num uint64, id string, value string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if queued := t.byID[id]; len(queued) > 0 {
		for _, tracked := range queued {
			tracked.value = &value
		}
		return
	}

	// already stored: it becomes the cursor's value unless a later block's value is there
	if storedNum, ok := t.recent[id]; ok && storedNum == num {
		if t.current != nil && (t.current.Value == nil || t.current.Value.Block.Num <= num) {
			t.current.Value = &CursorValue{Block: CursorBlockRef{Num: num, ID: id}, Value: value}
			t.write()
		}
		return
	}

	t.values[id] = pendingValue{num: num, value: value}
}

// Close writes the current cursor a last time and stops the writer. The tracker must not be
// used after.
func (t *CursorTracker) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	close(t.dirty)
	t.mu.Unlock()

	<-t.done
	t.Flush()
}

func (t *CursorTracker) dequeued(block *trackedBlock) {
	queued := t.byID[block.ref.ID]
	for i, tracked := range queued {
		if tracked == block {
			queued = append(queued[:i], queued[i+1:]...)
			break
		}
	}
	if len(queued) == 0 {
		delete(t.byID, block.ref.ID)
	} else {
		t.byID[block.ref.ID] = queued
	}
}

func (t *CursorTracker) remember(ref CursorBlockRef) {
	if _, ok := t.recent[ref.ID]; !ok {
		t.recentOrder = append(t.recentOrder, ref.ID)
	}
	t.recent[ref.ID] = ref.Num
	if len(t.recentOrder) > recentStoredBlocks {
		delete(t.recent, t.recentOrder[0])
		t.recentOrder = t.recentOrder[1:]
	}
}

// write has the cursor file written with the current cursor. Called with mu held.
func (t *CursorTracker) write() {
	if t.closed {
		return
	}
	select {
	case t.dirty <- struct{}{}:
	default:
	}
}

// Flush writes the current cursor now. The file is replaced atomically and synced, so a crash
// leaves either the previous cursor or this one.
func (t *CursorTracker) Flush() {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()

	t.mu.Lock()
	if t.current == nil {
		t.mu.Unlock()
		return
	}
	snapshot := *t.current
	if snapshot.Value != nil {
		value := *snapshot.Value
		snapshot.Value = &value
	}
	t.mu.Unlock()

	if err := writeFileSynced(t.path, &snapshot); err != nil {
		t.logger.Error("writing reader cursor", zap.String("path", t.path), zap.Error(err))
	}
}

func writeFileSynced(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
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
	if err := os.Rename(tmp, path); err != nil {
		return err
	}

	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
