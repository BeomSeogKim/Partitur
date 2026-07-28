//go:build linux || darwin

package launch

import (
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

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const gateReleaseByte = byte('G')

type launchDependencies struct {
	newNonce   func() (string, error)
	newCommand func(string, ...string) *exec.Cmd
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
		Kind:       request.Kind,
		LaunchDir:  launchDir,
		Nonce:      nonce,
		Executable: request.Executable,
		Arguments:  slices.Clone(request.Arguments),
	}
	arguments, err := encodeTrampolineArguments(configuration)
	if err != nil {
		closeAllFiles()
		return nil, err
	}
	command := dependencies.newCommand(request.TrampolinePath, arguments...)
	command.Env = slices.Clone(request.Environment)
	command.Dir = request.Directory
	command.Stdin = request.Stdin
	command.Stdout = request.Stdout
	command.Stderr = request.Stderr
	command.ExtraFiles = []*os.File{gateRead, readyWrite}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		closeAllFiles()
		return nil, fmt.Errorf("start launch trampoline: %w", err)
	}
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
		return nil, fmt.Errorf("launch handoff cancelled: %w", ctx.Err())
	}
	_ = readyRead.Close()
	if result.err != nil || result.count != 1 {
		_ = gateWrite.Close()
		waitErr := command.Wait()
		if result.err == nil {
			result.err = io.ErrUnexpectedEOF
		}
		if waitErr != nil {
			return nil, fmt.Errorf(
				"launch trampoline did not publish identity: %v: %w",
				waitErr,
				result.err,
			)
		}
		return nil, fmt.Errorf("launch trampoline did not publish identity: %w", result.err)
	}

	identity, matched, err := ReadHandoff(launchDir, nonce)
	if err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, err
	}
	if !matched {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, ErrStaleHandoff
	}
	if identity.PID != command.Process.Pid {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, fmt.Errorf(
			"%w: published pid %d does not identify trampoline %d",
			ErrInvalidHandoff,
			identity.PID,
			command.Process.Pid,
		)
	}
	match := procid.Matches(identity.PID, identity.Start)
	if match.Status != procid.MatchingAndLive {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, fmt.Errorf(
			"%w: published process identity is not live: %v",
			ErrInvalidHandoff,
			match.Err,
		)
	}
	if err := ctx.Err(); err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, fmt.Errorf("launch handoff cancelled: %w", err)
	}

	journalReceipt, err := request.RecordIdentity(identity)
	if err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, err
	}
	if err := validateReceipt(request.Kind, journalReceipt); err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, err
	}
	reach(request.Probe, recordedPoint(request.Kind))
	if err := ctx.Err(); err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, fmt.Errorf("launch handoff cancelled: %w", err)
	}
	if _, err := gateWrite.Write([]byte{gateReleaseByte}); err != nil {
		_ = gateWrite.Close()
		_ = command.Wait()
		return nil, fmt.Errorf("release launch gate: %w", err)
	}
	if err := gateWrite.Close(); err != nil {
		_ = command.Wait()
		return nil, fmt.Errorf("close launch gate: %w", err)
	}
	return &Process{
		Identity:    identity,
		LaunchDir:   launchDir,
		commandWait: command.Wait,
	}, nil
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
		request.Arguments == nil || request.Environment == nil ||
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
