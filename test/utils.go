package test

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"testing"
)

func loggingStdout(t *testing.T, stdoutPipe io.ReadCloser, instance string) {
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			// Log the stdout output as it comes in
			t.Logf("[%s stdout]: %s", instance, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			t.Logf("Error reading %s stdout: %v", instance, err)
		}
	}()
}

func loggingStderr(t *testing.T, stderrPipe io.ReadCloser, instance string) {
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			t.Logf("[%s stderr]: %s", instance, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			t.Logf("Error reading %s stderr: %v", instance, err)
		}
	}()
}

func handlingTestInstance(t *testing.T, cmd *exec.Cmd, instance string, withLog bool) error {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if withLog {
		loggingStdout(t, stdoutPipe, instance)
		loggingStderr(t, stderrPipe, instance)
	}

	if err = cmd.Start(); err != nil {
		return err
	}

	if err = cmd.Wait(); err != nil {
		return fmt.Errorf("%s process failed: %w", instance, err)
	}

	return err
}
