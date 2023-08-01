package firecore

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_searchBlockNum(t *testing.T) {
	tests := []struct {
		name          string
		startBlockNum uint64
		lastBlockNum  *uint64
		expect        uint64
		expectErr     bool
	}{
		{"has a block num", 1_690_600, uptr(208_853_300), 208_853_300, false},
		{"has no block num", 1_690_600, nil, 1_690_600, false},
		{"has no block num", 0, uptr(17821900), 17821900, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dstoreOpt := 0
			v, err := searchBlockNum(tt.startBlockNum, func(i uint64) (bool, error) {
				dstoreOpt++
				if tt.lastBlockNum == nil {
					return false, nil
				}
				if i > *tt.lastBlockNum {
					return false, nil
				}
				return true, nil
			})
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expect, v)
			}
			fmt.Println("dstoreOpt: ", dstoreOpt)
		})
	}
}

func uptr(v uint64) *uint64 {
	return &v
}
