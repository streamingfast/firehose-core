package superviser

import (
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ShinyTrinkets/overseer"
	"github.com/streamingfast/bstream"
	logplugin "github.com/streamingfast/firehose-core/node-manager/log_plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestSuperviser_ObserversStayResponsiveWhileStartWaitsForPreviousProcess covers the
// deadlock that hung CI for the full 10 minutes test timeout: Start() used to block on
// `<-s.cmd.Done()` of a still `STOPPING` command while holding cmdLock, so every observer
// (IsRunning, LastExitCode, Stopped) waited on that same lock. When the previous process
// never reached a final state, the lock was never released and the whole superviser wedged.
//
// `testing/synctest` cannot drive this test: the superviser supervises a real OS process,
// and the goroutines draining its stdout/stderr sit in a `read` syscall, which does not
// count as durably blocked. A bubble only advances its fake clock once every goroutine in
// it is durably blocked, so the `stoppingWaitTimeout` timer below would never fire and the
// bubble would hang until the test timeout instead.
func TestSuperviser_ObserversStayResponsiveWhileStartWaitsForPreviousProcess(t *testing.T) {
	// Long enough that the observers below are probed while Start() is still waiting on the
	// stopping command, short enough to keep the test quick.
	previousTimeout := stoppingWaitTimeout
	stoppingWaitTimeout = 2 * time.Second
	defer func() { stoppingWaitTimeout = previousTimeout }()

	// The script ignores SIGTERM, so the command stays in `STOPPING` forever once stopped,
	// exactly like a supervised process whose children keep its stdout/stderr open.
	superviser := testSuperviserSh(`trap '' TERM` + infiniteScript)
	lineChan := registerLineChanPlugin(superviser)

	go superviser.Start()
	waitForSuperviserTaskCompletion(t, superviser)
	waitForOutput(t, lineChan, waitDefaultTimeout)

	cmd := superviser.getCmd()
	require.NotNil(t, cmd)
	require.NoError(t, cmd.Stop())
	require.Eventually(t, func() bool {
		return cmdIsStopping(cmd)
	}, 5*time.Second, 5*time.Millisecond, "command never reached the STOPPING state")

	// SIGTERM is ignored, only SIGKILL takes that process group down. We deliberately do
	// not call Stop() here: should this test ever fail again, Stop() would be waiting on
	// the very lock the failure is about and turn a clean failure into a hung test binary.
	t.Cleanup(func() {
		assert.NoError(t, cmd.Signal(syscall.SIGKILL))
	})

	startReturned := make(chan struct{})
	go func() {
		defer close(startReturned)
		_ = superviser.Start()
	}()

	// Let Start() reach the point where it waits on the stopping command.
	select {
	case <-startReturned:
		require.Fail(t, "Start() returned before it even waited on the stopping command")
	case <-time.After(250 * time.Millisecond):
	}

	// The observers must answer right away, they must not wait on the stopping command.
	for _, observer := range []struct {
		name string
		call func()
	}{
		{"IsRunning", func() { superviser.IsRunning() }},
		{"LastExitCode", func() { superviser.LastExitCode() }},
		{"Stopped", func() { superviser.Stopped() }},
	} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			observer.call()
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			require.Fail(t, "observer blocked", "%s() blocked while Start() waits for the stopping command", observer.name)
		}
	}

	select {
	case <-startReturned:
	case <-time.After(5 * time.Second):
		require.Fail(t, "Start() never returned while the previous command stayed in STOPPING")
	}
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
	for range 4 {
		wg.Go(func() {
			for range 1000 {
				_ = superviser.Stopped()
				_ = superviser.LastExitCode()
			}
		})
	}

	for range 1000 {
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
