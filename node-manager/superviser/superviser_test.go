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

package superviser

import (
	"os"
	"testing"
	"time"

	logplugin "github.com/streamingfast/firehose-core/node-manager/log_plugin"
	"github.com/streamingfast/logging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var infiniteScript = `
	echo "Starting"
	while true; do
		sleep 0.25
		echo "In loop"
	done
`

var zlog = zap.NewNop()

func init() {
	if os.Getenv("DEBUG") != "" || os.Getenv("TRACE") == "true" {
		zlog, _ := zap.NewDevelopment()
		logging.Override(zlog)
	}
}

var waitDefaultTimeout = 500 * time.Millisecond

func TestSuperviser_NotRunningAfterCreation(t *testing.T) {
	assert.Equal(t, false, testSuperviserInfinite().IsRunning())
}

func TestSuperviser_StartsCorrectly(t *testing.T) {
	superviser := testSuperviserInfinite()
	defer superviser.Stop()

	lineChan := registerLineChanPlugin(superviser)

	go superviser.Start()

	waitForSuperviserTaskCompletion(t, superviser)
	waitForOutput(t, lineChan, waitDefaultTimeout)

	assert.Equal(t, true, superviser.IsRunning())
}

func TestSuperviser_CanBeRestartedCorrectly(t *testing.T) {
	superviser := testSuperviserInfinite()
	defer superviser.Stop()

	lineChan := registerLineChanPlugin(superviser)

	go superviser.Start()
	waitForSuperviserTaskCompletion(t, superviser)
	waitForOutput(t, lineChan, waitDefaultTimeout)

	require.NoError(t, superviser.Stop())
	assert.Equal(t, false, superviser.IsRunning())

	go superviser.Start()
	waitForSuperviserTaskCompletion(t, superviser)
	waitForOutput(t, lineChan, waitDefaultTimeout)

	assert.Equal(t, true, superviser.IsRunning())
}

func TestSuperviser_CapturesStdoutCorrectly(t *testing.T) {
	superviser := testSuperviserSh("echo first; sleep 0.1; echo second")
	defer superviser.Stop()

	lineChan := registerLineChanPlugin(superviser)

	go superviser.Start()
	waitForSuperviserTaskCompletion(t, superviser)

	var lines []string
	lines = append(lines, waitForOutput(t, lineChan, waitDefaultTimeout))
	lines = append(lines, waitForOutput(t, lineChan, waitDefaultTimeout))

	assert.Equal(t, []string{"first", "second"}, lines)
}

func testSuperviserBash(script string) *Superviser {
	return New(zlog, "bash", []string{"-c", script})
}

func testSuperviserSh(script string) *Superviser {
	return New(zlog, "sh", []string{"-c", script})
}

func testSuperviserInfinite() *Superviser {
	return testSuperviserSh(infiniteScript)
}

// registerLineChanPlugin registers a log plugin forwarding every line to the returned
// channel. The channel is buffered and never blocks the superviser read loop: a blocked
// read loop stops draining overseer's stdout/stderr channels, which in turn keeps the
// underlying `exec.Cmd.Wait` from ever returning, so the command never reaches a final
// state and `Done()` never fires.
func registerLineChanPlugin(superviser *Superviser) chan string {
	lineChan := make(chan string, 1024)
	superviser.RegisterLogPlugin(logplugin.LogPluginFunc(func(line string) {
		select {
		case lineChan <- line:
		default:
		}
	}))

	return lineChan
}

func waitForSuperviserTaskCompletion(t *testing.T, superviser *Superviser) {
	t.Helper()

	require.Eventually(t, func() bool {
		return superviser.getCmd() != nil
	}, 5*time.Second, 2*time.Millisecond, "superviser never created its command")
}

func waitForOutput(t *testing.T, lineChan chan string, timeout time.Duration) (line string) {
	select {
	case line = <-lineChan:
		return
	case <-time.After(timeout):
		t.Error("no line seen before timeout")
	}

	// Will fail before reaching this line
	return ""
}
