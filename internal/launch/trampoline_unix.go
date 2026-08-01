//go:build linux || darwin

package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const (
	gateFD  = 3
	readyFD = 4
)

type trampolineConfiguration struct {
	Kind        Kind     `json:"kind"`
	LaunchDir   string   `json:"launch_dir"`
	Nonce       string   `json:"nonce"`
	Executable  string   `json:"executable"`
	Arguments   []string `json:"arguments"`
	Environment []string `json:"environment"`
}

func encodeTrampolineArguments(
	configuration trampolineConfiguration,
) ([]string, error) {
	contents, err := json.Marshal(configuration)
	if err != nil {
		return nil, fmt.Errorf("encode trampoline arguments: %w", err)
	}
	return []string{"--partitur-launch", string(contents)}, nil
}

func decodeTrampolineArguments(
	arguments []string,
) (trampolineConfiguration, error) {
	if len(arguments) != 2 || arguments[0] != "--partitur-launch" {
		return trampolineConfiguration{}, fmt.Errorf(
			"%w: trampoline arguments",
			ErrInvalidRequest,
		)
	}
	var configuration trampolineConfiguration
	if err := decodeExact([]byte(arguments[1]), &configuration); err != nil {
		return trampolineConfiguration{}, fmt.Errorf(
			"%w: trampoline arguments: %v",
			ErrInvalidRequest,
			err,
		)
	}
	if configuration.Kind != Adapter && configuration.Kind != Criterion {
		return trampolineConfiguration{}, fmt.Errorf(
			"%w: trampoline kind",
			ErrInvalidRequest,
		)
	}
	if configuration.LaunchDir == "" || configuration.Nonce == "" ||
		configuration.Executable == "" || configuration.Arguments == nil || configuration.Environment == nil {
		return trampolineConfiguration{}, fmt.Errorf(
			"%w: trampoline field absent",
			ErrInvalidRequest,
		)
	}
	if !filepath.IsAbs(configuration.LaunchDir) ||
		!filepath.IsAbs(configuration.Executable) {
		return trampolineConfiguration{}, fmt.Errorf(
			"%w: trampoline paths must be absolute",
			ErrInvalidRequest,
		)
	}
	return configuration, nil
}

// RunTrampoline acquires and publishes the launch handoff, consumes the
// inherited gate, and execs the requested program in place. The probe exposes
// the exact Appendix E marker-held and gate-released boundaries.
func RunTrampoline(arguments []string, probe faultpoint.Probe) error {
	if probe == nil {
		return errors.New("launch: nil faultpoint probe")
	}
	configuration, err := decodeTrampolineArguments(arguments)
	if err != nil {
		return err
	}
	gate := os.NewFile(gateFD, "partitur-launch-gate")
	ready := os.NewFile(readyFD, "partitur-identity-receipt")
	if gate == nil || ready == nil {
		return errors.New("launch: inherited descriptors are absent")
	}
	defer gate.Close()
	defer ready.Close()

	marker, err := acquireMarker(configuration.LaunchDir, configuration.Nonce)
	if err != nil {
		return err
	}
	defer marker.Close()
	if err := keepAcrossExec(marker); err != nil {
		return fmt.Errorf("preserve launch marker across exec: %w", err)
	}
	probe.Reached(markerPoint(configuration.Kind))
	pid := os.Getpid()
	sessionID, err := currentSessionID()
	if err != nil {
		return fmt.Errorf("read trampoline session id: %w", err)
	}
	start, err := procid.Read(pid)
	if err != nil {
		return fmt.Errorf("read trampoline start identity: %w", err)
	}
	identity := runstate.ProcessIdentity{
		PID:       pid,
		SessionID: sessionID,
		Start:     start,
	}
	contents, err := encodeIdentity(configuration.Nonce, identity)
	if err != nil {
		return err
	}
	if err := publishIdentity(
		filepath.Join(configuration.LaunchDir, identityName),
		contents,
		syncDirectory,
	); err != nil {
		return err
	}
	probe.Reached(identityPoint(configuration.Kind))
	if _, err := ready.Write([]byte{1}); err != nil {
		return fmt.Errorf("publish identity receipt: %w", err)
	}
	if err := ready.Close(); err != nil {
		return fmt.Errorf("close identity receipt: %w", err)
	}

	var release [1]byte
	count, err := io.ReadFull(gate, release[:])
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || count == 0 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read launch gate: %w", err)
	}
	if release[0] != gateReleaseByte {
		return fmt.Errorf("invalid launch gate byte %q", release[0])
	}
	if err := gate.Close(); err != nil {
		return fmt.Errorf("close launch gate: %w", err)
	}
	probe.Reached(gatePoint(configuration.Kind))

	argv := make([]string, 1, len(configuration.Arguments)+1)
	argv[0] = configuration.Executable
	argv = append(argv, configuration.Arguments...)
	return syscall.Exec(configuration.Executable, argv, configuration.Environment)
}

func acquireMarker(launchDir, nonce string) (*os.File, error) {
	path := filepath.Join(launchDir, markerName)
	marker, err := os.OpenFile(
		path,
		os.O_CREATE|os.O_EXCL|os.O_RDWR,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("create launch marker: %w", err)
	}
	cleanup := func(cause error) (*os.File, error) {
		_ = marker.Close()
		return nil, cause
	}
	if _, err := marker.WriteString(nonce); err != nil {
		return cleanup(fmt.Errorf("write launch marker: %w", err))
	}
	if err := marker.Sync(); err != nil {
		return cleanup(fmt.Errorf("sync launch marker: %w", err))
	}
	if err := syscall.Flock(int(marker.Fd()), syscall.LOCK_EX); err != nil {
		return cleanup(fmt.Errorf("acquire launch marker: %w", err))
	}
	return marker, nil
}

func publishIdentity(
	path string,
	contents []byte,
	syncParent func(string) error,
) error {
	temporary := path + ".tmp"
	file, err := os.OpenFile(
		temporary,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("create identity temporary: %w", err)
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return fmt.Errorf("write identity temporary: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync identity temporary: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close identity temporary: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish identity: %w", err)
	}
	removeTemporary = false
	if err := syncParent(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync identity directory: %w", err)
	}
	return nil
}

func markerPoint(kind Kind) faultpoint.PointID {
	if kind == Criterion {
		return faultpoint.PointLaunchCriterionMarkerHeld
	}
	return faultpoint.PointLaunchAdapterMarkerHeld
}

func gatePoint(kind Kind) faultpoint.PointID {
	if kind == Criterion {
		return faultpoint.PointLaunchCriterionGateReleased
	}
	return faultpoint.PointLaunchAdapterGateReleased
}

func identityPoint(kind Kind) faultpoint.PointID {
	if kind == Criterion {
		return faultpoint.PointLaunchCriterionIdentityPublished
	}
	return faultpoint.PointLaunchAdapterIdentityPublished
}
