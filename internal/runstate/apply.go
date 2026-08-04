package runstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

var (
	ErrUnsupportedEventType = errors.New("unsupported event type")
	ErrInvalidEvent         = errors.New("invalid event")
	ErrIllegalTransition    = errors.New("illegal transition")
	ErrSweepUnverifiable    = errors.New("recovery session sweep is unverifiable")
)

// ValidateEvent validates the supported event's exact payload without applying
// its transition.
func ValidateEvent(event Event) error {
	if _, err := validatePayload(event); err != nil {
		return err
	}
	return validateDerivedCausationID(event)
}

// IdempotencyKey returns the Appendix B key for a supported event.
func IdempotencyKey(event Event) (string, error) {
	payload, err := validatePayload(event)
	if err != nil {
		return "", err
	}
	if err := validateDerivedCausationID(event); err != nil {
		return "", err
	}
	switch event.Type {
	case EventRunStarted, EventRunSucceeded, EventRunFailed, EventRunCancelled, EventCancelRequested:
		return string(event.RunID), nil
	case EventMovementReady, EventMovementStarted:
		return string(event.MovementID), nil
	case EventMovementFailed:
		return string(event.MovementID), nil
	case EventMovementCancelled:
		return event.CausationID + "\x00" + string(event.MovementID), nil
	case EventMovementSucceeded:
		return string(event.MovementID) + "\x00" + string(event.AttemptID), nil
	case EventPerformerSelected, EventAttemptStarted, EventAdapterProbed, EventPerformerCompleted,
		EventAttemptCompleted, EventAttemptBlocked, EventAttemptFailed, EventChangeSetRecorded, EventVerificationPassed, EventAcceptanceStarted,
		EventAcceptanceFailed, EventAcceptanceEvaluationCompleted:
		return string(event.AttemptID), nil
	case EventAttemptCancelled, EventAttemptSuperseded:
		return event.CausationID + "\x00" + string(event.AttemptID), nil
	case EventArtifactRecorded:
		return mustString(payload, "logical_output_id") + "\x00" + string(event.AttemptID), nil
	case EventDecisionRequested, EventDecisionResolved:
		return mustString(payload, "decision_id"), nil
	case EventDecisionObsoleted:
		return event.CausationID + "\x00" + mustString(payload, "decision_id"), nil
	case EventAmendmentRejected, EventAmendmentRoutedHuman, EventAmendmentHumanRejected:
		return mustString(payload, "proposal_id"), nil
	case EventCompositionConflicted, EventCompositionFailed:
		return mustString(payload, "scope") + "\x00" + mustString(payload, "target_id") + "\x00" + mustString(payload, "composition_subject_hash"), nil
	case EventApplicationCandidateRecorded:
		return mustString(payload, "candidate_id"), nil
	case EventCriterionStarted, EventCriterionCompleted:
		return string(event.AttemptID) + "\x00" + mustString(payload, "criterion_id"), nil
	case EventExecutionStarted, EventExecutionStopped:
		return mustString(payload, "interval_id"), nil
	case EventAmendmentApprovalPrepared, EventAmendmentApprovalAbandoned:
		return mustString(payload, "prepare_id"), nil
	case EventAmendmentApproved:
		return mustString(payload, "proposal_id"), nil
	case EventAuthorityGranted:
		return fmt.Sprintf("%d", mustUint(payload, "authority_epoch")), nil
	case EventJournalTailTruncated:
		return fmt.Sprintf("%d", mustUint(payload, "truncated_seq")), nil
	case EventLog, EventProgress:
		return "", nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedEventType, event.Type)
	}
}

// Apply validates and projects one supported event. Its returned state never
// aliases maps or slices in input, including when an error is returned.
func Apply(input State, event Event) (State, error) {
	state := cloneState(input)
	payload, err := validatePayload(event)
	if err != nil {
		return state, err
	}
	if err := validateDerivedCausation(state, event); err != nil {
		return state, err
	}
	wasTerminal := state.Run.Terminal()
	if state.PendingPrepare != nil && !preparePendingMutation(event.Type) {
		return state, transition(event, "prepare_pending")
	}

	switch event.Type {
	case EventRunStarted:
		if state.Run != RunNotStarted {
			return state, transition(event, "run already started")
		}
		state.Run = RunRunning
		state.ScoreHead = ScoreHead{
			Revision:     event.ScoreRevision,
			SemanticHash: Hash(mustString(payload, "score_hash")),
			FileHash:     Hash(mustString(payload, "score_file_hash")),
		}
	case EventRunSucceeded:
		if state.Run != RunRunning {
			return state, transition(event, "run is not RUNNING")
		}
		candidate := mustObject(payload, "candidate")
		versions, encodeErr := json.Marshal(payload["identity_versions"])
		if encodeErr != nil {
			return state, invalid(event, encodeErr.Error())
		}
		state.ApplicationCandidate = &ApplicationCandidate{
			ID:                        mustString(candidate, "candidate_id"),
			Revision:                  event.ScoreRevision,
			BaseTree:                  mustString(candidate, "base_tree"),
			ResultTree:                mustString(candidate, "result_tree"),
			OrderedChangeSets:         mustStrings(candidate, "ordered_change_sets"),
			Contributors:              candidateContributors(candidate["contributors"].([]any)),
			CompositionDependencyHash: Hash(mustString(candidate, "candidate_composition_dependency_hash")),
			IdentityVersions:          versions,
		}
		state.Run = RunSucceeded
		closeAllPendingDecisions(&state)
	case EventRunFailed:
		if state.Run != RunRunning {
			return state, transition(event, "run is not RUNNING")
		}
		state.Run = RunFailed
		closeAllPendingDecisions(&state)
	case EventRunCancelled:
		if state.Run == RunNotStarted || state.Run.Terminal() {
			return state, transition(event, "run is not nonterminal")
		}
		if state.PendingPrepare != nil {
			return state, transition(event, "prepare is still pending")
		}
		if !slices.Equal(mustStrings(payload, "obsoleted_decision_ids"), pendingDecisionIDs(state)) {
			return state, invalid(event, "obsoleted_decision_ids do not match the pre-event projection")
		}
		cancelledMovements := mustStrings(payload, "cancelled_movement_ids")
		wantMovements := cancellableMovementIDs(state)
		if !slices.Equal(cancelledMovements, wantMovements) {
			return state, invalid(event, "cancelled_movement_ids do not match the pre-event projection")
		}
		cancelledAttempts := mustStrings(payload, "cancelled_attempt_ids")
		wantAttempts := cancellableAttemptIDs(state)
		if !slices.Equal(cancelledAttempts, wantAttempts) {
			return state, invalid(event, "cancelled_attempt_ids do not match the pre-event projection")
		}
		for _, id := range wantMovements {
			state.Movements[MovementID(id)] = MovementCancelled
		}
		for _, id := range wantAttempts {
			attempt := state.Attempts[AttemptID(id)]
			attempt.State = AttemptCancelled
			state.Attempts[AttemptID(id)] = attempt
		}
		state.Run = RunCancelled
		closeAllPendingDecisions(&state)
		if epoch, ok := optionalUint(payload, "fenced_epoch"); ok {
			if epoch == state.Authority.Epoch+1 {
				state.Authority = Authority{Epoch: epoch}
			} else {
				return state, invalid(event, "fenced_epoch is not observed authority epoch plus one")
			}
		}
	case EventMovementReady:
		if err := requireMovement(state, event, MovementPending); err != nil {
			return state, err
		}
		state.Movements[event.MovementID] = MovementReady
	case EventMovementStarted:
		if err := requireMovement(state, event, MovementReady); err != nil {
			return state, err
		}
		state.Movements[event.MovementID] = MovementRunning
	case EventMovementFailed:
		if err := requireMovementOneOf(state, event, MovementRunning, MovementWaitingHuman); err != nil {
			return state, err
		}
		reason := mustString(payload, "reason")
		if reason == "human_gate_rejected" {
			attempt, err := requireAttempt(state, event, AttemptVerifying)
			if err != nil {
				return state, err
			}
			if attempt.MovementID != event.MovementID {
				return state, invalid(event, "attempt does not belong to movement")
			}
			finishesRun := allOtherMovementsFinished(state, event.MovementID)
			if mustBool(payload, "run_failed") != finishesRun {
				return state, invalid(event, "run_failed does not match final movement")
			}
			if finishesRun {
				if state.Run != RunRunning && state.Run != RunWaitingHuman {
					return state, transition(event, "run is not nonterminal")
				}
				state.Run = RunFailed
				closeAllPendingDecisions(&state)
			}
			attempt.State = AttemptFailed
			state.Attempts[event.AttemptID] = attempt
		} else if mustBool(payload, "run_failed") {
			return state, invalid(event, "run_failed is only valid for final human_gate_rejected")
		}
		state.Movements[event.MovementID] = MovementFailed
	case EventMovementSucceeded:
		if state.Run != RunRunning {
			return state, transition(event, "run is not RUNNING")
		}
		if err := requireMovement(state, event, MovementRunning); err != nil {
			return state, err
		}
		attempt, err := requireAttempt(state, event, AttemptCompleted)
		if err != nil {
			return state, err
		}
		if attempt.MovementID != event.MovementID {
			return state, invalid(event, "attempt does not belong to movement")
		}
		approvedArtifacts := mustStrings(payload, "approved_artifact_instance_ids")
		if !slices.Equal(approvedArtifacts, artifactIDsForAttempt(state, event.AttemptID)) {
			return state, invalid(event, "approved artifacts do not match the completed attempt")
		}
		approvedChangeSetID, hasApprovedChangeSet := payload["approved_change_set_id"].(string)
		if state.RepoWriteMovements[event.MovementID] != hasApprovedChangeSet {
			return state, invalid(event, "approved_change_set_id presence does not match repo_write")
		}
		finishesRun := state.FinalMovements[event.MovementID]
		if mustBool(payload, "run_succeeded") != finishesRun {
			return state, invalid(event, "run_succeeded does not match final movement")
		}
		if finishesRun && state.ApplicationCandidate == nil {
			return state, transition(event, "application candidate is not recorded")
		}
		state.Movements[event.MovementID] = MovementSucceeded
		state.MovementResults[event.MovementID] = MovementResult{
			AttemptID:                   event.AttemptID,
			ApprovedArtifactInstanceIDs: toArtifactInstanceIDs(approvedArtifacts),
			ApprovedChangeSetID:         approvedChangeSetID,
		}
		if finishesRun {
			state.Run = RunSucceeded
			closeAllPendingDecisions(&state)
		}
	case EventPerformerSelected:
		if event.AttemptID == "" || event.MovementID == "" {
			return state, invalid(event, "attempt_id and movement_id are required")
		}
		if _, exists := state.Attempts[event.AttemptID]; exists {
			return state, transition(event, "attempt already exists")
		}
		if state.Movements[event.MovementID] != MovementRunning {
			return state, transition(event, "movement is not RUNNING")
		}
		state.Attempts[event.AttemptID] = Attempt{
			MovementID:    event.MovementID,
			ScoreRevision: event.ScoreRevision,
			State:         AttemptStarting,
		}
	case EventAttemptStarted:
		attempt, err := requireAttempt(state, event, AttemptStarting)
		if err != nil {
			return state, err
		}
		_, hasBaseCompositionHash := payload["base_composition_hash"]
		if state.DependencyMovements[event.MovementID] != hasBaseCompositionHash {
			return state, invalid(event, "base_composition_hash presence does not match movement dependencies")
		}
		process, err := processIdentity(mustObject(payload, "adapter_process"))
		if err != nil {
			return state, invalid(event, err.Error())
		}
		attempt.State = AttemptRunning
		state.Attempts[event.AttemptID] = attempt
		state.AdapterLaunches[event.AttemptID] = AdapterLaunch{
			AttemptID: event.AttemptID,
			Process:   process,
		}
	case EventAdapterProbed:
		if _, err := requireAttempt(state, event, AttemptRunning); err != nil {
			return state, err
		}
		if _, exists := state.AdapterObservations[event.AttemptID]; exists {
			return state, transition(event, "adapter already probed")
		}
		versions, encodeErr := json.Marshal(payload["identity_versions"])
		if encodeErr != nil {
			return state, invalid(event, encodeErr.Error())
		}
		state.AdapterObservations[event.AttemptID] = AdapterObservation{
			AdapterVersion:          mustString(payload, "adapter_version"),
			Capabilities:            boolMap(mustObject(payload, "capabilities")),
			Enforcement:             boolMap(mustObject(payload, "enforcement")),
			NegotiatedFeatures:      mustStrings(payload, "negotiated_features"),
			TruncatedResolutions:    mustStrings(payload, "truncated_resolutions"),
			AdvisoryDimensions:      mustStrings(payload, "advisory_dimensions"),
			ExecutionDependencyHash: Hash(mustString(payload, "execution_dependency_hash")),
			IdentityVersions:        versions,
		}
	case EventPerformerCompleted:
		attempt, err := requireAttempt(state, event, AttemptRunning)
		if err != nil {
			return state, err
		}
		if _, probed := state.AdapterObservations[event.AttemptID]; !probed {
			return state, transition(event, "adapter is not probed")
		}
		attempt.State = AttemptVerifying
		state.Attempts[event.AttemptID] = attempt
	case EventAttemptCompleted:
		attempt, err := requireAttempt(state, event, AttemptVerifying)
		if err != nil {
			return state, err
		}
		acceptance := state.Acceptances[event.AttemptID]
		if !acceptance.EvaluationCompleted {
			return state, transition(event, "acceptance evaluation is not completed")
		}
		attempt.State = AttemptCompleted
		state.Attempts[event.AttemptID] = attempt
	case EventAttemptBlocked:
		attempt, err := requireAttempt(state, event, AttemptRunning)
		if err != nil {
			return state, err
		}
		if _, probed := state.AdapterObservations[event.AttemptID]; !probed {
			return state, transition(event, "adapter is not probed")
		}
		attempt.State = AttemptBlocked
		state.Attempts[event.AttemptID] = attempt
	case EventAttemptFailed:
		attempt, err := requireAttemptOneOf(state, event, AttemptStarting, AttemptRunning, AttemptVerifying)
		if err != nil {
			return state, err
		}
		attempt.State = AttemptFailed
		attempt.Failure = &AttemptFailure{
			Kind:        mustString(payload, "kind"),
			Reason:      stringOrEmpty(payload, "reason"),
			Disposition: disposition(mustObject(payload, "disposition")),
		}
		state.Attempts[event.AttemptID] = attempt
	case EventArtifactRecorded:
		attempt, err := requireAttemptOneOf(state, event, AttemptRunning, AttemptVerifying)
		if err != nil {
			return state, err
		}
		if attempt.MovementID != event.MovementID {
			return state, invalid(event, "attempt does not belong to movement")
		}
		if attempt.State == AttemptRunning {
			if _, probed := state.AdapterObservations[event.AttemptID]; !probed {
				return state, transition(event, "adapter is not probed")
			}
		}
		instanceID := ArtifactInstanceID(
			mustString(payload, "logical_output_id") + "@" + string(event.AttemptID),
		)
		if _, exists := state.Artifacts[instanceID]; exists {
			return state, transition(event, "artifact instance already recorded")
		}
		state.Artifacts[instanceID] = ArtifactRecord{
			AttemptID:       event.AttemptID,
			LogicalOutputID: mustString(payload, "logical_output_id"),
			Kind:            mustString(payload, "kind"),
			ContentHash:     Hash(mustString(payload, "content_hash")),
			SizeBytes:       mustUint(payload, "size_bytes"),
			Source:          mustString(payload, "source_path"),
		}
	case EventChangeSetRecorded:
		attempt, err := requireAttempt(state, event, AttemptVerifying)
		if err != nil {
			return state, err
		}
		if attempt.MovementID != event.MovementID {
			return state, invalid(event, "attempt does not belong to movement")
		}
		if !state.RepoWriteMovements[event.MovementID] {
			return state, transition(event, "movement is not repo_write")
		}
		if _, exists := state.ChangeSets[event.AttemptID]; exists {
			return state, transition(event, "change set already recorded")
		}
		versions, encodeErr := json.Marshal(payload["identity_versions"])
		if encodeErr != nil {
			return state, invalid(event, encodeErr.Error())
		}
		state.ChangeSets[event.AttemptID] = ChangeSetRecord{
			AttemptID:        event.AttemptID,
			ChangeSetID:      mustString(payload, "change_set_id"),
			BaseTree:         mustString(payload, "base_tree"),
			ResultTree:       mustString(payload, "result_tree"),
			Commit:           mustString(payload, "commit"),
			Ref:              mustString(payload, "ref"),
			IdentityVersions: versions,
		}
	case EventVerificationPassed:
		if _, err := requireAttempt(state, event, AttemptVerifying); err != nil {
			return state, err
		}
		if state.VerifiedAttempts[event.AttemptID] {
			return state, transition(event, "verification already passed")
		}
		state.VerifiedAttempts[event.AttemptID] = true
	case EventApplicationCandidateRecorded:
		if state.Run != RunRunning {
			return state, transition(event, "run is not RUNNING")
		}
		if state.ApplicationCandidate != nil {
			return state, transition(event, "application candidate already recorded")
		}
		for movementID := range state.RepoWriteMovements {
			movementState := state.Movements[movementID]
			if movementState != MovementSucceeded && movementState != MovementInapplicable {
				return state, transition(event, "repo_write movement has not succeeded")
			}
		}
		versions, encodeErr := json.Marshal(payload["identity_versions"])
		if encodeErr != nil {
			return state, invalid(event, encodeErr.Error())
		}
		state.ApplicationCandidate = &ApplicationCandidate{
			ID:                        mustString(payload, "candidate_id"),
			Revision:                  event.ScoreRevision,
			BaseTree:                  mustString(payload, "base_tree"),
			ResultTree:                mustString(payload, "result_tree"),
			OrderedChangeSets:         mustStrings(payload, "ordered_change_sets"),
			Contributors:              candidateContributors(payload["contributors"].([]any)),
			CompositionDependencyHash: Hash(mustString(payload, "candidate_composition_dependency_hash")),
			IdentityVersions:          versions,
		}
	case EventAcceptanceStarted:
		if _, err := requireAttempt(state, event, AttemptVerifying); err != nil {
			return state, err
		}
		if !state.VerifiedAttempts[event.AttemptID] {
			return state, transition(event, "verification has not passed")
		}
		if existing := state.Acceptances[event.AttemptID]; existing.Started {
			return state, transition(event, "acceptance already started")
		}
		state.Acceptances[event.AttemptID] = Acceptance{
			Started:             true,
			SubjectTree:         mustString(payload, "subject_tree"),
			SpecHash:            Hash(mustString(payload, "acceptance_spec_hash")),
			PlannedCriterionIDs: toCriterionIDs(mustStrings(payload, "planned_criterion_ids")),
			Criteria:            make(map[CriterionID]CriterionRecord),
		}
	case EventCriterionStarted:
		if _, err := requireAttempt(state, event, AttemptVerifying); err != nil {
			return state, err
		}
		acceptance := state.Acceptances[event.AttemptID]
		if !acceptance.Started {
			return state, transition(event, "acceptance is not started")
		}
		criterionID := CriterionID(mustString(payload, "criterion_id"))
		if !slices.Contains(acceptance.PlannedCriterionIDs, criterionID) {
			return state, invalid(event, "criterion_id is not in the acceptance plan")
		}
		if mustString(payload, "subject_tree") != acceptance.SubjectTree {
			return state, invalid(event, "criterion subject does not match acceptance")
		}
		if acceptance.Criteria[criterionID].Started {
			return state, transition(event, "criterion already started")
		}
		launch, err := criterionLaunch(payload)
		if err != nil {
			return state, invalid(event, err.Error())
		}
		acceptance.Criteria[criterionID] = CriterionRecord{
			Started:     true,
			SpecHash:    Hash(mustString(payload, "criterion_spec_hash")),
			SubjectTree: mustString(payload, "subject_tree"),
		}
		state.Acceptances[event.AttemptID] = acceptance
		state.CriterionLaunches[CriterionLaunchKey{
			AttemptID:   event.AttemptID,
			CriterionID: criterionID,
		}] = launch
	case EventCriterionCompleted:
		if _, err := requireAttempt(state, event, AttemptVerifying); err != nil {
			return state, err
		}
		criterionID := CriterionID(mustString(payload, "criterion_id"))
		acceptance := state.Acceptances[event.AttemptID]
		record := acceptance.Criteria[criterionID]
		if !record.Started || record.Completed {
			return state, transition(event, "criterion has no open launch")
		}
		if record.SpecHash != Hash(mustString(payload, "criterion_spec_hash")) ||
			record.SubjectTree != mustString(payload, "subject_tree") {
			return state, invalid(event, "criterion completion binding does not match its launch")
		}
		record.Completed = true
		record.Outcome = mustString(payload, "outcome")
		acceptance.Criteria[criterionID] = record
		state.Acceptances[event.AttemptID] = acceptance
	case EventAcceptanceFailed:
		attempt, err := requireAttempt(state, event, AttemptVerifying)
		if err != nil {
			return state, err
		}
		acceptance := state.Acceptances[event.AttemptID]
		if !acceptance.Started || acceptance.SubjectTree != mustString(payload, "subject_tree") {
			return state, invalid(event, "acceptance failure binding does not match its start")
		}
		attempt.State = AttemptFailed
		attempt.Failure = &AttemptFailure{
			Kind:        mustString(payload, "reason"),
			Disposition: disposition(mustObject(payload, "disposition")),
		}
		state.Attempts[event.AttemptID] = attempt
	case EventAcceptanceEvaluationCompleted:
		if _, err := requireAttempt(state, event, AttemptVerifying); err != nil {
			return state, err
		}
		acceptance := state.Acceptances[event.AttemptID]
		if !acceptance.Started || acceptance.EvaluationCompleted {
			return state, transition(event, "acceptance has no open evaluation")
		}
		if acceptance.SubjectTree != mustString(payload, "subject_tree") ||
			acceptance.SpecHash != Hash(mustString(payload, "acceptance_spec_hash")) {
			return state, invalid(event, "acceptance evaluation binding does not match its start")
		}
		outcomes := payload["criterion_outcomes"].([]any)
		if err := completedAcceptanceMatches(acceptance, outcomes); err != nil {
			return state, invalid(event, err.Error())
		}
		if err := validateEvaluationReview(payload); err != nil {
			return state, invalid(event, err.Error())
		}
		acceptance.EvaluationCompleted = true
		acceptance.ReviewOutcome, _ = payload["review_outcome"].(string)
		if values, ok := payload["blocking_findings"].([]any); ok {
			acceptance.BlockingFindings = findingReferences(values)
		}
		state.Acceptances[event.AttemptID] = acceptance
	case EventDecisionRequested:
		decisionID := mustString(payload, "decision_id")
		if _, exists := state.PendingDecisions[decisionID]; exists {
			return state, transition(event, "decision is already pending")
		}
		decisionType := mustString(payload, "decision_type")
		if decisionType == "amendment" || decisionType == "finalization" {
			proposalID := ProposalID(mustString(payload, "proposal_id"))
			routed, ok := state.RoutedAmendments[proposalID]
			if !ok || routed.DecisionID != decisionID || routed.DecisionType != decisionType || routed.Blocking != decisionBlocking(payload) {
				return state, transition(event, "decision does not match a routed amendment")
			}
		}
		decision := PendingDecision{
			ID:            decisionID,
			Type:          decisionType,
			Blocking:      decisionBlocking(payload),
			MovementID:    event.MovementID,
			AttemptID:     event.AttemptID,
			ScoreRevision: event.ScoreRevision,
		}
		if proposalID, ok := payload["proposal_id"].(string); ok {
			decision.ProposalID = ProposalID(proposalID)
		}
		if gateID, ok := payload["gate_id"].(string); ok {
			decision.GateID = gateID
			decision.SubjectTree = mustString(payload, "subject_tree")
			decision.BlockingFindings = findingReferences(payload["blocking_findings"].([]any))
		}
		state.PendingDecisions[decisionID] = decision
		refreshWaitingHuman(&state)
	case EventDecisionResolved:
		decisionID := mustString(payload, "decision_id")
		decision, ok := state.PendingDecisions[decisionID]
		if !ok || decision.Type != mustString(payload, "decision_type") {
			return state, transition(event, "matching decision is not pending")
		}
		if decision.Type == "human_gate" && (decision.GateID != mustString(payload, "gate_id") ||
			decision.SubjectTree != mustString(mustObject(payload, "scope"), "subject_tree")) {
			return state, invalid(event, "human gate resolution does not match the pending decision")
		}
		if decision.Type == "human_gate" {
			for _, overridden := range findingReferences(payload["overridden_findings"].([]any)) {
				if !containsFindingReference(decision.BlockingFindings, overridden) {
					return state, invalid(event, "human gate override is not a pending blocker")
				}
			}
			resolution := HumanGateResolution{
				DecisionID:         decisionID,
				MovementID:         decision.MovementID,
				AttemptID:          decision.AttemptID,
				ScoreRevision:      decision.ScoreRevision,
				GateID:             mustString(payload, "gate_id"),
				Scope:              HumanGateScope{SubjectTree: mustString(mustObject(payload, "scope"), "subject_tree")},
				Disposition:        mustString(payload, "disposition"),
				OverriddenFindings: findingReferences(payload["overridden_findings"].([]any)),
			}
			resolution.OverrideReason, _ = payload["override_reason"].(string)
			resolution.Reason, _ = payload["reason"].(string)
			state.ResolvedHumanGates[decision.AttemptID] = resolution
		}
		delete(state.PendingDecisions, decisionID)
		refreshWaitingHuman(&state)
	case EventAmendmentRejected:
		if decisionID, ok := payload["decision_id"].(string); ok {
			delete(state.PendingDecisions, decisionID)
		}
		delete(state.RoutedAmendments, ProposalID(mustString(payload, "proposal_id")))
		refreshWaitingHuman(&state)
	case EventAmendmentRoutedHuman:
		if state.Run == RunNotStarted || state.Run.Terminal() {
			return state, transition(event, "run is not nonterminal")
		}
		if state.CancelRequested || state.ScoreHead.Revision != mustUint(payload, "base_revision") ||
			state.ScoreHead.SemanticHash != Hash(mustString(payload, "base_hash")) {
			return state, transition(event, "amendment is not admissible at the current head")
		}
		proposalID := ProposalID(mustString(payload, "proposal_id"))
		if _, exists := state.RoutedAmendments[proposalID]; exists {
			return state, transition(event, "proposal is already routed")
		}
		state.RoutedAmendments[proposalID] = RoutedAmendment{
			ProposalID:        proposalID,
			DecisionID:        mustString(payload, "decision_id"),
			DecisionType:      mustString(payload, "decision_type"),
			Blocking:          mustBool(payload, "blocking"),
			BaseRevision:      mustUint(payload, "base_revision"),
			BaseHash:          Hash(mustString(payload, "base_hash")),
			ClassifierVersion: mustUint(payload, "classifier_version"),
		}
	case EventAmendmentHumanRejected:
		proposalID := ProposalID(mustString(payload, "proposal_id"))
		routed, ok := state.RoutedAmendments[proposalID]
		if !ok || routed.DecisionID != mustString(payload, "decision_id") {
			return state, transition(event, "matching proposal is not routed")
		}
		if routed.BaseRevision != mustUint(payload, "base_revision") ||
			routed.BaseHash != Hash(mustString(payload, "base_hash")) ||
			routed.ClassifierVersion != mustUint(payload, "classifier_version") {
			return state, invalid(event, "rejection binding does not match routed amendment")
		}
		delete(state.RoutedAmendments, proposalID)
		delete(state.PendingDecisions, routed.DecisionID)
		refreshWaitingHuman(&state)
	case EventCompositionConflicted, EventCompositionFailed:
		if err := requireCompositionSource(state, event, payload); err != nil {
			return state, err
		}
	case EventExecutionStarted:
		if state.OpenExecution != nil {
			return state, transition(event, "execution interval already open")
		}
		state.OpenExecution = &ExecutionInterval{
			ID:               IntervalID(mustString(payload, "interval_id")),
			Phase:            mustString(payload, "phase"),
			WallStart:        mustString(payload, "wall_start"),
			RemainingAtStart: mustInt(payload, "remaining_at_start"),
		}
	case EventExecutionStopped:
		intervalID := IntervalID(mustString(payload, "interval_id"))
		if state.OpenExecution == nil || state.OpenExecution.ID != intervalID {
			return state, transition(event, "matching execution interval is not open")
		}
		state.ConsumedBudgetMS += mustInt(payload, "charged_duration")
		state.OpenExecution = nil
	case EventAmendmentApprovalPrepared:
		if state.PendingPrepare != nil {
			return state, transition(event, "prepare already pending")
		}
		if state.Run == RunNotStarted || state.Run.Terminal() {
			return state, transition(event, "run is not nonterminal")
		}
		baseRevision := mustUint(payload, "base_revision")
		baseHash := Hash(mustString(payload, "base_hash"))
		if state.ScoreHead.Revision != baseRevision || state.ScoreHead.SemanticHash != baseHash {
			return state, transition(event, "prepare base does not match score head")
		}
		if mustUint(payload, "new_revision") != baseRevision+1 {
			return state, invalid(event, "new_revision must immediately follow base_revision")
		}
		if mustUint(payload, "observed_authority_epoch") != state.Authority.Epoch {
			return state, invalid(event, "observed_authority_epoch does not match current authority")
		}
		deadline, err := time.Parse("2006-01-02T15:04:05.000Z", mustString(payload, "quiesce_deadline"))
		if err != nil || deadline.Format("2006-01-02T15:04:05.000Z") != mustString(payload, "quiesce_deadline") {
			return state, invalid(event, "quiesce_deadline must be RFC 3339 milliseconds")
		}
		targetAttemptIDs := mustStrings(payload, "target_attempt_ids")
		if !slices.Equal(targetAttemptIDs, cancellableAttemptIDs(state)) {
			return state, invalid(event, "target_attempt_ids do not match the pre-event projection")
		}
		identityVersions, encodeErr := json.Marshal(payload["identity_versions"])
		if encodeErr != nil {
			return state, invalid(event, encodeErr.Error())
		}
		state.PendingPrepare = &PendingPrepare{
			ID:            PrepareID(mustString(payload, "prepare_id")),
			ProposalID:    ProposalID(mustString(payload, "proposal_id")),
			Mode:          mustString(payload, "mode"),
			DecisionID:    optionalStringPointer(payload, "decision_id"),
			EnvelopeClass: mustString(payload, "envelope_class"),
			BaseHead:      state.ScoreHead,
			NewHead: ScoreHead{
				Revision:     mustUint(payload, "new_revision"),
				SemanticHash: Hash(mustString(payload, "new_snapshot_hash")),
				FileHash:     Hash(mustString(payload, "new_snapshot_file_hash")),
			},
			PlanRecordHash:         Hash(mustString(payload, "plan_record_hash")),
			ObservedAuthorityEpoch: mustUint(payload, "observed_authority_epoch"),
			QuiesceDeadline:        mustString(payload, "quiesce_deadline"),
			TargetAttemptIDs:       toAttemptIDs(targetAttemptIDs),
			ClassifierVersion:      mustUint(payload, "classifier_version"),
			IdentityVersions:       identityVersions,
		}
	case EventAmendmentApprovalAbandoned:
		if state.PendingPrepare == nil ||
			state.PendingPrepare.ID != PrepareID(mustString(payload, "prepare_id")) ||
			state.PendingPrepare.ProposalID != ProposalID(mustString(payload, "proposal_id")) {
			return state, transition(event, "matching prepare is not pending")
		}
		if state.PendingPrepare.BaseHead.Revision != mustUint(payload, "base_revision") ||
			state.PendingPrepare.BaseHead.SemanticHash != Hash(mustString(payload, "base_hash")) {
			return state, invalid(event, "abandoned base does not match pending prepare")
		}
		state.PendingPrepare = nil
	case EventAmendmentApproved:
		if state.PendingPrepare == nil ||
			state.PendingPrepare.ProposalID != ProposalID(mustString(payload, "proposal_id")) {
			return state, transition(event, "matching prepare is not pending")
		}
		if mustUint(payload, "base_revision") != state.PendingPrepare.BaseHead.Revision ||
			Hash(mustString(payload, "base_hash")) != state.PendingPrepare.BaseHead.SemanticHash {
			return state, invalid(event, "approved base does not match pending prepare")
		}
		if mustString(payload, "mode") != state.PendingPrepare.Mode ||
			mustUint(payload, "classifier_version") != state.PendingPrepare.ClassifierVersion {
			return state, invalid(event, "approved policy binding does not match pending prepare")
		}
		switch state.PendingPrepare.Mode {
		case "auto":
			if mustString(payload, "envelope_class") != state.PendingPrepare.EnvelopeClass {
				return state, invalid(event, "approved policy binding does not match pending prepare")
			}
		case "human":
			decisionID := optionalStringPointer(payload, "decision_id")
			if decisionID == nil || state.PendingPrepare.DecisionID == nil || *decisionID != *state.PendingPrepare.DecisionID {
				return state, invalid(event, "approved policy binding does not match pending prepare")
			}
		}
		approvedHead := ScoreHead{
			Revision:     mustUint(payload, "new_revision"),
			SemanticHash: Hash(mustString(payload, "new_snapshot_hash")),
			FileHash:     Hash(mustString(payload, "new_snapshot_file_hash")),
		}
		if approvedHead != state.PendingPrepare.NewHead {
			return state, invalid(event, "approved head does not match pending prepare")
		}
		if event.ScoreRevision != approvedHead.Revision {
			return state, invalid(event, "approval envelope revision does not match the new head")
		}
		if !slices.Equal(mustStrings(payload, "obsoleted_decision_ids"), pendingDecisionIDs(state)) {
			return state, invalid(event, "obsoleted_decision_ids do not match the pre-event projection")
		}
		head, err := headMovements(payload["head_movements"].([]any))
		if err != nil {
			return state, invalid(event, err.Error())
		}
		movements, order, repoWrite, dependencies, final, err := reconciledHead(state, head)
		if err != nil {
			return state, invalid(event, err.Error())
		}
		supersededAttemptIDs := mustStrings(payload, "superseded_attempt_ids")
		if !slices.Equal(supersededAttemptIDs, cancellableAttemptIDs(state)) {
			return state, invalid(event, "superseded_attempt_ids do not match the pre-event projection")
		}
		observedEpoch := state.PendingPrepare.ObservedAuthorityEpoch
		if state.Authority.Epoch != observedEpoch {
			return state, invalid(event, "authority epoch does not match pending prepare observation")
		}
		fencedEpoch, fenced := optionalUint(payload, "fenced_epoch")
		if fenced && fencedEpoch != observedEpoch+1 {
			return state, invalid(event, "fenced_epoch is not observed authority epoch plus one")
		}
		state.ScoreHead = approvedHead
		state.Movements = movements
		state.MovementOrder = order
		state.RepoWriteMovements = repoWrite
		state.DependencyMovements = dependencies
		state.FinalMovements = final
		for _, id := range supersededAttemptIDs {
			attemptID := AttemptID(id)
			attempt, ok := state.Attempts[attemptID]
			if !ok || attempt.State.terminal() {
				return state, invalid(event, "superseded_attempt_ids contains a non-live attempt")
			}
			attempt.State = AttemptSuperseded
			state.Attempts[attemptID] = attempt
		}
		if fenced {
			state.Authority = Authority{Epoch: fencedEpoch}
		}
		state.PendingPrepare = nil
		closeAllPendingDecisions(&state)
	case EventAuthorityGranted:
		epoch := mustUint(payload, "authority_epoch")
		if state.Run == RunNotStarted || state.Run.Terminal() || epoch <= state.Authority.Epoch {
			return state, transition(event, "authority epoch cannot be granted")
		}
		start, err := startIdentity(mustObject(payload, "owner_start_identity"))
		if err != nil {
			return state, invalid(event, err.Error())
		}
		ownerPID := mustInt(payload, "owner_pid")
		if ownerPID <= 0 {
			return state, invalid(event, "owner_pid must be positive")
		}
		state.Authority = Authority{
			Epoch: epoch,
			Owner: &AuthorityOwner{PID: int(ownerPID), Start: start},
		}
	case EventCancelRequested:
		if state.Run == RunNotStarted || state.Run.Terminal() || state.CancelRequested {
			return state, transition(event, "cancellation cannot be requested")
		}
		state.CancelRequested = true
	case EventApplyStarted:
		if state.Application.State != ApplicationNotApplied && state.Application.State != ApplicationFailedClean {
			return state, transition(event, "application is not startable")
		}
		if state.ApplicationCandidate == nil || state.ApplicationCandidate.ID != mustString(payload, "candidate_id") {
			return state, invalid(event, "candidate does not match application candidate")
		}
		state.Application = ApplicationProjection{
			State:         ApplicationApplying,
			TransactionID: mustString(payload, "txn_id"),
			CandidateID:   mustString(payload, "candidate_id"),
		}
	case EventApplyCompleted:
		if (state.Application.State != ApplicationApplying && state.Application.State != ApplicationRecoveryRequired) ||
			!matchesApplicationTransaction(state, payload) {
			return state, transition(event, "application transaction is not recoverable")
		}
		state.Application.State = ApplicationApplied
		state.Application.Reason = ""
	case EventApplyFailed:
		if state.Application.State != ApplicationApplying || !matchesApplicationTransaction(state, payload) {
			return state, transition(event, "application transaction is not applying")
		}
		state.Application.State = ApplicationFailedClean
		state.Application.Reason = mustString(payload, "failure_detail")
	case EventApplyRecoveryRequired:
		if state.Application.State != ApplicationApplying || !matchesApplicationTransaction(state, payload) {
			return state, transition(event, "application transaction is not applying")
		}
		state.Application.State = ApplicationRecoveryRequired
		state.Application.Reason = mustString(payload, "failure_detail")
	case EventApplyRecoveryResolved:
		if state.Application.State != ApplicationRecoveryRequired || !matchesApplicationTransaction(state, payload) {
			return state, transition(event, "application transaction is not recovery-required")
		}
		state.Application.State = ApplicationFailedClean
		state.Application.Reason = ""
	case EventScorePromotionStarted:
		if state.Promotion.State == PromotionPromoting {
			if !matchesPromotionTransaction(state, payload) {
				return state, transition(event, "promotion transaction differs from the active transaction")
			}
			break
		}
		if state.Promotion.State != PromotionNotPromoted {
			return state, transition(event, "promotion is not startable")
		}
		state.Promotion = PromotionProjection{
			State:         PromotionPromoting,
			TransactionID: mustString(payload, "txn_id"),
			CandidateID:   mustString(payload, "candidate_id"),
		}
	case EventScorePromoted:
		if (state.Promotion.State != PromotionPromoting && state.Promotion.State != PromotionRecoveryRequired) ||
			!matchesPromotionTransaction(state, payload) {
			return state, transition(event, "promotion transaction is not recoverable")
		}
		state.Promotion.State = PromotionPromoted
		state.Promotion.Reason = ""
	case EventScorePromotionRecoveryRequired:
		if state.Promotion.State != PromotionPromoting || !matchesPromotionTransaction(state, payload) {
			return state, transition(event, "promotion transaction is not promoting")
		}
		state.Promotion.State = PromotionRecoveryRequired
		state.Promotion.Reason = mustString(payload, "failure_detail")
	case EventJournalTailTruncated:
		if mustUint(payload, "truncated_seq") != event.Seq {
			return state, invalid(event, "truncated_seq must equal the repair event sequence")
		}
		// Audit event; no state effect.
	case EventLog, EventProgress:
		// Observational event; no state effect.
	default:
		if isRegistryEvent(event.Type) {
			return state, fmt.Errorf("%w: %s", ErrUnsupportedEventType, event.Type)
		}
		return state, invalid(event, "event type is not in the registry")
	}
	if event.EventID != "" && !isObservationalEvent(event.Type) {
		if state.appliedEvents == nil {
			state.appliedEvents = make(map[string]appliedEvent)
		}
		state.appliedEvents[event.EventID] = appliedEvent{
			Type:            event.Type,
			Sequence:        event.Seq,
			TerminalizesRun: !wasTerminal && state.Run.Terminal(),
		}
	}
	return state, nil
}

func preparePendingMutation(eventType EventType) bool {
	switch eventType {
	case EventExecutionStopped, EventCancelRequested,
		EventAmendmentApprovalAbandoned, EventAmendmentApproved:
		return true
	default:
		return false
	}
}

func validateDerivedCausationID(event Event) error {
	if isDerivedEvent(event.Type) && event.CausationID == "" {
		return invalid(event, "causation_id is required for derived events")
	}
	return nil
}

func validateDerivedCausation(state State, event Event) error {
	if err := validateDerivedCausationID(event); err != nil || !isDerivedEvent(event.Type) {
		return err
	}
	source, exists := state.appliedEvents[event.CausationID]
	if !exists {
		return invalid(event, "causation_id does not reference an already applied event")
	}
	if event.Seq != 0 && source.Sequence >= event.Seq {
		return invalid(event, "causation_id must reference an earlier event")
	}
	switch event.Type {
	case EventMovementCancelled, EventAttemptCancelled:
		if source.Type != EventRunCancelled {
			return invalid(event, "causation_id must reference run.cancelled")
		}
	case EventAttemptSuperseded:
		if source.Type != EventAmendmentApproved {
			return invalid(event, "causation_id must reference amendment.approved")
		}
	case EventDecisionObsoleted:
		if source.Type != EventAmendmentApproved && !source.TerminalizesRun {
			return invalid(event, "causation_id must reference amendment.approved or a terminalizing event")
		}
	}
	return nil
}

func isDerivedEvent(eventType EventType) bool {
	switch eventType {
	case EventMovementCancelled, EventAttemptCancelled, EventAttemptSuperseded, EventDecisionObsoleted:
		return true
	default:
		return false
	}
}

func isObservationalEvent(eventType EventType) bool {
	return eventType == EventLog || eventType == EventProgress
}

func matchesApplicationTransaction(state State, payload map[string]any) bool {
	return state.Application.TransactionID == mustString(payload, "txn_id") &&
		state.Application.CandidateID == mustString(payload, "candidate_id")
}

func matchesPromotionTransaction(state State, payload map[string]any) bool {
	return state.Promotion.TransactionID == mustString(payload, "txn_id") &&
		state.Promotion.CandidateID == mustString(payload, "candidate_id")
}

func transition(event Event, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrIllegalTransition, event.Type, reason)
}

func invalid(event Event, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidEvent, event.Type, reason)
}

func requireMovement(state State, event Event, want MovementState) error {
	return requireMovementOneOf(state, event, want)
}

func requireMovementOneOf(state State, event Event, wants ...MovementState) error {
	if event.MovementID == "" {
		return invalid(event, "movement_id is required")
	}
	if !slices.Contains(wants, state.Movements[event.MovementID]) {
		return transition(event, "movement is not in a legal source state")
	}
	return nil
}

func requireAttempt(state State, event Event, want AttemptState) (Attempt, error) {
	return requireAttemptOneOf(state, event, want)
}

func requireAttemptOneOf(state State, event Event, wants ...AttemptState) (Attempt, error) {
	if event.AttemptID == "" {
		return Attempt{}, invalid(event, "attempt_id is required")
	}
	attempt, ok := state.Attempts[event.AttemptID]
	if !ok || !slices.Contains(wants, attempt.State) {
		return Attempt{}, transition(event, "attempt is not in a legal source state")
	}
	return attempt, nil
}

func requireCompositionSource(state State, event Event, payload map[string]any) error {
	scope := mustString(payload, "scope")
	targetID := mustString(payload, "target_id")
	switch scope {
	case "movement":
		if event.MovementID == "" || targetID != string(event.MovementID) {
			return invalid(event, "movement composition target does not match envelope movement")
		}
		if state.Movements[event.MovementID] != MovementRunning {
			return transition(event, "movement is not RUNNING for composition")
		}
	case "candidate":
		if targetID != string(event.RunID) {
			return invalid(event, "candidate composition target does not match run")
		}
		if state.Run != RunRunning || state.ApplicationCandidate != nil {
			return transition(event, "candidate composition is not legal")
		}
		for movementID := range state.RepoWriteMovements {
			if state.Movements[movementID] != MovementSucceeded && state.Movements[movementID] != MovementInapplicable {
				return transition(event, "repo_write movement has not succeeded")
			}
		}
	default:
		return invalid(event, "invalid composition scope")
	}
	return nil
}

func decisionBlocking(payload map[string]any) bool {
	switch mustString(payload, "decision_type") {
	case "amendment":
		return mustBool(payload, "blocking")
	default:
		return true
	}
}

func pendingDecisionIDs(state State) []string {
	ids := make([]string, 0, len(state.PendingDecisions))
	for id := range state.PendingDecisions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func closeAllPendingDecisions(state *State) {
	for id := range state.PendingDecisions {
		delete(state.PendingDecisions, id)
	}
	refreshWaitingHuman(state)
}

func refreshWaitingHuman(state *State) {
	blocking := false
	for _, decision := range state.PendingDecisions {
		if decision.Blocking {
			blocking = true
			if decision.MovementID != "" && state.Movements[decision.MovementID] == MovementRunning {
				state.Movements[decision.MovementID] = MovementWaitingHuman
			}
		}
	}
	if state.Run.Terminal() || state.Run == RunNotStarted {
		return
	}
	if blocking {
		state.Run = RunWaitingHuman
		return
	}
	if state.Run == RunWaitingHuman {
		state.Run = RunRunning
	}
	for id, movement := range state.Movements {
		if movement == MovementWaitingHuman {
			state.Movements[id] = MovementRunning
		}
	}
}

func cancellableMovementIDs(state State) []string {
	ids := make([]string, 0)
	for id, movementState := range state.Movements {
		if movementState == MovementPending || movementState == MovementReady ||
			movementState == MovementRunning || movementState == MovementWaitingHuman {
			ids = append(ids, string(id))
		}
	}
	slices.Sort(ids)
	return ids
}

func cancellableAttemptIDs(state State) []string {
	ids := make([]string, 0)
	for id, attempt := range state.Attempts {
		if !attempt.State.terminal() {
			ids = append(ids, string(id))
		}
	}
	slices.Sort(ids)
	return ids
}

// CancellationPayload returns the exact run.cancelled payload derived from
// the pre-event projection. The terminal transition validates these lists
// again when it is applied.
func CancellationPayload(state State, fencedEpoch *uint64) map[string]any {
	payload := map[string]any{
		"cancelled_movement_ids": cancellableMovementIDs(state),
		"cancelled_attempt_ids":  cancellableAttemptIDs(state),
		"obsoleted_decision_ids": pendingDecisionIDs(state),
	}
	if fencedEpoch != nil {
		payload["fenced_epoch"] = *fencedEpoch
	}
	return payload
}

func toAttemptIDs(ids []string) []AttemptID {
	output := make([]AttemptID, len(ids))
	for index, id := range ids {
		output[index] = AttemptID(id)
	}
	return output
}

func toCriterionIDs(ids []string) []CriterionID {
	output := make([]CriterionID, len(ids))
	for index, id := range ids {
		output[index] = CriterionID(id)
	}
	return output
}

func toArtifactInstanceIDs(ids []string) []ArtifactInstanceID {
	output := make([]ArtifactInstanceID, len(ids))
	for index, id := range ids {
		output[index] = ArtifactInstanceID(id)
	}
	return output
}

func artifactIDsForAttempt(state State, attemptID AttemptID) []string {
	var ids []string
	for id, artifact := range state.Artifacts {
		if artifact.AttemptID == attemptID {
			ids = append(ids, string(id))
		}
	}
	slices.Sort(ids)
	return ids
}

func allOtherMovementsFinished(state State, succeededID MovementID) bool {
	for id, movementState := range state.Movements {
		if id == succeededID {
			continue
		}
		if movementState != MovementSucceeded && movementState != MovementInapplicable {
			return false
		}
	}
	return true
}

func boolMap(value map[string]any) map[string]bool {
	output := make(map[string]bool, len(value))
	for name, raw := range value {
		output[name], _ = raw.(bool)
	}
	return output
}

func candidateContributors(values []any) []CandidateContributor {
	output := make([]CandidateContributor, len(values))
	for index, raw := range values {
		entry, _ := raw.(map[string]any)
		output[index] = CandidateContributor{
			MovementID:  MovementID(mustString(entry, "movement_id")),
			ChangeSetID: mustString(entry, "change_set_id"),
		}
	}
	return output
}

func completedAcceptanceMatches(acceptance Acceptance, outcomes []any) error {
	if len(outcomes) != len(acceptance.PlannedCriterionIDs) {
		return errors.New("criterion outcomes do not match the acceptance plan")
	}
	for index, raw := range outcomes {
		outcome, _ := raw.(map[string]any)
		criterionID := CriterionID(mustString(outcome, "criterion_id"))
		record := acceptance.Criteria[criterionID]
		if criterionID != acceptance.PlannedCriterionIDs[index] ||
			!record.Completed ||
			record.SpecHash != Hash(mustString(outcome, "criterion_spec_hash")) ||
			record.Outcome != "PASS" ||
			mustString(outcome, "outcome") != "PASS" {
			return errors.New("criterion outcomes do not match completed PASS results")
		}
	}
	return nil
}

func disposition(value map[string]any) Disposition {
	return Disposition{
		Charged:          mustString(value, "charged"),
		MovementTerminal: mustBool(value, "movement_terminal"),
		TerminalReason:   stringOrEmpty(value, "terminal_reason"),
	}
}

func criterionLaunch(payload map[string]any) (CriterionLaunch, error) {
	processValue, hasProcess := payload["criterion_process"]
	spawnFailedValue, hasSpawnFailed := payload["spawn_failed"]
	switch {
	case hasProcess && hasSpawnFailed:
		return nil, errors.New("criterion_process and spawn_failed are mutually exclusive")
	case hasProcess:
		object, ok := processValue.(map[string]any)
		if !ok {
			return nil, errors.New("criterion_process must be an object")
		}
		process, err := processIdentity(object)
		if err != nil {
			return nil, err
		}
		return SpawnedCriterionLaunch{Process: process}, nil
	case hasSpawnFailed:
		value, ok := spawnFailedValue.(bool)
		if !ok || !value {
			return nil, errors.New("spawn_failed must be true")
		}
		return SpawnFailedCriterionLaunch{}, nil
	default:
		return InProcessCriterionLaunch{}, nil
	}
}

func processIdentity(value map[string]any) (ProcessIdentity, error) {
	if err := fields(value, []string{"pid", "session_id", "start_identity"}, nil); err != nil {
		return ProcessIdentity{}, err
	}
	start, err := startIdentity(mustObject(value, "start_identity"))
	if err != nil {
		return ProcessIdentity{}, err
	}
	pid := mustInt(value, "pid")
	sessionID := mustInt(value, "session_id")
	if pid <= 0 || sessionID <= 0 {
		return ProcessIdentity{}, errors.New("pid and session_id must be positive")
	}
	return ProcessIdentity{
		PID:       int(pid),
		SessionID: int(sessionID),
		Start:     start,
	}, nil
}

func startIdentity(value map[string]any) (StartIdentity, error) {
	platform, ok := value["platform"].(string)
	if !ok {
		return nil, errors.New("start identity platform is required")
	}
	switch platform {
	case "linux":
		if err := fields(value, []string{"platform", "boot_id", "start_ticks"}, nil); err != nil {
			return nil, err
		}
		bootID := mustString(value, "boot_id")
		startTicks := mustString(value, "start_ticks")
		if bootID == "" || !validUintString(startTicks) {
			return nil, errors.New("Linux start identity fields must be non-empty")
		}
		return LinuxStartIdentity{BootID: bootID, StartTicks: startTicks}, nil
	case "darwin":
		if err := fields(value, []string{"platform", "start_tvsec", "start_tvusec"}, nil); err != nil {
			return nil, err
		}
		if err := namedTypes(value, []string{"platform"}, nil, nil, nil, []string{"start_tvsec", "start_tvusec"}); err != nil {
			return nil, err
		}
		useconds := mustUint(value, "start_tvusec")
		if useconds >= 1_000_000 {
			return nil, errors.New("Darwin start_tvusec is out of range")
		}
		return DarwinStartIdentity{StartTVSec: mustUint(value, "start_tvsec"), StartTVUsec: useconds}, nil
	default:
		return nil, fmt.Errorf("unsupported start identity platform %q", platform)
	}
}

func validatePayload(event Event) (map[string]any, error) {
	value, err := parseJSONExact(event.Payload)
	if err != nil {
		return nil, invalid(event, err.Error())
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return nil, invalid(event, "payload must be an object")
	}
	required, optional, known := payloadFields(event.Type)
	if !known {
		if isRegistryEvent(event.Type) {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedEventType, event.Type)
		}
		return nil, invalid(event, "event type is not in the registry")
	}
	if err := fields(payload, required, optional); err != nil {
		return nil, invalid(event, err.Error())
	}
	if err := validatePayloadTypes(event.Type, payload); err != nil {
		return nil, invalid(event, err.Error())
	}
	if err := validateNestedPayload(event.Type, payload); err != nil {
		return nil, invalid(event, err.Error())
	}
	if err := validatePayloadValues(event.Type, payload); err != nil {
		return nil, invalid(event, err.Error())
	}
	return payload, nil
}

func payloadFields(eventType EventType) (required, optional []string, known bool) {
	switch eventType {
	case EventRunStarted:
		return []string{"base_commit", "base_tree", "score_hash", "score_file_hash", "resolved_cast_hash", "identity_versions"}, nil, true
	case EventRunSucceeded:
		return []string{"candidate", "waiver", "identity_versions"}, nil, true
	case EventRunFailed:
		return []string{"reason"}, nil, true
	case EventRunCancelled:
		return []string{"cancelled_movement_ids", "cancelled_attempt_ids", "obsoleted_decision_ids"}, []string{"fenced_epoch"}, true
	case EventMovementReady, EventMovementStarted:
		return nil, nil, true
	case EventMovementFailed:
		return []string{"reason", "run_failed"}, []string{"decision_id", "subject_tree"}, true
	case EventMovementCancelled:
		return nil, nil, true
	case EventMovementSucceeded:
		return []string{"approved_artifact_instance_ids", "identity_versions", "run_succeeded"}, []string{"approved_change_set_id"}, true
	case EventPerformerSelected:
		return []string{"reason", "performer_id", "adapter_id", "model"}, nil, true
	case EventAttemptStarted:
		return []string{"attempt_number", "adapter_process", "granted_authority", "identity_versions"}, []string{"base_composition_hash", "review_subject_input"}, true
	case EventAdapterProbed:
		return []string{"adapter_version", "capabilities", "enforcement", "negotiated_features", "truncated_resolutions", "advisory_dimensions", "execution_dependency_hash", "identity_versions"}, nil, true
	case EventPerformerCompleted:
		return []string{"session_hint_stored"}, nil, true
	case EventAttemptCompleted, EventVerificationPassed, EventAttemptCancelled, EventAttemptSuperseded:
		return nil, nil, true
	case EventAttemptBlocked:
		return []string{"raised", "pending_decision_ids"}, nil, true
	case EventAttemptFailed:
		return []string{"kind", "disposition"}, []string{"reason", "detail"}, true
	case EventArtifactRecorded:
		return []string{"logical_output_id", "kind", "content_hash", "size_bytes", "source_path"}, nil, true
	case EventChangeSetRecorded:
		return []string{"change_set_id", "base_tree", "result_tree", "commit", "ref", "identity_versions"}, nil, true
	case EventApplicationCandidateRecorded:
		return []string{"candidate_id", "base_tree", "result_tree", "ordered_change_sets", "contributors", "candidate_composition_dependency_hash", "identity_versions"}, nil, true
	case EventAcceptanceStarted:
		return []string{"subject_tree", "acceptance_spec_hash", "planned_criterion_ids", "identity_versions"}, nil, true
	case EventCriterionStarted:
		return []string{"criterion_id", "criterion_spec_hash", "subject_tree", "identity_versions"}, []string{"criterion_process", "spawn_failed"}, true
	case EventCriterionCompleted:
		return []string{"criterion_id", "criterion_spec_hash", "subject_tree", "outcome", "identity_versions"}, []string{"exit_code", "duration_ms", "output_ref", "truncated_streams", "error_detail"}, true
	case EventAcceptanceFailed:
		return []string{"reason", "subject_tree", "disposition"}, []string{"failed_criterion_id"}, true
	case EventAcceptanceEvaluationCompleted:
		return []string{"subject_tree", "acceptance_spec_hash", "criterion_outcomes", "identity_versions"}, []string{"review_outcome", "blocking_findings"}, true
	case EventDecisionRequested:
		return []string{"decision_id", "decision_type"}, []string{"emitted_id", "question", "gate_id", "gate_mode", "subject_tree", "review_outcome", "blocking_findings", "proposal_id", "routed_reason", "blocking"}, true
	case EventDecisionResolved:
		return []string{"decision_id", "decision_type", "disposition"}, []string{"answer", "gate_id", "scope", "overridden_findings", "override_reason", "reason"}, true
	case EventDecisionObsoleted:
		return []string{"decision_id"}, nil, true
	case EventAmendmentRejected:
		return []string{"proposal_id", "reason", "base_revision", "base_hash", "classifier_version", "identity_versions"}, []string{"emitted_id", "condition", "typed_delta", "actual_impact", "patch_operations_hash", "error_location", "decision_id"}, true
	case EventExecutionStarted:
		return []string{"interval_id", "phase", "wall_start", "remaining_at_start"}, nil, true
	case EventExecutionStopped:
		return []string{"interval_id", "reason", "charging", "charged_duration"}, []string{"observed_at"}, true
	case EventAmendmentApprovalPrepared:
		return []string{"prepare_id", "proposal_id", "mode", "base_revision", "base_hash", "new_revision", "new_snapshot_hash", "new_snapshot_file_hash", "plan_record_hash", "target_attempt_ids", "observed_authority_epoch", "quiesce_deadline", "classifier_version", "identity_versions"}, []string{"decision_id", "envelope_class"}, true
	case EventAmendmentApprovalAbandoned:
		return []string{"prepare_id", "proposal_id", "reason", "base_revision", "base_hash", "classifier_version"}, nil, true
	case EventAmendmentRoutedHuman:
		return []string{"proposal_id", "reason", "decision_type", "blocking", "proposal_record_hash", "base_revision", "base_hash", "classifier_version", "decision_id", "typed_delta", "actual_impact", "identity_versions"}, []string{"emitted_id", "envelope_evaluation"}, true
	case EventAmendmentApproved:
		return []string{"proposal_id", "mode", "base_revision", "base_hash", "classifier_version", "new_revision", "new_snapshot_hash", "new_snapshot_file_hash", "typed_delta", "actual_impact", "head_movements", "superseded_attempt_ids", "obsoleted_decision_ids", "finalization", "identity_versions"}, []string{"emitted_id", "decision_id", "envelope_class", "candidate_id", "envelope_evaluation", "fenced_epoch"}, true
	case EventAuthorityGranted:
		return []string{"authority_epoch", "owner_pid", "owner_start_identity"}, []string{"reclaimed_from_epoch"}, true
	case EventCancelRequested:
		return []string{"requested_by"}, nil, true
	case EventAmendmentHumanRejected:
		return []string{"proposal_id", "decision_id", "human_reason", "base_revision", "base_hash", "classifier_version", "identity_versions"}, nil, true
	case EventCompositionConflicted:
		return []string{"scope", "target_id", "composition_subject_hash", "contributors", "conflicted_paths", "composition_algorithm_version", "identity_versions"}, nil, true
	case EventCompositionFailed:
		return []string{"scope", "target_id", "composition_subject_hash", "cause", "diagnostic", "contributors", "composition_algorithm_version", "identity_versions"}, []string{"git_exit_code"}, true
	case EventApplyStarted:
		return []string{"txn_id", "candidate_id", "before_tree", "result_tree", "touched_paths", "recovery", "identity_versions"}, nil, true
	case EventApplyCompleted:
		return []string{"txn_id", "candidate_id", "result_tree", "identity_versions"}, nil, true
	case EventApplyFailed:
		return []string{"txn_id", "candidate_id", "identity_versions", "failure_detail", "rollback_verified"}, nil, true
	case EventApplyRecoveryRequired:
		return []string{"txn_id", "candidate_id", "identity_versions", "failure_detail"}, []string{"observed_tree"}, true
	case EventApplyRecoveryResolved:
		return []string{"txn_id", "candidate_id", "identity_versions", "outcome"}, nil, true
	case EventScorePromotionStarted:
		return []string{"txn_id", "candidate_id", "identity_versions", "expected_root_file_hash", "target_snapshot_file_hash", "target_revision"}, nil, true
	case EventScorePromoted:
		return []string{"txn_id", "candidate_id", "identity_versions", "target_revision", "target_snapshot_file_hash"}, nil, true
	case EventScorePromotionRecoveryRequired:
		return []string{"txn_id", "candidate_id", "identity_versions", "failure_detail"}, []string{"observed_root_file_hash"}, true
	case EventJournalTailTruncated:
		return []string{"truncated_seq", "discarded_bytes"}, nil, true
	case EventLog:
		return []string{"level", "message"}, nil, true
	case EventProgress:
		return []string{"message"}, nil, true
	default:
		return nil, nil, false
	}
}

func validateNestedPayload(eventType EventType, payload map[string]any) error {
	if versions, ok := payload["identity_versions"].(map[string]any); ok {
		if err := validateIdentityVersions(versions); err != nil {
			return err
		}
	}
	switch eventType {
	case EventRunSucceeded:
		candidate := mustObject(payload, "candidate")
		if err := fields(candidate,
			[]string{"candidate_id", "base_tree", "result_tree", "ordered_change_sets", "contributors", "candidate_composition_dependency_hash"},
			nil,
		); err != nil {
			return fmt.Errorf("candidate: %w", err)
		}
		if err := namedTypes(
			candidate,
			[]string{"candidate_id", "base_tree", "result_tree", "candidate_composition_dependency_hash"},
			nil,
			[]string{"ordered_change_sets", "contributors"},
			nil,
			nil,
		); err != nil {
			return fmt.Errorf("candidate: %w", err)
		}
		if err := stringArray(candidate, "ordered_change_sets"); err != nil {
			return fmt.Errorf("candidate: %w", err)
		}
		if err := validateContributors(candidate["contributors"].([]any)); err != nil {
			return fmt.Errorf("candidate: %w", err)
		}
		waiver := mustObject(payload, "waiver")
		if err := fields(waiver, []string{"reason"}, nil); err != nil {
			return fmt.Errorf("waiver: %w", err)
		}
		if _, ok := waiver["reason"].(string); !ok {
			return errors.New("waiver.reason must be a string")
		}
	case EventAdapterProbed:
		capabilities := mustObject(payload, "capabilities")
		capabilityNames := []string{"repo_read", "repo_write", "shell", "network", "resumable_sessions"}
		if err := fields(capabilities, capabilityNames, nil); err != nil {
			return fmt.Errorf("capabilities: %w", err)
		}
		if err := namedTypes(capabilities, nil, nil, nil, capabilityNames, nil); err != nil {
			return fmt.Errorf("capabilities: %w", err)
		}
		enforcement := mustObject(payload, "enforcement")
		names := []string{"path_grants", "read_only", "network_grants", "shell_grants", "read_grants"}
		if err := fields(enforcement, names, nil); err != nil {
			return fmt.Errorf("enforcement: %w", err)
		}
		if err := namedTypes(enforcement, nil, nil, nil, names, nil); err != nil {
			return fmt.Errorf("enforcement: %w", err)
		}
	case EventAttemptBlocked:
		if err := validateRaisedDecisions(payload["raised"].([]any)); err != nil {
			return err
		}
	case EventApplicationCandidateRecorded:
		if err := validateContributors(payload["contributors"].([]any)); err != nil {
			return fmt.Errorf("contributors: %w", err)
		}
	case EventAcceptanceEvaluationCompleted:
		if err := validateCriterionOutcomes(payload["criterion_outcomes"].([]any)); err != nil {
			return err
		}
	case EventAttemptStarted:
		grants := mustObject(payload, "granted_authority")
		if err := fields(grants, []string{"paths_rw", "paths_ro", "shell", "network"}, nil); err != nil {
			return fmt.Errorf("granted_authority: %w", err)
		}
		if err := namedTypes(grants, nil, nil, []string{"paths_rw", "paths_ro"}, []string{"shell", "network"}, nil); err != nil {
			return fmt.Errorf("granted_authority: %w", err)
		}
		if err := stringArray(grants, "paths_rw"); err != nil {
			return fmt.Errorf("granted_authority: %w", err)
		}
		if err := stringArray(grants, "paths_ro"); err != nil {
			return fmt.Errorf("granted_authority: %w", err)
		}
		if review, present := payload["review_subject_input"]; present {
			value, ok := review.(map[string]any)
			if !ok {
				return errors.New("review_subject_input must be an object")
			}
			if err := fields(value, []string{"instance_id", "hash"}, nil); err != nil {
				return fmt.Errorf("review_subject_input: %w", err)
			}
			if err := namedTypes(value, []string{"instance_id", "hash"}, nil, nil, nil, nil); err != nil {
				return fmt.Errorf("review_subject_input: %w", err)
			}
		}
	case EventAmendmentApproved:
		if err := validateTypedDelta(payload["typed_delta"].([]any)); err != nil {
			return err
		}
		if err := validateActualImpact(mustObject(payload, "actual_impact")); err != nil {
			return err
		}
		if _, err := headMovements(payload["head_movements"].([]any)); err != nil {
			return err
		}
		if evaluation, ok := payload["envelope_evaluation"].(map[string]any); ok {
			if err := validateEnvelopeEvaluation(evaluation); err != nil {
				return err
			}
		}
	case EventAmendmentRejected:
		if typedDelta, ok := payload["typed_delta"].([]any); ok {
			if err := validateTypedDelta(typedDelta); err != nil {
				return err
			}
		}
		if actualImpact, ok := payload["actual_impact"].(map[string]any); ok {
			if err := validateActualImpact(actualImpact); err != nil {
				return err
			}
		}
	case EventAmendmentRoutedHuman:
		if err := validateTypedDelta(payload["typed_delta"].([]any)); err != nil {
			return err
		}
		if err := validateActualImpact(mustObject(payload, "actual_impact")); err != nil {
			return err
		}
		if evaluation, ok := payload["envelope_evaluation"].(map[string]any); ok {
			if err := validateEnvelopeEvaluation(evaluation); err != nil {
				return err
			}
		}
	case EventDecisionRequested:
		return validateDecisionRequest(payload)
	case EventDecisionResolved:
		return validateDecisionResolution(payload)
	case EventCompositionConflicted, EventCompositionFailed:
		if err := validateContributors(payload["contributors"].([]any)); err != nil {
			return fmt.Errorf("contributors: %w", err)
		}
	}
	return nil
}

func validateIdentityVersions(value map[string]any) error {
	if err := fields(value, []string{"canonical_encoding", "projections"}, []string{"classifier", "composition"}); err != nil {
		return fmt.Errorf("identity_versions: %w", err)
	}
	if err := namedTypes(
		value,
		nil,
		[]string{"projections"},
		nil,
		nil,
		append([]string{"canonical_encoding"}, optionalNames(value, "classifier", "composition")...),
	); err != nil {
		return fmt.Errorf("identity_versions: %w", err)
	}
	for domain, raw := range mustObject(value, "projections") {
		number, ok := raw.(float64)
		if !ok || number < 0 || number > 1<<53-1 || number != float64(uint64(number)) {
			return fmt.Errorf("identity_versions.projections.%s must be a non-negative safe integer", domain)
		}
	}
	return nil
}

func validateContributors(values []any) error {
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok {
			return errors.New("contributors must contain objects")
		}
		if err := fields(entry, []string{"movement_id", "change_set_id"}, nil); err != nil {
			return err
		}
		if err := namedTypes(entry, []string{"movement_id", "change_set_id"}, nil, nil, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func validateCriterionOutcomes(values []any) error {
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok {
			return errors.New("criterion_outcomes must contain objects")
		}
		if err := fields(entry, []string{"criterion_id", "criterion_spec_hash", "outcome"}, nil); err != nil {
			return fmt.Errorf("criterion_outcomes: %w", err)
		}
		if err := namedTypes(
			entry,
			[]string{"criterion_id", "criterion_spec_hash", "outcome"},
			nil,
			nil,
			nil,
			nil,
		); err != nil {
			return fmt.Errorf("criterion_outcomes: %w", err)
		}
		if mustString(entry, "outcome") != "PASS" {
			return errors.New("criterion_outcomes must contain only PASS")
		}
	}
	return nil
}

func validateTypedDelta(values []any) error {
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok {
			return errors.New("typed_delta must contain objects")
		}
		if err := fields(entry, []string{"selector", "operation"}, []string{"before_hash", "after_hash"}); err != nil {
			return fmt.Errorf("typed_delta: %w", err)
		}
		names := append([]string{"selector", "operation"}, optionalNames(entry, "before_hash", "after_hash")...)
		if err := namedTypes(entry, names, nil, nil, nil, nil); err != nil {
			return fmt.Errorf("typed_delta: %w", err)
		}
	}
	return nil
}

func validateDecisionRequest(payload map[string]any) error {
	switch mustString(payload, "decision_type") {
	case "question":
		return fields(payload, []string{"decision_id", "decision_type", "question"}, []string{"emitted_id"})
	case "human_gate":
		if err := fields(payload, []string{"decision_id", "decision_type", "gate_id", "gate_mode", "subject_tree", "blocking_findings"}, []string{"review_outcome"}); err != nil {
			return err
		}
		if gateMode := mustString(payload, "gate_mode"); gateMode != "always" && gateMode != "on_contested" {
			return errors.New("invalid gate_mode")
		}
		if reviewOutcome, ok := payload["review_outcome"].(string); ok && reviewOutcome != "CLEAN" && reviewOutcome != "CONTESTED" {
			return errors.New("invalid review_outcome")
		}
		return validateFindingPairs(payload["blocking_findings"].([]any))
	case "amendment":
		return fields(payload, []string{"decision_id", "decision_type", "proposal_id", "routed_reason", "blocking"}, []string{"emitted_id"})
	case "finalization":
		if err := fields(payload, []string{"decision_id", "decision_type", "proposal_id", "routed_reason"}, []string{"emitted_id"}); err != nil {
			return err
		}
		if mustString(payload, "routed_reason") != "draft_phase" {
			return errors.New("finalization routed_reason must be draft_phase")
		}
		return nil
	default:
		return errors.New("invalid decision_type")
	}
}

func validateDecisionResolution(payload map[string]any) error {
	switch mustString(payload, "decision_type") {
	case "question":
		if mustString(payload, "disposition") != "answered" {
			return errors.New("question disposition must be answered")
		}
		return fields(payload, []string{"decision_id", "decision_type", "disposition", "answer"}, nil)
	case "human_gate":
		if disposition := mustString(payload, "disposition"); disposition != "approved" && disposition != "rejected" {
			return errors.New("invalid human_gate disposition")
		}
		if err := fields(payload, []string{"decision_id", "decision_type", "disposition", "gate_id", "scope", "overridden_findings"}, []string{"override_reason", "reason"}); err != nil {
			return err
		}
		scope := mustObject(payload, "scope")
		if err := fields(scope, []string{"subject_tree"}, nil); err != nil {
			return fmt.Errorf("scope: %w", err)
		}
		if _, ok := scope["subject_tree"].(string); !ok {
			return errors.New("scope.subject_tree must be a string")
		}
		if err := validateFindingPairs(payload["overridden_findings"].([]any)); err != nil {
			return err
		}
		if mustString(payload, "disposition") == "rejected" && len(payload["overridden_findings"].([]any)) != 0 {
			return errors.New("rejected human gate cannot override findings")
		}
		_, overrideReason := payload["override_reason"]
		if (len(payload["overridden_findings"].([]any)) != 0) != overrideReason || (overrideReason && mustString(payload, "override_reason") == "") {
			return errors.New("override_reason is required and non-empty iff findings are overridden")
		}
		if reason, present := payload["reason"]; present {
			if mustString(payload, "disposition") != "rejected" {
				return errors.New("reason is only valid for a rejected human gate")
			}
			if _, ok := reason.(string); !ok || mustString(payload, "reason") == "" {
				return errors.New("reason must be a non-empty string")
			}
		}
		return nil
	default:
		return errors.New("invalid decision_type")
	}
}

func validateFindingPairs(values []any) error {
	previous := ""
	for _, raw := range values {
		pair, ok := raw.(map[string]any)
		if !ok {
			return errors.New("finding pairs must contain objects")
		}
		if err := fields(pair, []string{"artifact_instance_id", "finding_id"}, nil); err != nil {
			return err
		}
		if err := namedTypes(pair, []string{"artifact_instance_id", "finding_id"}, nil, nil, nil, nil); err != nil {
			return err
		}
		key := mustString(pair, "artifact_instance_id") + "\x00" + mustString(pair, "finding_id")
		if key <= previous {
			return errors.New("finding pairs must be sorted and unique")
		}
		previous = key
	}
	return nil
}

func validateEvaluationReview(payload map[string]any) error {
	outcome, present := payload["review_outcome"]
	blockers, blockersPresent := payload["blocking_findings"]
	if present != blockersPresent {
		return errors.New("review_outcome and blocking_findings must be present together")
	}
	if !present {
		return nil
	}
	if outcome != "CLEAN" && outcome != "CONTESTED" {
		return errors.New("invalid review_outcome")
	}
	values, ok := blockers.([]any)
	if !ok {
		return errors.New("blocking_findings must be an array")
	}
	if err := validateFindingPairs(values); err != nil {
		return err
	}
	if (outcome == "CONTESTED") != (len(values) != 0) {
		return errors.New("review_outcome does not match blocking_findings")
	}
	return nil
}

func findingReferences(values []any) []FindingReference {
	result := make([]FindingReference, len(values))
	for index, value := range values {
		pair := value.(map[string]any)
		result[index] = FindingReference{
			ArtifactInstanceID: mustString(pair, "artifact_instance_id"),
			FindingID:          mustString(pair, "finding_id"),
		}
	}
	return result
}

func containsFindingReference(references []FindingReference, candidate FindingReference) bool {
	for _, reference := range references {
		if reference.ArtifactInstanceID == candidate.ArtifactInstanceID && reference.FindingID == candidate.FindingID {
			return true
		}
	}
	return false
}

func validateEnvelopeEvaluation(value map[string]any) error {
	if err := fields(value, []string{"guard_passed"}, []string{"class", "guard_failure_reason"}); err != nil {
		return err
	}
	if err := namedTypes(value, optionalNames(value, "class", "guard_failure_reason"), nil, nil, []string{"guard_passed"}, nil); err != nil {
		return err
	}
	if class, ok := value["class"].(string); ok && !validEnvelopeClass(class) {
		return errors.New("invalid envelope_evaluation class")
	}
	_, failureReason := value["guard_failure_reason"]
	if mustBool(value, "guard_passed") == failureReason {
		return errors.New("guard_failure_reason presence must match guard_passed")
	}
	return nil
}

func headMovements(values []any) ([]HeadMovement, error) {
	if len(values) == 0 {
		return nil, errors.New("head_movements must not be empty")
	}
	head := make([]HeadMovement, 0, len(values))
	seen := make(map[MovementID]bool, len(values))
	for _, raw := range values {
		value, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("head_movements entries must be objects")
		}
		if err := fields(value, []string{"id", "initial", "repo_write", "has_dependencies", "final"}, nil); err != nil {
			return nil, fmt.Errorf("head_movements: %w", err)
		}
		if err := namedTypes(value, []string{"id", "initial"}, nil, nil, []string{"repo_write", "has_dependencies", "final"}, nil); err != nil {
			return nil, fmt.Errorf("head_movements: %w", err)
		}
		movement := HeadMovement{
			ID:              MovementID(mustString(value, "id")),
			Initial:         MovementState(mustString(value, "initial")),
			RepoWrite:       mustBool(value, "repo_write"),
			HasDependencies: mustBool(value, "has_dependencies"),
			Final:           mustBool(value, "final"),
		}
		if movement.ID == "" || seen[movement.ID] {
			return nil, errors.New("head_movements ids must be non-empty and unique")
		}
		if movement.Initial != MovementPending && movement.Initial != MovementInapplicable {
			return nil, errors.New("head_movements initial must be PENDING or INAPPLICABLE")
		}
		seen[movement.ID] = true
		head = append(head, movement)
	}
	return head, nil
}

func reconciledHead(state State, head []HeadMovement) (map[MovementID]MovementState, []MovementID, map[MovementID]bool, map[MovementID]bool, map[MovementID]bool, error) {
	movements := make(map[MovementID]MovementState, len(head))
	order := make([]MovementID, 0, len(head))
	repoWrite := make(map[MovementID]bool)
	dependencies := make(map[MovementID]bool)
	final := make(map[MovementID]bool)
	for _, movement := range head {
		if current, retained := state.Movements[movement.ID]; retained {
			movements[movement.ID] = current
		} else {
			movements[movement.ID] = movement.Initial
		}
		order = append(order, movement.ID)
		if movement.RepoWrite {
			repoWrite[movement.ID] = true
		}
		if movement.HasDependencies {
			dependencies[movement.ID] = true
		}
		if movement.Final {
			final[movement.ID] = true
		}
	}
	for id, current := range state.Movements {
		if _, retained := movements[id]; !retained && current == MovementSucceeded {
			return nil, nil, nil, nil, nil, fmt.Errorf("head_movements removes succeeded movement %q", id)
		}
	}
	return movements, order, repoWrite, dependencies, final, nil
}

func validateActualImpact(value map[string]any) error {
	if err := fields(value, []string{"score_changes", "authority", "budget"}, nil); err != nil {
		return fmt.Errorf("actual_impact: %w", err)
	}
	if err := namedTypes(value, nil, []string{"authority", "budget"}, []string{"score_changes"}, nil, nil); err != nil {
		return fmt.Errorf("actual_impact: %w", err)
	}
	if err := validateTypedDelta(value["score_changes"].([]any)); err != nil {
		return fmt.Errorf("actual_impact.score_changes: %w", err)
	}
	authority := mustObject(value, "authority")
	if err := fields(authority, []string{"allowed_paths", "grants", "side_effects"}, nil); err != nil {
		return fmt.Errorf("actual_impact.authority: %w", err)
	}
	for _, name := range []string{"allowed_paths", "side_effects"} {
		change := mustObject(authority, name)
		if err := fields(change, []string{"added", "removed"}, nil); err != nil {
			return fmt.Errorf("actual_impact.authority.%s: %w", name, err)
		}
		if err := namedTypes(change, nil, nil, []string{"added", "removed"}, nil, nil); err != nil {
			return fmt.Errorf("actual_impact.authority.%s: %w", name, err)
		}
		if err := stringArray(change, "added"); err != nil {
			return err
		}
		if err := stringArray(change, "removed"); err != nil {
			return err
		}
	}
	grants, ok := authority["grants"].([]any)
	if !ok {
		return errors.New("actual_impact.authority.grants must be an array")
	}
	for _, raw := range grants {
		entry, ok := raw.(map[string]any)
		if !ok {
			return errors.New("actual_impact.authority.grants must contain objects")
		}
		if err := fields(entry, []string{"movement_id", "added", "removed"}, nil); err != nil {
			return err
		}
		if err := namedTypes(entry, []string{"movement_id"}, nil, []string{"added", "removed"}, nil, nil); err != nil {
			return err
		}
		if err := stringArray(entry, "added"); err != nil {
			return err
		}
		if err := stringArray(entry, "removed"); err != nil {
			return err
		}
	}
	budget := mustObject(value, "budget")
	if err := fields(budget, nil, []string{"active_wall_clock_min", "retries_per_movement"}); err != nil {
		return fmt.Errorf("actual_impact.budget: %w", err)
	}
	for _, name := range optionalNames(budget, "active_wall_clock_min", "retries_per_movement") {
		change := mustObject(budget, name)
		if err := fields(change, []string{"from", "to"}, nil); err != nil {
			return fmt.Errorf("actual_impact.budget.%s: %w", name, err)
		}
		if err := namedTypes(change, nil, nil, nil, nil, []string{"from", "to"}); err != nil {
			return fmt.Errorf("actual_impact.budget.%s: %w", name, err)
		}
	}
	return nil
}

func validatePayloadTypes(eventType EventType, payload map[string]any) error {
	var strings, objects, arrays, bools, integers []string
	switch eventType {
	case EventRunStarted:
		strings = []string{"base_commit", "base_tree", "score_hash", "score_file_hash", "resolved_cast_hash"}
		objects = []string{"identity_versions"}
	case EventRunSucceeded:
		objects = []string{"candidate", "waiver", "identity_versions"}
	case EventRunFailed:
		strings = []string{"reason"}
	case EventRunCancelled:
		arrays = []string{"cancelled_movement_ids", "cancelled_attempt_ids", "obsoleted_decision_ids"}
		integers = optionalNames(payload, "fenced_epoch")
	case EventMovementFailed:
		strings = append([]string{"reason"}, optionalNames(payload, "decision_id", "subject_tree")...)
		bools = []string{"run_failed"}
	case EventMovementSucceeded:
		strings = optionalNames(payload, "approved_change_set_id")
		arrays = []string{"approved_artifact_instance_ids"}
		objects = []string{"identity_versions"}
		bools = []string{"run_succeeded"}
	case EventPerformerSelected:
		strings = []string{"reason", "performer_id", "adapter_id", "model"}
	case EventAdapterProbed:
		strings = []string{"adapter_version", "execution_dependency_hash"}
		objects = []string{"capabilities", "enforcement", "identity_versions"}
		arrays = []string{"negotiated_features", "truncated_resolutions", "advisory_dimensions"}
	case EventAttemptStarted:
		strings = optionalNames(payload, "base_composition_hash")
		objects = append([]string{"adapter_process", "granted_authority", "identity_versions"}, optionalNames(payload, "review_subject_input")...)
		integers = []string{"attempt_number"}
	case EventPerformerCompleted:
		bools = []string{"session_hint_stored"}
	case EventAttemptBlocked:
		arrays = []string{"raised", "pending_decision_ids"}
	case EventAttemptFailed:
		strings = append([]string{"kind"}, optionalNames(payload, "reason", "detail")...)
		objects = []string{"disposition"}
	case EventArtifactRecorded:
		strings = []string{"logical_output_id", "kind", "content_hash", "source_path"}
		integers = []string{"size_bytes"}
	case EventChangeSetRecorded:
		strings = []string{"change_set_id", "base_tree", "result_tree", "commit", "ref"}
		objects = []string{"identity_versions"}
	case EventApplicationCandidateRecorded:
		strings = []string{"candidate_id", "base_tree", "result_tree", "candidate_composition_dependency_hash"}
		arrays = []string{"ordered_change_sets", "contributors"}
		objects = []string{"identity_versions"}
	case EventAcceptanceStarted:
		strings = []string{"subject_tree", "acceptance_spec_hash"}
		arrays = []string{"planned_criterion_ids"}
		objects = []string{"identity_versions"}
	case EventCriterionStarted:
		strings = []string{"criterion_id", "criterion_spec_hash", "subject_tree"}
		objects = append([]string{"identity_versions"}, optionalNames(payload, "criterion_process")...)
		bools = optionalNames(payload, "spawn_failed")
	case EventCriterionCompleted:
		strings = append([]string{"criterion_id", "criterion_spec_hash", "subject_tree", "outcome"}, optionalNames(payload, "output_ref", "error_detail")...)
		objects = []string{"identity_versions"}
		arrays = optionalNames(payload, "truncated_streams")
		integers = optionalNames(payload, "exit_code", "duration_ms")
	case EventAcceptanceFailed:
		strings = append([]string{"reason", "subject_tree"}, optionalNames(payload, "failed_criterion_id")...)
		objects = []string{"disposition"}
	case EventAcceptanceEvaluationCompleted:
		strings = []string{"subject_tree", "acceptance_spec_hash"}
		strings = append(strings, optionalNames(payload, "review_outcome")...)
		arrays = append([]string{"criterion_outcomes"}, optionalNames(payload, "blocking_findings")...)
		objects = []string{"identity_versions"}
	case EventDecisionRequested:
		strings = append([]string{"decision_id", "decision_type"}, optionalNames(payload, "emitted_id", "question", "gate_id", "gate_mode", "subject_tree", "review_outcome", "proposal_id", "routed_reason")...)
		arrays = optionalNames(payload, "blocking_findings")
		bools = optionalNames(payload, "blocking")
	case EventDecisionResolved:
		strings = append([]string{"decision_id", "decision_type", "disposition"}, optionalNames(payload, "answer", "gate_id", "override_reason", "reason")...)
		objects = optionalNames(payload, "scope")
		arrays = optionalNames(payload, "overridden_findings")
	case EventDecisionObsoleted:
		strings = []string{"decision_id"}
	case EventAmendmentRejected:
		strings = append([]string{"proposal_id", "reason", "base_hash"}, optionalNames(payload, "emitted_id", "condition", "patch_operations_hash", "error_location", "decision_id")...)
		arrays = optionalNames(payload, "typed_delta")
		objects = append([]string{"identity_versions"}, optionalNames(payload, "actual_impact")...)
		integers = []string{"base_revision", "classifier_version"}
	case EventExecutionStarted:
		strings = []string{"interval_id", "phase", "wall_start"}
		integers = []string{"remaining_at_start"}
	case EventExecutionStopped:
		strings = append([]string{"interval_id", "reason", "charging"}, optionalNames(payload, "observed_at")...)
		integers = []string{"charged_duration"}
	case EventAmendmentApprovalPrepared:
		strings = append([]string{"prepare_id", "proposal_id", "mode", "base_hash", "new_snapshot_hash", "new_snapshot_file_hash", "plan_record_hash", "quiesce_deadline"}, optionalNames(payload, "decision_id", "envelope_class")...)
		arrays = []string{"target_attempt_ids"}
		objects = []string{"identity_versions"}
		integers = []string{"base_revision", "new_revision", "observed_authority_epoch", "classifier_version"}
	case EventAmendmentApprovalAbandoned:
		strings = []string{"prepare_id", "proposal_id", "reason", "base_hash"}
		integers = []string{"base_revision", "classifier_version"}
	case EventAmendmentRoutedHuman:
		strings = append([]string{"proposal_id", "reason", "decision_type", "proposal_record_hash", "base_hash", "decision_id"}, optionalNames(payload, "emitted_id")...)
		arrays = []string{"typed_delta"}
		objects = append([]string{"actual_impact", "identity_versions"}, optionalNames(payload, "envelope_evaluation")...)
		bools = []string{"blocking"}
		integers = []string{"base_revision", "classifier_version"}
	case EventAmendmentApproved:
		strings = append([]string{"proposal_id", "mode", "base_hash", "new_snapshot_hash", "new_snapshot_file_hash"}, optionalNames(payload, "emitted_id", "decision_id", "envelope_class", "candidate_id")...)
		arrays = []string{"typed_delta", "head_movements", "superseded_attempt_ids", "obsoleted_decision_ids"}
		objects = append([]string{"actual_impact", "identity_versions"}, optionalNames(payload, "envelope_evaluation")...)
		bools = []string{"finalization"}
		integers = append([]string{"base_revision", "classifier_version", "new_revision"}, optionalNames(payload, "fenced_epoch")...)
	case EventAuthorityGranted:
		objects = []string{"owner_start_identity"}
		integers = append([]string{"authority_epoch", "owner_pid"}, optionalNames(payload, "reclaimed_from_epoch")...)
	case EventCancelRequested:
		strings = []string{"requested_by"}
	case EventAmendmentHumanRejected:
		strings = []string{"proposal_id", "decision_id", "human_reason", "base_hash"}
		objects = []string{"identity_versions"}
		integers = []string{"base_revision", "classifier_version"}
	case EventCompositionConflicted:
		strings = []string{"scope", "target_id", "composition_subject_hash", "composition_algorithm_version"}
		arrays = []string{"contributors", "conflicted_paths"}
		objects = []string{"identity_versions"}
	case EventCompositionFailed:
		strings = []string{"scope", "target_id", "composition_subject_hash", "cause", "diagnostic", "composition_algorithm_version"}
		arrays = []string{"contributors"}
		objects = []string{"identity_versions"}
		integers = optionalNames(payload, "git_exit_code")
	case EventApplyStarted:
		strings = []string{"txn_id", "candidate_id", "before_tree", "result_tree"}
		arrays = []string{"touched_paths"}
		objects = []string{"recovery", "identity_versions"}
	case EventApplyCompleted:
		strings = []string{"txn_id", "candidate_id", "result_tree"}
		objects = []string{"identity_versions"}
	case EventApplyFailed:
		strings = []string{"txn_id", "candidate_id", "failure_detail"}
		objects = []string{"identity_versions"}
		bools = []string{"rollback_verified"}
	case EventApplyRecoveryRequired:
		strings = append([]string{"txn_id", "candidate_id", "failure_detail"}, optionalNames(payload, "observed_tree")...)
		objects = []string{"identity_versions"}
	case EventApplyRecoveryResolved:
		strings = []string{"txn_id", "candidate_id", "outcome"}
		objects = []string{"identity_versions"}
	case EventScorePromotionStarted:
		strings = []string{"txn_id", "candidate_id", "expected_root_file_hash", "target_snapshot_file_hash"}
		objects = []string{"identity_versions"}
		integers = []string{"target_revision"}
	case EventScorePromoted:
		strings = []string{"txn_id", "candidate_id", "target_snapshot_file_hash"}
		objects = []string{"identity_versions"}
		integers = []string{"target_revision"}
	case EventScorePromotionRecoveryRequired:
		strings = append([]string{"txn_id", "candidate_id", "failure_detail"}, optionalNames(payload, "observed_root_file_hash")...)
		objects = []string{"identity_versions"}
	case EventJournalTailTruncated:
		integers = []string{"truncated_seq", "discarded_bytes"}
	case EventLog:
		strings = []string{"level", "message"}
	case EventProgress:
		strings = []string{"message"}
	}
	if err := namedTypes(payload, strings, objects, arrays, bools, integers); err != nil {
		return err
	}
	for _, name := range arrays {
		if name == "typed_delta" || name == "head_movements" || name == "contributors" || name == "criterion_outcomes" || name == "raised" || name == "blocking_findings" || name == "overridden_findings" {
			continue
		}
		if err := stringArray(payload, name); err != nil {
			return err
		}
	}
	if dispositionValue, ok := payload["disposition"].(map[string]any); ok {
		if err := fields(dispositionValue, []string{"charged", "movement_terminal"}, []string{"terminal_reason"}); err != nil {
			return fmt.Errorf("disposition: %w", err)
		}
		if _, ok := dispositionValue["charged"].(string); !ok {
			return errors.New("disposition.charged must be a string")
		}
		if _, ok := dispositionValue["movement_terminal"].(bool); !ok {
			return errors.New("disposition.movement_terminal must be a boolean")
		}
		if value, present := dispositionValue["terminal_reason"]; present {
			if _, ok := value.(string); !ok {
				return errors.New("disposition.terminal_reason must be a string")
			}
		}
		charged := mustString(dispositionValue, "charged")
		if charged != "quality_retry" && charged != "fallback" && charged != "none" {
			return errors.New("disposition.charged is invalid")
		}
	}
	return nil
}

func optionalNames(value map[string]any, names ...string) []string {
	var present []string
	for _, name := range names {
		if _, ok := value[name]; ok {
			present = append(present, name)
		}
	}
	return present
}

func namedTypes(value map[string]any, strings, objects, arrays, bools, integers []string) error {
	for _, name := range strings {
		if _, ok := value[name].(string); !ok {
			return fmt.Errorf("%s must be a string", name)
		}
	}
	for _, name := range objects {
		if _, ok := value[name].(map[string]any); !ok {
			return fmt.Errorf("%s must be an object", name)
		}
	}
	for _, name := range arrays {
		if _, ok := value[name].([]any); !ok {
			return fmt.Errorf("%s must be an array", name)
		}
	}
	for _, name := range bools {
		if _, ok := value[name].(bool); !ok {
			return fmt.Errorf("%s must be a boolean", name)
		}
	}
	for _, name := range integers {
		number, ok := value[name].(float64)
		if !ok || number < 0 || number > 1<<53-1 || number != float64(uint64(number)) {
			return fmt.Errorf("%s must be a non-negative safe integer", name)
		}
	}
	return nil
}

func stringArray(value map[string]any, name string) error {
	for _, item := range value[name].([]any) {
		if _, ok := item.(string); !ok {
			return fmt.Errorf("%s must contain only strings", name)
		}
	}
	return nil
}

func validatePayloadValues(eventType EventType, payload map[string]any) error {
	switch eventType {
	case EventRunCancelled:
		return sortedStringFields(payload, "cancelled_movement_ids", "cancelled_attempt_ids", "obsoleted_decision_ids")
	case EventMovementSucceeded:
		return sortedStringFields(payload, "approved_artifact_instance_ids")
	case EventMovementFailed:
		reason := mustString(payload, "reason")
		_, decisionID := payload["decision_id"]
		_, subjectTree := payload["subject_tree"]
		if (reason == "human_gate_rejected") != (decisionID && subjectTree) {
			return errors.New("decision_id and subject_tree are required iff reason is human_gate_rejected")
		}
		if !validMovementFailureReason(reason) {
			return errors.New("invalid movement failure reason")
		}
	case EventAttemptBlocked:
		return validatePendingDecisionIDs(payload)
	case EventDecisionRequested:
		return nil
	case EventDecisionResolved:
		return nil
	case EventAmendmentRejected:
		reason := mustString(payload, "reason")
		if !validAmendmentRejectionReason(reason) {
			return errors.New("invalid amendment rejection reason")
		}
		_, condition := payload["condition"]
		if (reason == "candidate_incompatible") != condition {
			return errors.New("condition is required iff reason is candidate_incompatible")
		}
		if condition && !validCandidateIncompatibleCondition(mustString(payload, "condition")) {
			return errors.New("invalid candidate_incompatible condition")
		}
		_, typedDelta := payload["typed_delta"]
		_, actualImpact := payload["actual_impact"]
		_, patchHash := payload["patch_operations_hash"]
		_, location := payload["error_location"]
		if typedDelta != actualImpact || patchHash != location || typedDelta == patchHash {
			return errors.New("amendment rejection must carry exactly one diagnostic form")
		}
	case EventAmendmentRoutedHuman:
		if !validAmendmentRouteReason(mustString(payload, "reason")) {
			return errors.New("invalid amendment routing reason")
		}
		decisionType := mustString(payload, "decision_type")
		if decisionType != "amendment" && decisionType != "finalization" {
			return errors.New("invalid amendment routing decision_type")
		}
		if decisionType == "finalization" && (!mustBool(payload, "blocking") || mustString(payload, "reason") != "draft_phase") {
			return errors.New("finalization routing must be blocking draft_phase")
		}
	case EventAmendmentHumanRejected:
		if mustString(payload, "human_reason") == "" {
			return errors.New("human_reason must be non-empty")
		}
	case EventCompositionConflicted:
		if err := sortedStringFields(payload, "conflicted_paths"); err != nil {
			return err
		}
		if !validCompositionScope(mustString(payload, "scope")) {
			return errors.New("invalid composition scope")
		}
	case EventCompositionFailed:
		if !validCompositionScope(mustString(payload, "scope")) {
			return errors.New("invalid composition scope")
		}
		cause := mustString(payload, "cause")
		if !validCompositionFailureCause(cause) {
			return errors.New("invalid composition failure cause")
		}
		_, exitCode := payload["git_exit_code"]
		if (cause == "git_exit" || cause == "output_unusable") != exitCode {
			return errors.New("git_exit_code is required iff the merge exited")
		}
		if len(mustString(payload, "diagnostic")) > 4096 {
			return errors.New("diagnostic exceeds 4 KiB")
		}
	case EventPerformerSelected:
		switch mustString(payload, "reason") {
		case "initial", "quality_retry", "fallback", "revision_restart", "decision_resume":
			return nil
		default:
			return errors.New("invalid performer selection reason")
		}
	case EventAdapterProbed:
		return sortedStringFields(
			payload,
			"negotiated_features",
			"truncated_resolutions",
			"advisory_dimensions",
		)
	case EventAmendmentApprovalPrepared:
		switch mustString(payload, "mode") {
		case "auto":
			if hasField(payload, "decision_id") || !validEnvelopeClass(mustString(payload, "envelope_class")) {
				return errors.New("auto prepare requires envelope_class and forbids decision_id")
			}
		case "human":
			if hasField(payload, "envelope_class") || mustString(payload, "decision_id") == "" {
				return errors.New("human prepare requires decision_id and forbids envelope_class")
			}
		default:
			return errors.New("invalid amendment prepare mode")
		}
		return sortedStringFields(payload, "target_attempt_ids")
	case EventAmendmentApproved:
		switch mustString(payload, "mode") {
		case "auto":
			if hasField(payload, "decision_id") || hasField(payload, "envelope_evaluation") ||
				!validEnvelopeClass(mustString(payload, "envelope_class")) {
				return errors.New("auto approval requires envelope_class and forbids decision_id and envelope_evaluation")
			}
		case "human":
			if hasField(payload, "envelope_class") ||
				mustString(payload, "decision_id") == "" {
				return errors.New("human approval requires decision_id and forbids envelope_class")
			}
			if !hasField(payload, "envelope_evaluation") {
				return errors.New("human approval requires envelope_evaluation")
			}
		default:
			return errors.New("invalid amendment approval mode")
		}
		if mustBool(payload, "finalization") {
			return errors.New("finalization is outside the supported projector")
		}
		return sortedStringFields(payload, "superseded_attempt_ids", "obsoleted_decision_ids")
	case EventExecutionStarted:
		if mustInt(payload, "remaining_at_start") < 0 {
			return errors.New("remaining_at_start must be non-negative")
		}
	case EventExecutionStopped:
		if mustInt(payload, "charged_duration") < 0 {
			return errors.New("charged_duration must be non-negative")
		}
		charging := mustString(payload, "charging")
		_, observed := payload["observed_at"]
		if (charging == "clamped") != observed {
			return errors.New("observed_at is required iff charging is clamped")
		}
	case EventCriterionCompleted:
		outcome := mustString(payload, "outcome")
		if outcome != "PASS" && outcome != "FAIL" && outcome != "ERROR" {
			return errors.New("invalid criterion outcome")
		}
		_, detail := payload["error_detail"]
		if (outcome == "ERROR") != detail {
			return errors.New("error_detail is required iff outcome is ERROR")
		}
		if streams, present := payload["truncated_streams"]; present {
			values, ok := streams.([]any)
			if !ok || len(values) == 0 {
				return errors.New("truncated_streams must be a non-empty array")
			}
			previous := ""
			for _, raw := range values {
				stream, ok := raw.(string)
				if !ok || (stream != "stdout" && stream != "stderr") || stream <= previous {
					return errors.New("truncated_streams must be sorted unique stdout/stderr")
				}
				previous = stream
			}
		}
	case EventCancelRequested:
		if mustString(payload, "requested_by") != "cli" {
			return errors.New("requested_by must be cli")
		}
	case EventApplyFailed:
		if !mustBool(payload, "rollback_verified") {
			return errors.New("rollback_verified must be true")
		}
	case EventApplyRecoveryResolved:
		if mustString(payload, "outcome") != "rolled_back" {
			return errors.New("invalid apply recovery outcome")
		}
	}
	return nil
}

func validateRaisedDecisions(values []any) error {
	seen := make(map[string]bool, len(values))
	for index, raw := range values {
		decision, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("raised[%d] must be an object", index)
		}
		kind, ok := decision["kind"].(string)
		if !ok {
			return fmt.Errorf("raised[%d].kind must be a string", index)
		}
		var required []string
		switch kind {
		case "question":
			required = []string{"decision_id", "emitted_id", "kind", "question", "blocking"}
		case "proposal":
			required = []string{"decision_id", "emitted_id", "kind", "proposal_id", "blocking"}
		default:
			return fmt.Errorf("raised[%d].kind is invalid", index)
		}
		if err := fields(decision, required, nil); err != nil {
			return fmt.Errorf("raised[%d]: %w", index, err)
		}
		strings := []string{"decision_id", "emitted_id"}
		if kind == "question" {
			strings = append(strings, "question")
		} else {
			strings = append(strings, "proposal_id")
		}
		if err := namedTypes(decision, strings, nil, nil, []string{"blocking"}, nil); err != nil {
			return fmt.Errorf("raised[%d]: %w", index, err)
		}
		decisionID := mustString(decision, "decision_id")
		if seen[decisionID] {
			return errors.New("raised decision_id must be unique")
		}
		seen[decisionID] = true
	}
	return nil
}

func validatePendingDecisionIDs(payload map[string]any) error {
	if err := sortedStringFields(payload, "pending_decision_ids"); err != nil {
		return err
	}
	var expected []string
	for _, raw := range payload["raised"].([]any) {
		decision := raw.(map[string]any)
		if mustBool(decision, "blocking") {
			expected = append(expected, mustString(decision, "decision_id"))
		}
	}
	slices.Sort(expected)
	if !slices.Equal(mustStrings(payload, "pending_decision_ids"), expected) {
		return errors.New("pending_decision_ids must equal blocking raised decision ids")
	}
	return nil
}

func validMovementFailureReason(reason string) bool {
	switch reason {
	case "retries_exhausted", "fallbacks_exhausted", "budget_exhausted", "human_gate_rejected",
		"grant_denied", "protocol_error", "composition_unresolvable", "composition_failed":
		return true
	default:
		return false
	}
}

func validAmendmentRejectionReason(reason string) bool {
	switch reason {
	case "run_terminal", "run_cancelling", "stale", "patch_error", "invalid_score", "reserved_field", "no_op", "claim_narrower", "executed_dependency_changed", "candidate_incompatible":
		return true
	default:
		return false
	}
}

func validCandidateIncompatibleCondition(condition string) bool {
	switch condition {
	case "succeeded_dependency_changed", "composition_changed", "verification_episode_finished", "verification_mode_changed":
		return true
	default:
		return false
	}
}

func validAmendmentRouteReason(reason string) bool {
	switch reason {
	case "draft_phase", "auto_disabled", "unclassified_change", "recognized_non_monotone", "runtime_scope_started":
		return true
	default:
		return false
	}
}

func validCompositionScope(scope string) bool {
	return scope == "movement" || scope == "candidate"
}

func validCompositionFailureCause(cause string) bool {
	switch cause {
	case "repository_unusable", "inspection_failed", "driver_rejected", "spawn_failed", "git_exit", "git_signalled", "output_unusable", "status_unobtainable":
		return true
	default:
		return false
	}
}

func fields(value map[string]any, required, optional []string) error {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
		if _, ok := value[name]; !ok {
			return fmt.Errorf("required field %q is absent", name)
		}
	}
	for _, name := range optional {
		allowed[name] = true
	}
	for name := range value {
		if !allowed[name] {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}

func hasField(value map[string]any, name string) bool {
	_, present := value[name]
	return present
}

func sortedStringFields(payload map[string]any, names ...string) error {
	for _, name := range names {
		values := mustStrings(payload, name)
		if !slices.IsSorted(values) {
			return fmt.Errorf("%s must be sorted", name)
		}
		for index := 1; index < len(values); index++ {
			if values[index] == values[index-1] {
				return fmt.Errorf("%s must not contain duplicates", name)
			}
		}
	}
	return nil
}

func mustObject(value map[string]any, name string) map[string]any {
	object, _ := value[name].(map[string]any)
	return object
}

func mustString(value map[string]any, name string) string {
	result, _ := value[name].(string)
	return result
}

func stringOrEmpty(value map[string]any, name string) string {
	result, _ := value[name].(string)
	return result
}

func mustStrings(value map[string]any, name string) []string {
	raw, _ := value[name].([]any)
	result := make([]string, len(raw))
	for index, item := range raw {
		result[index], _ = item.(string)
	}
	return result
}

func mustBool(value map[string]any, name string) bool {
	result, _ := value[name].(bool)
	return result
}

func mustInt(value map[string]any, name string) int64 {
	number, _ := value[name].(float64)
	return int64(number)
}

func mustUint(value map[string]any, name string) uint64 {
	return uint64(mustInt(value, name))
}

func optionalUint(value map[string]any, name string) (uint64, bool) {
	raw, ok := value[name]
	if !ok {
		return 0, false
	}
	number, _ := raw.(float64)
	return uint64(number), true
}

func optionalStringPointer(value map[string]any, name string) *string {
	raw, present := value[name]
	if !present {
		return nil
	}
	result, _ := raw.(string)
	return &result
}

func validEnvelopeClass(value string) bool {
	return value == "NARROW_PATHS" || value == "NARROW_GRANTS" || value == "BUDGET_DECREASE"
}

var registryEvents = map[EventType]bool{
	"run.started": true, "run.succeeded": true, "run.failed": true, "run.cancelled": true,
	"movement.ready": true, "movement.started": true, "movement.succeeded": true, "movement.failed": true,
	"movement.cancelled": true, "performer.selected": true, "attempt.started": true,
	"adapter.probed": true, "performer.completed": true, "attempt.completed": true, "attempt.blocked": true,
	"attempt.failed": true, "attempt.cancelled": true, "attempt.superseded": true,
	"execution.started": true, "execution.stopped": true, "artifact.recorded": true,
	"change_set.recorded": true, "verification.passed": true, "composition.conflicted": true, "composition.failed": true,
	"application_candidate.recorded": true, "acceptance.started": true, "criterion.started": true,
	"criterion.completed": true, "acceptance.failed": true, "acceptance.evaluation_completed": true,
	"decision.requested": true, "decision.resolved": true, "decision.obsoleted": true,
	"amendment.rejected": true, "amendment.approval_prepared": true,
	"amendment.approval_abandoned": true, "amendment.routed_human": true,
	"amendment.approved": true, "amendment.human_rejected": true,
	"apply.started": true, "apply.completed": true, "apply.failed": true,
	"apply.recovery_required": true, "apply.recovery_resolved": true,
	"score.promotion_started": true, "score.promoted": true,
	"score.promotion_recovery_required": true, "authority.granted": true,
	"cancel.requested": true, "journal.tail_truncated": true, "log": true, "progress": true,
}

func isRegistryEvent(eventType EventType) bool {
	return registryEvents[eventType]
}

func isSupportedEvent(eventType EventType) bool {
	if eventType == EventMovementCancelled || eventType == EventAttemptCancelled || eventType == EventAttemptSuperseded || eventType == EventDecisionObsoleted {
		return false
	}
	_, _, known := payloadFields(eventType)
	return known
}
