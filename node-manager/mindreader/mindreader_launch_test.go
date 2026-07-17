package mindreader

import (
	"errors"
	"testing"
	"time"

	"github.com/streamingfast/shutter"
	"github.com/stretchr/testify/require"
)

// TestMindReaderPlugin_LaunchWithFailingConsoleReaderFactory ensures that when
// the console reader factory fails, Launch shuts down cleanly with the error
// instead of starting the read loop with a nil console reader (which would
// panic in a goroutine and crash the process).
func TestMindReaderPlugin_LaunchWithFailingConsoleReaderFactory(t *testing.T) {
	factoryErr := errors.New("factory failure")

	p := &MindReaderPlugin{
		Shutter: shutter.New(),
		consoleReaderFactory: func(lines chan string) (ConsolerReader, error) {
			return nil, factoryErr
		},
		zlogger: testLogger,
	}

	p.Launch()

	select {
	case <-p.Terminated():
	case <-time.After(time.Second):
		t.Fatal("mindreader should have terminated after console reader factory failure")
	}

	require.ErrorIs(t, p.Err(), factoryErr)

	// Give a would-be read loop goroutine (started on a nil console reader by
	// the buggy code path) the time to panic and crash the test binary.
	time.Sleep(50 * time.Millisecond)
}
