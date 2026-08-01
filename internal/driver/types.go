// Package driver composes the durable one-movement run execution slice.
package driver

import (
	"context"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/cancellation"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
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

// AttemptExecution is the complete immutable and run-owned input for one
// already-created attempt. Callers supply the pinned score and resolved cast;
// this package never reloads current-root configuration while executing it.
type AttemptExecution struct {
	RepositoryRoot       string
	Score                *score.Score
	Cast                 *cast.Cast
	RunID                runstate.RunID
	Attempt              *workspace.AttemptWorkspace
	BaseTree             string
	BaseCompositionHash  string
	CandidateTree        string
	Authority            *runstore.Driver
	PerformerID          string
	SelectionReason      string
	SelectionCausationID string
	// SelectionDurable says the live between-unit scheduler has already
	// appended performer.selected before this attempt is launched.
	SelectionDurable  bool
	RemainingMS       int64
	RetriesConsumed   int
	VisitedPerformers []string
	Control           *cancellation.Watcher
}

// AcceptanceBudgetTerminalization supplies the already-open acceptance
// interval close to the shared budget terminal sequence. Live execution owns a
// measured close; recovery supplies its authoritative clamped close.
type AcceptanceBudgetTerminalization struct {
	RepositoryRoot string
	RunID          runstate.RunID
	AttemptID      runstate.AttemptID
	Authority      *runstore.Driver
	Control        *cancellation.Watcher
	Probe          faultpoint.Probe
	Close          func() error
}

// ExecutionDependencies are the process-facing dependencies of an attempt.
// They are separate from live-run creation so recovery can reuse execution
// without validating current inputs or starting a new run.
type ExecutionDependencies struct {
	Probe             faultpoint.Probe
	Client            AdapterExecutor
	ResolveTrampoline func() (string, error)
	Now               func() time.Time
	NewID             func() (string, error)
	// afterMovementFailed is a test-only interleaving hook. Production leaves it nil.
	afterMovementFailed func()
}

// AdapterExecutor is the process-facing adapter boundary for one attempt.
// The production adapter client implements it; tests may provide a
// deterministic completed execution.
type AdapterExecutor interface {
	Resolve(adapterID string) (string, error)
	Execute(context.Context, adapter.ExecutePlan) (adapter.ExecuteReport, error)
}
