package types

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/streamingfast/bstream"
	"github.com/streamingfast/cli/sflags"
)

func GetBlockRangeFromArg(in string) (out BlockRange, err error) {
	return ParseBlockRange(in, bstream.GetProtocolFirstStreamableBlock)
}

func GetBlockRangeFromFlag(cmd *cobra.Command, flagName string) (out BlockRange, err error) {
	stringRange := sflags.MustGetString(cmd, flagName)

	rawRanges := strings.Split(stringRange, ",")
	if len(rawRanges) == 0 {
		return
	}

	if len(rawRanges) > 1 {
		return out, fmt.Errorf("accepting a single range for now, got %d", len(rawRanges))
	}

	out, err = ParseBlockRange(rawRanges[0], bstream.GetProtocolFirstStreamableBlock)
	if err != nil {
		return out, fmt.Errorf("decode range: %w", err)
	}

	return
}
