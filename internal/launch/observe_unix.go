//go:build linux || darwin

package launch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// HandoffObservation is one non-mutating sample of a launch handoff.
// MarkerFree proves only the narrow §4 property: no released mutator remains.
type HandoffObservation struct {
	Identity    runstate.ProcessIdentity
	HasMarker   bool
	HasIdentity bool
	MarkerFree  bool
	MarkerHeld  bool
}

// ObserveHandoff reads the two nonce-bearing handoff files and attempts a
// nonblocking marker lock. It never writes, removes, or releases another
// process's lock.
func ObserveHandoff(launchDir string) (HandoffObservation, error) {
	marker, err := os.Open(filepath.Join(launchDir, markerName))
	if errors.Is(err, fs.ErrNotExist) {
		return HandoffObservation{MarkerFree: true}, nil
	}
	if err != nil {
		return HandoffObservation{}, fmt.Errorf("open launch marker: %w", err)
	}
	defer marker.Close()
	nonce, err := os.ReadFile(filepath.Join(launchDir, markerName))
	if err != nil {
		return HandoffObservation{}, fmt.Errorf("read launch marker: %w", err)
	}
	if len(nonce) == 0 {
		return HandoffObservation{}, ErrInvalidHandoff
	}
	identity, matched, err := ReadHandoff(launchDir, string(nonce))
	if err != nil {
		return HandoffObservation{}, err
	}
	if matched {
		return HandoffObservation{Identity: identity, HasMarker: true, HasIdentity: true}, nil
	}
	if err := syscall.Flock(int(marker.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		if unlockErr := syscall.Flock(int(marker.Fd()), syscall.LOCK_UN); unlockErr != nil {
			return HandoffObservation{}, fmt.Errorf("release launch marker observation: %w", unlockErr)
		}
		return HandoffObservation{HasMarker: true, MarkerFree: true}, nil
	} else if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return HandoffObservation{HasMarker: true, MarkerHeld: true}, nil
	} else {
		return HandoffObservation{}, fmt.Errorf("observe launch marker lock: %w", err)
	}
}
