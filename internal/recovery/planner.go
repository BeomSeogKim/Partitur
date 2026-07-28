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
	CaseRoutedAmendment        CaseID = "RC-RESUME-037"
	CaseRevisionRestart        CaseID = "RC-RESUME-042"
	CaseCompositionTerminal    CaseID = "RC-RESUME-011"
	CaseContinue               CaseID = "RC-RESUME-012"
)

// HaltReason is an Appendix D recovery halt reason.
type HaltReason string

const (
	HaltOwnerUnverifiable      HaltReason = "owner_unverifiable"
	HaltRootSnapshotDivergence HaltReason = "root_snapshot_divergence"
	HaltMissingArtifactFile    HaltReason = "missing_artifact_file"
	HaltMissingSnapshotFile    HaltReason = "missing_snapshot_file"
	HaltMissingChangeSetRef    HaltReason = "missing_changeset_ref"
	HaltMissingProposalRecord  HaltReason = "missing_proposal_record"
	HaltMissingResolvedCast    HaltReason = "missing_resolved_cast"
	HaltMissingPreparePlan     HaltReason = "missing_prepare_plan"
)

// OwnerState is the caller's observation of the owner named by a readable
// current lease. It is deliberately an observation, not a process probe.
type OwnerState string

const (
	OwnerDead         OwnerState = "dead"
	OwnerLive         OwnerState = "live"
	OwnerUnverifiable OwnerState = "unverifiable"
)

// LeaseObservation is supplied by the caller after inspecting driver.lease.
// An unreadable lease is unsafe to resume beside, so it is represented as an
// unverifiable owner rather than being treated as absent.
type LeaseObservation struct {
	Exists   bool
	Readable bool
	Epoch    uint64
	Owner    OwnerState
}

// ReferenceKind identifies one event-named run-level object checked by
// RC-RESUME-010. Present means both present and hash-matched.
type ReferenceKind string

const (
	ReferenceArtifact       ReferenceKind = "artifact"
	ReferenceSnapshot       ReferenceKind = "snapshot"
	ReferenceChangeSetRef   ReferenceKind = "change_set_ref"
	ReferenceProposalRecord ReferenceKind = "proposal_record"
	ReferenceResolvedCast   ReferenceKind = "resolved_cast"
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
	MovementID runstate.MovementID
}

// CompositionTerminal is replay-derived evidence waiting for the terminal
// event Appendix B.3 already determines. The evidence itself remains outside
// runstate's ordinary lifecycle projection.
type CompositionTerminal struct {
	Scope    string
	TargetID string
	Reason   string
}

// Projection is the recovery-specific view built while replaying the journal.
// State is the ordinary runstate projection. The two additional facts preserve
// evidence-only and derived recovery state without making the planner inspect
// the journal itself.
type Projection struct {
	State                 runstate.State
	RevisionRestarts      []RevisionRestart
	CompositionTerminals  []CompositionTerminal
	HasCurrentHeadAttempt bool
}

// Observations are all non-journal inputs used by C.1.
type Observations struct {
	Lease                  LeaseObservation
	RootSnapshotDivergence bool
	References             []ReferenceObservation
	Prepare                PrepareObservation
}

// Input is the complete, already-replayed input to the pure C.1 planner.
type Input struct {
	Projection   Projection
	Observations Observations
}

// ActionKind names an action for the later recovery executor. No ActionKind
// performs the named work in this package.
type ActionKind string

const (
	ActionTerminalCleanup           ActionKind = "terminal_cleanup"
	ActionRemoveStaleLease          ActionKind = "remove_stale_lease"
	ActionQuarantineOrphanLease     ActionKind = "quarantine_orphan_lease"
	ActionRefuseResume              ActionKind = "refuse_resume"
	ActionExecuteCancellation       ActionKind = "execute_cancellation_oracle"
	ActionCompleteOrAbandonPrepare  ActionKind = "complete_or_abandon_prepare"
	ActionReclaimAuthority          ActionKind = "reclaim_authority"
	ActionAppendRoutedRequest       ActionKind = "append_routed_request"
	ActionSelectRevisionRestart     ActionKind = "select_revision_restart"
	ActionAppendCompositionTerminal ActionKind = "append_composition_terminal"
	ActionProceedAttempt            ActionKind = "proceed_c2"
	ActionProceedScheduler          ActionKind = "proceed_c4"
)

// Action is a pure description for the recovery executor. Replan means the
// executor must apply this one action and invoke Plan again from the top.
type Action struct {
	Kind                ActionKind
	Replan              bool
	RoutedProposalID    runstate.ProposalID
	RevisionRestart     *RevisionRestart
	CompositionTerminal *CompositionTerminal
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
	if state.Authority.Epoch > 0 && (!lease.Exists || lease.Owner == OwnerDead) {
		return action(CaseReclaimAuthority, ActionReclaimAuthority, false)
	}
	if input.Observations.RootSnapshotDivergence {
		return halt(CaseRootSnapshotDivergence, HaltRootSnapshotDivergence)
	}
	if reason, ok := firstMissingReferenceReason(input.Observations.References); ok {
		return halt(CaseMissingReference, reason)
	}
	if proposalID, ok := firstMissingRoutedRequest(state); ok {
		decision := action(CaseRoutedAmendment, ActionAppendRoutedRequest, true)
		decision.Action.RoutedProposalID = proposalID
		return decision
	}
	if restart, ok := firstRevisionRestart(input.Projection.RevisionRestarts); ok {
		decision := action(CaseRevisionRestart, ActionSelectRevisionRestart, false)
		decision.Action.RevisionRestart = &restart
		return decision
	}
	if terminal, ok := firstCompositionTerminal(input.Projection.CompositionTerminals); ok {
		decision := action(CaseCompositionTerminal, ActionAppendCompositionTerminal, false)
		decision.Action.CompositionTerminal = &terminal
		return decision
	}
	if input.Projection.HasCurrentHeadAttempt {
		return action(CaseContinue, ActionProceedAttempt, false)
	}
	return action(CaseContinue, ActionProceedScheduler, false)
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

func firstRevisionRestart(restarts []RevisionRestart) (RevisionRestart, bool) {
	var selected RevisionRestart
	for _, restart := range restarts {
		if restart.MovementID == "" {
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
	case ReferenceArtifact:
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
