// Package recovery selects the next run-level recovery action.
//
// It deliberately does not read the filesystem, inspect processes, append
// journal events, or execute an action. Callers replay the journal and gather
// external observations before calling Plan; the recovery executor owns the
// selected action and any subsequent re-plan.
package recovery

import "github.com/BeomSeogKim/Partitur/internal/runstate"

// CaseID is a stable Appendix C recovery-case identifier.
type CaseID string

const (
	CaseOpenExecution          CaseID = "RC-RESUME-001"
	CaseTerminal               CaseID = "RC-RESUME-002"
	CaseStaleLease             CaseID = "RC-RESUME-003"
	CaseOrphanLease            CaseID = "RC-RESUME-004"
	CaseOwnerUnverifiable      CaseID = "RC-RESUME-005"
	CaseLiveOwner              CaseID = "RC-RESUME-046"
	CaseCancellation           CaseID = "RC-RESUME-006"
	CasePendingPrepare         CaseID = "RC-RESUME-007"
	CaseReclaimAuthority       CaseID = "RC-RESUME-008"
	CaseRootSnapshotDivergence CaseID = "RC-RESUME-009"
	CaseMissingReference       CaseID = "RC-RESUME-010"
	CaseBlockedProposalRoute   CaseID = "RC-RESUME-049"
	CaseRoutedAmendment        CaseID = "RC-RESUME-037"
	CaseRevisionRestart        CaseID = "RC-RESUME-042"
	CaseCompositionTerminal    CaseID = "RC-RESUME-011"
	CaseContinue               CaseID = "RC-RESUME-012"
	CaseRealizeDisposition     CaseID = "RC-RESUME-039"
	CaseAppendQuestionRequest  CaseID = "RC-RESUME-040"
	CaseDecisionResume         CaseID = "RC-RESUME-041"
	CaseWaitingHuman           CaseID = "RC-RESUME-048"
	CaseUnstartedAttempt       CaseID = "RC-RESUME-013"
	CaseUnprobedAttempt        CaseID = "RC-RESUME-014"
	CaseIncompleteAttempt      CaseID = "RC-RESUME-015"
	CaseCaptureChangeSet       CaseID = "RC-RESUME-016"
	CasePostHocVerification    CaseID = "RC-RESUME-017"
	CaseStartAcceptance        CaseID = "RC-RESUME-018"
	CaseMovementSucceeded      CaseID = "RC-RESUME-019"
	CaseRunFailed              CaseID = "RC-RESUME-020"
	CaseFinalGateRejected      CaseID = "RC-RESUME-021"
	CaseAcceptanceFailed       CaseID = "RC-RESUME-022"
	CaseCriterionFailed        CaseID = "RC-RESUME-023"
	CaseIncompleteCriterion    CaseID = "RC-RESUME-024"
	CaseCriteriaPassed         CaseID = "RC-RESUME-025"
	CaseRequestHumanGate       CaseID = "RC-RESUME-026"
	CaseHumanGateWaiting       CaseID = "RC-RESUME-027"
	CaseHumanGateApproved      CaseID = "RC-RESUME-028"
	CaseHumanGateRejected      CaseID = "RC-RESUME-029"
	CaseGateFreeCompletion     CaseID = "RC-RESUME-030"
	CaseUnjournaledLaunch      CaseID = "RC-RESUME-031"
	CaseFirstCriterion         CaseID = "RC-RESUME-032"
	CaseNextCriterion          CaseID = "RC-RESUME-033"
	CaseScheduler              CaseID = "RC-RESUME-043"
	CaseRecoveredComposition   CaseID = "RC-RESUME-044"
	CaseBudgetExhausted        CaseID = "RC-RESUME-045"
)

// HaltReason is an Appendix D recovery halt reason.
type HaltReason string

const (
	HaltJournalIdempotencyConflict HaltReason = "journal_idempotency_conflict"
	HaltUnsupportedRunFormat       HaltReason = "unsupported_run_format"
	HaltOwnerUnverifiable          HaltReason = "owner_unverifiable"
	HaltRootSnapshotDivergence     HaltReason = "root_snapshot_divergence"
	HaltMissingArtifactFile        HaltReason = "missing_artifact_file"
	HaltMissingSnapshotFile        HaltReason = "missing_snapshot_file"
	HaltMissingChangeSetRef        HaltReason = "missing_changeset_ref"
	HaltMissingProposalRecord      HaltReason = "missing_proposal_record"
	HaltMissingResolvedCast        HaltReason = "missing_resolved_cast"
	HaltMissingPreparePlan         HaltReason = "missing_prepare_plan"
	HaltGitUnverifiable            HaltReason = "git_unverifiable"
	HaltSweepUnverifiable          HaltReason = "sweep_unverifiable"
	HaltSpawnHandoffUnverifiable   HaltReason = "spawn_handoff_unverifiable"
	HaltPrepareLeaseEpochMismatch  HaltReason = "prepare_lease_epoch_mismatch"
	HaltJournalCorrupt             HaltReason = "journal_corrupt"
)

var appendixDHaltReasonSet = map[HaltReason]bool{
	HaltJournalIdempotencyConflict: true,
	HaltUnsupportedRunFormat:       true,
	HaltOwnerUnverifiable:          true,
	HaltRootSnapshotDivergence:     true,
	HaltMissingArtifactFile:        true,
	HaltMissingSnapshotFile:        true,
	HaltMissingChangeSetRef:        true,
	HaltMissingProposalRecord:      true,
	HaltMissingResolvedCast:        true,
	HaltMissingPreparePlan:         true,
	HaltGitUnverifiable:            true,
	HaltSweepUnverifiable:          true,
	HaltSpawnHandoffUnverifiable:   true,
	HaltPrepareLeaseEpochMismatch:  true,
	HaltJournalCorrupt:             true,
}

// IsHaltReason reports whether reason is one of Appendix D's closed recovery
// halt reasons.
func IsHaltReason(reason HaltReason) bool {
	return appendixDHaltReasonSet[reason]
}

// AppendixDHaltReasons returns the closed set used at the command boundary.
func AppendixDHaltReasons() []HaltReason {
	reasons := make([]HaltReason, 0, len(appendixDHaltReasonSet))
	for reason := range appendixDHaltReasonSet {
		reasons = append(reasons, reason)
	}
	return reasons
}

// OwnerState is the caller's observation of the owner named by a readable
// current lease. It is deliberately an observation, not a process probe.
type OwnerState string

const (
	OwnerDead          OwnerState = "dead"
	OwnerLive          OwnerState = "live"
	OwnerCurrentDriver OwnerState = "current_driver"
	OwnerUnverifiable  OwnerState = "unverifiable"
)

// LeaseObservation is supplied by the caller after inspecting driver.lease.
// An unreadable lease is unsafe to resume beside, so it is represented as an
// unverifiable owner rather than being treated as absent.
type LeaseObservation struct {
	Exists   bool
	Readable bool
	Epoch    uint64
	Owner    OwnerState
	Identity *LeaseIdentity
}

// LeaseIdentity identifies the lease observed before recovery planning.
type LeaseIdentity struct {
	Epoch uint64
	Token string
	PID   int
	Start runstate.StartIdentity
}

// ReferenceKind identifies one event-named run-level object checked by
// RC-RESUME-010. Present means both present and hash-matched.
type ReferenceKind string

const (
	ReferenceArtifact           ReferenceKind = "artifact"
	ReferenceReviewSubjectInput ReferenceKind = "review_subject_input"
	ReferenceSnapshot           ReferenceKind = "snapshot"
	ReferenceChangeSetRef       ReferenceKind = "change_set_ref"
	ReferenceProposalRecord     ReferenceKind = "proposal_record"
	ReferenceResolvedCast       ReferenceKind = "resolved_cast"
)

// ReferenceObservation is one caller-supplied file observation.
type ReferenceObservation struct {
	Kind    ReferenceKind
	Present bool
}

// PrepareObservation contains the two checks RC-RESUME-007 owns ahead of
// RC-RESUME-010. Present means both present and hash/binding verified.
type PrepareObservation struct {
	PlanPresent     bool
	SnapshotPresent bool
}

// RevisionRestart is a replay-derived fact. It exists only when an approval
// established State.ScoreHead and the named affected movement has no attempt
// on that head. The planner does not rediscover that fact from live state.
type RevisionRestart struct {
	MovementID      runstate.MovementID
	AttemptID       runstate.AttemptID
	ApprovalEventID string
	Performer       string
}

// BlockedProposalRoute is the replay-derived authority for RC-RESUME-049.
// It exists only for a blocking proposal whose attempt.blocked route descriptor
// is durable but whose matching amendment.routed_human is not.
type BlockedProposalRoute struct {
	ProposalID    runstate.ProposalID
	AttemptID     runstate.AttemptID
	ScoreRevision uint64
}

// CompositionTerminal is replay-derived evidence waiting for the terminal
// event Appendix B.3 already determines. The evidence itself remains outside
// runstate's ordinary lifecycle projection.
type CompositionTerminal struct {
	Scope           string
	TargetID        string
	Reason          string
	EvidenceEventID string
	ScoreRevision   uint64
}

// CompositionRecovery is the replay-derived close of a composition interval.
// A recovered close is deliberately not a conflict or failure verdict. Those
// verdicts are represented separately by CompositionTerminal once observed.
type CompositionRecovery struct {
	Scope         string
	MovementID    runstate.MovementID
	Recovered     bool
	ScoreRevision uint64
}

// ScheduledMovement is the compiled, declaration-ordered lifecycle input C.4
// needs. It intentionally contains no adapter, process, or filesystem handle.
type ScheduledMovement struct {
	ID        runstate.MovementID
	Needs     []runstate.MovementID
	RepoWrite bool
	Final     bool
}

// PendingSuccessor is a successor already selected by §3.1, RC-RESUME-041,
// or RC-RESUME-042. C.4 only makes it durable; it never chooses another one.
type PendingSuccessor struct {
	MovementID  runstate.MovementID
	AttemptID   runstate.AttemptID
	Performer   string
	Reason      string
	CausationID string
}

// Scheduler is the compiled score view C.4 needs after journal replay. A
// non-waived lifecycle has exactly one Final movement; a waived lifecycle has
// none and completes by carrying the candidate in run.succeeded.
type Scheduler struct {
	Movements        []ScheduledMovement
	PendingSuccessor *PendingSuccessor
	GateWaived       bool
	RemainingTime    int64
}

// QuestionRequest is the replay-derived request cut for one question raised
// by a blocked attempt. Entries retain attempt.blocked's raised order; a
// proposal is deliberately absent because RC-RESUME-037 owns its request.
type QuestionRequest struct {
	DecisionID string
	Durable    bool
}

// AttemptRecovery is the replay-derived C.2 view of one attempt. ScoreRevision
// is compared to State.ScoreHead by the planner, so an event-tail predicate on
// an older revision cannot make its attempt current.
type AttemptRecovery struct {
	AttemptID     runstate.AttemptID
	MovementID    runstate.MovementID
	ScoreRevision uint64
	State         runstate.AttemptState

	// FailureDispositionRealized says Arm 2 has already consumed the durable
	// disposition on a current-head attempt.failed event.
	FailureDispositionRealized bool
	RecordedDisposition        *runstate.Disposition
	QuestionRequests           []QuestionRequest
	ChangeSetRecorded          bool
	AcceptanceStarted          bool
	MovementSucceeded          bool
	MovementFailed             bool
	FinalGateRejected          bool

	// FailureClassification is the run-owned Arm 1 input for a new failure on
	// this attempt. It is assembled during journal replay from the pinned score
	// and cast; the planner and executor never rediscover it from live state.
	FailureClassification FailureClassification
}

// FailureClassification is the complete, durable-input view required by
// successor.Classify for one current-head attempt. Fallbacks and retry policy
// come from the run-owned score/cast snapshots; performer history comes from
// performer.selected journal events.
type FailureClassification struct {
	CurrentPerformer   string
	VisitedPerformers  []string
	Fallbacks          []string
	RetriesConsumed    int
	RetriesPerMovement int
	RemainingTimeMS    int64
}

// HandoffState and SweepState are caller-supplied observations. The planner
// names the later executor operation but never inspects a process itself.
type HandoffState string

const (
	HandoffSafe         HandoffState = "safe"
	HandoffUnverifiable HandoffState = "handoff_unverifiable"
	HandoffSweepFailed  HandoffState = "sweep_unverifiable"
)

type SweepState string

const (
	SweepSafe         SweepState = "safe"
	SweepUnverifiable SweepState = "sweep_unverifiable"
)

type WorktreeState string

const (
	WorktreePresent WorktreeState = "present"
	WorktreeMissing WorktreeState = "missing"
)

// SubjectVerification is the caller's full-invariant observation for an
// acceptance worktree. It covers the recorded subject tree, non-ignored
// untracked files, symlink targets, modes, and protected-path integrity.
type SubjectVerification string

const (
	SubjectUnverified SubjectVerification = ""
	SubjectMatched    SubjectVerification = "matched"
	SubjectMismatched SubjectVerification = "mismatched"
)

// UnjournaledLaunchState is the caller's observation of a launch directory
// which has no criterion.started event. Safe states have already established
// that no released criterion mutator remains.
type UnjournaledLaunchState string

const (
	UnjournaledLaunchAbsent              UnjournaledLaunchState = ""
	UnjournaledLaunchUnstabilized        UnjournaledLaunchState = "unstabilized"
	UnjournaledLaunchMarkerFree          UnjournaledLaunchState = "marker_free"
	UnjournaledLaunchSessionEmpty        UnjournaledLaunchState = "session_empty"
	UnjournaledLaunchHandoffUnverifiable UnjournaledLaunchState = "handoff_unverifiable"
	UnjournaledLaunchSweepUnverifiable   UnjournaledLaunchState = "sweep_unverifiable"
)

// GateRecovery is the recovery view of ordinary human-gate projection data.
type GateRecovery struct {
	Required         bool
	Requested        bool
	Resolved         bool
	Approved         bool
	DecisionID       string
	GateID           string
	ReviewOutcome    string
	BlockingFindings []runstate.FindingReference
}

// AcceptanceRecovery holds the C.3 facts which ordinary lifecycle projection
// intentionally does not retain, including a durable acceptance.failed origin
// and a resolved human-gate result.
type AcceptanceRecovery struct {
	Failed bool
	Gate   GateRecovery
}

// Projection is the recovery-specific view built while replaying the journal.
// State is the ordinary runstate projection. The additional facts preserve
// evidence-only and derived recovery state without making the planner inspect
// the journal itself.
type Projection struct {
	State                 runstate.State
	BlockedProposalRoutes []BlockedProposalRoute
	RevisionRestarts      []RevisionRestart
	CompositionTerminals  []CompositionTerminal
	CompositionRecovery   *CompositionRecovery
	CurrentHeadAttempt    *AttemptRecovery
	Acceptance            *AcceptanceRecovery
	Scheduler             Scheduler
}

// Observations are all non-journal inputs used by C.1 and C.2.
type Observations struct {
	Lease                  LeaseObservation
	RootSnapshotDivergence bool
	References             []ReferenceObservation
	Prepare                PrepareObservation
	Handoff                HandoffState
	AdapterSweep           SweepState
	Worktree               WorktreeState
	CriterionSweep         SweepState
	AcceptanceSubject      SubjectVerification
	UnjournaledLaunch      UnjournaledLaunchState
}

// Input is the complete, already-replayed input to the pure recovery planner.
type Input struct {
	Projection   Projection
	Observations Observations
}

// ActionKind names an action for the later recovery executor. No ActionKind
// performs the named work in this package.
type ActionKind string

const (
	ActionCloseOpenExecutionInterval ActionKind = "close_open_execution_interval"
	ActionTerminalCleanup            ActionKind = "terminal_cleanup"
	ActionRemoveStaleLease           ActionKind = "remove_stale_lease"
	ActionQuarantineOrphanLease      ActionKind = "quarantine_orphan_lease"
	ActionRefuseResume               ActionKind = "refuse_resume"
	ActionExecuteCancellation        ActionKind = "execute_cancellation_oracle"
	ActionCompleteOrAbandonPrepare   ActionKind = "complete_or_abandon_prepare"
	ActionReclaimAuthority           ActionKind = "reclaim_authority"
	ActionAppendBlockedProposalRoute ActionKind = "append_blocked_proposal_route"
	ActionAppendRoutedRequest        ActionKind = "append_routed_request"
	ActionSelectRevisionRestart      ActionKind = "select_revision_restart"
	ActionAppendCompositionTerminal  ActionKind = "append_composition_terminal"
	ActionProceedAttempt             ActionKind = "proceed_c2"
	ActionProceedScheduler           ActionKind = "proceed_c4"
	ActionRealizeRecordedDisposition ActionKind = "realize_recorded_disposition"
	ActionAppendQuestionRequest      ActionKind = "append_question_request"
	ActionSelectDecisionResume       ActionKind = "select_decision_resume"
	ActionReturnWaitingHuman         ActionKind = "return_waiting_human"
	ActionRecoverUnstartedAttempt    ActionKind = "recover_unstarted_attempt"
	ActionRecoverUnprobedAttempt     ActionKind = "recover_unprobed_attempt"
	ActionRecoverIncompleteAttempt   ActionKind = "recover_incomplete_attempt"
	ActionCaptureChangeSet           ActionKind = "capture_change_set"
	ActionFailWorktreeLost           ActionKind = "fail_worktree_lost"
	ActionRerunPostHocVerification   ActionKind = "rerun_post_hoc_verification"
	ActionAppendAcceptanceStarted    ActionKind = "append_acceptance_started"
	ActionAppendMovementSucceeded    ActionKind = "append_movement_succeeded"
	ActionAppendRunFailed            ActionKind = "append_run_failed"
	ActionAppendFinalGateFailure     ActionKind = "append_final_gate_failure"
	ActionProceedAcceptance          ActionKind = "proceed_c3"
	ActionAppendAcceptanceFailure    ActionKind = "append_acceptance_failure"
	ActionRecoverIncompleteCriterion ActionKind = "recover_incomplete_criterion"
	ActionVerifyAcceptanceSubject    ActionKind = "verify_acceptance_subject"
	ActionAppendEvaluationCompleted  ActionKind = "append_acceptance_evaluation_completed"
	ActionAppendHumanGateRequest     ActionKind = "append_human_gate_request"
	ActionAppendAcceptanceSuccess    ActionKind = "append_acceptance_success"
	ActionAppendGateRejectedFailure  ActionKind = "append_gate_rejected_failure"
	ActionStabilizeUnjournaledLaunch ActionKind = "stabilize_unjournaled_criterion_launch"
	ActionRemoveUnjournaledLaunch    ActionKind = "remove_unjournaled_criterion_launch"
	ActionResumeCriterion            ActionKind = "resume_criterion"
	ActionAppendMovementReady        ActionKind = "append_movement_ready"
	ActionAppendMovementStarted      ActionKind = "append_movement_started"
	ActionSelectInitialPerformer     ActionKind = "select_initial_performer"
	ActionMaterializeSuccessor       ActionKind = "materialize_selected_successor"
	ActionRerunComposition           ActionKind = "rerun_deterministic_composition"
	ActionComposeCandidate           ActionKind = "compose_application_candidate"
	ActionAppendBudgetFailure        ActionKind = "append_budget_exhaustion"
)

// Continuation names the next Appendix C table after one action value has
// been materialized. It is not an execution request.
type Continuation string

const (
	ContinuationC2 Continuation = "c2"
	ContinuationC3 Continuation = "c3"
	ContinuationC4 Continuation = "c4"
)

// ActionStep makes order-sensitive executor work explicit. In particular, an
// open adapter interval is never closed before its recorded session is swept.
type ActionStep string

const (
	StepStabilizeHandoff            ActionStep = "stabilize_handoff_and_sweep_published_session"
	StepSweepRecordedSession        ActionStep = "sweep_recorded_session"
	StepCloseAdapterInterval        ActionStep = "close_adapter_interval_recovered"
	StepClassifyAndAppendFailure    ActionStep = "classify_and_append_attempt_failure"
	StepSweepCriterionSession       ActionStep = "sweep_criterion_session"
	StepVerifyAcceptanceSubject     ActionStep = "verify_acceptance_subject"
	StepClassifyAcceptanceFailure   ActionStep = "classify_and_append_acceptance_failure"
	StepAppendAttemptCompleted      ActionStep = "append_attempt_completed"
	StepAppendMovementSucceeded     ActionStep = "append_movement_succeeded"
	StepAppendMovementBudgetFailure ActionStep = "append_movement_failed_budget_exhausted"
	StepAppendRunFailed             ActionStep = "append_run_failed"
)

// Action is a pure description for the recovery executor. Replan means the
// executor must apply this one action and invoke Plan again from the top.
type Action struct {
	Kind                 ActionKind
	Replan               bool
	BlockedProposalRoute *BlockedProposalRoute
	RoutedProposalID     runstate.ProposalID
	RevisionRestart      *RevisionRestart
	CompositionTerminal  *CompositionTerminal
	PendingSuccessor     *PendingSuccessor
	AttemptID            runstate.AttemptID
	QuestionDecisionID   string
	CriterionID          runstate.CriterionID
	MovementID           runstate.MovementID
	SubjectTree          string
	ReviewOutcome        string
	BlockingFindings     []runstate.FindingReference
	FailureKind          string
	FailureReason        string
	RecordedDisposition  *runstate.Disposition
	CandidateCarrying    bool
	Steps                []ActionStep
	Continuation         Continuation
}

// Decision always carries exactly one Appendix C case. It contains either an
// action or one named halt reason, never both.
type Decision struct {
	CaseID CaseID
	Action *Action
	Halt   HaltReason
}

// Valid reports whether the decision has exactly one action or halt outcome.
func (decision Decision) Valid() bool {
	if decision.CaseID == "" {
		return false
	}
	if decision.Action != nil {
		return decision.Action.Kind != "" && decision.Halt == ""
	}
	return decision.Halt != ""
}

// Plan applies Appendix C.1 in its normative top-down order. It is total over
// Input: an absent observation never causes a filesystem or process lookup.
func Plan(input Input) Decision {
	state := input.Projection.State
	lease := input.Observations.Lease

	if state.Run.Terminal() {
		return action(CaseTerminal, ActionTerminalCleanup, false)
	}
	if lease.Exists && lease.Readable && lease.Epoch < state.Authority.Epoch {
		return action(CaseStaleLease, ActionRemoveStaleLease, true)
	}
	if lease.Exists && (!lease.Readable || lease.Epoch > state.Authority.Epoch || state.Authority.Owner == nil) {
		if !lease.Readable {
			return halt(CaseOwnerUnverifiable, HaltOwnerUnverifiable)
		}
		return action(CaseOrphanLease, ActionQuarantineOrphanLease, true)
	}
	if lease.Exists && lease.Epoch == state.Authority.Epoch {
		switch lease.Owner {
		case OwnerLive:
			return action(CaseLiveOwner, ActionRefuseResume, false)
		case OwnerCurrentDriver:
			// This executor established the current recovery lease.
		case OwnerDead:
			// RC-RESUME-008 owns the no-live-owner case below.
		default:
			return halt(CaseOwnerUnverifiable, HaltOwnerUnverifiable)
		}
	}
	if state.CancelRequested {
		return action(CaseCancellation, ActionExecuteCancellation, false)
	}
	if state.PendingPrepare != nil {
		if !input.Observations.Prepare.PlanPresent {
			return halt(CasePendingPrepare, HaltMissingPreparePlan)
		}
		if !input.Observations.Prepare.SnapshotPresent {
			return halt(CasePendingPrepare, HaltMissingSnapshotFile)
		}
		return action(CasePendingPrepare, ActionCompleteOrAbandonPrepare, false)
	}
	if (!lease.Exists || lease.Owner == OwnerCurrentDriver) && hasAnyUnresolvedBlockingDecision(state) {
		return action(CaseHumanGateWaiting, ActionReturnWaitingHuman, false)
	}
	if state.Authority.Epoch > 0 && (!lease.Exists || lease.Owner == OwnerDead) {
		return action(CaseReclaimAuthority, ActionReclaimAuthority, false)
	}
	// RC-RESUME-001 closes every non-adapter interval before its durable
	// consequence is interpreted. C.2 owns open adapter intervals because it
	// must first sweep their recorded session.
	if state.OpenExecution != nil && state.OpenExecution.Phase != "adapter" {
		return action(CaseOpenExecution, ActionCloseOpenExecutionInterval, true)
	}
	if input.Observations.RootSnapshotDivergence {
		return halt(CaseRootSnapshotDivergence, HaltRootSnapshotDivergence)
	}
	if reason, ok := firstMissingReferenceReason(input.Observations.References); ok {
		return halt(CaseMissingReference, reason)
	}
	if route, ok := firstBlockedProposalRoute(input.Projection.BlockedProposalRoutes); ok {
		decision := action(CaseBlockedProposalRoute, ActionAppendBlockedProposalRoute, true)
		decision.Action.BlockedProposalRoute = &route
		return decision
	}
	if proposalID, ok := firstMissingRoutedRequest(state); ok {
		decision := action(CaseRoutedAmendment, ActionAppendRoutedRequest, true)
		decision.Action.RoutedProposalID = proposalID
		return decision
	}
	if restart, ok := firstRevisionRestart(input.Projection.RevisionRestarts); ok {
		decision := action(CaseRevisionRestart, ActionSelectRevisionRestart, false)
		decision.Action.RevisionRestart = &restart
		decision.Action.PendingSuccessor = &PendingSuccessor{
			MovementID:  restart.MovementID,
			AttemptID:   restart.AttemptID,
			Performer:   restart.Performer,
			Reason:      "revision_restart",
			CausationID: restart.ApprovalEventID,
		}
		decision.Action.Continuation = ContinuationC4
		return decision
	}
	if terminal, ok := firstCompositionTerminal(input.Projection.CompositionTerminals); ok {
		decision := action(CaseCompositionTerminal, ActionAppendCompositionTerminal, true)
		decision.Action.CompositionTerminal = &terminal
		return decision
	}
	if currentHeadAttempt(input.Projection) != nil {
		decision := action(CaseContinue, ActionProceedAttempt, false)
		decision.Action.Continuation = ContinuationC2
		return decision
	}
	decision := action(CaseContinue, ActionProceedScheduler, false)
	decision.Action.Continuation = ContinuationC4
	return decision
}

func hasAnyUnresolvedBlockingDecision(state runstate.State) bool {
	for _, decision := range state.PendingDecisions {
		if decision.Blocking {
			return true
		}
	}
	return false
}

// PlanAttempt applies Appendix C.2 after C.1 selects ActionProceedAttempt.
// It is intentionally separate from Plan: C.1 remains the run-level
// dispatcher, while C.2 owns only the current-head, non-superseded attempt.
func PlanAttempt(input Input) Decision {
	attempt := currentHeadAttempt(input.Projection)
	if attempt == nil {
		decision := action(CaseContinue, ActionProceedScheduler, false)
		decision.Action.Continuation = ContinuationC4
		return decision
	}
	if attempt.State == runstate.AttemptFailed && !attempt.FailureDispositionRealized {
		return realizeRecordedDisposition(CaseRealizeDisposition, attempt, input.Projection.Scheduler)
	}
	if attempt.State == runstate.AttemptBlocked {
		if request, ok := firstMissingQuestionRequest(attempt.QuestionRequests); ok {
			decision := action(CaseAppendQuestionRequest, ActionAppendQuestionRequest, true)
			decision.Action.AttemptID = attempt.AttemptID
			decision.Action.QuestionDecisionID = request.DecisionID
			return decision
		}
		if hasUnresolvedBlockingDecision(input.Projection.State, attempt.AttemptID) {
			return action(CaseWaitingHuman, ActionReturnWaitingHuman, false)
		}
		decision := action(CaseDecisionResume, ActionSelectDecisionResume, false)
		decision.Action.AttemptID = attempt.AttemptID
		decision.Action.PendingSuccessor = &PendingSuccessor{
			MovementID: attempt.MovementID,
			AttemptID:  attempt.AttemptID,
			Performer:  attempt.FailureClassification.CurrentPerformer,
			Reason:     "decision_resume",
		}
		decision.Action.Continuation = ContinuationC4
		return decision
	}

	if attempt.State == runstate.AttemptStarting {
		switch input.Observations.Handoff {
		case HandoffUnverifiable:
			return halt(CaseUnstartedAttempt, HaltSpawnHandoffUnverifiable)
		case HandoffSweepFailed:
			return halt(CaseUnstartedAttempt, HaltSweepUnverifiable)
		default:
			return recoveryFailureAction(
				CaseUnstartedAttempt,
				ActionRecoverUnstartedAttempt,
				attempt.AttemptID,
				"task_failed",
				"attempt_never_started",
				[]ActionStep{StepStabilizeHandoff, StepCloseAdapterInterval, StepClassifyAndAppendFailure},
			)
		}
	}
	if attempt.State == runstate.AttemptRunning && !hasAdapterProbe(input.Projection.State, attempt.AttemptID) {
		if input.Observations.AdapterSweep == SweepUnverifiable {
			return halt(CaseUnprobedAttempt, HaltSweepUnverifiable)
		}
		return recoveryFailureAction(
			CaseUnprobedAttempt,
			ActionRecoverUnprobedAttempt,
			attempt.AttemptID,
			"adapter_unavailable",
			"probe_terminated_incomplete",
			[]ActionStep{StepSweepRecordedSession, StepCloseAdapterInterval, StepClassifyAndAppendFailure},
		)
	}
	if attempt.State == runstate.AttemptRunning && hasAdapterProbe(input.Projection.State, attempt.AttemptID) {
		if input.Observations.AdapterSweep == SweepUnverifiable {
			return halt(CaseIncompleteAttempt, HaltSweepUnverifiable)
		}
		return recoveryFailureAction(
			CaseIncompleteAttempt,
			ActionRecoverIncompleteAttempt,
			attempt.AttemptID,
			"task_failed",
			"attempt_terminated_incomplete",
			[]ActionStep{StepSweepRecordedSession, StepCloseAdapterInterval, StepClassifyAndAppendFailure},
		)
	}
	if attempt.State == runstate.AttemptVerifying && movementHasRepoWrite(input.Projection.State, attempt.MovementID) && !attempt.ChangeSetRecorded {
		if input.Observations.Worktree == WorktreeMissing {
			return recoveryFailureAction(CaseCaptureChangeSet, ActionFailWorktreeLost, attempt.AttemptID, "task_failed", "worktree_lost", []ActionStep{StepClassifyAndAppendFailure})
		}
		decision := action(CaseCaptureChangeSet, ActionCaptureChangeSet, true)
		decision.Action.AttemptID = attempt.AttemptID
		return decision
	}
	if attempt.State == runstate.AttemptVerifying && !hasVerificationPassed(input.Projection.State, attempt.AttemptID) {
		if input.Observations.Worktree == WorktreeMissing {
			return recoveryFailureAction(CasePostHocVerification, ActionFailWorktreeLost, attempt.AttemptID, "task_failed", "worktree_lost", []ActionStep{StepClassifyAndAppendFailure})
		}
		decision := action(CasePostHocVerification, ActionRerunPostHocVerification, true)
		decision.Action.AttemptID = attempt.AttemptID
		return decision
	}
	if attempt.State == runstate.AttemptVerifying && (attempt.ChangeSetRecorded || hasVerificationPassed(input.Projection.State, attempt.AttemptID)) && !attempt.AcceptanceStarted {
		decision := action(CaseStartAcceptance, ActionAppendAcceptanceStarted, false)
		decision.Action.AttemptID = attempt.AttemptID
		decision.Action.Continuation = ContinuationC3
		return decision
	}
	if attempt.State == runstate.AttemptCompleted && !attempt.MovementSucceeded {
		decision := action(CaseMovementSucceeded, ActionAppendMovementSucceeded, true)
		decision.Action.AttemptID = attempt.AttemptID
		return decision
	}
	if attempt.State == runstate.AttemptCompleted && attempt.MovementSucceeded {
		return proceedScheduler()
	}
	if attempt.MovementFailed && !input.Projection.State.Run.Terminal() {
		decision := action(CaseRunFailed, ActionAppendRunFailed, true)
		decision.Action.AttemptID = attempt.AttemptID
		return decision
	}
	if attempt.FinalGateRejected && !attempt.MovementFailed {
		decision := action(CaseFinalGateRejected, ActionAppendFinalGateFailure, true)
		decision.Action.AttemptID = attempt.AttemptID
		if input.Projection.Acceptance != nil {
			decision.Action.QuestionDecisionID = input.Projection.Acceptance.Gate.DecisionID
			decision.Action.SubjectTree = input.Projection.State.Acceptances[attempt.AttemptID].SubjectTree
		}
		return decision
	}
	if attempt.AcceptanceStarted {
		decision := action(CaseContinue, ActionProceedAcceptance, false)
		decision.Action.AttemptID = attempt.AttemptID
		decision.Action.Continuation = ContinuationC3
		return decision
	}
	decision := action(CaseContinue, ActionProceedScheduler, false)
	decision.Action.Continuation = ContinuationC4
	return decision
}

// PlanAcceptance applies Appendix C.3 after C.2 selects ContinuationC3. It
// receives only replayed facts and caller-supplied observations; no worktree,
// process, or launch-directory inspection occurs here.
func PlanAcceptance(input Input) Decision {
	attempt := currentHeadAttempt(input.Projection)
	if attempt == nil {
		return proceedScheduler()
	}
	acceptance, ok := input.Projection.State.Acceptances[attempt.AttemptID]
	if !ok || !acceptance.Started {
		return proceedScheduler()
	}
	recovery := input.Projection.Acceptance

	if recovery != nil && recovery.Failed {
		return realizeRecordedDisposition(CaseAcceptanceFailed, attempt, input.Projection.Scheduler)
	}
	if failed, ok := firstFailedCriterion(acceptance); ok {
		return acceptanceFailureAction(CaseCriterionFailed, attempt.AttemptID, failed.ID, failed.Reason)
	}
	if criterionID, ok := firstInFlightCriterion(acceptance); ok {
		if input.Observations.CriterionSweep == SweepUnverifiable {
			return halt(CaseIncompleteCriterion, HaltSweepUnverifiable)
		}
		// RC-RESUME-024 always sweeps before the subject verdict. The supplied
		// subject observation may have been collected while the criterion still
		// held the worktree, so it cannot choose the post-sweep consequence.
		decision := verifyAcceptanceSubject(CaseIncompleteCriterion, ActionRecoverIncompleteCriterion, attempt.AttemptID, []ActionStep{StepSweepCriterionSession})
		decision.Action.CriterionID = criterionID
		return decision
	}
	if acceptance.EvaluationCompleted {
		return planEvaluatedAcceptance(input, attempt, acceptance, recovery)
	}
	if allCriteriaPassed(acceptance) {
		switch input.Observations.AcceptanceSubject {
		case SubjectMismatched:
			return acceptanceFailureAction(CaseCriteriaPassed, attempt.AttemptID, "", "recovery_subject_mismatch")
		case SubjectMatched:
			decision := action(CaseCriteriaPassed, ActionAppendEvaluationCompleted, true)
			decision.Action.AttemptID = attempt.AttemptID
			decision.Action.SubjectTree = acceptance.SubjectTree
			return decision
		default:
			return verifyAcceptanceSubject(CaseCriteriaPassed, ActionVerifyAcceptanceSubject, attempt.AttemptID, nil)
		}
	}
	if input.Observations.UnjournaledLaunch != UnjournaledLaunchAbsent {
		return planUnjournaledLaunch(input, attempt.AttemptID)
	}
	caseID := CaseFirstCriterion
	if hasCompletedCriteria(acceptance) {
		caseID = CaseNextCriterion
	}
	switch input.Observations.AcceptanceSubject {
	case SubjectMismatched:
		return acceptanceFailureAction(caseID, attempt.AttemptID, "", "recovery_subject_mismatch")
	case SubjectMatched:
		if criterionID, ok := nextUnstartedCriterion(acceptance); ok {
			decision := action(caseID, ActionResumeCriterion, true)
			decision.Action.AttemptID = attempt.AttemptID
			decision.Action.CriterionID = criterionID
			decision.Action.SubjectTree = acceptance.SubjectTree
			return decision
		}
	default:
		return verifyAcceptanceSubject(caseID, ActionVerifyAcceptanceSubject, attempt.AttemptID, nil)
	}
	return proceedScheduler()
}

// PlanScheduler applies Appendix C.4 after C.1 selected ContinuationC4. It
// advances one compiled lifecycle step only; the executor must replay and
// return to Plan before taking another step.
func PlanScheduler(input Input) Decision {
	state := input.Projection.State

	if state.Run.Terminal() {
		return action(CaseTerminal, ActionTerminalCleanup, false)
	}
	if currentHeadAttemptInFlight(input.Projection) {
		decision := action(CaseContinue, ActionProceedAttempt, false)
		decision.Action.Continuation = ContinuationC2
		return decision
	}
	decision := PlanBetweenUnit(input.Projection)
	if decision.CaseID == CaseBudgetExhausted {
		return decision
	}
	if recovery := input.Projection.CompositionRecovery; recovery != nil && recovery.Recovered {
		decision := action(CaseRecoveredComposition, ActionRerunComposition, true)
		decision.Action.MovementID = recovery.MovementID
		return decision
	}
	if !decision.Valid() {
		// This preserves PlanScheduler's total recovery surface for synthetic,
		// uncompiled planner inputs. A live caller must treat this as an error.
		return action(CaseScheduler, ActionProceedScheduler, false)
	}
	return decision
}

// PlanBetweenUnit is the pure shared selector for one compiled lifecycle
// choice between units. Both live execution and recovery replay the same
// projection before using it; it deliberately accepts no recovery observation.
func PlanBetweenUnit(projection Projection) Decision {
	state := projection.State
	scheduler := projection.Scheduler

	if scheduler.RemainingTime == 0 {
		return budgetExhaustion(state, scheduler)
	}
	if successor := scheduler.PendingSuccessor; successor != nil {
		decision := action(CaseScheduler, ActionMaterializeSuccessor, true)
		copy := *successor
		decision.Action.PendingSuccessor = &copy
		decision.Action.MovementID = successor.MovementID
		return decision
	}

	for _, movement := range scheduler.Movements {
		if state.Movements[movement.ID] != runstate.MovementPending || !dependenciesSucceeded(state, movement.Needs) {
			continue
		}
		// §8 requires the candidate before the final movement may become READY.
		if movement.Final && !scheduler.GateWaived && state.ApplicationCandidate == nil {
			continue
		}
		decision := action(CaseScheduler, ActionAppendMovementReady, true)
		decision.Action.MovementID = movement.ID
		return decision
	}
	for _, movement := range scheduler.Movements {
		switch state.Movements[movement.ID] {
		case runstate.MovementReady:
			decision := action(CaseScheduler, ActionAppendMovementStarted, true)
			decision.Action.MovementID = movement.ID
			return decision
		case runstate.MovementRunning:
			decision := action(CaseScheduler, ActionSelectInitialPerformer, true)
			decision.Action.MovementID = movement.ID
			return decision
		}
	}
	if scheduler.GateWaived && waivedCompletion(state, scheduler) {
		decision := action(CaseScheduler, ActionComposeCandidate, true)
		decision.Action.CandidateCarrying = true
		return decision
	}
	if !scheduler.GateWaived && state.ApplicationCandidate == nil && candidatePrecondition(state, scheduler) {
		return action(CaseScheduler, ActionComposeCandidate, true)
	}

	// This invalid decision is an error sentinel for live callers: a compiled
	// lifecycle must always have a next effect. It is intentionally not
	// ActionProceedScheduler, which a live executor could otherwise loop on.
	return Decision{CaseID: CaseScheduler}
}

func budgetExhaustion(state runstate.State, scheduler Scheduler) Decision {
	for _, movement := range scheduler.Movements {
		if state.Movements[movement.ID] == runstate.MovementRunning {
			decision := action(CaseBudgetExhausted, ActionAppendBudgetFailure, true)
			decision.Action.MovementID = movement.ID
			decision.Action.FailureReason = "budget_exhausted"
			decision.Action.Steps = []ActionStep{StepAppendMovementBudgetFailure, StepAppendRunFailed}
			return decision
		}
	}
	decision := action(CaseBudgetExhausted, ActionAppendRunFailed, true)
	decision.Action.FailureReason = "budget_exhausted"
	return decision
}

func dependenciesSucceeded(state runstate.State, needs []runstate.MovementID) bool {
	for _, need := range needs {
		if state.Movements[need] != runstate.MovementSucceeded {
			return false
		}
	}
	return true
}

func candidatePrecondition(state runstate.State, scheduler Scheduler) bool {
	for _, movement := range scheduler.Movements {
		if movement.Final || !movement.RepoWrite {
			continue
		}
		if state.Movements[movement.ID] != runstate.MovementSucceeded {
			return false
		}
	}
	return true
}

func waivedCompletion(state runstate.State, scheduler Scheduler) bool {
	for _, movement := range scheduler.Movements {
		if state.Movements[movement.ID] != runstate.MovementSucceeded &&
			state.Movements[movement.ID] != runstate.MovementInapplicable {
			return false
		}
	}
	return len(scheduler.Movements) != 0
}

func planEvaluatedAcceptance(input Input, attempt *AttemptRecovery, acceptance runstate.Acceptance, recovery *AcceptanceRecovery) Decision {
	gate := GateRecovery{}
	if recovery != nil {
		gate = recovery.Gate
	}
	if gate.Required && !gate.Requested {
		decision := action(CaseRequestHumanGate, ActionAppendHumanGateRequest, true)
		decision.Action.AttemptID = attempt.AttemptID
		decision.Action.QuestionDecisionID = gate.DecisionID
		decision.Action.SubjectTree = acceptance.SubjectTree
		decision.Action.ReviewOutcome = gate.ReviewOutcome
		decision.Action.BlockingFindings = append([]runstate.FindingReference(nil), gate.BlockingFindings...)
		return decision
	}
	if gate.Required && gate.Requested && !gate.Resolved {
		return action(CaseHumanGateWaiting, ActionReturnWaitingHuman, false)
	}
	if gate.Required && gate.Resolved && !gate.Approved {
		decision := action(CaseHumanGateRejected, ActionAppendGateRejectedFailure, true)
		decision.Action.AttemptID = attempt.AttemptID
		decision.Action.MovementID = attempt.MovementID
		decision.Action.QuestionDecisionID = gate.DecisionID
		decision.Action.SubjectTree = acceptance.SubjectTree
		return decision
	}
	caseID := CaseGateFreeCompletion
	if gate.Required {
		caseID = CaseHumanGateApproved
	}
	switch input.Observations.AcceptanceSubject {
	case SubjectMismatched:
		return acceptanceFailureAction(caseID, attempt.AttemptID, "", "recovery_subject_mismatch")
	case SubjectMatched:
		decision := action(caseID, ActionAppendAcceptanceSuccess, true)
		decision.Action.AttemptID = attempt.AttemptID
		decision.Action.MovementID = attempt.MovementID
		decision.Action.SubjectTree = acceptance.SubjectTree
		decision.Action.Steps = []ActionStep{StepAppendAttemptCompleted, StepAppendMovementSucceeded}
		return decision
	default:
		return verifyAcceptanceSubject(caseID, ActionVerifyAcceptanceSubject, attempt.AttemptID, nil)
	}
}

func planUnjournaledLaunch(input Input, attemptID runstate.AttemptID) Decision {
	switch input.Observations.UnjournaledLaunch {
	case UnjournaledLaunchHandoffUnverifiable:
		return halt(CaseUnjournaledLaunch, HaltSpawnHandoffUnverifiable)
	case UnjournaledLaunchSweepUnverifiable:
		return halt(CaseUnjournaledLaunch, HaltSweepUnverifiable)
	case UnjournaledLaunchMarkerFree, UnjournaledLaunchSessionEmpty:
		decision := action(CaseUnjournaledLaunch, ActionRemoveUnjournaledLaunch, true)
		decision.Action.AttemptID = attemptID
		decision.Action.Continuation = ContinuationC3
		return decision
	default:
		decision := action(CaseUnjournaledLaunch, ActionStabilizeUnjournaledLaunch, true)
		decision.Action.AttemptID = attemptID
		return decision
	}
}

func acceptanceFailureAction(caseID CaseID, attemptID runstate.AttemptID, criterionID runstate.CriterionID, reason string) Decision {
	decision := action(caseID, ActionAppendAcceptanceFailure, true)
	decision.Action.AttemptID = attemptID
	decision.Action.CriterionID = criterionID
	decision.Action.FailureReason = reason
	decision.Action.Steps = []ActionStep{StepClassifyAcceptanceFailure}
	return decision
}

func verifyAcceptanceSubject(caseID CaseID, kind ActionKind, attemptID runstate.AttemptID, prefix []ActionStep) Decision {
	decision := action(caseID, kind, true)
	decision.Action.AttemptID = attemptID
	decision.Action.Steps = append(prefix, StepVerifyAcceptanceSubject)
	return decision
}

type failedCriterion struct {
	ID     runstate.CriterionID
	Reason string
}

func firstFailedCriterion(acceptance runstate.Acceptance) (failedCriterion, bool) {
	for _, criterionID := range acceptance.PlannedCriterionIDs {
		record := acceptance.Criteria[criterionID]
		if !record.Completed {
			continue
		}
		switch record.Outcome {
		case "FAIL":
			return failedCriterion{ID: criterionID, Reason: "criterion_failed"}, true
		case "ERROR":
			return failedCriterion{ID: criterionID, Reason: "criterion_errored"}, true
		}
	}
	return failedCriterion{}, false
}

func firstInFlightCriterion(acceptance runstate.Acceptance) (runstate.CriterionID, bool) {
	for _, criterionID := range acceptance.PlannedCriterionIDs {
		record := acceptance.Criteria[criterionID]
		if record.Started && !record.Completed {
			return criterionID, true
		}
	}
	return "", false
}

func allCriteriaPassed(acceptance runstate.Acceptance) bool {
	if len(acceptance.PlannedCriterionIDs) == 0 {
		return true
	}
	for _, criterionID := range acceptance.PlannedCriterionIDs {
		record := acceptance.Criteria[criterionID]
		if !record.Completed || record.Outcome != "PASS" {
			return false
		}
	}
	return true
}

func hasCompletedCriteria(acceptance runstate.Acceptance) bool {
	for _, criterionID := range acceptance.PlannedCriterionIDs {
		if acceptance.Criteria[criterionID].Completed {
			return true
		}
	}
	return false
}

func nextUnstartedCriterion(acceptance runstate.Acceptance) (runstate.CriterionID, bool) {
	for _, criterionID := range acceptance.PlannedCriterionIDs {
		if !acceptance.Criteria[criterionID].Started {
			return criterionID, true
		}
	}
	return "", false
}

func proceedScheduler() Decision {
	decision := action(CaseContinue, ActionProceedScheduler, false)
	decision.Action.Continuation = ContinuationC4
	return decision
}

func realizeRecordedDisposition(caseID CaseID, attempt *AttemptRecovery, scheduler Scheduler) Decision {
	decision := action(caseID, ActionRealizeRecordedDisposition, true)
	decision.Action.AttemptID = attempt.AttemptID
	if attempt.RecordedDisposition == nil {
		return decision
	}
	disposition := *attempt.RecordedDisposition
	decision.Action.RecordedDisposition = &disposition
	if disposition.Charged != "quality_retry" && disposition.Charged != "fallback" {
		return decision
	}
	pending := scheduler.PendingSuccessor
	if pending == nil || pending.AttemptID != attempt.AttemptID {
		return decision
	}
	copy := *pending
	decision.Action.PendingSuccessor = &copy
	decision.Action.Continuation = ContinuationC4
	decision.Action.Replan = false
	return decision
}

func currentHeadAttempt(projection Projection) *AttemptRecovery {
	attempt := projection.CurrentHeadAttempt
	if attempt == nil || attempt.ScoreRevision != projection.State.ScoreHead.Revision || attempt.State == runstate.AttemptSuperseded {
		return nil
	}
	return attempt
}

func currentHeadAttemptInFlight(projection Projection) bool {
	attempt := currentHeadAttempt(projection)
	if attempt == nil {
		return false
	}
	switch attempt.State {
	case runstate.AttemptStarting, runstate.AttemptRunning, runstate.AttemptVerifying:
		return true
	default:
		return false
	}
}

func recoveryFailureAction(caseID CaseID, kind ActionKind, attemptID runstate.AttemptID, failureKind, failureReason string, steps []ActionStep) Decision {
	decision := action(caseID, kind, true)
	decision.Action.AttemptID = attemptID
	decision.Action.FailureKind = failureKind
	decision.Action.FailureReason = failureReason
	decision.Action.Steps = steps
	return decision
}

func firstMissingQuestionRequest(requests []QuestionRequest) (QuestionRequest, bool) {
	for _, request := range requests {
		if request.DecisionID != "" && !request.Durable {
			return request, true
		}
	}
	return QuestionRequest{}, false
}

func hasUnresolvedBlockingDecision(state runstate.State, attemptID runstate.AttemptID) bool {
	for _, decision := range state.PendingDecisions {
		if decision.AttemptID == attemptID && decision.Blocking {
			return true
		}
	}
	return false
}

func hasAdapterProbe(state runstate.State, attemptID runstate.AttemptID) bool {
	_, ok := state.AdapterObservations[attemptID]
	return ok
}

func movementHasRepoWrite(state runstate.State, movementID runstate.MovementID) bool {
	return state.RepoWriteMovements[movementID]
}

func hasVerificationPassed(state runstate.State, attemptID runstate.AttemptID) bool {
	return state.VerifiedAttempts[attemptID]
}

func action(caseID CaseID, kind ActionKind, replan bool) Decision {
	return Decision{CaseID: caseID, Action: &Action{Kind: kind, Replan: replan}}
}

func halt(caseID CaseID, reason HaltReason) Decision {
	return Decision{CaseID: caseID, Halt: reason}
}

func firstMissingRoutedRequest(state runstate.State) (runstate.ProposalID, bool) {
	var selected runstate.ProposalID
	for proposalID, routed := range state.RoutedAmendments {
		if _, exists := state.PendingDecisions[routed.DecisionID]; exists {
			continue
		}
		if selected == "" || proposalID < selected {
			selected = proposalID
		}
	}
	return selected, selected != ""
}

func firstBlockedProposalRoute(routes []BlockedProposalRoute) (BlockedProposalRoute, bool) {
	for _, route := range routes {
		if route.ProposalID != "" && route.AttemptID != "" {
			return route, true
		}
	}
	return BlockedProposalRoute{}, false
}

func firstRevisionRestart(restarts []RevisionRestart) (RevisionRestart, bool) {
	var selected RevisionRestart
	for _, restart := range restarts {
		if restart.MovementID == "" || restart.AttemptID == "" || restart.ApprovalEventID == "" || restart.Performer == "" {
			continue
		}
		if selected.MovementID == "" || restart.MovementID < selected.MovementID {
			selected = restart
		}
	}
	return selected, selected.MovementID != ""
}

func firstCompositionTerminal(terminals []CompositionTerminal) (CompositionTerminal, bool) {
	var selected CompositionTerminal
	for _, terminal := range terminals {
		if terminal.Scope == "" || terminal.TargetID == "" || terminal.Reason == "" {
			continue
		}
		if selected.TargetID == "" || compositionKey(terminal) < compositionKey(selected) {
			selected = terminal
		}
	}
	return selected, selected.TargetID != ""
}

func compositionKey(terminal CompositionTerminal) string {
	return terminal.Scope + "\x00" + terminal.TargetID + "\x00" + terminal.Reason
}

func firstMissingReferenceReason(references []ReferenceObservation) (HaltReason, bool) {
	for _, kind := range []ReferenceKind{
		ReferenceArtifact,
		ReferenceReviewSubjectInput,
		ReferenceSnapshot,
		ReferenceChangeSetRef,
		ReferenceProposalRecord,
		ReferenceResolvedCast,
	} {
		for _, reference := range references {
			if reference.Kind != kind || reference.Present {
				continue
			}
			reason, _ := missingReferenceReason(kind)
			return reason, true
		}
	}
	return "", false
}

func missingReferenceReason(kind ReferenceKind) (HaltReason, bool) {
	switch kind {
	case ReferenceArtifact, ReferenceReviewSubjectInput:
		return HaltMissingArtifactFile, true
	case ReferenceSnapshot:
		return HaltMissingSnapshotFile, true
	case ReferenceChangeSetRef:
		return HaltMissingChangeSetRef, true
	case ReferenceProposalRecord:
		return HaltMissingProposalRecord, true
	case ReferenceResolvedCast:
		return HaltMissingResolvedCast, true
	default:
		return "", false
	}
}
