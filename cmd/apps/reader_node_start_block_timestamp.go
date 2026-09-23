package apps

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func parseReaderNodeStartBlockTimestamp(input string) (*time.Time, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	if unixSeconds, err := strconv.ParseInt(input, 10, 64); err == nil {
		t := time.Unix(unixSeconds, 0).UTC()
		return &t, nil
	}

	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, input); err == nil {
			t := parsed.UTC()
			return &t, nil
		}
	}

	return nil, fmt.Errorf("invalid reader-node-start-block-timestamp %q: expected unix seconds or RFC3339", input)
}
