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
	assert.Equal(t, uint64(0), state.missingBlockCount)

	// Block 1 links to block 0
	b1 := newTestOneBlockFile(1, "bbbbbbbbbbbbbbbb", b0.ID, 0)
	state.process(b1)
	assert.Equal(t, uint64(0), state.missingBlockCount)

	// Block 2 links to block 1
	b2 := newTestOneBlockFile(2, "cccccccccccccccc", b1.ID, 0)
	state.process(b2)
	assert.Equal(t, uint64(0), state.missingBlockCount)

	assert.Equal(t, uint64(3), state.processedCount)
	assert.Equal(t, uint64(3), state.distinctHeights)
	assert.Equal(t, uint64(0), state.brokenParentCount)
	assert.Empty(t, state.forks)
	assert.Empty(t, state.missing)
}

func TestOneBlocksState_DetectsHole(t *testing.T) {
	state := newOneBlocksState(0)

	// Block 0
	b0 := newTestOneBlockFile(0, "aaaaaaaaaaaaaaaa", "0000000000000000", 0)
	state.process(b0)

	// Block 2 (block 1 is missing - hole)
	b2 := newTestOneBlockFile(2, "cccccccccccccccc", "bbbbbbbbbbbbbbbb", 0)
	state.process(b2)

	require.Len(t, state.missing, 1)
	assert.Equal(t, uint64(1), state.missing[0].from)
	assert.Equal(t, uint64(1), state.missing[0].to)
	assert.Equal(t, uint64(1), state.missingBlockCount)
}

func TestOneBlocksState_DetectsHoleRange(t *testing.T) {
	state := newOneBlocksState(0)

	b0 := newTestOneBlockFile(0, "aaaaaaaaaaaaaaaa", "0000000000000000", 0)
	state.process(b0)

	// Jump from #0 directly to #11: blocks #1..#10 are all missing (a range of 10).
	b11 := newTestOneBlockFile(11, "1111111111111111", "0000000000000000", 0)
	state.process(b11)

	require.Len(t, state.missing, 1)
	assert.Equal(t, uint64(1), state.missing[0].from)
	assert.Equal(t, uint64(10), state.missing[0].to)
	assert.Equal(t, uint64(10), state.missingBlockCount)
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

	assert.Equal(t, uint64(0), state.missingBlockCount)
	assert.Equal(t, uint64(0), state.brokenParentCount)
}

func TestOneBlocksState_DetectsFork(t *testing.T) {
	state := newOneBlocksState(0)

	b0 := newTestOneBlockFile(0, "aaaaaaaaaaaaaaaa", "0000000000000000", 0)
	state.process(b0)

	// Two different blocks at height 1 — a fork.
	b1a := newTestOneBlockFile(1, "1111111111111111", b0.ID, 0)
	b1b := newTestOneBlockFile(1, "2222222222222222", b0.ID, 0)
	state.process(b1a)
	state.process(b1b)

	require.Len(t, state.forks, 1)
	assert.Equal(t, uint64(1), state.forks[0].num)
	assert.Equal(t, 2, state.forks[0].candidates)
}

func TestOneBlocksState_DuplicateNotFork(t *testing.T) {
	// Processing the exact same one-block file twice (same canonical name) must
	// not produce a fork: the second occurrence is a duplicate, not a sibling.
	state := newOneBlocksState(0)

	b0 := newTestOneBlockFile(0, "aaaaaaaaaaaaaaaa", "0000000000000000", 0)
	state.process(b0)
	state.process(b0)

	assert.Equal(t, uint64(1), state.processedCount)
	assert.Empty(t, state.forks)
}

func TestOneBlocksState_BrokenParentLinkage(t *testing.T) {
	// Two blocks at height 1 with different IDs (fork), then a block at height 2
	// whose PreviousID matches neither — broken parent linkage.
	state := newOneBlocksState(0)

	b0 := newTestOneBlockFile(0, "aaaaaaaaaaaaaaaa", "0000000000000000", 0)
	state.process(b0)

	b1a := newTestOneBlockFile(1, "1111111111111111", b0.ID, 0)
	state.process(b1a)

	// Block 2 references a parent ID that does not match b1a.
	b2 := newTestOneBlockFile(2, "cccccccccccccccc", "deadbeefdeadbeef", 0)
	state.process(b2)

	assert.Equal(t, uint64(1), state.brokenParentCount)
	assert.Equal(t, uint64(0), state.missingBlockCount, "no height is missing — only the linkage is broken")
}

func TestNewOneBlocksState_ProgressEachOverride(t *testing.T) {
	state := newOneBlocksState(2)
	assert.Equal(t, uint64(2), state.progressEachOverride)
	assert.Equal(t, uint64(2), state.progressStep(0))
	assert.Equal(t, uint64(2), state.progressStep(1_000_000))
}

func TestOneBlocksState_ProgressLadder(t *testing.T) {
	state := newOneBlocksState(0)

	// Ladder steps according to the spec.
	tests := []struct {
		count    uint64
		wantStep uint64
	}{
		{0, 10_000},
		{9_999, 10_000},
		{99_999, 10_000},
		{100_000, 100_000},
		{499_999, 100_000},
		{500_000, 500_000},
		{999_999, 500_000},
		{1_000_000, 1_000_000},
		{5_500_000, 1_000_000},
	}

	for _, tc := range tests {
		assert.Equalf(t, tc.wantStep, state.progressStep(tc.count),
			"ladder step at count=%d", tc.count)
	}
}

func TestOneBlocksState_ProgressNextStep(t *testing.T) {
	state := newOneBlocksState(0)
	// nextProgress is initialized in the constructor for count 0.
	assert.Equal(t, uint64(10_000), state.nextProgressAt)

	// After advancing to 10_000, next target is 20_000 (still in the 10K band).
	assert.Equal(t, uint64(20_000), state.computeNextProgress(10_000))

	// At 99_999 we're still in the 10K band, so the next target is 100_000.
	assert.Equal(t, uint64(100_000), state.computeNextProgress(99_999))

	// At 100_000 we move to the 100K band, next is 200_000.
	assert.Equal(t, uint64(200_000), state.computeNextProgress(100_000))

	// At 1_000_000 we move to the 1M band, next is 2_000_000.
	assert.Equal(t, uint64(2_000_000), state.computeNextProgress(1_000_000))
}
