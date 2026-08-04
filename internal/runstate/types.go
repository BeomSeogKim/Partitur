// Package runstate implements the E-scoped partial journal projection.
//
// State is a pure function of the initial movement seed derived from the
// authenticated pinned score and the journal. This package supports only the
// forty-nine event types needed by DESIGN Appendix E, the first run success
// path, and the shipping status projection. Valid registry events
// outside that subset fail closed with ErrUnsupportedEventType.
//
// Amendment projection records human routing and rejection histories, while
// approval preparation and commit remain auto-mode only in this E-scoped
// projector.
package runstate

import "encoding/json"

type RunID string
type MovementID string
type AttemptID string
type CriterionID string
type ArtifactInstanceID string
type PrepareID string
type ProposalID string
type IntervalID string
type Hash string

// MovementSeed is derived from the authenticated pinned score.
type MovementSeed struct {
	ID              MovementID
	Initial         MovementState
	RepoWrite       bool
	HasDependencies bool
	Final           bool
}

// HeadMovement is the approved head's complete current-movement projection.
// It is carried by amendment.approved and its persisted approval plan.
type HeadMovement struct {
	ID              MovementID    `json:"id"`
	Initial         MovementState `json:"initial"`
	RepoWrite       bool          `json:"repo_write"`
	HasDependencies bool          `json:"has_dependencies"`
	Final           bool          `json:"final"`
}

type RunLifecycle string

const (
	RunNotStarted   RunLifecycle = ""
	RunRunning      RunLifecycle = "RUNNING"
	RunWaitingHuman RunLifecycle = "WAITING_HUMAN"
	RunSucceeded    RunLifecycle = "SUCCEEDED"
	RunFailed       RunLifecycle = "FAILED"
	RunCancelled    RunLifecycle = "CANCELLED"
)

func (state RunLifecycle) Terminal() bool {
	return state == RunSucceeded || state == RunFailed || state == RunCancelled
}

type MovementState string

const (
	MovementPending      MovementState = "PENDING"
	MovementReady        MovementState = "READY"
	MovementRunning      MovementState = "RUNNING"
	MovementWaitingHuman MovementState = "WAITING_HUMAN"
	MovementSucceeded    MovementState = "SUCCEEDED"
	MovementFailed       MovementState = "FAILED"
	MovementCancelled    MovementState = "CANCELLED"
	MovementInapplicable MovementState = "INAPPLICABLE"
)

type AttemptState string

const (
	AttemptStarting   AttemptState = "STARTING"
	AttemptRunning    AttemptState = "RUNNING"
	AttemptVerifying  AttemptState = "VERIFYING"
	AttemptCompleted  AttemptState = "COMPLETED"
	AttemptBlocked    AttemptState = "BLOCKED"
	AttemptFailed     AttemptState = "FAILED"
	AttemptCancelled  AttemptState = "CANCELLED"
	AttemptSuperseded AttemptState = "SUPERSEDED"
)

func (state AttemptState) terminal() bool {
	return state == AttemptCompleted || state == AttemptBlocked || state == AttemptFailed ||
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
	DecisionID             *string
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

type PendingDecision struct {
	ID               string
	Type             string
	Blocking         bool
	MovementID       MovementID
	AttemptID        AttemptID
	ScoreRevision    uint64
	ProposalID       ProposalID
	GateID           string
	SubjectTree      string
	BlockingFindings []FindingReference
}

// HumanGateScope is the exact scope carried by a human-gate resolution.
type HumanGateScope struct {
	SubjectTree string
}

// HumanGateResolution retains a resolved human gate after it leaves the
// pending-decision projection. It is the ordinary projection source for
// status and recovery; the journal remains authoritative.
type HumanGateResolution struct {
	DecisionID         string
	MovementID         MovementID
	AttemptID          AttemptID
	ScoreRevision      uint64
	GateID             string
	Scope              HumanGateScope
	Disposition        string
	OverriddenFindings []FindingReference
	OverrideReason     string
	Reason             string
}

type RoutedAmendment struct {
	ProposalID        ProposalID
	DecisionID        string
	DecisionType      string
	Blocking          bool
	BaseRevision      uint64
	BaseHash          Hash
	ClassifierVersion uint64
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
	TerminalReason   string `json:"terminal_reason,omitempty"`
}

type Attempt struct {
	MovementID    MovementID
	ScoreRevision uint64
	State         AttemptState
	Failure       *AttemptFailure
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

type AdapterObservation struct {
	AdapterVersion          string
	Capabilities            map[string]bool
	Enforcement             map[string]bool
	NegotiatedFeatures      []string
	TruncatedResolutions    []string
	AdvisoryDimensions      []string
	ExecutionDependencyHash Hash
	IdentityVersions        json.RawMessage
}

type ArtifactRecord struct {
	AttemptID       AttemptID
	LogicalOutputID string
	Kind            string
	ContentHash     Hash
	SizeBytes       uint64
	Source          string
}

// ChangeSetRecord is the pinned repository result captured for one verifying
// repo-write attempt.
type ChangeSetRecord struct {
	AttemptID        AttemptID
	ChangeSetID      string
	BaseTree         string
	ResultTree       string
	Commit           string
	Ref              string
	IdentityVersions json.RawMessage
}

type CandidateContributor struct {
	MovementID  MovementID
	ChangeSetID string
}

type ApplicationCandidate struct {
	ID                         string
	Revision                   uint64
	BaseTree                   string
	ResultTree                 string
	OrderedChangeSets          []string
	Contributors               []CandidateContributor
	CompositionDependencyHash  Hash
	CompositionEnvironmentHash Hash
	IdentityVersions           json.RawMessage
}

type ApplicationState string

const (
	ApplicationNotApplied       ApplicationState = "NOT_APPLIED"
	ApplicationApplying         ApplicationState = "APPLYING"
	ApplicationApplied          ApplicationState = "APPLIED"
	ApplicationFailedClean      ApplicationState = "FAILED_CLEAN"
	ApplicationRecoveryRequired ApplicationState = "RECOVERY_REQUIRED"
)

type ApplicationProjection struct {
	State         ApplicationState
	TransactionID string
	CandidateID   string
	Reason        string
}

type PromotionState string

const (
	PromotionNotPromoted      PromotionState = "NOT_PROMOTED"
	PromotionPromoting        PromotionState = "PROMOTING"
	PromotionPromoted         PromotionState = "PROMOTED"
	PromotionRecoveryRequired PromotionState = "RECOVERY_REQUIRED"
)

type PromotionProjection struct {
	State         PromotionState
	TransactionID string
	CandidateID   string
	Reason        string
}

type MovementResult struct {
	AttemptID                   AttemptID
	ApprovedArtifactInstanceIDs []ArtifactInstanceID
	ApprovedChangeSetID         string
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

// FindingReference identifies one validated blocking finding in an immutable
// review artifact instance.
type FindingReference struct {
	ArtifactInstanceID string
	FindingID          string
}

type Acceptance struct {
	Started             bool
	EvaluationCompleted bool
	SubjectTree         string
	SpecHash            Hash
	PlannedCriterionIDs []CriterionID
	Criteria            map[CriterionID]CriterionRecord
	ReviewOutcome       string
	BlockingFindings    []FindingReference
}

type State struct {
	Run                  RunLifecycle
	Movements            map[MovementID]MovementState
	MovementOrder        []MovementID
	RepoWriteMovements   map[MovementID]bool
	DependencyMovements  map[MovementID]bool
	FinalMovements       map[MovementID]bool
	Attempts             map[AttemptID]Attempt
	ScoreHead            ScoreHead
	Authority            Authority
	PendingPrepare       *PendingPrepare
	OpenExecution        *ExecutionInterval
	ConsumedBudgetMS     int64
	AdapterLaunches      map[AttemptID]AdapterLaunch
	AdapterObservations  map[AttemptID]AdapterObservation
	Artifacts            map[ArtifactInstanceID]ArtifactRecord
	ChangeSets           map[AttemptID]ChangeSetRecord
	VerifiedAttempts     map[AttemptID]bool
	MovementResults      map[MovementID]MovementResult
	ApplicationCandidate *ApplicationCandidate
	Application          ApplicationProjection
	Promotion            PromotionProjection
	CriterionLaunches    map[CriterionLaunchKey]CriterionLaunch
	Acceptances          map[AttemptID]Acceptance
	PendingDecisions     map[string]PendingDecision
	ResolvedHumanGates   map[AttemptID]HumanGateResolution
	RoutedAmendments     map[ProposalID]RoutedAmendment
	CancelRequested      bool
	appliedEvents        map[string]appliedEvent
}

// appliedEvent retains the envelope authority needed to validate a later
// derived event during journal projection.
type appliedEvent struct {
	Type            EventType
	Sequence        uint64
	TerminalizesRun bool
}

type EventType string

const (
	EventRunStarted                     EventType = "run.started"
	EventRunSucceeded                   EventType = "run.succeeded"
	EventRunFailed                      EventType = "run.failed"
	EventRunCancelled                   EventType = "run.cancelled"
	EventMovementReady                  EventType = "movement.ready"
	EventMovementStarted                EventType = "movement.started"
	EventMovementSucceeded              EventType = "movement.succeeded"
	EventMovementFailed                 EventType = "movement.failed"
	EventMovementCancelled              EventType = "movement.cancelled"
	EventPerformerSelected              EventType = "performer.selected"
	EventAttemptStarted                 EventType = "attempt.started"
	EventAdapterProbed                  EventType = "adapter.probed"
	EventPerformerCompleted             EventType = "performer.completed"
	EventAttemptCompleted               EventType = "attempt.completed"
	EventAttemptBlocked                 EventType = "attempt.blocked"
	EventAttemptFailed                  EventType = "attempt.failed"
	EventAttemptCancelled               EventType = "attempt.cancelled"
	EventAttemptSuperseded              EventType = "attempt.superseded"
	EventArtifactRecorded               EventType = "artifact.recorded"
	EventChangeSetRecorded              EventType = "change_set.recorded"
	EventVerificationPassed             EventType = "verification.passed"
	EventApplicationCandidateRecorded   EventType = "application_candidate.recorded"
	EventAcceptanceStarted              EventType = "acceptance.started"
	EventCriterionStarted               EventType = "criterion.started"
	EventCriterionCompleted             EventType = "criterion.completed"
	EventAcceptanceFailed               EventType = "acceptance.failed"
	EventAcceptanceEvaluationCompleted  EventType = "acceptance.evaluation_completed"
	EventDecisionRequested              EventType = "decision.requested"
	EventDecisionResolved               EventType = "decision.resolved"
	EventDecisionObsoleted              EventType = "decision.obsoleted"
	EventAmendmentRejected              EventType = "amendment.rejected"
	EventExecutionStarted               EventType = "execution.started"
	EventExecutionStopped               EventType = "execution.stopped"
	EventAmendmentApprovalPrepared      EventType = "amendment.approval_prepared"
	EventAmendmentApprovalAbandoned     EventType = "amendment.approval_abandoned"
	EventAmendmentRoutedHuman           EventType = "amendment.routed_human"
	EventAmendmentApproved              EventType = "amendment.approved"
	EventAmendmentHumanRejected         EventType = "amendment.human_rejected"
	EventCompositionConflicted          EventType = "composition.conflicted"
	EventCompositionFailed              EventType = "composition.failed"
	EventAuthorityGranted               EventType = "authority.granted"
	EventCancelRequested                EventType = "cancel.requested"
	EventJournalTailTruncated           EventType = "journal.tail_truncated"
	EventApplyStarted                   EventType = "apply.started"
	EventApplyCompleted                 EventType = "apply.completed"
	EventApplyFailed                    EventType = "apply.failed"
	EventApplyRecoveryRequired          EventType = "apply.recovery_required"
	EventApplyRecoveryResolved          EventType = "apply.recovery_resolved"
	EventScorePromotionStarted          EventType = "score.promotion_started"
	EventScorePromoted                  EventType = "score.promoted"
	EventScorePromotionRecoveryRequired EventType = "score.promotion_recovery_required"
	EventLog                            EventType = "log"
	EventProgress                       EventType = "progress"
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
