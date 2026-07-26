// Package runstate implements the E-scoped partial journal projection.
//
// State is a pure function of the initial movement seed derived from the
// authenticated pinned score and the journal. This package supports only the
// twenty-two event types needed by DESIGN Appendix E. Valid registry events
// outside that subset fail closed with ErrUnsupportedEventType.
//
// Amendment projection is intentionally auto-mode only. Human histories need
// decision-lifecycle projection and revision-aware movement-set handling,
// neither of which is in this E-scoped projector.
package runstate

import "encoding/json"

type RunID string
type MovementID string
type AttemptID string
type CriterionID string
type PrepareID string
type ProposalID string
type IntervalID string
type Hash string

// MovementSeed is derived from the authenticated pinned score.
type MovementSeed struct {
	ID      MovementID
	Initial MovementState
}

type RunLifecycle string

const (
	RunNotStarted RunLifecycle = ""
	RunRunning    RunLifecycle = "RUNNING"
	RunSucceeded  RunLifecycle = "SUCCEEDED"
	RunFailed     RunLifecycle = "FAILED"
	RunCancelled  RunLifecycle = "CANCELLED"
)

func (state RunLifecycle) Terminal() bool {
	return state == RunSucceeded || state == RunFailed || state == RunCancelled
}

type MovementState string

const (
	MovementPending      MovementState = "PENDING"
	MovementReady        MovementState = "READY"
	MovementRunning      MovementState = "RUNNING"
	MovementCancelled    MovementState = "CANCELLED"
	MovementInapplicable MovementState = "INAPPLICABLE"
)

type AttemptState string

const (
	AttemptStarting   AttemptState = "STARTING"
	AttemptRunning    AttemptState = "RUNNING"
	AttemptVerifying  AttemptState = "VERIFYING"
	AttemptCompleted  AttemptState = "COMPLETED"
	AttemptFailed     AttemptState = "FAILED"
	AttemptCancelled  AttemptState = "CANCELLED"
	AttemptSuperseded AttemptState = "SUPERSEDED"
)

func (state AttemptState) terminal() bool {
	return state == AttemptCompleted || state == AttemptFailed ||
		state == AttemptCancelled || state == AttemptSuperseded
}

// StartIdentity is the strict platform-tagged process-start identity union.
type StartIdentity interface {
	Platform() string
	isStartIdentity()
}

type LinuxStartIdentity struct {
	BootID     string
	StartTicks string
}

func (LinuxStartIdentity) Platform() string { return "linux" }
func (LinuxStartIdentity) isStartIdentity() {}

type DarwinStartIdentity struct {
	StartTVSec  uint64
	StartTVUsec uint64
}

func (DarwinStartIdentity) Platform() string { return "darwin" }
func (DarwinStartIdentity) isStartIdentity() {}

type ProcessIdentity struct {
	PID       int
	SessionID int
	Start     StartIdentity
}

type ScoreHead struct {
	Revision     uint64
	SemanticHash Hash
	FileHash     Hash
}

type Authority struct {
	Epoch uint64
	Owner *AuthorityOwner
}

type AuthorityOwner struct {
	PID   int
	Start StartIdentity
}

type PendingPrepare struct {
	ID                     PrepareID
	ProposalID             ProposalID
	Mode                   string
	EnvelopeClass          string
	BaseHead               ScoreHead
	NewHead                ScoreHead
	PlanRecordHash         Hash
	ObservedAuthorityEpoch uint64
	QuiesceDeadline        string
	TargetAttemptIDs       []AttemptID
	ClassifierVersion      uint64
	IdentityVersions       json.RawMessage
}

type ExecutionInterval struct {
	ID               IntervalID
	Phase            string
	WallStart        string
	RemainingAtStart int64
}

type Disposition struct {
	Charged          string `json:"charged"`
	MovementTerminal bool   `json:"movement_terminal"`
}

type Attempt struct {
	MovementID MovementID
	State      AttemptState
	Failure    *AttemptFailure
}

type AttemptFailure struct {
	Kind        string
	Reason      string
	Disposition Disposition
}

type AdapterLaunch struct {
	AttemptID AttemptID
	Process   ProcessIdentity
}

// CriterionLaunch is a strict union of the three launch variants in B.3.
type CriterionLaunch interface {
	isCriterionLaunch()
}

type SpawnedCriterionLaunch struct {
	Process ProcessIdentity
}

func (SpawnedCriterionLaunch) isCriterionLaunch() {}

type SpawnFailedCriterionLaunch struct{}

func (SpawnFailedCriterionLaunch) isCriterionLaunch() {}

type InProcessCriterionLaunch struct{}

func (InProcessCriterionLaunch) isCriterionLaunch() {}

type CriterionLaunchKey struct {
	AttemptID   AttemptID
	CriterionID CriterionID
}

type CriterionRecord struct {
	Started     bool
	Completed   bool
	SpecHash    Hash
	SubjectTree string
	Outcome     string
}

type Acceptance struct {
	Started             bool
	SubjectTree         string
	SpecHash            Hash
	PlannedCriterionIDs []CriterionID
	Criteria            map[CriterionID]CriterionRecord
}

type State struct {
	Run               RunLifecycle
	Movements         map[MovementID]MovementState
	Attempts          map[AttemptID]Attempt
	ScoreHead         ScoreHead
	Authority         Authority
	PendingPrepare    *PendingPrepare
	OpenExecution     *ExecutionInterval
	ConsumedBudgetMS  int64
	AdapterLaunches   map[AttemptID]AdapterLaunch
	CriterionLaunches map[CriterionLaunchKey]CriterionLaunch
	Acceptances       map[AttemptID]Acceptance
	CancelRequested   bool
}

type EventType string

const (
	EventRunStarted                 EventType = "run.started"
	EventRunSucceeded               EventType = "run.succeeded"
	EventRunFailed                  EventType = "run.failed"
	EventRunCancelled               EventType = "run.cancelled"
	EventMovementReady              EventType = "movement.ready"
	EventMovementStarted            EventType = "movement.started"
	EventPerformerSelected          EventType = "performer.selected"
	EventAttemptStarted             EventType = "attempt.started"
	EventPerformerCompleted         EventType = "performer.completed"
	EventAttemptFailed              EventType = "attempt.failed"
	EventAcceptanceStarted          EventType = "acceptance.started"
	EventCriterionStarted           EventType = "criterion.started"
	EventCriterionCompleted         EventType = "criterion.completed"
	EventAcceptanceFailed           EventType = "acceptance.failed"
	EventExecutionStarted           EventType = "execution.started"
	EventExecutionStopped           EventType = "execution.stopped"
	EventAmendmentApprovalPrepared  EventType = "amendment.approval_prepared"
	EventAmendmentApprovalAbandoned EventType = "amendment.approval_abandoned"
	EventAmendmentApproved          EventType = "amendment.approved"
	EventAuthorityGranted           EventType = "authority.granted"
	EventCancelRequested            EventType = "cancel.requested"
	EventJournalTailTruncated       EventType = "journal.tail_truncated"
)

// Event is one strict journal envelope. Payload is validated before projection.
type Event struct {
	EventID       string          `json:"event_id"`
	Seq           uint64          `json:"seq"`
	Timestamp     string          `json:"ts"`
	RunID         RunID           `json:"run_id"`
	ScoreRevision uint64          `json:"score_revision"`
	MovementID    MovementID      `json:"movement_id,omitempty"`
	PartID        string          `json:"part_id,omitempty"`
	AttemptID     AttemptID       `json:"attempt_id,omitempty"`
	Type          EventType       `json:"type"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}
