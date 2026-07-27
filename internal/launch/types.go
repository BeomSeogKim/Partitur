// Package launch owns the gated, session-leader launch handoff shared by
// run-scoped adapters and external criteria.
package launch

import (
	"errors"
	"os"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

type Kind string

const (
	Adapter   Kind = "adapter"
	Criterion Kind = "criterion"
)

var (
	ErrInvalidRequest  = errors.New("invalid launch request")
	ErrLaunchCollision = errors.New("launch handoff already exists")
	ErrStaleHandoff    = errors.New("launch handoff nonce does not match")
	ErrInvalidHandoff  = errors.New("invalid launch handoff")
	ErrInvalidReceipt  = errors.New("invalid launch durability receipt")
)

// RecordIdentity appends and durably syncs the event that owns the launched
// process identity. Returning an error leaves the gate closed.
type RecordIdentity func(
	runstate.ProcessIdentity,
) (faultpoint.DurabilityReceipt, error)

// Request contains only the inputs needed to launch one adapter or external
// criterion. AttemptRoot is the attempt staging directory; LaunchID selects
// its per-launch child directory.
type Request struct {
	Kind           Kind
	TrampolinePath string
	AttemptRoot    string
	LaunchID       string
	Executable     string
	Arguments      []string
	Environment    []string
	Directory      string
	Stdin          *os.File
	Stdout         *os.File
	Stderr         *os.File
	RecordIdentity RecordIdentity
}

// Process is a released trampoline that has exec'd, or is about to exec, the
// requested program in place.
type Process struct {
	Identity  runstate.ProcessIdentity
	LaunchDir string

	commandWait func() error
}

// Wait waits for the launched process. The caller must drain any owned output
// pipes before calling Wait.
func (process *Process) Wait() error {
	if process == nil || process.commandWait == nil {
		return errors.New("launch: nil Process")
	}
	return process.commandWait()
}
