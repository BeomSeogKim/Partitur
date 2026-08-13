// Package recoveryconsequence owns the durable recovery consequences that
// must be shared by recovery and amendment preparation.
package recoveryconsequence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/successor"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

var (
	ErrAuthorityRequired     = errors.New("recovery consequence requires established driver authority")
	ErrUnrecognizedCase      = errors.New("recovery consequence case is not in the catalog")
	ErrInvalidAction         = errors.New("recovery consequence action does not match its catalog case")
	ErrMissingProposalRecord = errors.New("recovery proposal record is missing or does not match its route descriptor")
	ErrReplan                = errors.New("recovery action must replan before mutation")
)

// HandlerContext gives a consequence handler the replayed facts and the
// run-owned authority needed to append its one durable effect.
type HandlerContext struct {
	Store  *runstore.Store
	Driver *runstore.Driver
	RunID  runstate.RunID
	Input  recovery.Input

	// AfterCompositionEvidence is a deterministic interleave seam for tests.
	// Production callers leave it nil.
	AfterCompositionEvidence func()
}

type handler func(context.Context, HandlerContext, recovery.Action) error

type entry struct {
	kind  recovery.ActionKind
	apply handler
	steps map[recovery.ActionStep]handler
}

var catalog = map[recovery.CaseID]entry{
	recovery.CaseCompositionTerminal: {
		kind: recovery.ActionAppendCompositionTerminal, apply: AppendCompositionTerminal,
	},
	recovery.CaseRealizeDisposition: {
		kind: recovery.ActionRealizeRecordedDisposition, apply: RealizeRecordedDisposition,
	},
	recovery.CaseAppendQuestionRequest: {
		kind: recovery.ActionAppendQuestionRequest, apply: AppendQuestionRequest,
	},
	recovery.CaseBlockedProposalRoute: {
		kind: recovery.ActionAppendBlockedProposalRoute, apply: AppendBlockedProposalRoute,
	},
	recovery.CaseRoutedAmendment: {
		kind: recovery.ActionAppendRoutedRequest, apply: AppendRoutedRequest,
	},
	recovery.CaseMovementSucceeded: {
		kind: recovery.ActionAppendMovementSucceeded, apply: AppendMovementSucceeded,
	},
	recovery.CaseRunFailed: {
		kind: recovery.ActionAppendRunFailed, apply: AppendRunFailed,
	},
	recovery.CaseFinalGateRejected: {
		kind: recovery.ActionAppendFinalGateFailure, apply: AppendGateRejectedFailure,
	},
	recovery.CaseAcceptanceFailed: {
		kind: recovery.ActionRealizeRecordedDisposition, apply: RealizeRecordedDisposition,
	},
	recovery.CaseCriterionFailed: {
		kind: recovery.ActionAppendAcceptanceFailure, apply: AppendAcceptanceFailure,
	},
	recovery.CaseCriteriaPassed: {
		kind: recovery.ActionAppendEvaluationCompleted, apply: AppendAcceptanceEvaluationCompleted,
	},
	recovery.CaseRequestHumanGate: {
		kind: recovery.ActionAppendHumanGateRequest, apply: AppendHumanGateRequest,
	},
	recovery.CaseHumanGateApproved: {
		kind: recovery.ActionAppendAcceptanceSuccess, apply: appendAcceptanceSuccess,
		steps: map[recovery.ActionStep]handler{
			recovery.StepAppendAttemptCompleted:  AppendAttemptCompleted,
			recovery.StepAppendMovementSucceeded: AppendMovementSucceeded,
		},
	},
	recovery.CaseHumanGateRejected: {
		kind: recovery.ActionAppendGateRejectedFailure, apply: AppendGateRejectedFailure,
	},
	recovery.CaseGateFreeCompletion: {
		kind: recovery.ActionAppendAcceptanceSuccess, apply: appendAcceptanceSuccess,
		steps: map[recovery.ActionStep]handler{
			recovery.StepAppendAttemptCompleted:  AppendAttemptCompleted,
			recovery.StepAppendMovementSucceeded: AppendMovementSucceeded,
		},
	},
}

// Handles reports whether caseID has a durable-consequence handler today.
func Handles(caseID recovery.CaseID) bool {
	_, ok := catalog[caseID]
	return ok
}

// HandlesStep reports whether the catalog owns this exact step route.
func HandlesStep(caseID recovery.CaseID, kind recovery.ActionKind, step recovery.ActionStep) bool {
	entry := catalog[caseID]
	return entry.kind == kind && entry.steps[step] != nil
}

// Cases returns the catalog's implemented case IDs.
func Cases() []recovery.CaseID {
	cases := make([]recovery.CaseID, 0, len(catalog))
	for caseID := range catalog {
		cases = append(cases, caseID)
	}
	slices.Sort(cases)
	return cases
}

// Apply executes the one consequence selected for caseID. It refuses cases
// absent from the catalog so a caller cannot manufacture a recovery effect.
func Apply(ctx context.Context, execution HandlerContext, caseID recovery.CaseID, action recovery.Action) error {
	entry, ok := catalog[caseID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnrecognizedCase, caseID)
	}
	if action.Kind != entry.kind {
		return fmt.Errorf("%w: %s has %s, want %s", ErrInvalidAction, caseID, action.Kind, entry.kind)
	}
	return entry.apply(ctx, execution, action)
}

// ApplyStep executes one order-sensitive substep of a catalogued consequence.
// Recovery uses this form so its reload boundary remains between the two
// lifecycle appends; amendment preparation can use Apply for the same case.
func ApplyStep(ctx context.Context, execution HandlerContext, caseID recovery.CaseID, action recovery.Action, step recovery.ActionStep) error {
	entry, ok := catalog[caseID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnrecognizedCase, caseID)
	}
	if action.Kind != entry.kind {
		return fmt.Errorf("%w: %s has %s, want %s", ErrInvalidAction, caseID, action.Kind, entry.kind)
	}
	handler, ok := entry.steps[step]
	if !ok {
		return fmt.Errorf("%w: %s step %s", ErrInvalidAction, caseID, step)
	}
	return handler(ctx, execution, action)
}

func appendAcceptanceSuccess(ctx context.Context, execution HandlerContext, action recovery.Action) error {
	if err := AppendAttemptCompleted(ctx, execution, action); err != nil {
		return err
	}
	return AppendMovementSucceeded(ctx, execution, action)
}

func AppendCompositionTerminal(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.CompositionTerminal == nil {
		return errors.New("recovery composition terminal requires store, driver, and evidence")
	}
	terminal := action.CompositionTerminal
	journal, err := execution.Store.ReadJournal(execution.Driver.RunID())
	if err != nil {
		return err
	}
	cause, err := LatestEventID(journal.Events, func(event runstate.Event) bool {
		return (event.Type == runstate.EventCompositionConflicted || event.Type == runstate.EventCompositionFailed) &&
			event.EventID == terminal.EvidenceEventID && event.ScoreRevision == terminal.ScoreRevision &&
			payloadString(event.Payload, "scope") == terminal.Scope && payloadString(event.Payload, "target_id") == terminal.TargetID
	})
	if err != nil {
		return err
	}
	terminalPoint, err := compositionTerminalPoint(terminal.Scope)
	if err != nil {
		return err
	}
	err = execution.Driver.Mutate(func(transaction *runstore.Txn, state runstate.State) error {
		if state.CancelRequested || terminal.ScoreRevision != state.ScoreHead.Revision {
			return ErrReplan
		}
		if execution.AfterCompositionEvidence != nil {
			execution.AfterCompositionEvidence()
		}
		var event runstate.Event
		var address faultpoint.ReceiptAddress
		switch terminal.Scope {
		case "movement":
			event = runstate.Event{RunID: execution.Driver.RunID(), ScoreRevision: state.ScoreHead.Revision, MovementID: runstate.MovementID(terminal.TargetID), Type: runstate.EventMovementFailed, CausationID: cause, Payload: RecoveryPayload(map[string]any{"reason": terminal.Reason, "run_failed": false})}
			address = "recovery.movement.failed.composition"
		case "candidate":
			event = runstate.Event{RunID: execution.Driver.RunID(), ScoreRevision: state.ScoreHead.Revision, Type: runstate.EventRunFailed, CausationID: cause, Payload: RecoveryPayload(map[string]any{"reason": terminal.Reason})}
			address = "recovery.run.failed.composition"
		default:
			return fmt.Errorf("recovery composition terminal has invalid scope %q", terminal.Scope)
		}
		if _, err := runstate.Apply(state, event); err != nil {
			return err
		}
		_, err := transaction.At(address).Append(event)
		return err
	})
	if err != nil {
		return err
	}
	execution.Store.Reached(terminalPoint)
	return nil
}

func compositionTerminalPoint(scope string) (faultpoint.PointID, error) {
	switch scope {
	case "movement":
		return faultpoint.PointCompositionMovementTerminal, nil
	case "candidate":
		return faultpoint.PointCompositionCandidateTerminal, nil
	default:
		return "", fmt.Errorf("recovery composition terminal has invalid scope %q", scope)
	}
}

// RecoveryPayload encodes a recovery-owned journal payload.
func RecoveryPayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func AppendAcceptanceEvaluationCompleted(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.AttemptID == "" {
		return errors.New("recovery acceptance completion requires store, driver, and attempt")
	}
	input, err := execution.Store.LoadRunInput(execution.RunID)
	if err != nil {
		return err
	}
	attempt, ok := input.Projection.State.Attempts[action.AttemptID]
	if !ok {
		return fmt.Errorf("recovery acceptance completion attempt %q is absent", action.AttemptID)
	}
	acceptanceState, ok := input.Projection.State.Acceptances[action.AttemptID]
	if !ok || !acceptanceState.Started {
		return fmt.Errorf("recovery acceptance completion for %q is absent", action.AttemptID)
	}
	for _, movement := range input.Score.Movements() {
		if runstate.MovementID(movement.ID) != attempt.MovementID {
			continue
		}
		plan, err := acceptance.Compile(movement)
		if err != nil {
			return err
		}
		outcomes := make([]acceptance.CriterionOutcome, 0, len(acceptanceState.PlannedCriterionIDs))
		for _, criterionID := range acceptanceState.PlannedCriterionIDs {
			criterion, ok := acceptanceState.Criteria[criterionID]
			if !ok || !criterion.Completed || criterion.Outcome != "PASS" {
				return fmt.Errorf("recovery acceptance criterion %q is not PASS", criterionID)
			}
			outcomes = append(outcomes, acceptance.CriterionOutcome{CriterionID: string(criterionID), Outcome: criterion.Outcome})
		}
		_, err = acceptance.CompleteStarted(plan, acceptance.Evaluation{
			RunID: execution.Driver.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision,
			MovementID: attempt.MovementID, PartID: movement.PartID, AttemptID: action.AttemptID,
			SubjectTree: acceptanceState.SubjectTree,
			LookupArtifact: func(id runstate.ArtifactInstanceID) (runstate.ArtifactRecord, bool, error) {
				state, err := execution.Driver.State()
				if err != nil {
					return runstate.ArtifactRecord{}, false, err
				}
				record, exists := state.Artifacts[id]
				return record, exists, nil
			},
			Append: func(event runstate.Event) (faultpoint.DurabilityReceipt, error) {
				return execution.Driver.Append(event, faultpoint.ReceiptAddress("recovery.acceptance."+string(event.Type)))
			},
		}, outcomes)
		return err
	}
	return fmt.Errorf("recovery acceptance completion movement %q is absent from pinned score", attempt.MovementID)
}

func AppendHumanGateRequest(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.AttemptID == "" {
		return errors.New("recovery human gate request requires store, driver, and attempt")
	}
	input, err := execution.Store.LoadRunInput(execution.RunID)
	if err != nil {
		return err
	}
	attempt, ok := input.Projection.State.Attempts[action.AttemptID]
	if !ok {
		return fmt.Errorf("recovery human gate attempt %q is absent", action.AttemptID)
	}
	var gateMode string
	for _, movement := range input.Score.Movements() {
		if runstate.MovementID(movement.ID) == attempt.MovementID {
			gateMode = movement.Acceptance.HumanGate
			break
		}
	}
	if gateMode != "always" && gateMode != "on_contested" {
		return fmt.Errorf("invalid human_gate %q", gateMode)
	}
	gateID, err := humanGateID(action.AttemptID)
	if err != nil {
		return err
	}
	decisionID, err := workspace.NewID()
	if err != nil {
		return fmt.Errorf("allocate human gate decision id: %w", err)
	}
	blocking := make([]any, len(action.BlockingFindings))
	for index, finding := range action.BlockingFindings {
		blocking[index] = map[string]any{"artifact_instance_id": finding.ArtifactInstanceID, "finding_id": finding.FindingID}
	}
	payload := map[string]any{"decision_id": decisionID, "decision_type": "human_gate", "gate_id": gateID, "gate_mode": gateMode, "subject_tree": action.SubjectTree, "blocking_findings": blocking}
	if action.ReviewOutcome != "" {
		payload["review_outcome"] = action.ReviewOutcome
	}
	err = AppendEvent(execution, input.Projection.State, action, runstate.EventDecisionRequested, payload)
	if err == nil {
		execution.Store.Reached(faultpoint.PointHumanGateDecisionRequested)
	}
	return err
}

func humanGateID(attemptID runstate.AttemptID) (string, error) {
	encoded, err := canonical.Encode(map[string]any{"attempt_id": string(attemptID)})
	if err != nil {
		return "", fmt.Errorf("encode human gate id: %w", err)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("partitur/gate-id"))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(encoded)
	return fmt.Sprintf("gat-%x", digest.Sum(nil)), nil
}

func AppendGateRejectedFailure(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	movementID, err := ActionMovement(state, action)
	if err != nil {
		return err
	}
	action.FailureReason = "human_gate_rejected"
	return AppendEvent(execution, state, WithMovement(action, movementID), runstate.EventMovementFailed, map[string]any{"reason": "human_gate_rejected", "decision_id": action.QuestionDecisionID, "subject_tree": action.SubjectTree, "run_failed": state.FinalMovements[movementID]})
}

func RealizeRecordedDisposition(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if action.RecordedDisposition == nil {
		return errors.New("recovery recorded disposition is absent")
	}
	switch action.RecordedDisposition.Charged {
	case "none":
		if action.RecordedDisposition.TerminalReason == "" {
			return errors.New("recovery terminal disposition has no reason")
		}
		state, err := execution.Driver.State()
		if err != nil {
			return err
		}
		failure := action
		failure.FailureReason = action.RecordedDisposition.TerminalReason
		return AppendEvent(execution, state, failure, runstate.EventMovementFailed, map[string]any{"reason": failure.FailureReason, "run_failed": false})
	case "quality_retry", "fallback":
		if action.PendingSuccessor == nil {
			return errors.New("recovery recorded successor is absent")
		}
		return nil
	default:
		return fmt.Errorf("recovery recorded disposition has unknown charge %q", action.RecordedDisposition.Charged)
	}
}

func AppendQuestionRequest(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.AttemptID == "" || action.QuestionDecisionID == "" {
		return errors.New("recovery question request requires store, driver, attempt, and decision")
	}
	journal, err := execution.Store.ReadJournal(execution.RunID)
	if err != nil {
		return err
	}
	for _, source := range journal.Events {
		if source.Type != runstate.EventAttemptBlocked || source.AttemptID != action.AttemptID {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(source.Payload, &payload); err != nil {
			return err
		}
		raisedValues, ok := payload["raised"].([]any)
		if !ok {
			return fmt.Errorf("blocked attempt %q has invalid raised decisions", action.AttemptID)
		}
		for _, raw := range raisedValues {
			raised, ok := raw.(map[string]any)
			kind, _ := raised["kind"].(string)
			decisionID, _ := raised["decision_id"].(string)
			if !ok || kind != "question" || decisionID != action.QuestionDecisionID {
				continue
			}
			emittedID, _ := raised["emitted_id"].(string)
			question, _ := raised["question"].(string)
			event := runstate.Event{RunID: execution.RunID, ScoreRevision: source.ScoreRevision, MovementID: source.MovementID, PartID: source.PartID, AttemptID: source.AttemptID, Type: runstate.EventDecisionRequested, Payload: RecoveryPayload(map[string]any{"decision_id": action.QuestionDecisionID, "decision_type": "question", "emitted_id": emittedID, "question": question})}
			_, err := execution.Driver.Append(event, "recovery.decision.requested.question")
			return err
		}
	}
	return fmt.Errorf("recovery question %q is absent from blocked attempt %q", action.QuestionDecisionID, action.AttemptID)
}

// AppendBlockedProposalRoute completes the frozen source-to-route cut for a
// blocking proposal. It reads no current amendment input and reconstructs no
// policy decision: attempt.blocked and its hash-bound immutable record are the
// complete authority for this append.
func AppendBlockedProposalRoute(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.BlockedProposalRoute == nil {
		return errors.New("recovery blocked proposal route requires store, driver, and route")
	}
	route := action.BlockedProposalRoute
	journal, err := execution.Store.ReadJournal(execution.RunID)
	if err != nil {
		return err
	}
	for _, source := range journal.Events {
		if source.Type != runstate.EventAttemptBlocked || source.AttemptID != route.AttemptID || source.ScoreRevision != route.ScoreRevision {
			continue
		}
		var blocked map[string]any
		if err := json.Unmarshal(source.Payload, &blocked); err != nil {
			return err
		}
		for _, raw := range blocked["raised"].([]any) {
			raised, ok := raw.(map[string]any)
			if !ok || raised["kind"] != "proposal" || raised["proposal_id"] != string(route.ProposalID) || raised["blocking"] != true {
				continue
			}
			descriptor, ok := raised["route"].(map[string]any)
			if !ok {
				continue
			}
			payload, err := frozenRoutePayload(execution, source, raised, descriptor)
			if err != nil {
				return err
			}
			event := runstate.Event{
				RunID: execution.RunID, ScoreRevision: source.ScoreRevision, MovementID: source.MovementID,
				PartID: source.PartID, AttemptID: source.AttemptID, Type: runstate.EventAmendmentRoutedHuman,
				Payload: RecoveryPayload(payload),
			}
			_, err = execution.Driver.Append(event, "recovery.amendment.routed_human")
			return err
		}
	}
	return fmt.Errorf("recovery blocked proposal route %q is absent from attempt %q", route.ProposalID, route.AttemptID)
}

// AppendRoutedRequest completes the separate route-to-request cut. The
// routed_human event is the only request source; attempt.blocked is not read.
func AppendRoutedRequest(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.RoutedProposalID == "" {
		return errors.New("recovery routed request requires store, driver, and proposal")
	}
	journal, err := execution.Store.ReadJournal(execution.RunID)
	if err != nil {
		return err
	}
	for _, source := range journal.Events {
		if source.Type != runstate.EventAmendmentRoutedHuman {
			continue
		}
		var routed map[string]any
		if err := json.Unmarshal(source.Payload, &routed); err != nil {
			return err
		}
		if routed["proposal_id"] != string(action.RoutedProposalID) {
			continue
		}
		payload := map[string]any{
			"decision_id":   routed["decision_id"],
			"decision_type": routed["decision_type"],
			"proposal_id":   routed["proposal_id"],
			"routed_reason": routed["reason"],
			"blocking":      routed["blocking"],
		}
		address := faultpoint.ReceiptAddress("recovery.decision.requested.amendment")
		if routed["decision_type"] == "finalization" {
			delete(payload, "blocking")
			address = faultpoint.ReceiptAddress("recovery.decision.requested.finalization")
		}
		if emittedID, ok := routed["emitted_id"]; ok {
			payload["emitted_id"] = emittedID
		}
		event := runstate.Event{
			RunID: execution.RunID, ScoreRevision: source.ScoreRevision, MovementID: source.MovementID,
			PartID: source.PartID, AttemptID: source.AttemptID, Type: runstate.EventDecisionRequested,
			Payload: RecoveryPayload(payload),
		}
		_, err = execution.Driver.Append(event, address)
		return err
	}
	return fmt.Errorf("recovery routed amendment %q is absent", action.RoutedProposalID)
}

func frozenRoutePayload(execution HandlerContext, source runstate.Event, raised, descriptor map[string]any) (map[string]any, error) {
	proposalID, _ := raised["proposal_id"].(string)
	recordHash, _ := descriptor["proposal_record_hash"].(string)
	if proposalID == "" || filepath.Base(proposalID) != proposalID || recordHash == "" {
		return nil, ErrMissingProposalRecord
	}
	record, err := os.ReadFile(filepath.Join(execution.Store.RepositoryRoot(), ".partitur", "runs", string(execution.RunID), "proposals", proposalID+".json"))
	if err != nil || rawHash(record) != recordHash {
		return nil, ErrMissingProposalRecord
	}
	var immutable struct {
		ProposalID       string `json:"proposal_id"`
		AttemptID        string `json:"attempt_id"`
		EmittedID        string `json:"emitted_id"`
		RequiresDecision bool   `json:"requires_decision"`
	}
	if err := json.Unmarshal(record, &immutable); err != nil || immutable.ProposalID != proposalID || immutable.AttemptID != string(source.AttemptID) || immutable.EmittedID == "" || !immutable.RequiresDecision {
		return nil, ErrMissingProposalRecord
	}
	payload := make(map[string]any, len(descriptor)+4)
	for key, value := range descriptor {
		payload[key] = value
	}
	payload["proposal_id"] = proposalID
	payload["emitted_id"] = immutable.EmittedID
	payload["decision_id"] = raised["decision_id"]
	payload["blocking"] = true
	return payload, nil
}

func rawHash(value []byte) string {
	sum := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", sum)
}

func AppendAcceptanceFailure(_ context.Context, execution HandlerContext, action recovery.Action) error {
	disposition, err := Classify(execution.Input, action, successor.FailureCase{AcceptanceReason: action.FailureReason})
	if err != nil {
		return err
	}
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	acceptanceState, ok := state.Acceptances[action.AttemptID]
	if !ok {
		return fmt.Errorf("acceptance for %q is absent", action.AttemptID)
	}
	payload := map[string]any{"reason": action.FailureReason, "subject_tree": acceptanceState.SubjectTree, "disposition": DispositionPayload(disposition)}
	if action.CriterionID != "" && action.FailureReason != "recovery_subject_mismatch" {
		payload["failed_criterion_id"] = action.CriterionID
	}
	return AppendEvent(execution, state, action, runstate.EventAcceptanceFailed, payload)
}

func AppendAttemptCompleted(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	return AppendEvent(execution, state, action, runstate.EventAttemptCompleted, map[string]any{})
}

func AppendMovementSucceeded(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	movementID, err := ActionMovement(state, action)
	if err != nil {
		return err
	}
	artifactIDs := make([]string, 0)
	for id, artifact := range state.Artifacts {
		if artifact.AttemptID == action.AttemptID {
			artifactIDs = append(artifactIDs, string(id))
		}
	}
	slices.Sort(artifactIDs)
	domains := []canonical.Domain(nil)
	payload := map[string]any{"approved_artifact_instance_ids": artifactIDs, "run_succeeded": state.FinalMovements[movementID]}
	if state.RepoWriteMovements[movementID] {
		changeSet, ok := state.ChangeSets[action.AttemptID]
		if !ok {
			return fmt.Errorf("change set for repo-write attempt %q is absent", action.AttemptID)
		}
		payload["approved_change_set_id"] = changeSet.ChangeSetID
		domains = append(domains, canonical.DomainChangeSet)
	}
	versions, err := IdentityVersions(domains...)
	if err != nil {
		return err
	}
	payload["identity_versions"] = versions
	return AppendEvent(execution, state, WithMovement(action, movementID), runstate.EventMovementSucceeded, payload)
}

func AppendRunFailed(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	reason := action.FailureReason
	if reason == "" {
		reason = "movement_failed"
	}
	if err := AppendEvent(execution, state, action, runstate.EventRunFailed, map[string]any{"reason": reason}); err != nil {
		return err
	}
	execution.Store.Reached(faultpoint.PointLifecycleRunFailed)
	return nil
}

// Classify realizes a disposition from the planner's replayed facts only.
func Classify(input recovery.Input, action recovery.Action, failure successor.FailureCase) (runstate.Disposition, error) {
	attempt := input.Projection.CurrentHeadAttempt
	if attempt == nil || attempt.AttemptID != action.AttemptID {
		return runstate.Disposition{}, errors.New("recovery classification facts do not match selected attempt")
	}
	facts := attempt.FailureClassification
	visited := make(map[string]bool, len(facts.VisitedPerformers))
	for _, performer := range facts.VisitedPerformers {
		visited[performer] = true
	}
	visited[facts.CurrentPerformer] = true
	hasUnvisitedFallback := false
	for _, performer := range facts.Fallbacks {
		if !visited[performer] {
			hasUnvisitedFallback = true
			break
		}
	}
	return successor.Classify(successor.ClassificationInput{Failure: failure, HasUnvisitedFallback: hasUnvisitedFallback, RetriesConsumed: facts.RetriesConsumed, RetriesPerMovement: facts.RetriesPerMovement, RemainingTimeMS: facts.RemainingTimeMS})
}

// AppendEvent appends a consequence with its source-authority causation.
func AppendEvent(execution HandlerContext, state runstate.State, action recovery.Action, eventType runstate.EventType, payload any) error {
	if execution.Driver == nil {
		return ErrAuthorityRequired
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	event := runstate.Event{RunID: execution.Driver.RunID(), ScoreRevision: state.ScoreHead.Revision, AttemptID: action.AttemptID, Type: eventType, Payload: encoded}
	if eventType != runstate.EventRunFailed && eventType != runstate.EventExecutionStopped {
		movementID, err := ActionMovement(state, action)
		if err != nil {
			return err
		}
		event.MovementID = movementID
	} else {
		event.AttemptID = ""
	}
	causationID, err := sourceAuthority(execution, state, action, eventType)
	if err != nil {
		return err
	}
	event.CausationID = causationID
	_, err = execution.Driver.Append(event, faultpoint.ReceiptAddress("recovery."+string(eventType)))
	return err
}

func sourceAuthority(execution HandlerContext, state runstate.State, action recovery.Action, eventType runstate.EventType) (string, error) {
	if execution.Store == nil || execution.Driver == nil {
		return "", errors.New("recovery executor requires store access for causation")
	}
	journal, err := execution.Store.ReadJournal(execution.Driver.RunID())
	if err != nil {
		return "", err
	}
	match := func(event runstate.Event) bool { return event.AttemptID == action.AttemptID }
	switch eventType {
	case runstate.EventDecisionRequested:
		return LatestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventAcceptanceEvaluationCompleted && match(event)
		})
	case runstate.EventExecutionStopped:
		if state.OpenExecution == nil {
			return "", errors.New("recovered interval source is absent")
		}
		return LatestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventExecutionStarted && payloadString(event.Payload, "interval_id") == string(state.OpenExecution.ID)
		})
	case runstate.EventAttemptFailed:
		source := runstate.EventPerformerSelected
		switch action.FailureReason {
		case "probe_terminated_incomplete":
			source = runstate.EventAttemptStarted
		case "attempt_terminated_incomplete":
			source = runstate.EventAdapterProbed
		case "worktree_lost", "draft_no_blocking_output":
			source = runstate.EventPerformerCompleted
		}
		return LatestEventID(journal.Events, func(event runstate.Event) bool { return event.Type == source && match(event) })
	case runstate.EventCriterionCompleted:
		return LatestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventCriterionStarted && match(event) && payloadString(event.Payload, "criterion_id") == string(action.CriterionID)
		})
	case runstate.EventAcceptanceFailed:
		if action.FailureReason == "recovery_subject_mismatch" {
			return LatestEventID(journal.Events, func(event runstate.Event) bool { return event.Type == runstate.EventAcceptanceStarted && match(event) })
		}
		return LatestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventCriterionCompleted && match(event) && payloadString(event.Payload, "criterion_id") == string(action.CriterionID)
		})
	case runstate.EventAttemptCompleted:
		source := runstate.EventAcceptanceEvaluationCompleted
		if execution.Input.Projection.Acceptance != nil && execution.Input.Projection.Acceptance.Gate.Required {
			source = runstate.EventDecisionResolved
		}
		return LatestEventID(journal.Events, func(event runstate.Event) bool { return event.Type == source && match(event) })
	case runstate.EventMovementSucceeded:
		return LatestEventID(journal.Events, func(event runstate.Event) bool { return event.Type == runstate.EventAttemptCompleted && match(event) })
	case runstate.EventMovementFailed:
		if action.FailureReason == "human_gate_rejected" {
			return LatestEventID(journal.Events, func(event runstate.Event) bool {
				return event.Type == runstate.EventDecisionResolved && match(event) && payloadString(event.Payload, "decision_id") == action.QuestionDecisionID
			})
		}
		if action.FailureReason == "budget_exhausted" {
			return LatestEventID(journal.Events, func(event runstate.Event) bool {
				return event.Type == runstate.EventExecutionStopped && payloadString(event.Payload, "reason") == "budget_exhausted"
			})
		}
		if source, err := LatestEventID(journal.Events, func(event runstate.Event) bool { return event.Type == runstate.EventAttemptFailed && match(event) }); err == nil {
			return source, nil
		}
		movementID, err := ActionMovement(state, action)
		if err != nil {
			return "", err
		}
		return LatestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventMovementStarted && event.MovementID == movementID
		})
	case runstate.EventRunFailed:
		if action.FailureReason == "budget_exhausted" {
			return LatestEventID(journal.Events, func(event runstate.Event) bool {
				return event.Type == runstate.EventExecutionStopped && payloadString(event.Payload, "reason") == "budget_exhausted"
			})
		}
		if action.MovementID != "" || action.AttemptID != "" {
			movementID, err := ActionMovement(state, action)
			if err != nil {
				return "", err
			}
			return LatestEventID(journal.Events, func(event runstate.Event) bool {
				return event.Type == runstate.EventMovementFailed && event.MovementID == movementID
			})
		}
		return LatestEventID(journal.Events, func(event runstate.Event) bool { return event.Type == runstate.EventMovementFailed })
	default:
		return "", fmt.Errorf("no recovery causation source for %s", eventType)
	}
}

// LatestEventID returns the latest journal event that matches the authority predicate.
func LatestEventID(events []runstate.Event, matches func(runstate.Event) bool) (string, error) {
	for index := len(events) - 1; index >= 0; index-- {
		if matches(events[index]) {
			return events[index].EventID, nil
		}
	}
	return "", errors.New("recovery source authority is absent")
}

func payloadString(payload json.RawMessage, key string) string {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	result, _ := value[key].(string)
	return result
}

// ActionMovement finds the action's movement from explicit action data or its attempt.
func ActionMovement(state runstate.State, action recovery.Action) (runstate.MovementID, error) {
	if action.MovementID != "" {
		return action.MovementID, nil
	}
	if attempt, ok := state.Attempts[action.AttemptID]; ok {
		return attempt.MovementID, nil
	}
	return "", fmt.Errorf("movement for recovery action %s is absent", action.Kind)
}

// WithMovement returns action with its resolved movement identity.
func WithMovement(action recovery.Action, movementID runstate.MovementID) recovery.Action {
	action.MovementID = movementID
	return action
}

// DispositionPayload is the canonical journal representation of a recorded disposition.
func DispositionPayload(disposition runstate.Disposition) map[string]any {
	payload := map[string]any{"charged": disposition.Charged, "movement_terminal": disposition.MovementTerminal}
	if disposition.TerminalReason != "" {
		payload["terminal_reason"] = disposition.TerminalReason
	}
	return payload
}

// IdentityVersions returns the version payload for a consequence-owned event.
func IdentityVersions(domains ...canonical.Domain) (map[string]any, error) {
	projections := make(map[string]any, len(domains))
	for _, domain := range domains {
		versions, err := canonical.CurrentVersions(domain)
		if err != nil {
			return nil, err
		}
		projections[string(domain)] = versions.Projection
	}
	return map[string]any{"canonical_encoding": canonical.CanonicalEncodingVersion, "projections": projections}, nil
}
