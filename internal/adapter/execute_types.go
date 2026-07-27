package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

var (
	ErrInvalidExecutePlan = errors.New("invalid execute plan")
	ErrSweepUnverifiable  = errors.New("adapter session sweep is unverifiable")
)

// ExecutePlan contains the already-selected attempt and its open adapter
// interval. The caller owns journal payload construction; the callbacks return
// receipts only after the named append is durable.
type ExecutePlan struct {
	AdapterID      string
	AdapterPath    string
	TrampolinePath string
	AttemptRoot    string
	LaunchID       string
	Directory      string
	Request        protocol.ExecuteRequest
	IntervalID     runstate.IntervalID
	IntervalOpened time.Time
	MayPropose     bool
	Draft          bool
	RecordIdentity launch.RecordIdentity
	Recorder       ExecuteRecorder
}

type ExecuteRecorder struct {
	RecordProbe            func(protocol.ProbeResult) (faultpoint.DurabilityReceipt, error)
	RecordArtifact         func(ArtifactObservation) (faultpoint.DurabilityReceipt, error)
	RecordExecutionStopped func(ExecutionStop) (faultpoint.DurabilityReceipt, error)
	RecordOutcome          func(OutcomeObservation) (faultpoint.DurabilityReceipt, error)
	ObserveLog             func(protocol.LogEvent)
	ObserveProgress        func(protocol.ProgressEvent)
}

type ArtifactObservation struct {
	ArtifactID string
	Kind       string
	Path       string
	SourcePath string
}

type ExecutionStop struct {
	IntervalID        runstate.IntervalID
	Reason            string
	Charging          string
	ChargedDurationMS int64
	ObservedAt        time.Time
}

type RaisedDecision struct {
	Kind     protocol.EventType
	Question *protocol.QuestionEvent
	Proposal *protocol.ProposalEvent
}

type OutcomeObservation struct {
	EventType     string
	Result        protocol.ExecuteResult
	FailureReason string
	Raised        []RaisedDecision
	Stderr        string
}

type ExecuteReport struct {
	Probe     protocol.ProbeResult
	Result    *protocol.ExecuteResult
	Artifacts []ArtifactObservation
	Proposals []protocol.ProposalEvent
	Questions []protocol.QuestionEvent
	Stderr    string
}

// HaltError reports a recovery halt boundary. In particular, callers must not
// close the interval or append an attempt outcome after ErrSweepUnverifiable.
type HaltError struct {
	Reason error
	Cause  error
}

func (e *HaltError) Error() string {
	return e.Reason.Error() + ": " + e.Cause.Error()
}

func (e *HaltError) Unwrap() error {
	return e.Reason
}

// Execute runs one gated adapter session. Cancellation is owned by the
// caller's §6 path; Execute sweeps the session and returns ctx.Err without
// closing the interval or recording a response-derived outcome.
func (c *Client) Execute(ctx context.Context, plan ExecutePlan) (ExecuteReport, error) {
	return c.execute(ctx, plan)
}
