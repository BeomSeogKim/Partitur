//go:build darwin || linux

package adapterkit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

func runProcess(ctx context.Context, spec ProcessSpec, onStdoutLine func([]byte) error, grace time.Duration) (ProcessResult, error) {
	if spec.Path == "" {
		return ProcessResult{}, errors.New("process path is required")
	}
	if onStdoutLine == nil {
		onStdoutLine = func([]byte) error { return nil }
	}

	command := exec.Command(spec.Path, spec.Args...)
	command.Dir = spec.Dir
	command.Env = make([]string, len(spec.Env))
	copy(command.Env, spec.Env)
	command.Stdin = spec.Stdin
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := command.StdoutPipe()
	if err != nil {
		return ProcessResult{}, fmt.Errorf("open child stdout: %w", err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return ProcessResult{}, fmt.Errorf("open child stderr: %w", err)
	}
	if err := command.Start(); err != nil {
		return ProcessResult{}, fmt.Errorf("start vendor process: %w", err)
	}
	processGroup := command.Process.Pid

	stdoutDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64<<10), protocol.MaxFrameBytes)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			if err := onStdoutLine(line); err != nil {
				stdoutDone <- err
				return
			}
		}
		stdoutDone <- scanner.Err()
	}()

	stderrDone := make(chan error, 1)
	go func() {
		target := spec.Stderr
		if target == nil {
			target = io.Discard
		}
		_, err := io.Copy(target, stderr)
		stderrDone <- err
	}()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- command.Wait()
	}()

	var waitErr error
	var groupErr error
	cancelled := false
	stdoutRead := false
	var stdoutErr error
	var stderrErr error
	select {
	case waitErr = <-waitDone:
	case lineErr := <-stdoutDone:
		stdoutRead = true
		stdoutErr = lineErr
		if lineErr != nil {
			waitErr, groupErr = terminateProcessGroup(processGroup, grace, waitDone)
			_, _ = drainProcessPipes(stdoutDone, stderrDone, stdoutRead, stdoutErr)
			if groupErr != nil {
				return processResult(command, waitErr), fmt.Errorf("read vendor stdout: %w; %v", lineErr, groupErr)
			}
			return processResult(command, waitErr), fmt.Errorf("read vendor stdout: %w", lineErr)
		}
		waitErr = <-waitDone
	case <-ctx.Done():
		cancelled = true
		waitErr, groupErr = terminateProcessGroup(processGroup, grace, waitDone)
	}

	if cleanupErr := killRemainingProcessGroup(processGroup); groupErr == nil {
		groupErr = cleanupErr
	}
	stdoutErr, stderrErr = drainProcessPipes(stdoutDone, stderrDone, stdoutRead, stdoutErr)
	result := processResult(command, waitErr)

	if cancelled {
		if groupErr != nil {
			return result, fmt.Errorf("%w: %v", ctx.Err(), groupErr)
		}
		return result, ctx.Err()
	}
	if groupErr != nil {
		return result, groupErr
	}
	if stdoutErr != nil {
		return result, fmt.Errorf("read vendor stdout: %w", stdoutErr)
	}
	if stderrErr != nil {
		return result, fmt.Errorf("copy vendor stderr: %w", stderrErr)
	}
	if waitErr != nil {
		return result, fmt.Errorf("vendor process exited: %w", waitErr)
	}
	return result, nil
}

func terminateProcessGroup(processGroup int, grace time.Duration, waitDone <-chan error) (error, error) {
	if !processGroupExists(processGroup) {
		return <-waitDone, nil
	}
	_ = syscall.Kill(-processGroup, syscall.SIGTERM)
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var waitErr error
	waited := false
	for {
		select {
		case waitErr = <-waitDone:
			waited = true
			waitDone = nil
		case <-deadline.C:
			_ = syscall.Kill(-processGroup, syscall.SIGKILL)
			if !waited {
				waitErr = <-waitDone
			}
			if !waitForProcessGroupExit(processGroup, time.Second) {
				return waitErr, errors.New("vendor process group survived SIGKILL")
			}
			return waitErr, nil
		case <-ticker.C:
			if !processGroupExists(processGroup) {
				if !waited {
					waitErr = <-waitDone
				}
				return waitErr, nil
			}
		}
	}
}

func killRemainingProcessGroup(processGroup int) error {
	if processGroupExists(processGroup) {
		_ = syscall.Kill(-processGroup, syscall.SIGKILL)
		if !waitForProcessGroupExit(processGroup, time.Second) {
			return errors.New("vendor process group survived SIGKILL")
		}
	}
	return nil
}

func waitForProcessGroupExit(processGroup int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for processGroupExists(processGroup) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return !processGroupExists(processGroup)
}

func processGroupExists(processGroup int) bool {
	err := syscall.Kill(-processGroup, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func processResult(command *exec.Cmd, waitErr error) ProcessResult {
	exitCode := 0
	if waitErr != nil {
		exitCode = -1
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	return ProcessResult{ExitCode: exitCode}
}

func drainProcessPipes(stdoutDone, stderrDone <-chan error, stdoutRead bool, stdoutErr error) (error, error) {
	var stderrErr error
	var wait sync.WaitGroup
	if !stdoutRead {
		wait.Add(1)
		go func() {
			defer wait.Done()
			stdoutErr = <-stdoutDone
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		stderrErr = <-stderrDone
	}()
	wait.Wait()
	return stdoutErr, stderrErr
}
