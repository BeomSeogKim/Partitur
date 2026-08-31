//go:build linux || darwin

package launch

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const gateReleaseByte = byte('G')
const launchStderrLimit = adapterkit.MaxDiagnosticBytes

type launchDependencies struct {
	newNonce   func() (string, error)
	newCommand func(string, ...string) *exec.Cmd
	closeGate  func(*os.File) error
}

// Launch starts one trusted trampoline and opens its gate only after
// RecordIdentity returns the matching durable journal receipt.
func Launch(request Request) (*Process, error) {
	return LaunchContext(context.Background(), request)
}

// LaunchContext applies ctx to the still-gated handoff. Once the gate is
// released, the launched process is detached from this context and is owned by
// its recorded session identity.
func LaunchContext(ctx context.Context, request Request) (*Process, error) {
	return launch(ctx, request, launchDependencies{
		newNonce: newNonce,
		newCommand: func(path string, arguments ...string) *exec.Cmd {
			return exec.Command(path, arguments...)
		},
		closeGate: func(file *os.File) error { return file.Close() },
	})
}

func launch(
	ctx context.Context,
	request Request,
	dependencies launchDependencies,
) (*Process, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	nonce, err := dependencies.newNonce()
	if err != nil {
		return nil, fmt.Errorf("allocate launch nonce: %w", err)
	}
	launchDir := filepath.Join(request.AttemptRoot, request.LaunchID)
	if err := os.Mkdir(launchDir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrLaunchCollision
		}
		return nil, fmt.Errorf("create launch handoff directory: %w", err)
	}
	if err := syncDirectory(request.AttemptRoot); err != nil {
		return nil, fmt.Errorf("sync attempt staging directory: %w", err)
	}

	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create launch gate: %w", err)
	}
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		return nil, fmt.Errorf("create identity receipt pipe: %w", err)
	}
	closeParentFiles := func() {
		_ = gateRead.Close()
		_ = readyWrite.Close()
	}
	closeAllFiles := func() {
		closeParentFiles()
		_ = gateWrite.Close()
		_ = readyRead.Close()
	}

	configuration := trampolineConfiguration{
		Kind:               request.Kind,
		LaunchDir:          launchDir,
		Nonce:              nonce,
		Executable:         request.Executable,
		Arguments:          slices.Clone(request.Arguments),
		CommandEnvironment: slices.Clone([]string(request.CommandEnvironment)),
	}
	arguments, err := encodeTrampolineArguments(configuration)
	if err != nil {
		closeAllFiles()
		return nil, err
	}
	command := dependencies.newCommand(request.TrampolinePath, arguments...)
	command.Env = slices.Clone([]string(request.TrampolineEnvironment))
	command.Dir = request.Directory
	command.Stdin = request.Stdin
	command.Stdout = request.Stdout
	stderrCapture, err := newLaunchStderrCapture(request.Stderr)
	if err != nil {
		closeAllFiles()
		return nil, fmt.Errorf("capture launch trampoline stderr: %w", err)
	}
	command.Stderr = stderrCapture.writer
	command.ExtraFiles = []*os.File{gateRead, readyWrite}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	launchError := func(err error) error {
		return stderrCapture.wrap(err)
	}
	releasedError := func(err error) error {
		stderrCapture.closeReader()
		go func() { _ = command.Wait() }()
		return postReleaseError(err)
	}
	if err := command.Start(); err != nil {
		closeAllFiles()
		return nil, launchError(fmt.Errorf("start launch trampoline: %w", err))
	}
	stderrCapture.closeParentWriter()
	closeParentFiles()

	type readyResult struct {
		count int
		err   error
	}
	ready := make(chan readyResult, 1)
	go func() {
		var receipt [1]byte
		count, readyErr := io.ReadFull(readyRead, receipt[:])
		ready <- readyResult{count: count, err: readyErr}
	}()
	var result readyResult
	select {
	case result = <-ready:
	case <-ctx.Done():
		_ = gateWrite.Close()
		_ = command.Process.Kill()
		result = <-ready
		_ = readyRead.Close()
		_ = command.Wait()
		return nil, launchError(fmt.Errorf("launch handoff cancelled: %w", ctx.Err()))
	}
	_ = readyRead.Close()
	if result.err != nil || result.count != 1 {
		_ = gateWrite.Close()
		waitErr := command.Wait()
		if result.err == nil {
			result.err = io.ErrUnexpectedEOF
		}
		if waitErr != nil {
			return nil, launchError(fmt.Errorf(
				"launch trampoline did not publish identity: %v: %w",
				waitErr,
				result.err,
			))
		}
		return nil, launchError(fmt.Errorf("launch trampoline did not publish identity: %w", result.err))
	}

	identity, matched, err := ReadHandoff(launchDir, nonce)
	if err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, launchError(err)
	}
	if !matched {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, launchError(ErrStaleHandoff)
	}
	if identity.PID != command.Process.Pid {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, launchError(fmt.Errorf(
			"%w: published pid %d does not identify trampoline %d",
			ErrInvalidHandoff,
			identity.PID,
			command.Process.Pid,
		))
	}
	match := procid.Matches(identity.PID, identity.Start)
	if match.Status != procid.MatchingAndLive {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, launchError(fmt.Errorf(
			"%w: published process identity is not live: %v",
			ErrInvalidHandoff,
			match.Err,
		))
	}
	if err := ctx.Err(); err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, launchError(fmt.Errorf("launch handoff cancelled: %w", err))
	}

	journalReceipt, err := request.RecordIdentity(identity)
	if err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, launchError(err)
	}
	if err := validateReceipt(request.Kind, journalReceipt); err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, launchError(err)
	}
	reach(request.Probe, recordedPoint(request.Kind))
	if err := ctx.Err(); err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, launchError(fmt.Errorf("launch handoff cancelled: %w", err))
	}
	count, writeErr := gateWrite.Write([]byte{gateReleaseByte})
	if writeErr != nil || count != 1 {
		_ = gateWrite.Close()
		if count == 1 {
			return nil, releasedError(fmt.Errorf("release launch gate: %w", writeErr))
		}
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		// The gate byte never left, so no program was released and no
		// descendant can hold the capture pipe. Reaping here is safe, and
		// omitting it would leave the trampoline unreaped.
		_ = command.Wait()
		return nil, launchError(fmt.Errorf("release launch gate: %w", writeErr))
	}
	if err := dependencies.closeGate(gateWrite); err != nil {
		return nil, releasedError(fmt.Errorf("close launch gate: %w", err))
	}
	return &Process{
		Identity:    identity,
		LaunchDir:   launchDir,
		commandWait: command.Wait,
	}, nil
}

type launchStderrCapture struct {
	writer            io.Writer
	closeParentWriter func()
	closeReader       func()
	wrap              func(error) error
}

type releasedHandoffError struct {
	cause error
}

func (err *releasedHandoffError) Error() string {
	return err.cause.Error()
}

func (err *releasedHandoffError) Unwrap() error {
	return err.cause
}

func (err *releasedHandoffError) Is(target error) bool {
	return target == ErrHandoffReleased
}

func postReleaseError(cause error) error {
	return &releasedHandoffError{cause: cause}
}

func newLaunchStderrCapture(stderr *os.File) (launchStderrCapture, error) {
	if stderr != nil {
		return launchStderrCapture{
			writer:            stderr,
			closeParentWriter: func() {},
			closeReader:       func() {},
			wrap:              func(err error) error { return err },
		}, nil
	}
	read, write, err := os.Pipe()
	if err != nil {
		return launchStderrCapture{}, err
	}
	var buffer boundedLaunchStderr
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buffer, read)
		_ = read.Close()
		close(done)
	}()
	return launchStderrCapture{
		writer: write,
		closeParentWriter: func() {
			_ = write.Close()
		},
		closeReader: func() {
			_ = read.Close()
		},
		wrap: func(err error) error {
			_ = write.Close()
			<-done
			diagnostic := adapterkit.SanitizeDiagnostic(buffer.String())
			if diagnostic == "" {
				return err
			}
			return fmt.Errorf("%w: trampoline stderr: %s", err, diagnostic)
		},
	}, nil
}

type boundedLaunchStderr struct {
	bytes.Buffer
}

func (buffer *boundedLaunchStderr) Write(value []byte) (int, error) {
	count := len(value)
	remaining := launchStderrLimit - buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.Buffer.Write(value)
	}
	return count, nil
}

func recordedPoint(kind Kind) faultpoint.PointID {
	if kind == Criterion {
		return faultpoint.PointLaunchCriterionIdentityRecorded
	}
	return faultpoint.PointLaunchAdapterIdentityRecorded
}

func reach(probe faultpoint.Probe, point faultpoint.PointID) {
	if probe != nil {
		probe.Reached(point)
	}
}

func validateRequest(request Request) error {
	if request.Kind != Adapter && request.Kind != Criterion {
		return fmt.Errorf("%w: launch kind", ErrInvalidRequest)
	}
	if request.TrampolinePath == "" || request.AttemptRoot == "" ||
		request.LaunchID == "" || request.Executable == "" ||
		request.Arguments == nil || request.CommandEnvironment == nil ||
		request.TrampolineEnvironment == nil ||
		request.RecordIdentity == nil {
		return fmt.Errorf("%w: required field absent", ErrInvalidRequest)
	}
	if !filepath.IsAbs(request.TrampolinePath) ||
		!filepath.IsAbs(request.Executable) {
		return fmt.Errorf("%w: executable paths must be absolute", ErrInvalidRequest)
	}
	if filepath.IsAbs(request.LaunchID) ||
		filepath.Clean(request.LaunchID) != request.LaunchID ||
		strings.ContainsAny(request.LaunchID, `/\`) {
		return fmt.Errorf("%w: launch id", ErrInvalidRequest)
	}
	return nil
}

func validateReceipt(
	kind Kind,
	receipt faultpoint.DurabilityReceipt,
) error {
	expectedType := string(runstate.EventAttemptStarted)
	if kind == Criterion {
		expectedType = string(runstate.EventCriterionStarted)
	}
	mutation := receipt.Mutation
	if mutation.Kind != faultpoint.JournalAppend ||
		mutation.EventType != expectedType ||
		mutation.EventID == "" || mutation.Sequence == 0 ||
		mutation.Timestamp == "" || mutation.Path == "" {
		return fmt.Errorf(
			"%w: want durable %s journal append",
			ErrInvalidReceipt,
			expectedType,
		)
	}
	return nil
}

func newNonce() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
