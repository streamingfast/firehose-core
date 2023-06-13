package tools

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/dustin/go-humanize"
)

var errStopWalk = errors.New("stop walk")

// BlockRange is actually an UnresolvedBlockRange so both the start and end could be
// negative values.
//
// This is in opposition to `bstream.Range` which is a resolved range meaning that start/stop
// values will never be negative.
type BlockRange struct {
	Start int64
	Stop  *uint64
}

func NewOpenRange(start int64) BlockRange {
	return BlockRange{Start: int64(start), Stop: nil}
}

func NewClosedRange(start int64, stop uint64) BlockRange {
	return BlockRange{Start: start, Stop: &stop}
}

// IsResolved returns true if the range is both closed and fully
// resolved (e.g. both start and stop are positive values). Returns
// false otherwise.
func (b BlockRange) IsResolved() bool {
	return b.Start >= 0 && b.IsClosed()
}

func (b BlockRange) IsOpen() bool {
	return b.Stop == nil
}

func (b BlockRange) IsClosed() bool {
	return b.Stop != nil
}

func (b BlockRange) GetStopBlock() int64 {
	return b.Start
}

func (b BlockRange) GetStopBlockOr(defaultIfOpenRange uint64) uint64 {
	if b.IsOpen() {
		return defaultIfOpenRange
	}

	return *b.Stop
}

func (b BlockRange) ReprocRange() string {
	if b.IsClosed() {
		return "<Invalid Unbounded Range>"
	}

	if !b.IsResolved() {
		return "<Invalid Unresolved Range>"
	}

	return fmt.Sprintf("%d:%d", b.Start, *b.Stop+1)
}

func (b BlockRange) String() string {
	if b.IsOpen() {
		return fmt.Sprintf("[%s, +∞]", BlockNum(b.Start))
	}

	return fmt.Sprintf("[%s, %s]", BlockNum(b.Start), BlockNum(*b.Stop))
}

type BlockNum int64

var HeadBlockNum BlockNum = -1

func (b BlockNum) String() string {
	if b < 0 {
		if b == HeadBlockNum {
			return "HEAD"
		}

		return fmt.Sprintf("HEAD - %d", uint64(math.Abs(float64(b))))
	}

	return "#" + strings.ReplaceAll(humanize.Comma(int64(b)), ",", " ")
}

type FilteringFilters struct {
	Include string
	Exclude string
	System  string
}

func (f *FilteringFilters) Key() string {
	return f.System + f.Exclude + f.System
}
