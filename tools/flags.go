package tools

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

var Flags = &flags{}

type flags struct {
}

func (*flags) GetBlockRange(flagName string) (out BlockRange, err error) {
	stringRange := viper.GetString(flagName)
	if stringRange == "" {
		return
	}

	rawRanges := strings.Split(stringRange, ",")
	if len(rawRanges) == 0 {
		return
	}

	if len(rawRanges) > 1 {
		return out, fmt.Errorf("accepting a single range for now, got %d", len(rawRanges))
	}

	out, err = decodeRange(rawRanges[0])
	if err != nil {
		return out, fmt.Errorf("decode range: %w", err)
	}

	return
}

func decodeRanges(rawRanges string) (out []BlockRange, err error) {
	for _, rawRange := range strings.Split(rawRanges, ",") {
		blockRange, err := decodeRange(rawRange)
		if err != nil {
			fmt.Println(err.Error())
			os.Exit(1)
		}

		out = append(out, blockRange)
	}

	return
}

func decodeRange(rawRange string) (out BlockRange, err error) {
	parts := strings.SplitN(rawRange, ":", 2)
	if len(parts) != 2 {
		return out, fmt.Errorf("invalid range %q, not matching format `<start>:<end>`", rawRange)
	}

	out.Start, err = decodeBlockNum("start", parts[0])
	if err != nil {
		return
	}

	out.Stop, err = decodeBlockNum("end", parts[1])
	if err != nil {
		return
	}

	return
}

func decodeBlockNum(tag string, part string) (out uint64, err error) {
	trimmedValue := strings.Trim(part, " ")

	if trimmedValue != "" {
		out, err = strconv.ParseUint(trimmedValue, 10, 64)
		if err != nil {
			return out, fmt.Errorf("`<%s>` value %q is not a valid integer", tag, part)
		}
	}

	return
}
