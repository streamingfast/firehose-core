// Copyright 2019 dfuse Platform Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package relayer

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/streamingfast/bstream"
	pbbstream "github.com/streamingfast/bstream/pb/sf/bstream/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStuckDetectorState_Observe(t *testing.T) {
	threshold := 3 * time.Minute
	start := time.Now()

	t.Run("first observation never trips", func(t *testing.T) {
		var state stuckDetectorState
		tripped, state := state.observe(start, 100, 100, threshold)
		assert.False(t, tripped)
		assert.True(t, state.initialized)
		assert.Equal(t, uint64(100), state.lastHeadNum)
		assert.True(t, state.frozenSince.IsZero())
	})

	t.Run("head advancing never trips, even while a gap remains open", func(t *testing.T) {
		var state stuckDetectorState
		_, state = state.observe(start, 100, 500, threshold)

		for i, headNum := range []uint64{101, 150, 200, 499, 500} {
			now := start.Add(time.Duration(i+1) * threshold * 2) // plenty past threshold each step
			tripped, next := state.observe(now, headNum, 500, threshold)
			require.False(t, tripped, "head advanced to %d, must not trip", headNum)
			state = next
		}
	})

	t.Run("frozen head but source not ahead never trips", func(t *testing.T) {
		var state stuckDetectorState
		_, state = state.observe(start, 100, 100, threshold)

		tripped, state := state.observe(start.Add(threshold*10), 100, 100, threshold)
		assert.False(t, tripped)
		tripped, _ = state.observe(start.Add(threshold*20), 100, 50, threshold)
		assert.False(t, tripped, "source behind head must never trip")
	})

	t.Run("frozen head with source ahead trips only once threshold elapses", func(t *testing.T) {
		var state stuckDetectorState
		_, state = state.observe(start, 100, 150, threshold)

		// first tick after the freeze starts the clock, does not trip yet
		tripped, state := state.observe(start.Add(1*time.Second), 100, 150, threshold)
		assert.False(t, tripped)

		// still under threshold
		tripped, state = state.observe(start.Add(threshold-time.Second), 100, 150, threshold)
		assert.False(t, tripped)

		// past threshold since frozenSince was set
		tripped, _ = state.observe(start.Add(threshold+time.Second), 100, 150, threshold)
		assert.True(t, tripped)
	})

	t.Run("a resolved freeze resets the clock for the next one", func(t *testing.T) {
		var state stuckDetectorState
		_, state = state.observe(start, 100, 150, threshold)
		_, state = state.observe(start.Add(time.Second), 100, 150, threshold)

		// head catches up before threshold
		_, state = state.observe(start.Add(2*time.Second), 150, 150, threshold)
		assert.True(t, state.frozenSince.IsZero())

		// freezes again later; must need a full new threshold window, not reuse the old one
		_, state = state.observe(start.Add(3*time.Second), 150, 200, threshold)
		tripped, _ := state.observe(start.Add(threshold), 150, 200, threshold)
		assert.False(t, tripped, "new freeze must not inherit the previous frozenSince")
	})
}

func TestRaiseUint64(t *testing.T) {
	var v atomic.Uint64

	raiseUint64(&v, 10)
	assert.Equal(t, uint64(10), v.Load())

	raiseUint64(&v, 5)
	assert.Equal(t, uint64(10), v.Load(), "must not lower the value")

	raiseUint64(&v, 20)
	assert.Equal(t, uint64(20), v.Load())
}

func TestWrapLiveSourceFactoryForStuckDetection(t *testing.T) {
	var sourceMaxBlockNum atomic.Uint64
	var received []uint64

	inner := bstream.HandlerFunc(func(blk *pbbstream.Block, obj any) error {
		received = append(received, blk.Number)
		return nil
	})

	factory := bstream.SourceFactory(func(h bstream.Handler) bstream.Source {
		require.NoError(t, h.ProcessBlock(&pbbstream.Block{Number: 42}, nil))
		require.NoError(t, h.ProcessBlock(&pbbstream.Block{Number: 41}, nil))
		require.NoError(t, h.ProcessBlock(&pbbstream.Block{Number: 43}, nil))
		return nil
	})

	wrapped := wrapLiveSourceFactoryForStuckDetection(factory, &sourceMaxBlockNum)
	wrapped(inner)

	assert.Equal(t, []uint64{42, 41, 43}, received, "every block must still reach the real handler, in order")
	assert.Equal(t, uint64(43), sourceMaxBlockNum.Load(), "must track the highest block number seen")
}

func TestWrapLiveSourceFactoryForStuckDetection_PropagatesHandlerError(t *testing.T) {
	var sourceMaxBlockNum atomic.Uint64
	boom := errors.New("boom")

	inner := bstream.HandlerFunc(func(blk *pbbstream.Block, obj any) error {
		return boom
	})

	var gotErr error
	factory := bstream.SourceFactory(func(h bstream.Handler) bstream.Source {
		gotErr = h.ProcessBlock(&pbbstream.Block{Number: 1}, nil)
		return nil
	})

	wrapLiveSourceFactoryForStuckDetection(factory, &sourceMaxBlockNum)(inner)
	assert.ErrorIs(t, gotErr, boom)
}
