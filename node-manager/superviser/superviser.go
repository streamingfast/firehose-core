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
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ShinyTrinkets/overseer"
	"github.com/streamingfast/bstream"
	nodeManager "github.com/streamingfast/firehose-core/node-manager"
	logplugin "github.com/streamingfast/firehose-core/node-manager/log_plugin"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
)

// mindreaderPlugin can be used to check if `logplugin.LogPlugin` is actually a mindreader one.
// This is not in `mindreader` package to not introduce a cycle dependencies
type mindreaderPlugin interface {
	logplugin.LogPlugin

	LastSeenBlock() bstream.BlockRef
}

// stoppingWaitTimeout is how long `Start` waits for a previous command still in the
// `STOPPING` state before giving up. A supervised process that ignores SIGTERM, or whose
// children keep its stdout/stderr open, never reaches a final state and would otherwise
// block the restart forever. It is a variable only so that tests can shorten it.
var stoppingWaitTimeout = 30 * time.Second

type Superviser struct {
	*shutter.Shutter
	Binary    string
	Arguments []string
	// Env represents the environment variables the command will run with, the `nil`
	// is handled differently than the `[]string{}` empty case. In the `nil` case,
	// the process inherits from the parent process. In the empty case, it starts
	// without any variables set.
	Env    []string
	Logger *zap.Logger

	// runLock serializes the process lifecycle operations (`Start` and `Stop`) against
	// each other. It is held for the whole duration of those operations, including while
	// waiting for the underlying process to actually terminate.
	runLock sync.Mutex

	// cmdLock guards access to `cmd` only. It must never be held while waiting on
	// anything, otherwise observers like `IsRunning`, `LastExitCode` and `Stopped` block
	// for as long as the wait lasts (and deadlock outright when the wait never completes).
	cmd     *overseer.Cmd
	cmdLock sync.Mutex

	logPlugins     []logplugin.LogPlugin
	logPluginsLock sync.RWMutex

	enableDeepMind bool
}

func New(logger *zap.Logger, binary string, arguments []string) *Superviser {
	s := &Superviser{
		Shutter:   shutter.New(),
		Binary:    binary,
		Arguments: arguments,
		Logger:    logger,
	}

	s.Shutter.OnTerminating(func(_ error) {
		s.Logger.Info("superviser is terminating")

		if err := s.Stop(); err != nil {
			s.Logger.Error("failed to stop supervised node process", zap.Error(err))
		}

		s.Logger.Info("shutting down plugins", zap.Int("last_exit_code", s.LastExitCode()))
		s.endLogPlugins()
	})

	return s
}

func (s *Superviser) RegisterLogPlugin(plugin logplugin.LogPlugin) {
	s.logPluginsLock.Lock()
	defer s.logPluginsLock.Unlock()

	s.logPlugins = append(s.logPlugins, plugin)
	if shut, ok := plugin.(logplugin.Shutter); ok {
		s.Logger.Info("adding superviser shutdown to plugins", zap.String("plugin_name", plugin.Name()))
		shut.OnTerminating(func(err error) {
			if !s.IsTerminating() {
				s.Logger.Info("superviser shutting down because of a plugin", zap.String("plugin_name", plugin.Name()))
				go s.Shutdown(err)
			}
		})
	}

	s.Logger.Info("registered log plugin", zap.Int("plugin count", len(s.logPlugins)))
}

func (s *Superviser) GetLogPlugins() []logplugin.LogPlugin {
	s.logPluginsLock.RLock()
	defer s.logPluginsLock.RUnlock()

	return s.logPlugins
}

func (s *Superviser) setDeepMindDebug(enabled bool) {
	s.Logger.Info("setting deep mind debug mode", zap.Bool("enabled", enabled))
	for _, logPlugin := range s.logPlugins {
		if v, ok := logPlugin.(nodeManager.DeepMindDebuggable); ok {
			v.DebugDeepMind(enabled)
		}
	}
}

// getCmd returns the current command, or `nil` if there is none. The lock is released
// before returning, the caller is free to block on the returned command.
func (s *Superviser) getCmd() *overseer.Cmd {
	s.cmdLock.Lock()
	defer s.cmdLock.Unlock()

	return s.cmd
}

func (s *Superviser) setCmd(cmd *overseer.Cmd) {
	s.cmdLock.Lock()
	defer s.cmdLock.Unlock()

	s.cmd = cmd
}

func (s *Superviser) Stopped() <-chan struct{} {
	if cmd := s.getCmd(); cmd != nil {
		return cmd.Done()
	}
	return nil
}

func (s *Superviser) LastExitCode() int {
	if cmd := s.getCmd(); cmd != nil {
		return cmd.Status().Exit
	}
	return 0
}

func (s *Superviser) LastLogLines() []string {
	if s.hasToConsolePlugin() {
		// There is no point in showing the last log lines when the user already saw it through the to console log plugin
		return nil
	}

	for _, plugin := range s.logPlugins {
		if v, ok := plugin.(*logplugin.KeepLastLinesLogPlugin); ok {
			return v.LastLines()
		}
	}

	return nil
}

func (s *Superviser) LastSeenBlockNum() uint64 {
	for _, plugin := range s.GetLogPlugins() {
		if v, ok := plugin.(mindreaderPlugin); ok {
			// The plugin might not have seen any block yet, in which case the
			// last seen block is nil and we must not dereference it.
			if lastSeenBlock := v.LastSeenBlock(); lastSeenBlock != nil {
				return lastSeenBlock.Num()
			}
			return 0
		}
	}
	return 0
}

func (s *Superviser) Start(options ...nodeManager.StartOption) error {
	var startOptions nodeManager.StartOptions
	for _, opt := range options {
		opt.Apply(&startOptions)
	}

	for _, plugin := range s.logPlugins {
		plugin.Launch()
	}

	s.runLock.Lock()
	defer s.runLock.Unlock()

	if cmd := s.getCmd(); cmd != nil {
		if cmd.IsRunningState() {
			s.Logger.Info("underlying process already running, nothing to do")
			return nil
		}

		if cmdIsStopping(cmd) {
			s.Logger.Info("underlying process is currently stopping, waiting for it to finish")

			// A previous process that never reaches its final state would otherwise wedge
			// the superviser forever, so we give up after a while and report it instead.
			select {
			case <-cmd.Done():
			case <-time.After(stoppingWaitTimeout):
				return fmt.Errorf("previous process is still stopping after %s, refusing to start a new one", stoppingWaitTimeout)
			}
		}
	}

	s.Logger.Info("creating new command instance and launch read loop",
		zap.String("binary", s.Binary),
		zap.Strings("arguments", s.Arguments),
		zap.Any("env", ""))

	env := s.Env
	envToLog := []string{fmt.Sprintf("<inherited from process>={%d vars}", len(os.Environ()))}
	if len(env) > 0 {
		envToLog = env
	}

	if env == nil && len(startOptions.ExtraEnv) >= 1 {
		// If there is extra env to add and the s.Env is nil, we need to inherit from the parent process
		// otherwise, we would start with an empty env and have the extra env only.
		env = os.Environ()
	}

	for k, v := range startOptions.ExtraEnv {
		entry := fmt.Sprintf("%s=%s", k, v)

		env = append(env, entry)
		envToLog = append(envToLog, entry)
	}

	s.Logger.Info("creating new command instance and launch read loop",
		zap.String("binary", s.Binary),
		zap.Strings("arguments", s.Arguments),
		zap.Any("env", explodeToMap(envToLog)))

	cmd := overseer.NewCmd(s.Binary, s.Arguments, overseer.Options{Streaming: true, Env: env})
	s.setCmd(cmd)

	go s.start(cmd)

	return nil
}

func explodeToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		left, right, _ := strings.Cut(entry, "=")
		out[left] = right
	}
	return out
}

func (s *Superviser) Stop() error {
	s.runLock.Lock()
	defer s.runLock.Unlock()

	s.Logger.Info("supervisor received a stop request, terminating supervised node process")

	cmd := s.getCmd()
	if !cmdIsRunning(cmd) {
		s.Logger.Info("underlying process is not running, nothing to do")
		return nil
	}

	if cmd.IsRunningState() {
		s.Logger.Info("stopping underlying process")
		if err := cmd.Stop(); err != nil {
			// The command is left in place on purpose even though `Stop` already flipped it
			// to `STOPPING`: the process may well still be alive, and dropping our handle on
			// it would let a later `Start` spawn a second process over the same data. A
			// later `Start` bails out through `stoppingWaitTimeout` instead.
			s.Logger.Error("failed to stop overseer cmd", zap.Error(err))
			return err
		}
	}

	// Blocks until command finished completely
	s.Logger.Debug("blocking until command actually ends")

nodeProcessDone:
	for {
		select {
		case <-cmd.Done():
			break nodeProcessDone
		case <-time.After(500 * time.Millisecond):
			s.Logger.Debug("still blocking until command actually ends")
		}
	}

	s.Logger.Info("supervised process has been terminated")

	s.Logger.Info("waiting for stdout and stderr to be drained", cmdOutputStatsLogFields(cmd)...)
	for !cmdBufferEmpty(cmd) {
		s.Logger.Debug("draining stdout and stderr", cmdOutputStatsLogFields(cmd)...)
		time.Sleep(500 * time.Millisecond)
	}

	s.Logger.Info("stdout and stderr are now drained")

	s.setCmd(nil)

	return nil
}

func cmdOutputStatsLogFields(cmd *overseer.Cmd) []zap.Field {
	var stdoutLineCount, stderrLineCount int
	if cmd != nil {
		stdoutLineCount, stderrLineCount = len(cmd.Stdout), len(cmd.Stderr)
	}

	return []zap.Field{zap.Int("stdout_len", stdoutLineCount), zap.Int("stderr_len", stderrLineCount)}
}

func (s *Superviser) IsRunning() bool {
	return cmdIsRunning(s.getCmd())
}

// cmdIsRunning reports whether the command is starting, running or stopping.
//
// overseer guards `Cmd.State` with an unexported lock, reading the field directly races
// with the command's own goroutine, so everything here goes through the locked accessors
// it exposes. Not being initial nor final leaves exactly those three states.
func cmdIsRunning(cmd *overseer.Cmd) bool {
	if cmd == nil {
		return false
	}
	return !cmd.IsInitialState() && !cmd.IsFinalState()
}

// cmdIsStopping reports whether the command is in the `STOPPING` state, which overseer
// has no accessor for, so we exclude every other non-final state instead.
func cmdIsStopping(cmd *overseer.Cmd) bool {
	if cmd == nil {
		return false
	}
	return !cmd.IsInitialState() && !cmd.IsRunningState() && !cmd.IsFinalState()
}

func cmdBufferEmpty(cmd *overseer.Cmd) bool {
	if cmd == nil {
		return true
	}

	return len(cmd.Stdout) == 0 && len(cmd.Stderr) == 0
}

func (s *Superviser) start(cmd *overseer.Cmd) {
	statusChan := cmd.Start()

	processTerminated := false
	for {
		select {
		case status := <-statusChan:
			processTerminated = true
			if status.Exit == 0 {
				s.Logger.Info("command terminated with zero status", cmdOutputStatsLogFields(cmd)...)
			} else {
				s.Logger.Error(fmt.Sprintf("command terminated with non-zero status, last log lines:\n%s\n", formatLogLines(s.LastLogLines())), overseerStatusLogFields(status)...)
			}

		case line := <-cmd.Stdout:
			s.processLogLine(line)
		case line := <-cmd.Stderr:
			s.processLogLine(line)
		}

		if processTerminated {
			s.Logger.Debug("command terminated but continue read loop to fully consume stdout/sdterr line channels", zap.Bool("buffer_empty", cmdBufferEmpty(cmd)))
			if cmdBufferEmpty(cmd) {
				return
			}
		}
	}
}

func overseerStatusLogFields(status overseer.Status) []zap.Field {
	fields := []zap.Field{
		zap.String("command", status.Cmd),
		zap.Int("exit_code", status.Exit),
	}

	if status.Error != nil {
		fields = append(fields, zap.String("error", status.Error.Error()))
	}

	if status.PID != 0 {
		fields = append(fields, zap.Int("pid", status.PID))
	}

	if status.Runtime > 0 {
		fields = append(fields, zap.Duration("runtime", time.Duration(status.Runtime*float64(time.Second))))
	}

	if status.StartTs > 0 {
		fields = append(fields, zap.Time("started_at", time.Unix(0, status.StartTs)))
	}

	if status.StopTs > 0 {
		fields = append(fields, zap.Time("stopped_at", time.Unix(0, status.StopTs)))
	}

	return fields
}

func formatLogLines(lines []string) string {
	if len(lines) == 0 {
		return "<None>"
	}

	formattedLines := make([]string, len(lines))
	for i, line := range lines {
		formattedLines[i] = "  " + line
	}

	return strings.Join(formattedLines, "\n")
}

func (s *Superviser) endLogPlugins() {
	s.logPluginsLock.Lock()
	defer s.logPluginsLock.Unlock()

	for _, plugin := range s.logPlugins {
		s.Logger.Info("stopping plugin", zap.String("plugin_name", plugin.Name()))
		plugin.Stop()
	}
	s.Logger.Info("all plugins closed")
}

func (s *Superviser) processLogLine(line string) {
	s.logPluginsLock.Lock()
	defer s.logPluginsLock.Unlock()

	for _, plugin := range s.logPlugins {
		plugin.LogLine(line)
	}
}

func (s *Superviser) hasToConsolePlugin() bool {
	for _, plugin := range s.logPlugins {
		if _, ok := plugin.(*logplugin.ToConsoleLogPlugin); ok {
			return true
		}
	}

	return false
}
