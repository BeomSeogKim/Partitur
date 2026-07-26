// Package procid reads platform process-start identities without treating any
// checkpoint file as authority.
package procid

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

type MatchStatus string

const (
	MatchingAndLive MatchStatus = "matching_and_live"
	GoneOrReused    MatchStatus = "gone_or_reused"
	Unverifiable    MatchStatus = "unverifiable"
)

type MatchResult struct {
	Status MatchStatus
	Err    error
}

// Read returns the current process-start identity for pid.
func Read(pid int) (runstate.StartIdentity, error) {
	identity, _, err := read(pid)
	return identity, err
}

// Matches compares a recorded identity with the currently observable process.
func Matches(pid int, expected runstate.StartIdentity) MatchResult {
	if expected == nil {
		return MatchResult{Status: Unverifiable, Err: errors.New("recorded start identity is absent")}
	}
	current, zombie, err := read(pid)
	if err != nil {
		if processGone(err) {
			return MatchResult{Status: GoneOrReused}
		}
		return MatchResult{Status: Unverifiable, Err: err}
	}
	if current.Platform() != expected.Platform() {
		return MatchResult{
			Status: Unverifiable,
			Err:    fmt.Errorf("recorded platform %q cannot be compared on %q", expected.Platform(), current.Platform()),
		}
	}
	if zombie || !identitiesEqual(current, expected) {
		return MatchResult{Status: GoneOrReused}
	}
	return MatchResult{Status: MatchingAndLive}
}

func processGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, fs.ErrNotExist)
}

func identitiesEqual(current, expected runstate.StartIdentity) bool {
	switch current := current.(type) {
	case runstate.LinuxStartIdentity:
		recorded, ok := expected.(runstate.LinuxStartIdentity)
		return ok && current == recorded
	case runstate.DarwinStartIdentity:
		recorded, ok := expected.(runstate.DarwinStartIdentity)
		return ok && current == recorded
	default:
		return false
	}
}
