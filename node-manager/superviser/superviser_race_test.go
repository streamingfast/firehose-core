package superviser

import (
	"sync"
	"testing"

	"github.com/ShinyTrinkets/overseer"
	"github.com/streamingfast/bstream"
	logplugin "github.com/streamingfast/firehose-core/node-manager/log_plugin"
	"github.com/stretchr/testify/assert"
)

// fakeMindreaderPlugin mimics mindreader.MindReaderPlugin before any block has
// been seen: LastSeenBlock() returns a nil bstream.BlockRef.
type fakeMindreaderPlugin struct {
	logplugin.LogPluginFunc
}

func (fakeMindreaderPlugin) LastSeenBlock() bstream.BlockRef { return nil }

func TestSuperviser_LastSeenBlockNumWithoutAnyBlockSeen(t *testing.T) {
	superviser := testSuperviserInfinite()
	superviser.RegisterLogPlugin(fakeMindreaderPlugin{
		LogPluginFunc: logplugin.LogPluginFunc(func(string) {}),
	})

	// Must not panic (this is the backup path of GitHub issue #90) and must
	// report 0 when no block has been seen yet.
	assert.Equal(t, uint64(0), superviser.LastSeenBlockNum())
}

// TestSuperviser_StoppedAndLastExitCodeConcurrentWithStop ensures Stopped()
// and LastExitCode() take cmdLock: Stop() sets s.cmd = nil under that lock
// while other goroutines may be reading it. We reproduce the exact write
// pattern of Start()/Stop() (assignment under cmdLock) instead of driving a
// real process, because a real one would trip a pre-existing, unrelated race
// on overseer's Cmd.State field. Run with -race.
func TestSuperviser_StoppedAndLastExitCodeConcurrentWithStop(t *testing.T) {
	superviser := testSuperviserInfinite()

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				_ = superviser.Stopped()
				_ = superviser.LastExitCode()
			}
		}()
	}

	for i := 0; i < 1000; i++ {
		// Same write Start() does when (re)creating the command
		superviser.cmdLock.Lock()
		superviser.cmd = overseer.NewCmd(superviser.Binary, superviser.Arguments)
		superviser.cmdLock.Unlock()

		// Same write Stop() does once the process is fully stopped
		superviser.cmdLock.Lock()
		superviser.cmd = nil
		superviser.cmdLock.Unlock()
	}

	wg.Wait()

	assert.Nil(t, superviser.Stopped())
	assert.Equal(t, 0, superviser.LastExitCode())
}
