package utils

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/streamingfast/dmetrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestGetEnvForceFinalityAfterBlocks(t *testing.T) {
	// Set up test case
	expected := uint64(10)
	os.Setenv("FORCE_FINALITY_AFTER_BLOCKS", strconv.FormatUint(expected, 10))
	defer os.Unsetenv("FORCE_FINALITY_AFTER_BLOCKS")

	// Call the function
	result := GetEnvForceFinalityAfterBlocks()

	// Check the result
	if result == nil {
		t.Errorf("Expected non-nil result, got nil")
	} else if *result != expected {
		t.Errorf("Expected %d, got %d", expected, *result)
	}
}
func TestGetEnvMergerMaxUnlinkableBlocks(t *testing.T) {
	// unset: caller should get nil and fall back to its own default
	os.Unsetenv("MERGER_MAX_UNLINKABLE_BLOCKS")
	assert.Nil(t, GetEnvMergerMaxUnlinkableBlocks())

	// set: caller should get the override
	expected := 4000
	os.Setenv("MERGER_MAX_UNLINKABLE_BLOCKS", strconv.Itoa(expected))
	defer os.Unsetenv("MERGER_MAX_UNLINKABLE_BLOCKS")

	result := GetEnvMergerMaxUnlinkableBlocks()
	if result == nil {
		t.Errorf("Expected non-nil result, got nil")
	} else if *result != expected {
		t.Errorf("Expected %d, got %d", expected, *result)
	}
}

func TestTweakBlockFinality(t *testing.T) {
	// Define test cases
	testCases := []struct {
		blk                *pbbstream.Block
		maxDistanceToBlock uint64
		expectedLibNum     uint64
	}{
		{
			blk: &pbbstream.Block{
				Number: 100,
				LibNum: 80,
			},
			maxDistanceToBlock: 10,
			expectedLibNum:     90,
		},
		{
			blk: &pbbstream.Block{
				Number: 100,
				LibNum: 80,
			},
			maxDistanceToBlock: 200,
			expectedLibNum:     80,
		},
		{
			blk: &pbbstream.Block{
				Number: 100,
				LibNum: 0,
			},
			maxDistanceToBlock: 200,
			expectedLibNum:     0,
		},
		{
			blk: &pbbstream.Block{
				Number: 100,
				LibNum: 0,
			},
			maxDistanceToBlock: 10,
			expectedLibNum:     90,
		},
	}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			TweakBlockFinality(tc.blk, tc.maxDistanceToBlock)
			assert.Equal(t, tc.expectedLibNum, tc.blk.LibNum)
		})
	}
}

func TestClampLibNum(t *testing.T) {
	testCases := []struct {
		name           string
		blk            *pbbstream.Block
		expectedLibNum uint64
		expectClamp    bool
	}{
		{
			name:           "lib_num less than number",
			blk:            &pbbstream.Block{Number: 100, Id: "block-100", LibNum: 80},
			expectedLibNum: 80,
			expectClamp:    false,
		},
		{
			name:           "lib_num equal to number is valid (e.g. an already-rooted slot)",
			blk:            &pbbstream.Block{Number: 100, Id: "block-100", LibNum: 100},
			expectedLibNum: 100,
			expectClamp:    false,
		},
		{
			name:           "lib_num greater than number gets clamped",
			blk:            &pbbstream.Block{Number: 508940774, Id: "block-508940774", LibNum: 508940775},
			expectedLibNum: 508940774,
			expectClamp:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			originalLibNum := tc.blk.LibNum

			core, logs := observer.New(zapcore.ErrorLevel)
			logger := zap.New(core)

			metricSet := dmetrics.NewSet()
			counter := metricSet.NewCounter("test_clamp_libnum_count")

			require.NotPanics(t, func() {
				ClampLibNum(tc.blk, logger, counter)
			})

			assert.Equal(t, tc.expectedLibNum, tc.blk.LibNum)

			if !tc.expectClamp {
				assert.Equal(t, 0, logs.Len())
				assert.Equal(t, float64(0), counter.Get())
				return
			}

			require.Equal(t, 1, logs.Len())
			entry := logs.All()[0]
			assert.Equal(t, zapcore.ErrorLevel, entry.Level)
			assert.Equal(t, tc.blk.Number, entry.ContextMap()["block_num"])
			assert.Equal(t, tc.blk.Id, entry.ContextMap()["block_id"])
			assert.Equal(t, originalLibNum, entry.ContextMap()["original_lib_num"])
			assert.Equal(t, float64(1), counter.Get())
		})
	}
}
