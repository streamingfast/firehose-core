package mindreader

import (
	"fmt"
	"testing"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// newTestTracker closes the tracker when the test ends, before its directory is removed
func newTestTracker(t *testing.T, dir string) (*CursorTracker, error) {
	tracker, err := NewCursorTracker(dir, zap.NewNop())
	if err == nil {
		t.Cleanup(tracker.Close)
	}
	return tracker, err
}

func testBlock(num uint64, id string) *pbbstream.Block {
	return &pbbstream.Block{Number: num, Id: id, LibNum: num - 1}
}

func TestCursorTracker_AdvancesOverStoredPrefixOnly(t *testing.T) {
	dir := t.TempDir()
	tracker, err := newTestTracker(t, dir)
	require.NoError(t, err)

	for i, id := range []string{"a", "b", "c"} {
		tracker.Queued(id, testBlock(uint64(10+i), id))
	}

	// "b" and "c" stored before "a": the cursor waits for "a"
	tracker.Uploaded("c")
	tracker.Uploaded("b")
	tracker.Flush()
	cursor, err := ReadReaderCursor(tracker.Path())
	require.NoError(t, err)
	assert.Nil(t, cursor)

	tracker.Uploaded("a")
	tracker.Flush()
	cursor, err = ReadReaderCursor(tracker.Path())
	require.NoError(t, err)
	require.NotNil(t, cursor)
	assert.Equal(t, CursorBlockRef{Num: 12, ID: "c"}, cursor.Block)
	assert.Equal(t, uint64(11), cursor.LibNum)
}

func TestCursorTracker_Values(t *testing.T) {
	dir := t.TempDir()
	tracker, err := newTestTracker(t, dir)
	require.NoError(t, err)

	// a value before its block is queued, one while queued, one after it was stored
	tracker.SetValue(10, "a", "va")
	tracker.Queued("a", testBlock(10, "a"))
	tracker.Queued("b", testBlock(11, "b"))
	tracker.SetValue(11, "b", "vb")
	tracker.Queued("c", testBlock(12, "c"))

	tracker.Uploaded("a")
	tracker.Flush()
	cursor, _ := ReadReaderCursor(tracker.Path())
	assert.Equal(t, &CursorValue{Block: CursorBlockRef{Num: 10, ID: "a"}, Value: "va"}, cursor.Value)

	tracker.Uploaded("b")
	tracker.Uploaded("c")
	tracker.Flush()
	cursor, _ = ReadReaderCursor(tracker.Path())
	assert.Equal(t, CursorBlockRef{Num: 12, ID: "c"}, cursor.Block)
	assert.Equal(t, &CursorValue{Block: CursorBlockRef{Num: 11, ID: "b"}, Value: "vb"}, cursor.Value, "c has no value yet")

	tracker.SetValue(12, "c", "vc")
	tracker.Flush()
	cursor, _ = ReadReaderCursor(tracker.Path())
	assert.Equal(t, &CursorValue{Block: CursorBlockRef{Num: 12, ID: "c"}, Value: "vc"}, cursor.Value)

	// a restart keeps the cursor
	tracker, err = newTestTracker(t, dir)
	require.NoError(t, err)
	tracker.Queued("d", testBlock(13, "d"))
	tracker.Uploaded("d")
	tracker.Flush()
	cursor, _ = ReadReaderCursor(tracker.Path())
	assert.Equal(t, CursorBlockRef{Num: 13, ID: "d"}, cursor.Block)
	assert.Equal(t, "vc", cursor.Value.Value)
}

func TestCursorTracker_BlockPrintedTwice(t *testing.T) {
	tracker, err := newTestTracker(t, t.TempDir())
	require.NoError(t, err)

	tracker.Queued("a", testBlock(10, "a"))
	tracker.Queued("a", testBlock(10, "a"))
	tracker.Queued("b", testBlock(11, "b"))
	tracker.Uploaded("a")
	tracker.Uploaded("b")

	tracker.Flush()
	cursor, _ := ReadReaderCursor(tracker.Path())
	assert.Equal(t, CursorBlockRef{Num: 11, ID: "b"}, cursor.Block)
}

func TestCursorTracker_CloseWritesTheLastCursor(t *testing.T) {
	tracker, err := newTestTracker(t, t.TempDir())
	require.NoError(t, err)

	tracker.Queued("a", testBlock(10, "a"))
	tracker.Uploaded("a")
	tracker.Close()
	tracker.Close() // a second close is a no-op

	cursor, err := ReadReaderCursor(tracker.Path())
	require.NoError(t, err)
	assert.Equal(t, CursorBlockRef{Num: 10, ID: "a"}, cursor.Block)
}

func TestCursorTracker_LateValueForARecentlyStoredBlock(t *testing.T) {
	tracker, err := newTestTracker(t, t.TempDir())
	require.NoError(t, err)

	for num := uint64(1); num <= 3; num++ {
		id := fmt.Sprintf("b%d", num)
		tracker.Queued(id, testBlock(num, id))
		tracker.Uploaded(id)
	}
	// a value for block 2 arriving after block 3 was stored: block 3 has no value, so it
	// applies, tied to block 2
	tracker.SetValue(2, "b2", "v2")
	tracker.Flush()
	cursor, _ := ReadReaderCursor(tracker.Path())
	assert.Equal(t, CursorBlockRef{Num: 3, ID: "b3"}, cursor.Block)
	assert.Equal(t, &CursorValue{Block: CursorBlockRef{Num: 2, ID: "b2"}, Value: "v2"}, cursor.Value)

	// an older one doesn't replace it
	tracker.SetValue(1, "b1", "v1")
	tracker.Flush()
	cursor, _ = ReadReaderCursor(tracker.Path())
	assert.Equal(t, "v2", cursor.Value.Value)
}

func TestCursorTracker_ValuesForBlocksNeverQueuedDontPileUp(t *testing.T) {
	tracker, err := newTestTracker(t, t.TempDir())
	require.NoError(t, err)

	for num := uint64(1); num <= 2*recentStoredBlocks; num++ {
		tracker.SetValue(num, fmt.Sprintf("skipped%d", num), "v")
	}
	tracker.Queued("a", testBlock(5000, "a"))
	assert.Empty(t, tracker.values)
}
