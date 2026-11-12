package merger

import (
	"context"
	"time"

	"github.com/streamingfast/bstream"
	"github.com/streamingfast/firehose-core/merger/metrics"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

// headBlockTimeReadInterval bounds how often a stream of irreversible blocks
// triggers a one-block-file read for the head drift metric, so catching up on
// a long backlog does not open every file twice.
const headBlockTimeReadInterval = 5 * time.Second

// headBlockTimeWarnInterval bounds how often a failed read is logged at Warn,
// so a broken one-block store does not flood the logs on fast chains.
const headBlockTimeWarnInterval = time.Minute

// headBlockTimeReader feeds the head drift metric from the one-block files the
// bundler accepts. A block's time is only known by reading its file, so reads
// run one at a time in the background and always read the newest irreversible
// block at the time the read starts, never the block that asked for it.
type headBlockTimeReader struct {
	head     atomic.Pointer[bstream.OneBlockFile] // newest irreversible block seen
	readNum  atomic.Uint64                        // block whose time was last read, or failed to be
	lastRead atomic.Int64                         // unix nanos of the last read start
	lastWarn atomic.Int64                         // unix nanos of the last failed read logged at Warn
	inFlight atomic.Bool                          // a read goroutine is running

	// setBlockTime receives each block time read, overridden in tests.
	setBlockTime func(time.Time)
}

// setHeadBlock records obf as the newest irreversible block and starts a read
// of its time, at most once every headBlockTimeReadInterval.
func (b *Bundler) setHeadBlock(obf *bstream.OneBlockFile) {
	b.headTime.head.Store(obf)
	if time.Since(time.Unix(0, b.headTime.lastRead.Load())) < headBlockTimeReadInterval {
		return
	}
	b.startHeadBlockTimeRead()
}

// RefreshHeadBlockTime reads the time of the newest irreversible block if it
// was not read yet, regardless of the throttle. The merger calls it after each
// pass over the one-block files: irreversible blocks often arrive in bursts
// (a whole epoch at once on Ethereum, or the whole backlog after a restart),
// and the throttled read at the start of a burst would otherwise leave the
// metric on the burst's first block until the next one, minutes later. A call
// made while a read is running is dropped; the next pass makes it again.
func (b *Bundler) RefreshHeadBlockTime() {
	b.startHeadBlockTimeRead()
}

func (b *Bundler) startHeadBlockTimeRead() {
	head := b.headTime.head.Load()
	if head == nil || head.Num == b.headTime.readNum.Load() {
		return
	}
	if !b.headTime.inFlight.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer b.headTime.inFlight.Store(false)
		b.readHeadBlockTime()
	}()
}

func (b *Bundler) readHeadBlockTime() {
	head := b.headTime.head.Load()
	if head == nil || head.Num == b.headTime.readNum.Load() {
		return
	}
	b.headTime.lastRead.Store(time.Now().UnixNano())

	t, err := readBlockTimestamp(context.Background(), head, b.io.OpenOneBlockFile)
	// a failed block is not retried, the next irreversible block will be
	b.headTime.readNum.Store(head.Num)
	if err != nil {
		b.logHeadBlockTimeReadError(head, err)
		return
	}

	if b.headTime.setBlockTime != nil {
		b.headTime.setBlockTime(t)
		return
	}
	metrics.HeadBlockTimeDrift.SetBlockTimeForward(t)
}

func (b *Bundler) logHeadBlockTimeReadError(head *bstream.OneBlockFile, err error) {
	fields := []zap.Field{zap.Uint64("block_num", head.Num), zap.Error(err)}

	now := time.Now().UnixNano()
	last := b.headTime.lastWarn.Load()
	if time.Duration(now-last) >= headBlockTimeWarnInterval && b.headTime.lastWarn.CompareAndSwap(last, now) {
		b.logger.Warn("cannot read block timestamp for head drift metric, drift will be overstated until a read succeeds", fields...)
		return
	}
	b.logger.Debug("cannot read block timestamp for head drift metric", fields...)
}
