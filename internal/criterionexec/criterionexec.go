// Package criterionexec owns the process-facing execution of hard.run.
package criterionexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

const outputLimit = 4 * 1024 * 1024

type Config struct {
	RunID          runstate.RunID
	AttemptID      runstate.AttemptID
	AttemptRoot    string
	Worktree       string
	RepositoryRoot string
	SubjectTree    string
	TrampolinePath string
	RemainingMS    int64
	Probe          faultpoint.Probe
	// Cancel is the owning driver's continuous control watch. A closed channel
	// stops the recorded criterion session before Run returns to that driver.
	Cancel <-chan struct{}
}

// Run executes one score-declared command through the criterion trampoline.
func Run(config Config, request acceptance.RunCriterionRequest) acceptance.RunCriterionResult {
	if config.AttemptRoot == "" || config.Worktree == "" || config.RepositoryRoot == "" || config.SubjectTree == "" || config.TrampolinePath == "" || request.ID == "" || len(request.Argv) == 0 || request.RecordStarted == nil {
		return acceptance.RunCriterionResult{Err: errors.New("criterion executor is incomplete")}
	}
	command, err := exec.LookPath(request.Argv[0])
	if err != nil {
		return spawnFailure(fmt.Errorf("resolve criterion command: %w", err))
	}
	command, err = filepath.Abs(command)
	if err != nil {
		return acceptance.RunCriterionResult{Err: err}
	}
	capture := filepath.Join(config.RepositoryRoot, ".partitur", "runs", string(config.RunID), "attempts", string(config.AttemptID), "criteria", request.ID)
	if err := os.MkdirAll(capture, 0o700); err != nil {
		return acceptance.RunCriterionResult{Err: fmt.Errorf("create criterion capture: %w", err)}
	}
	if err := os.MkdirAll(config.AttemptRoot, 0o700); err != nil {
		return acceptance.RunCriterionResult{Err: fmt.Errorf("create criterion staging: %w", err)}
	}
	temporary := filepath.Join(config.AttemptRoot, "tmp")
	if err := os.MkdirAll(temporary, 0o700); err != nil {
		return acceptance.RunCriterionResult{Err: fmt.Errorf("create criterion temporary directory: %w", err)}
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return acceptance.RunCriterionResult{Err: err}
	}
	defer stdoutRead.Close()
	defer stdoutWrite.Close()
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		return acceptance.RunCriterionResult{Err: err}
	}
	defer stderrRead.Close()
	defer stderrWrite.Close()
	stdout, err := os.OpenFile(filepath.Join(capture, "stdout"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return acceptance.RunCriterionResult{Err: err}
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(capture, "stderr"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return acceptance.RunCriterionResult{Err: err}
	}
	defer stderr.Close()
	environment := criterionEnvironment(config.Worktree, temporary)
	launchID := "criterion-" + request.ID
	launchContext, cancelLaunch := context.WithCancel(context.Background())
	defer cancelLaunch()
	if criterionCancelled(config.Cancel) {
		cancelLaunch()
	} else if config.Cancel != nil {
		go func() {
			select {
			case <-config.Cancel:
				cancelLaunch()
			case <-launchContext.Done():
			}
		}()
	}
	process, err := launch.LaunchContext(launchContext, launch.Request{
		Kind: launch.Criterion, TrampolinePath: config.TrampolinePath, AttemptRoot: config.AttemptRoot, LaunchID: launchID,
		Executable: command, Arguments: request.Argv[1:], CommandEnvironment: launch.CommandEnvironment(environment),
		TrampolineEnvironment: launch.TrampolineEnvironment(slices.Clone(os.Environ())), Directory: config.Worktree,
		Stdout: stdoutWrite, Stderr: stderrWrite, Probe: config.Probe,
		RecordIdentity: func(identity runstate.ProcessIdentity) (faultpoint.DurabilityReceipt, error) {
			receipt, recordErr := request.RecordStarted(identity)
			if criterionCancelled(config.Cancel) {
				cancelLaunch()
			}
			return receipt, recordErr
		},
	})
	if err != nil {
		if criterionCancelled(config.Cancel) {
			return acceptance.RunCriterionResult{Cancelled: true}
		}
		return spawnFailure(err)
	}
	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()
	stdoutDone := make(chan bool, 1)
	stderrDone := make(chan bool, 1)
	go func() { stdoutDone <- copyBounded(stdout, stdoutRead) }()
	go func() { stderrDone <- copyBounded(stderr, stderrRead) }()
	started := time.Now()
	timeout, budget, deadlineTied := effectiveTimeout(config.RemainingMS, request.TimeoutMin)
	wait := make(chan error, 1)
	go func() { wait <- process.Wait() }()
	var waitErr error
	timedOut := timeout <= 0
	cancelled := false
	if !timedOut {
		timer := time.NewTimer(timeout)
		select {
		case waitErr = <-wait:
		case <-timer.C:
			timedOut = true
		case <-config.Cancel:
			cancelled = true
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	if !cancelled && criterionCancelled(config.Cancel) {
		cancelled = true
	}
	grace := 30 * time.Second
	if timeout <= 0 {
		grace = 0
	}
	if err := adapter.SweepSession(process.Identity, grace); err != nil {
		return acceptance.RunCriterionResult{Outcome: "ERROR", Reason: "criterion_errored", ErrorDetail: "sweep_unverifiable", OutputRef: criterionOutputRef(config, request.ID)}
	}
	if timedOut || cancelled {
		waitErr = <-wait
	}
	truncated := streams(<-stdoutDone, <-stderrDone)
	if cancelled || criterionCancelled(config.Cancel) {
		return acceptance.RunCriterionResult{Cancelled: true, OutputRef: criterionOutputRef(config, request.ID)}
	}
	matched, err := verifySubject(config)
	if err != nil {
		return acceptance.RunCriterionResult{Outcome: "ERROR", Reason: "criterion_errored", ErrorDetail: "workspace_verification_failed", OutputRef: criterionOutputRef(config, request.ID), TruncatedStreams: truncated}
	}
	if !matched {
		return acceptance.RunCriterionResult{Outcome: "FAIL", Reason: "acceptance_mutated_workspace", DurationMS: time.Since(started).Milliseconds(), OutputRef: criterionOutputRef(config, request.ID), TruncatedStreams: truncated}
	}
	if timedOut {
		duration := time.Since(started).Milliseconds()
		if timeout <= 0 {
			duration = 0
		}
		return acceptance.RunCriterionResult{BudgetExhausted: budget, DeadlineTied: deadlineTied, Outcome: "ERROR", Reason: "criterion_errored", ErrorDetail: timeoutDetail(budget), DurationMS: duration, OutputRef: criterionOutputRef(config, request.ID), TruncatedStreams: truncated}
	}
	result := acceptance.RunCriterionResult{DurationMS: time.Since(started).Milliseconds(), OutputRef: criterionOutputRef(config, request.ID), TruncatedStreams: truncated}
	if waitErr == nil {
		result.Outcome = "PASS"
		return result
	}
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		code := int64(exit.ExitCode())
		result.ExitCode = &code
		result.Outcome = "FAIL"
		result.Reason = "criterion_failed"
		return result
	}
	return acceptance.RunCriterionResult{Outcome: "ERROR", Reason: "criterion_errored", ErrorDetail: "criterion_wait_failed", OutputRef: criterionOutputRef(config, request.ID), TruncatedStreams: truncated}
}

func criterionCancelled(cancel <-chan struct{}) bool {
	if cancel == nil {
		return false
	}
	select {
	case <-cancel:
		return true
	default:
		return false
	}
}

func spawnFailure(err error) acceptance.RunCriterionResult {
	return acceptance.RunCriterionResult{SpawnFailed: true, Outcome: "ERROR", Reason: "criterion_errored", ErrorDetail: "spawn_failed"}
}
func criterionEnvironment(worktree, temporary string) []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "PWD=" + worktree, "TMPDIR=" + temporary, "TMP=" + temporary, "TEMP=" + temporary}
}
func criterionOutputRef(config Config, id string) string {
	return filepath.ToSlash(filepath.Join("attempts", string(config.AttemptID), "criteria", id))
}
func streams(stdout, stderr bool) []string {
	result := []string{}
	if stderr {
		result = append(result, "stderr")
	}
	if stdout {
		result = append(result, "stdout")
	}
	slices.Sort(result)
	return result
}
func copyBounded(destination *os.File, source *os.File) bool {
	remaining := int64(outputLimit)
	buffer := make([]byte, 32*1024)
	truncated := false
	for {
		count, err := source.Read(buffer)
		if count > 0 {
			write := count
			if int64(write) > remaining {
				write = int(remaining)
			}
			if write > 0 {
				_, _ = destination.Write(buffer[:write])
				remaining -= int64(write)
			}
			if write < count {
				truncated = true
			}
		}
		if err == io.EOF {
			return truncated
		}
		if err != nil {
			return truncated
		}
	}
}
func verifySubject(config Config) (bool, error) {
	return workspace.VerifyRecoverySubject(config.RepositoryRoot, config.Worktree, config.SubjectTree)
}

func timeoutDetail(budget bool) string {
	if budget {
		return "acceptance_budget_exhausted"
	}
	return "criterion_timeout"
}

func effectiveTimeout(remainingMS, timeoutMin int64) (time.Duration, bool, bool) {
	timeout := time.Duration(remainingMS) * time.Millisecond
	budget := true
	criterionTimeout := time.Duration(timeoutMin) * time.Minute
	deadlineTied := timeoutMin != 0 && criterionTimeout == timeout
	if timeoutMin != 0 && criterionTimeout <= timeout {
		timeout = criterionTimeout
		budget = false
	}
	return timeout, budget, deadlineTied
}
