//go:build darwin || linux

package adapterkit

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
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
	if err := ctx.Err(); err != nil {
		return ProcessResult{}, err
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

	stdoutWait := (<-chan error)(stdoutDone)
	stderrWait := (<-chan error)(stderrDone)
	ctxDone := ctx.Done()
	var terminationWait <-chan error
	terminationStarted := false
	startTermination := func() {
		if terminationStarted {
			return
		}
		terminationStarted = true
		done := make(chan error, 1)
		terminationWait = done
		go func() {
			done <- terminateProcessGroup(processGroup, grace)
		}()
	}

	var groupErr error
	cancelled := false
	var readerErr error
	var readerOperation string
	for stdoutWait != nil || stderrWait != nil {
		select {
		case err := <-stdoutWait:
			stdoutWait = nil
			if err != nil && readerErr == nil {
				readerErr = err
				readerOperation = "read vendor stdout"
				startTermination()
			}
		case err := <-stderrWait:
			stderrWait = nil
			if err != nil && readerErr == nil {
				readerErr = err
				readerOperation = "copy vendor stderr"
				startTermination()
			}
		case <-ctxDone:
			cancelled = true
			ctxDone = nil
			startTermination()
		case err := <-terminationWait:
			terminationWait = nil
			groupErr = err
			if stdoutWait != nil {
				_ = stdout.Close()
			}
			if stderrWait != nil {
				_ = stderr.Close()
			}
		}
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- command.Wait()
	}()

	var waitErr error
	if terminationStarted {
		waitErr = <-waitDone
	} else {
		select {
		case waitErr = <-waitDone:
		case <-ctxDone:
			cancelled = true
			ctxDone = nil
			startTermination()
			waitErr = <-waitDone
		}
	}
	if terminationWait != nil {
		groupErr = <-terminationWait
	}
	if cleanupErr := killRemainingProcessGroup(processGroup); groupErr == nil {
		groupErr = cleanupErr
	}
	result := processResult(command, waitErr)

	if cancelled || ctx.Err() != nil {
		if groupErr != nil {
			return result, fmt.Errorf("%w: %v", ctx.Err(), groupErr)
		}
		return result, ctx.Err()
	}
	if groupErr != nil {
		return result, groupErr
	}
	if readerErr != nil {
		return result, fmt.Errorf("%s: %w", readerOperation, readerErr)
	}
	if waitErr != nil {
		return result, fmt.Errorf("vendor process exited: %w", waitErr)
	}
	return result, nil
}

func terminateProcessGroup(processGroup int, grace time.Duration) error {
	if !processGroupExists(processGroup) {
		return nil
	}
	_ = syscall.Kill(-processGroup, syscall.SIGTERM)
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline.C:
			if processGroupExists(processGroup) {
				_ = syscall.Kill(-processGroup, syscall.SIGKILL)
			}
			return nil
		case <-ticker.C:
			if !processGroupExists(processGroup) {
				return nil
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
