// Package driver composes the durable one-movement run execution slice.
package driver

import (
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

type Outcome string

const (
	OutcomeSucceeded   Outcome = "SUCCEEDED"
	OutcomeFailed      Outcome = "FAILED"
	OutcomeCancelled   Outcome = "CANCELLED"
	OutcomeHalted      Outcome = "HALTED"
	OutcomeInterrupted Outcome = "INTERRUPTED"
)

// Result is the operator-visible terminal projection of one driver episode.
type Result struct {
	RunID   runstate.RunID
	Outcome Outcome
	Reason  string
	Err     error
}

// StartedObserver is called exactly once after run.started is durable.
type StartedObserver func(runstate.RunID) error
