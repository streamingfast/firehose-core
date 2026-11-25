package apps

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_parseReaderNodeStartBlockTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *time.Time
		wantErr string
	}{
		{
			name:  "empty means no gate",
			input: "",
			want:  nil,
		},
		{
			name:  "unix seconds",
			input: "1712880000",
			want:  ptrTime(time.Unix(1712880000, 0).UTC()),
		},
		{
			name:  "rfc3339",
			input: "2024-04-12T00:00:00Z",
			want:  ptrTime(time.Date(2024, 4, 12, 0, 0, 0, 0, time.UTC)),
		},
		{
			name:    "invalid",
			input:   "not-a-timestamp",
			wantErr: "invalid reader-node-start-block-timestamp",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseReaderNodeStartBlockTimestamp(test.input)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}

			require.NoError(t, err)
			if test.want == nil {
				require.Nil(t, got)
				return
			}

			require.NotNil(t, got)
			require.Equal(t, *test.want, *got)
		})
	}
}

func ptrTime(t time.Time) *time.Time {
	return &t
}
