package launcher

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// fakeOpenFilesLimit simulates RLIMIT_NOFILE for a process that cannot raise its
// hard limit, like a container without CAP_SYS_RESOURCE: setting a value above
// maxSettable fails, anything else succeeds (lowering is always allowed).
type fakeOpenFilesLimit struct {
	current     uint64
	maxSettable uint64
	setCalls    []uint64
}

func (f *fakeOpenFilesLimit) get() (uint64, error) {
	return f.current, nil
}

func (f *fakeOpenFilesLimit) set(value uint64) error {
	f.setCalls = append(f.setCalls, value)
	if value > f.maxSettable {
		return errors.New("operation not permitted")
	}
	f.current = value
	return nil
}

func TestSetMaxOpenFilesLimit(t *testing.T) {
	tests := []struct {
		name         string
		current      uint64
		maxSettable  uint64
		expectLimit  uint64
		expectCalls  []uint64
		expectKept   bool
		expectErrors bool
	}{
		{
			name:        "already good enough",
			current:     1048576,
			maxSettable: 1048576,
			expectLimit: 1048576,
		},
		{
			name:        "raised to good enough",
			current:     999999,
			maxSettable: 1048576,
			expectLimit: 1000000,
			expectCalls: []uint64{1000000},
		},
		{
			name:        "k0s limit kept",
			current:     999999,
			maxSettable: 999999,
			expectLimit: 999999,
			expectCalls: []uint64{1000000},
			expectKept:  true,
		},
		{
			name:        "containerd 2.x limit kept",
			current:     524288,
			maxSettable: 524288,
			expectLimit: 524288,
			expectCalls: []uint64{1000000},
			expectKept:  true,
		},
		{
			name:        "low limit raised to fallback",
			current:     10240,
			maxSettable: 24576,
			expectLimit: 24576,
			expectCalls: []uint64{1000000, 24576},
		},
		{
			name:         "low limit and fallback not settable",
			current:      256,
			maxSettable:  10240,
			expectLimit:  256,
			expectCalls:  []uint64{1000000, 24576},
			expectErrors: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limit := &fakeOpenFilesLimit{current: test.current, maxSettable: test.maxSettable}
			core, logs := observer.New(zapcore.DebugLevel)

			err := setMaxOpenFilesLimit(zap.New(core), goodEnoughMaxOpenFilesLimit, osxStockMaxOpenFilesLimit, limit.get, limit.set)
			if test.expectErrors {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, test.expectLimit, limit.current)
			assert.Equal(t, test.expectCalls, limit.setCalls)
			assert.Equal(t, test.expectKept, logs.FilterMessageSnippet("keeping current value").Len() > 0)
		})
	}
}
