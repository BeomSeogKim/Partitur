// Package driver composes the durable one-movement run execution slice.
package driver

import (
	"context"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/cancellation"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

type Outcome string

const (
	OutcomeSucceeded    Outcome = "SUCCEEDED"
	OutcomeFailed       Outcome = "FAILED"
	OutcomeCancelled    Outcome = "CANCELLED"
	OutcomeWaitingHuman Outcome = "WAITING_HUMAN"
	OutcomeHalted       Outcome = "HALTED"
	OutcomeInterrupted  Outcome = "INTERRUPTED"
)

// Result is the operator-visible terminal projection of one driver episode.
type Result struct {
	RunID   runstate.RunID
	Outcome Outcome
	Reason  string
	Err     error

	prepareAcknowledged bool
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
	// ProposalDisposition is the sole bridge from an adapter proposal to the
	// §9 transaction. It runs before this package appends attempt.blocked, so a
	// blocking route descriptor can be frozen in that one terminal event. A nil
	// implementation is deliberately fail-closed when a proposal arrives.
	ProposalDisposition ProposalDispositioner
	// afterMovementFailed is a test-only interleaving hook. Production leaves it nil.
	afterMovementFailed func()
	// AfterPrepareAcknowledged is a test-only hook between the durable lease
	// move and the shared approval table. Production leaves it nil.
	AfterPrepareAcknowledged func()
	// AcquireDriver is a test-only fresh-acquisition seam. Production leaves it nil.
	AcquireDriver func(*runstore.Store, runstate.RunID, []runstate.MovementSeed) (*runstore.Driver, error)
}

// ProposalDispositioner prepares the already-specified durable consequence of
// one adapter proposal. It does not append attempt.blocked: the driver remains
// the single owner of the adapter outcome event and its wire-order payload.
type ProposalDispositioner interface {
	PrepareAdapterProposal(context.Context, AdapterProposal) (AdapterProposalDisposition, error)
}

// AutoProposalShapeGuard lets a dispositioner that can prepare an auto
// approval reject a mixed raised set before the prepare mutation is possible.
// Ordinary non-blocking proposals remain valid for dispositioners that do not
// produce prepares.
type AutoProposalShapeGuard interface {
	RequiresSingleRaisedForAuto() bool
}

// AdapterProposal is the driver-owned identity boundary supplied before
// attempt.blocked becomes durable.
type AdapterProposal struct {
	Store         *runstore.Store
	Authority     *runstore.Driver
	RunID         runstate.RunID
	ScoreRevision uint64
	AttemptID     runstate.AttemptID
	MovementID    runstate.MovementID
	PartID        string
	ProposalID    runstate.ProposalID
	DecisionID    string
	Event         protocol.ProposalEvent
}

// AdapterProposalDisposition carries the optional frozen route descriptor.
// Its exact payload is validated by runstate when the driver appends
// attempt.blocked; a nil descriptor is the specified rejection/non-blocking
// shape, not a request source.
type AdapterProposalDisposition struct {
	RouteDescriptor map[string]any
	// AppendRoute completes a durable routed_human marker only after the
	// driver has appended the proposal's attempt.blocked source event. It is
	// nil for rejections, and never for a returned route descriptor.
	AppendRoute func(context.Context) error
	// PreparedReceipt is the durable auto-approval request. It replaces the
	// otherwise-required attempt.blocked receipt because preparation raises the
	// mutation barrier.
	PreparedReceipt *faultpoint.DurabilityReceipt
}

// AdapterExecutor is the process-facing adapter boundary for one attempt.
// The production adapter client implements it; tests may provide a
// deterministic completed execution.
type AdapterExecutor interface {
	Resolve(adapterID string) (string, error)
	Execute(context.Context, adapter.ExecutePlan) (adapter.ExecuteReport, error)
}
