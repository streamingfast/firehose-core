package check

import (
	"fmt"
	"testing"

	"github.com/streamingfast/bstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestOneBlockFile creates a OneBlockFile directly from components, bypassing filename parsing.
func newTestOneBlockFile(num uint64, id, prevID string, libNum uint64) *bstream.OneBlockFile {
	// Truncate IDs to 16 chars like the real format does
	truncID := truncate16(id)
	truncPrev := truncate16(prevID)
	canonical := fmt.Sprintf("%010d-%s-%s-%d", num, truncID, truncPrev, libNum)
	filename := canonical + "-test"
	return &bstream.OneBlockFile{
		CanonicalName: canonical,
		Filenames:     map[string]bool{filename: true},
		ID:            truncID,
		Num:           num,
		PreviousID:    truncPrev,
		LibNum:        libNum,
	}
}

func truncate16(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[len(s)-16:]
}

func TestOneBlocksState_NoHole(t *testing.T) {
	state := newOneBlocksState(0)

	// Block 0: genesis, no parent
	b0 := newTestOneBlockFile(0, "aaaaaaaaaaaaaaaa", "0000000000000000", 0)
	state.process(b0)
	assert.Equal(t, 0, state.holeCount)

	// Block 1 links to block 0
	b1 := newTestOneBlockFile(1, "bbbbbbbbbbbbbbbb", b0.ID, 0)
	state.process(b1)
	assert.Equal(t, 0, state.holeCount)

	// Block 2 links to block 1
	b2 := newTestOneBlockFile(2, "cccccccccccccccc", b1.ID, 0)
	state.process(b2)
	assert.Equal(t, 0, state.holeCount)

	assert.Equal(t, uint64(3), state.processedCount)
}

func TestOneBlocksState_DetectsHole(t *testing.T) {
	state := newOneBlocksState(0)

	// Block 0
	b0 := newTestOneBlockFile(0, "aaaaaaaaaaaaaaaa", "0000000000000000", 0)
	state.process(b0)

	// Block 2 (block 1 is missing - hole)
	b2 := newTestOneBlockFile(2, "cccccccccccccccc", "bbbbbbbbbbbbbbbb", 0)
	state.process(b2)

	assert.Equal(t, 1, state.holeCount)
}

func TestOneBlocksState_PrunesStateOnFinalization(t *testing.T) {
	state := newOneBlocksState(0)

	b0 := newTestOneBlockFile(0, "aaaaaaaaaaaaaaaa", "0000000000000000", 0)
	state.process(b0)

	b1 := newTestOneBlockFile(1, "bbbbbbbbbbbbbbbb", b0.ID, 0)
	state.process(b1)

	// b2 with libNum=1 means blocks up to 1 are finalized
	b2 := newTestOneBlockFile(2, "cccccccccccccccc", b1.ID, 1)
	state.process(b2)

	// Blocks 0 and 1 should be pruned
	assert.NotContains(t, state.blocksByNum, uint64(0))
	assert.NotContains(t, state.blocksByNum, uint64(1))
	// Block 2 should still be in state
	assert.Contains(t, state.blocksByNum, uint64(2))
}

func TestOneBlocksState_NoHoleAfterFinalized(t *testing.T) {
	// When a block's parent is below finalized boundary, it should NOT be counted as a hole
	state := newOneBlocksState(0)

	b0 := newTestOneBlockFile(0, "aaaaaaaaaaaaaaaa", "0000000000000000", 0)
	state.process(b0)

	b1 := newTestOneBlockFile(1, "bbbbbbbbbbbbbbbb", b0.ID, 0)
	state.process(b1)

	// b2 with libNum=1: finalizes block 1, prunes b0 and b1
	b2 := newTestOneBlockFile(2, "cccccccccccccccc", b1.ID, 1)
	state.process(b2)

	// b3 with libNum=2: finalizes block 2, prunes b2
	b3 := newTestOneBlockFile(3, "dddddddddddddddd", b2.ID, 2)
	state.process(b3)

	// b4: parent b3 is still in state since libNum=2 (b3 not yet finalized)
	b4 := newTestOneBlockFile(4, "eeeeeeeeeeeeeeee", b3.ID, 3)
	state.process(b4)

	assert.Equal(t, 0, state.holeCount)
}

func TestOneBlocksState_DetectHoleLength(t *testing.T) {
	state := newOneBlocksState(0)

	b0 := newTestOneBlockFile(0, "aaaaaaaaaaaaaaaa", "0000000000000000", 0)
	state.process(b0)

	// Skip blocks 1, 2, 3 - present block 4 without parent
	b4 := newTestOneBlockFile(4, "eeeeeeeeeeeeeeee", "dddddddddddddddd", 0)
	holeLen := state.detectHoleLength(b4)

	// Should detect hole of at least 1 (blocks 1, 2, 3 missing)
	require.GreaterOrEqual(t, holeLen, uint64(1))
}

func TestNewOneBlocksState_ProgressEach(t *testing.T) {
	state := newOneBlocksState(2)
	assert.Equal(t, uint64(2), state.progressEach)
}
