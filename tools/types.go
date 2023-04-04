package tools

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dustin/go-humanize"
)

var errStopWalk = errors.New("stop walk")

type BlockRange struct {
	Start uint64
	Stop  uint64
}

func (b BlockRange) Bounded() bool {
	return b.Stop != 0
}

func (b BlockRange) Unbounded() bool {
	return b.Stop == 0
}

func (b BlockRange) ReprocRange() string {
	return fmt.Sprintf("%d:%d", b.Start, b.Stop+1)
}

func (b BlockRange) String() string {
	return fmt.Sprintf("%s - %s", BlockNum(b.Start), BlockNum(b.Stop))
}

type BlockNum uint64

func (b BlockNum) String() string {
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
