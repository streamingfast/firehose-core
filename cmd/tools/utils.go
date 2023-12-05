package tools

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dustin/go-humanize"
)

func RoundToBundleStartBlock(block, fileBlockSize uint64) uint64 {
	// From a non-rounded block `1085` and size of `100`, we remove from it the value of
	// `modulo % fileblock` (`85`) making it flush (`1000`).
	return block - (block % fileBlockSize)
}

func RoundToBundleEndBlock(block, fileBlockSize uint64) uint64 {
	// From a non-rounded block `1085` and size of `100`, we remove from it the value of
	// `modulo % fileblock` (`85`) making it flush (`1000`) than adding to it the last
	// merged block num value for this size which simply `size - 1` (`99`) giving us
	// a resolved formulae of `1085 - (1085 % 100) + (100 - 1) = 1085 - (85) + (99)`.
	return block - (block % fileBlockSize) + (fileBlockSize - 1)
}

func PrettyBlockNum(b uint64) string {
	return "#" + strings.ReplaceAll(humanize.Comma(int64(b)), ",", " ")
}

func parseBlockRange(input string, firstStreamableBlock uint64) (out BlockRange, err error) {
	if input == "" || input == "-1" {
		return NewOpenRange(-1), nil
	}

	before, after, rangeHasStartAndStop := strings.Cut(input, ":")

	beforeAsInt64, beforeIsEmpty, beforeIsPositiveRelative, err := parseNumber(before)
	if err != nil {
		return out, fmt.Errorf("parse number %q: %w", before, err)
	}

	afterAsInt64, afterIsEmpty, afterIsPositiveRelative := int64(0), false, false
	if rangeHasStartAndStop {
		afterAsInt64, afterIsEmpty, afterIsPositiveRelative, err = parseNumber(after)
		if err != nil {
			return out, fmt.Errorf("parse number %q: %w", after, err)
		}
	}

	if !rangeHasStartAndStop {
		// If there is no `:` we assume it's a stop block value right away
		if beforeIsPositiveRelative {
			return out, fmt.Errorf("invalid range: a single block cannot be positively relative (so starting with a + sign)")
		}

		return NewOpenRange(resolveBlockNumber(int64(beforeAsInt64), int64(firstStreamableBlock), beforeIsEmpty)), nil
	} else {
		// Otherwise, we have a `:` sign so we assume it's a start/stop range
		if beforeIsPositiveRelative {
			return out, fmt.Errorf("invalid range: start block of a range cannot be positively relative (so starting with a + sign)")
		}

		start := resolveBlockNumber(int64(beforeAsInt64), int64(firstStreamableBlock), beforeIsEmpty)

		if afterIsEmpty {
			return BlockRange{Start: start}, nil
		}

		if start < 0 && afterIsPositiveRelative {
			return out, fmt.Errorf("invalid range: stop block of a range cannot be positively relative (so starting with a + sign) if start position is negative")
		}

		if afterAsInt64 < 0 {
			if afterAsInt64 == -1 {
				return NewOpenRange(start), nil
			}

			return out, fmt.Errorf("invalid range: stop block of a range cannot be negative")
		}

		stop := uint64(afterAsInt64)
		if afterIsPositiveRelative {
			stop += uint64(start)
		}

		if start >= 0 && uint64(start) > stop {
			return out, fmt.Errorf("invalid range: start block %d is above stop block %d (inclusive)", start, stop)
		}

		return NewClosedRange(start, stop), nil
	}
}

// parseNumber parses a number and indicates whether the number is relative, meaning it starts with a +
func parseNumber(number string) (numberInt64 int64, numberIsEmpty bool, numberIsPositiveRelative bool, err error) {
	if number == "" {
		numberIsEmpty = true
		return
	}

	numberIsPositiveRelative = strings.HasPrefix(number, "+")
	numberInt64, err = strconv.ParseInt(strings.TrimPrefix(number, "+"), 0, 64)
	if err != nil {
		return 0, false, false, fmt.Errorf("invalid block number value: %w", err)
	}

	return
}

func resolveBlockNumber(value int64, defaultIfEmpty int64, valueIsEmpty bool) int64 {
	if valueIsEmpty {
		return defaultIfEmpty
	}

	return value
}
