package recoveryexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/cancellation"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/successor"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

const recoverySweepGrace = 30 * time.Second

// namedUnimplementedActionOwners is the single owner assignment for recovery
// actions whose implementation belongs to a later unit. The unit assignments
// are documentation whose authority is the project roadmap; no test validates
// their values, only their presence, uniqueness of bucket, and propagation
// into the refusal message.
var namedUnimplementedActionOwners = map[recovery.ActionKind]string{
	recovery.ActionCompleteOrAbandonPrepare: "4.2",
	recovery.ActionAppendRoutedRequest:      "4.2",
	recovery.ActionSelectRevisionRestart:    "4.2",
	recovery.ActionAppendQuestionRequest:    "4.1",
	// Temporary executor/planner mismatch, not a 4.1 handler requirement:
	// RC-RESUME-041 must hand decision_resume materialization to C.4.
	recovery.ActionSelectDecisionResume:       "4.1",
	recovery.ActionAppendFinalGateFailure:     "4.1",
	recovery.ActionAppendEvaluationCompleted:  "4.1",
	recovery.ActionAppendHumanGateRequest:     "4.1",
	recovery.ActionAppendGateRejectedFailure:  "4.1",
	recovery.ActionRecoverIncompleteCriterion: "3.2",
}

func defaultSteps() map[recovery.ActionStep]StepHandler {
	return map[recovery.ActionStep]StepHandler{
		recovery.StepStabilizeHandoff:            stabilizeHandoff,
		recovery.StepSweepRecordedSession:        sweepRecordedSession,
		recovery.StepCloseAdapterInterval:        closeAdapterInterval,
		recovery.StepClassifyAndAppendFailure:    appendAttemptFailure,
		recovery.StepSweepCriterionSession:       sweepCriterionSession,
		recovery.StepVerifyAcceptanceSubject:     verifyAcceptanceSubject,
		recovery.StepSynthesizeCriterionError:    synthesizeCriterionError,
		recovery.StepClassifyAcceptanceFailure:   appendAcceptanceFailure,
		recovery.StepAppendAttemptCompleted:      appendAttemptCompleted,
		recovery.StepAppendMovementSucceeded:     appendMovementSucceeded,
		recovery.StepAppendMovementBudgetFailure: appendMovementBudgetFailure,
		recovery.StepAppendRunFailed:             appendRunFailed,
	}
}

func defaultKinds() map[recovery.ActionKind]StepHandler {
	return map[recovery.ActionKind]StepHandler{
		recovery.ActionCloseOpenExecutionInterval: closeOpenExecutionInterval,
		recovery.ActionTerminalCleanup:            terminalCleanup,
		recovery.ActionRemoveStaleLease:           removeStaleLease,
		recovery.ActionQuarantineOrphanLease:      quarantineOrphanLease,
		recovery.ActionAppendMovementSucceeded:    appendMovementSucceeded,
		recovery.ActionAppendMovementReady:        appendMovementReady,
		recovery.ActionAppendMovementStarted:      appendMovementStarted,
		recovery.ActionAppendAcceptanceStarted:    appendAcceptanceStarted,
		recovery.ActionSelectInitialPerformer:     selectInitialPerformer,
		recovery.ActionResumeCriterion:            resumeCriterion,
		recovery.ActionRealizeRecordedDisposition: realizeRecordedDisposition,
		recovery.ActionMaterializeSuccessor:       materializeSuccessor,
		recovery.ActionAppendRunFailed:            appendRunFailed,
		recovery.ActionAppendBudgetFailure:        appendMovementBudgetFailure,
		recovery.ActionReturnWaitingHuman:         returnWaitingHuman,
		recovery.ActionRefuseResume:               refuseResume,
		recovery.ActionStabilizeUnjournaledLaunch: stabilizeUnjournaledLaunch,
		recovery.ActionRemoveUnjournaledLaunch:    removeUnjournaledLaunch,
		recovery.ActionComposeCandidate:           composeCandidate,
		recovery.ActionRerunPostHocVerification:   rerunPostHocVerification,
		recovery.ActionCaptureChangeSet:           captureChangeSet,
		recovery.ActionExecuteCancellation:        executeCancellation,
		recovery.ActionAppendCompositionTerminal:  appendCompositionTerminal,
		recovery.ActionRerunComposition:           rerunMovementComposition,
	}
}

func executeCancellation(ctx context.Context, execution HandlerContext, _ recovery.Action) error {
	if execution.Store == nil || execution.RunID == "" {
		return errors.New("recovery executor requires store and run id for cancellation")
	}
	return cancellation.Execute(ctx, execution.Store, execution.RunID)
}

func composeCandidate(_ context.Context, execution HandlerContext, _ recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || execution.RunID == "" {
		return errors.New("recovery executor requires store, driver, and run id for candidate composition")
	}
	input, err := execution.Store.LoadRunInput(execution.RunID)
	if err != nil {
		return err
	}
	err = driver.ComposeCandidate(execution.Store, execution.Driver, input, input.Projection.Scheduler.RemainingTime, time.Now, workspace.NewID)
	if errors.Is(err, driver.ErrCompositionTerminalized) {
		return nil
	}
	if errors.Is(err, driver.ErrCompositionCancelled) {
		return ErrRecoveryReplan
	}
	return err
}

func appendCompositionTerminal(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.CompositionTerminal == nil {
		return errors.New("recovery composition terminal requires store, driver, and evidence")
	}
	terminal := action.CompositionTerminal
	journal, err := execution.Store.ReadJournal(execution.Driver.RunID())
	if err != nil {
		return err
	}
	cause, err := latestEventID(journal.Events, func(event runstate.Event) bool {
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
		// C.1 cancellation is checked with the terminal append while the state
		// lock and the existing lease predicate are both held.
		if state.CancelRequested || terminal.ScoreRevision != state.ScoreHead.Revision {
			return ErrRecoveryReplan
		}
		if execution.afterCompositionEvidence != nil {
			execution.afterCompositionEvidence()
		}
		var event runstate.Event
		var address faultpoint.ReceiptAddress
		switch terminal.Scope {
		case "movement":
			event = runstate.Event{RunID: execution.Driver.RunID(), ScoreRevision: state.ScoreHead.Revision, MovementID: runstate.MovementID(terminal.TargetID), Type: runstate.EventMovementFailed, CausationID: cause, Payload: recoveryPayload(map[string]any{"reason": terminal.Reason, "run_failed": false})}
			address = "recovery.movement.failed.composition"
		case "candidate":
			event = runstate.Event{RunID: execution.Driver.RunID(), ScoreRevision: state.ScoreHead.Revision, Type: runstate.EventRunFailed, CausationID: cause, Payload: recoveryPayload(map[string]any{"reason": terminal.Reason})}
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

func recoveryPayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func rerunMovementComposition(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil {
		return errors.New("recovery composition rerun requires store and driver")
	}
	input, err := execution.Store.LoadRunInput(execution.RunID)
	if err != nil {
		return err
	}
	if action.MovementID == "" {
		err = driver.ComposeCandidate(execution.Store, execution.Driver, input, input.Projection.Scheduler.RemainingTime, time.Now, workspace.NewID)
		if errors.Is(err, driver.ErrCompositionTerminalized) {
			return nil
		}
		if errors.Is(err, driver.ErrCompositionCancelled) {
			return ErrRecoveryReplan
		}
		return err
	}
	_, err = driver.PrepareMovementBase(execution.Store, execution.Driver, input, action.MovementID, input.Projection.Scheduler.RemainingTime, time.Now, workspace.NewID)
	if errors.Is(err, driver.ErrCompositionTerminalized) {
		return nil
	}
	return err
}

func rerunPostHocVerification(ctx context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || execution.RunID == "" {
		return errors.New("recovery executor requires store, driver, and run id for post-hoc verification")
	}
	input, err := execution.Store.LoadRunInput(execution.RunID)
	if err != nil {
		return err
	}
	err = workspace.VerifyRecoveredPostHoc(execution.Store, execution.Driver, input, action.AttemptID)
	var verification *workspace.VerificationError
	if !errors.As(err, &verification) {
		return err
	}
	failure := action
	failure.FailureKind = successor.KindGrantDenied
	failure.FailureReason = verification.Reason
	return appendAttemptFailure(ctx, execution, failure)
}

func captureChangeSet(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || execution.RunID == "" || action.AttemptID == "" {
		return errors.New("recovery executor requires store, driver, run id, and attempt for change set capture")
	}
	input, err := execution.Store.LoadRunInput(execution.RunID)
	if err != nil {
		return err
	}
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	if _, recorded := state.ChangeSets[action.AttemptID]; recorded {
		return nil
	}
	changeSet, err := workspace.CaptureRecoveredChangeSet(
		execution.Store,
		execution.Driver,
		input,
		action.AttemptID,
	)
	if err != nil {
		return err
	}
	execution.Store.Reached(faultpoint.PointChangeSetCaptured)
	attempt, ok := state.Attempts[action.AttemptID]
	if !ok {
		return fmt.Errorf("recovery change set attempt %q is absent", action.AttemptID)
	}
	partID := ""
	for _, movement := range input.Score.Movements() {
		if movement.ID == string(attempt.MovementID) {
			partID = movement.PartID
			break
		}
	}
	event, err := workspace.ChangeSetRecordedEvent(
		execution.RunID,
		state.ScoreHead.Revision,
		attempt.MovementID,
		partID,
		action.AttemptID,
		changeSet,
	)
	if err != nil {
		return err
	}
	if _, err := execution.Driver.Append(event, faultpoint.ReceiptAddress("recovery.change_set.recorded")); err != nil {
		return err
	}
	execution.Store.Reached(faultpoint.PointChangeSetRecorded)
	return nil
}

func appendMovementReady(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Driver == nil || action.MovementID == "" {
		return errors.New("recovery movement ready requires driver and movement")
	}
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	_, err = execution.Driver.Append(runstate.Event{
		RunID:         execution.Driver.RunID(),
		ScoreRevision: state.ScoreHead.Revision,
		MovementID:    action.MovementID,
		Type:          runstate.EventMovementReady,
		Payload:       json.RawMessage(`{}`),
	}, "recovery.movement.ready")
	return err
}

func appendMovementStarted(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Driver == nil || action.MovementID == "" {
		return errors.New("recovery movement start requires driver and movement")
	}
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	_, err = execution.Driver.Append(runstate.Event{
		RunID:         execution.Driver.RunID(),
		ScoreRevision: state.ScoreHead.Revision,
		MovementID:    action.MovementID,
		Type:          runstate.EventMovementStarted,
		Payload:       json.RawMessage(`{}`),
	}, "recovery.movement.started")
	return err
}

func appendAcceptanceStarted(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.AttemptID == "" {
		return errors.New("recovery acceptance start requires store, driver, and attempt")
	}
	input, err := execution.Store.LoadRunInput(execution.RunID)
	if err != nil {
		return err
	}
	attempt, ok := input.Projection.State.Attempts[action.AttemptID]
	if !ok {
		return fmt.Errorf("recovery acceptance start attempt %q is absent", action.AttemptID)
	}
	for _, movement := range input.Score.Movements() {
		if runstate.MovementID(movement.ID) != attempt.MovementID {
			continue
		}
		plan, err := acceptance.Compile(movement)
		if err != nil {
			return err
		}
		subjectTree := input.BaseTree
		if candidate := input.Projection.State.ApplicationCandidate; candidate != nil {
			subjectTree = candidate.ResultTree
		}
		event, err := plan.StartEvent(runstate.Event{
			RunID:         execution.Driver.RunID(),
			ScoreRevision: input.Projection.State.ScoreHead.Revision,
			MovementID:    attempt.MovementID,
			PartID:        movement.PartID,
			AttemptID:     action.AttemptID,
		}, subjectTree)
		if err != nil {
			return err
		}
		journal, err := execution.Store.ReadJournal(execution.RunID)
		if err != nil {
			return err
		}
		event.CausationID, err = latestEventID(journal.Events, func(previous runstate.Event) bool {
			return previous.AttemptID == action.AttemptID && previous.Type == runstate.EventVerificationPassed
		})
		if err != nil {
			return err
		}
		_, err = execution.Driver.Append(event, "recovery.acceptance.started")
		return err
	}
	return fmt.Errorf("recovery acceptance start movement %q is absent from pinned score", attempt.MovementID)
}

func selectInitialPerformer(ctx context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.MovementID == "" {
		return errors.New("recovery initial performer selection requires store, driver, and movement")
	}
	input, err := execution.Store.LoadRunInput(execution.RunID)
	if err != nil {
		return err
	}
	var movementID string
	var partID string
	for _, movement := range input.Score.Movements() {
		if runstate.MovementID(movement.ID) == action.MovementID {
			movementID, partID = movement.ID, movement.PartID
			break
		}
	}
	if movementID == "" {
		return fmt.Errorf("recovery initial performer movement %q is absent from pinned score", action.MovementID)
	}
	binding, ok := input.Cast.Binding(partID)
	if !ok {
		return fmt.Errorf("recovery initial performer binding for %q is absent from resolved cast", partID)
	}
	performer, ok := input.Cast.Performer(binding.Performer)
	if !ok {
		return fmt.Errorf("recovery initial performer %q is absent from resolved cast", binding.Performer)
	}
	baseCommit := ""
	baseTree := input.BaseTree
	baseHash := ""
	for _, movement := range input.Score.Movements() {
		if movement.ID != movementID || len(movement.Needs) == 0 {
			continue
		}
		composed, err := driver.PrepareMovementBase(execution.Store, execution.Driver, input, action.MovementID, input.Projection.Scheduler.RemainingTime, time.Now, workspace.NewID)
		if err != nil {
			if errors.Is(err, driver.ErrCompositionTerminalized) {
				return errMovementCompositionTerminalized
			}
			return err
		}
		baseCommit, baseTree, baseHash = composed.Commit, composed.Tree, composed.Hash
		break
	}
	return executeRecoveredAttemptAtBase(ctx, execution, input, movementID, performer.ID, "initial", "", baseCommit, baseTree, baseHash)
}

func resumeCriterion(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.AttemptID == "" || action.CriterionID == "" {
		return errors.New("recovery criterion resume requires store, driver, attempt, and criterion")
	}
	input, err := execution.Store.LoadRunInput(execution.RunID)
	if err != nil {
		return err
	}
	attempt, ok := input.Projection.State.Attempts[action.AttemptID]
	if !ok {
		return fmt.Errorf("recovery criterion attempt %q is absent", action.AttemptID)
	}
	acceptanceState, ok := input.Projection.State.Acceptances[action.AttemptID]
	if !ok || !acceptanceState.Started {
		return fmt.Errorf("recovery criterion acceptance for %q is absent", action.AttemptID)
	}
	for _, movement := range input.Score.Movements() {
		if runstate.MovementID(movement.ID) != attempt.MovementID {
			continue
		}
		plan, err := acceptance.Compile(movement)
		if err != nil {
			return err
		}
		disposition, err := classify(
			recovery.Input{Projection: input.Projection},
			action,
			successor.FailureCase{AcceptanceReason: "acceptance_failed"},
		)
		if err != nil {
			return err
		}
		_, err = acceptance.EvaluateStarted(plan, acceptance.Evaluation{
			RunID: execution.Driver.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision,
			MovementID: attempt.MovementID, PartID: movement.PartID, AttemptID: action.AttemptID,
			SubjectTree:        acceptanceState.SubjectTree,
			FailureDisposition: disposition,
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
		})
		return err
	}
	return fmt.Errorf("recovery criterion movement %q is absent from pinned score", attempt.MovementID)
}

func realizeRecordedDisposition(_ context.Context, execution HandlerContext, action recovery.Action) error {
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
		return appendEvent(execution, state, failure, runstate.EventMovementFailed, map[string]any{
			"reason": failure.FailureReason, "run_failed": false,
		})
	case "quality_retry", "fallback":
		if action.PendingSuccessor == nil {
			return errors.New("recovery recorded successor is absent")
		}
		return nil
	default:
		return fmt.Errorf("recovery recorded disposition has unknown charge %q", action.RecordedDisposition.Charged)
	}
}

func materializeSuccessor(ctx context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.PendingSuccessor == nil {
		return errors.New("recovery successor materialization requires store, driver, and pending successor")
	}
	pending := action.PendingSuccessor
	input, err := execution.Store.LoadRunInput(execution.RunID)
	if err != nil {
		return err
	}
	var partID string
	for _, movement := range input.Score.Movements() {
		if runstate.MovementID(movement.ID) == pending.MovementID {
			partID = movement.PartID
			break
		}
	}
	if partID == "" {
		return fmt.Errorf("recovery successor movement %q is absent from pinned score", pending.MovementID)
	}
	performer, ok := input.Cast.Performer(pending.Performer)
	if !ok {
		return fmt.Errorf("recovery successor performer %q is absent from resolved cast", pending.Performer)
	}
	journal, err := execution.Store.ReadJournal(execution.RunID)
	if err != nil {
		return err
	}
	causationID, err := latestEventID(journal.Events, func(previous runstate.Event) bool {
		return previous.AttemptID == pending.AttemptID &&
			(previous.Type == runstate.EventAttemptFailed || previous.Type == runstate.EventAcceptanceFailed)
	})
	if err != nil {
		return err
	}
	return executeRecoveredAttempt(
		ctx, execution, input, string(pending.MovementID), performer.ID, pending.Reason, causationID,
	)
}

// executeRecoveredAttempt is deliberately only an input assembler. The
// adapter/probe/acceptance sequence remains in driver.ExecuteAttempt, shared
// with the live run wrapper.
func executeRecoveredAttempt(
	ctx context.Context,
	execution HandlerContext,
	input runstore.RunInput,
	movementID, performerID, reason, causationID string,
) error {
	return executeRecoveredAttemptAtBase(ctx, execution, input, movementID, performerID, reason, causationID, "", input.BaseTree, "")
}

func executeRecoveredAttemptAtBase(
	ctx context.Context,
	execution HandlerContext,
	input runstore.RunInput,
	movementID, performerID, reason, causationID, baseCommit, baseTree, baseCompositionHash string,
) error {
	if execution.Store == nil || execution.Driver == nil || input.Score == nil || input.Cast == nil {
		return errors.New("recovery attempt execution requires durable score, cast, store, and driver")
	}
	candidateTree := input.BaseTree
	if candidate := input.Projection.State.ApplicationCandidate; candidate != nil {
		candidateTree = candidate.ResultTree
	}
	attempt, err := workspace.CreateRecoveredAttemptAtBase(execution.Store, execution.Driver, input, movementID, baseCommit)
	if err != nil {
		return err
	}
	result := driver.ExecuteAttempt(ctx, driver.AttemptExecution{
		RepositoryRoot:       execution.Store.RepositoryRoot(),
		Score:                input.Score,
		Cast:                 input.Cast,
		RunID:                execution.Driver.RunID(),
		Attempt:              attempt,
		BaseTree:             baseTree,
		BaseCompositionHash:  baseCompositionHash,
		CandidateTree:        candidateTree,
		Authority:            execution.Driver,
		PerformerID:          performerID,
		SelectionReason:      reason,
		SelectionCausationID: causationID,
		RemainingMS:          input.Projection.Scheduler.RemainingTime,
		RetriesConsumed:      retriesConsumed(input.Projection.State, runstate.MovementID(movementID)),
		VisitedPerformers:    visitedPerformers(input.Projection, runstate.MovementID(movementID)),
	}, driver.DefaultExecutionDependencies(faultpoint.ProbeFromEnvironment()))
	if result.Err != nil {
		return result.Err
	}
	return recoveredAttemptOutcome(result.Outcome)
}

// recoveredAttemptOutcome maps what a recovery-owned attempt ended as onto what the executor
// should see. Cancellation is not a failure: the driver observed the request mid-attempt and
// ran the §6 oracle, so the run is already terminal, and reporting an error here would make
// `resume` call a cancelled run an operational interruption where §7 gives it exit 4. The
// sentinel makes the executor replan so C.1's terminal row supplies the outcome, rather than
// this handler inventing a second way out.
func recoveredAttemptOutcome(outcome driver.Outcome) error {
	switch outcome {
	case driver.OutcomeSucceeded:
		return nil
	case driver.OutcomeCancelled:
		return ErrRunCancelledDuringRecovery
	default:
		return fmt.Errorf("recovery attempt execution ended %s", outcome)
	}
}

func retriesConsumed(state runstate.State, movementID runstate.MovementID) int {
	count := 0
	for _, attempt := range state.Attempts {
		if attempt.MovementID == movementID && attempt.Failure != nil && attempt.Failure.Disposition.Charged == "quality_retry" {
			count++
		}
	}
	return count
}

func visitedPerformers(projection recovery.Projection, movementID runstate.MovementID) []string {
	attempt := projection.CurrentHeadAttempt
	if attempt == nil || attempt.MovementID != movementID {
		return nil
	}
	return append([]string(nil), attempt.FailureClassification.VisitedPerformers...)
}

func terminalCleanup(_ context.Context, execution HandlerContext, _ recovery.Action) error {
	if execution.Store == nil || execution.RunID == "" {
		return errors.New("recovery executor requires store and run id for terminal cleanup")
	}
	residues, err := terminalCleanupResidues(execution.Store.RepositoryRoot(), execution.RunID)
	if err != nil {
		return err
	}
	if err := execution.Store.Mutate(execution.RunID, "", func(transaction *runstore.Txn) error {
		lease, present, err := transaction.ReadLease()
		if err != nil {
			return err
		}
		if present {
			if _, err := transaction.At("recovery.terminal_cleanup/lease").CompareRemoveLease(lease.Identity()); err != nil {
				return err
			}
		}
		for _, residue := range residues {
			if _, err := transaction.At("recovery.terminal_cleanup/" + faultpoint.ReceiptAddress(residue)).RemoveDurable(runstore.Path(residue)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(execution.Store.RepositoryRoot(), ".partitur", "work", string(execution.RunID)))
}

func terminalCleanupResidues(root string, runID runstate.RunID) ([]string, error) {
	runRoot := filepath.Join(root, ".partitur", "runs", string(runID))
	entries, err := os.ReadDir(runRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read terminal cleanup run root: %w", err)
	}
	residues := make([]string, 0)
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "driver.quiesced.") && entry.Name() != "driver.quiesced." {
			residues = append(residues, entry.Name())
		}
	}
	prepares := filepath.Join(runRoot, "prepares")
	entries, err = os.ReadDir(prepares)
	if errors.Is(err, fs.ErrNotExist) {
		return residues, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read terminal cleanup prepares: %w", err)
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			residues = append(residues, filepath.ToSlash(filepath.Join("prepares", entry.Name())))
		}
	}
	slices.Sort(residues)
	return residues, nil
}

func removeStaleLease(_ context.Context, execution HandlerContext, _ recovery.Action) error {
	if execution.Store == nil || execution.RunID == "" {
		return errors.New("recovery executor requires store and run id for stale lease removal")
	}
	return execution.Store.Mutate(execution.RunID, "", func(transaction *runstore.Txn) error {
		lease, present, err := transaction.ReadLease()
		if err != nil || !present {
			return err
		}
		if lease.Epoch >= execution.Input.Projection.State.Authority.Epoch {
			return runstore.ErrLeaseConflict
		}
		_, err = transaction.At("recovery.remove_stale_lease").CompareRemoveLease(lease.Identity())
		return err
	})
}

func quarantineOrphanLease(_ context.Context, execution HandlerContext, _ recovery.Action) error {
	if execution.Store == nil || execution.RunID == "" {
		return errors.New("recovery executor requires store and run id for orphan lease quarantine")
	}
	return execution.Store.Mutate(execution.RunID, "", func(transaction *runstore.Txn) error {
		lease, present, err := transaction.ReadLease()
		if err != nil || !present {
			return err
		}
		if lease.Epoch <= execution.Input.Projection.State.Authority.Epoch {
			return runstore.ErrLeaseConflict
		}
		_, err = transaction.At("recovery.quarantine_orphan_lease").QuarantineAs("orphan_lease").Quarantine("driver.lease")
		return err
	})
}

func stabilizeHandoff(ctx context.Context, execution HandlerContext, action recovery.Action) error {
	directory, err := attemptLaunchDirectory(execution, action.AttemptID)
	if err != nil {
		return err
	}
	if directory == "" {
		return nil
	}
	deadline := time.Now().Add(recoverySweepGrace)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		observation, err := launch.ObserveHandoff(directory)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrHandoffUnverifiable, err)
		}
		if observation.HasIdentity {
			if err := adapter.SweepSession(observation.Identity, recoverySweepGrace); err != nil {
				return fmt.Errorf("%w: %v", runstate.ErrSweepUnverifiable, err)
			}
			return nil
		}
		if observation.MarkerFree {
			return nil
		}
		if !observation.MarkerHeld || !time.Now().Before(deadline) {
			return ErrHandoffUnverifiable
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func sweepRecordedSession(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	launch, ok := state.AdapterLaunches[action.AttemptID]
	if !ok {
		return fmt.Errorf("recorded adapter session for %q is absent", action.AttemptID)
	}
	if err := adapter.SweepSession(launch.Process, recoverySweepGrace); err != nil {
		return fmt.Errorf("%w: %v", runstate.ErrSweepUnverifiable, err)
	}
	return nil
}

func closeAdapterInterval(ctx context.Context, execution HandlerContext, action recovery.Action) error {
	return closeOpenExecutionInterval(ctx, execution, action)
}

func closeOpenExecutionInterval(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	interval := state.OpenExecution
	if interval == nil {
		return nil
	}
	observed := time.Now().UTC()
	started, err := time.Parse(time.RFC3339Nano, interval.WallStart)
	if err != nil {
		return fmt.Errorf("parse interval wall_start: %w", err)
	}
	duration := observed.Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	if duration > interval.RemainingAtStart {
		duration = interval.RemainingAtStart
	}
	return appendEvent(execution, state, action, runstate.EventExecutionStopped, map[string]any{
		"interval_id": interval.ID, "reason": "recovered", "charging": "clamped",
		"charged_duration": duration, "observed_at": observed.Format("2006-01-02T15:04:05.000Z"),
	})
}

func appendAttemptFailure(_ context.Context, execution HandlerContext, action recovery.Action) error {
	disposition, err := classify(execution.Input, action, successor.FailureCase{AttemptKind: action.FailureKind})
	if err != nil {
		return err
	}
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	payload := map[string]any{"kind": action.FailureKind, "disposition": dispositionPayload(disposition)}
	if action.FailureReason != "" {
		payload["reason"] = action.FailureReason
	}
	return appendEvent(execution, state, action, runstate.EventAttemptFailed, payload)
}

func sweepCriterionSession(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	launch, ok := state.CriterionLaunches[runstate.CriterionLaunchKey{AttemptID: action.AttemptID, CriterionID: action.CriterionID}]
	if !ok {
		return fmt.Errorf("recorded criterion session for %q is absent", action.CriterionID)
	}
	spawned, ok := launch.(runstate.SpawnedCriterionLaunch)
	if !ok {
		return nil
	}
	if err := adapter.SweepSession(spawned.Process, recoverySweepGrace); err != nil {
		return fmt.Errorf("%w: %v", runstate.ErrSweepUnverifiable, err)
	}
	return nil
}

func verifyAcceptanceSubject(ctx context.Context, execution HandlerContext, action recovery.Action) error {
	// The executor has refreshed Input after the preceding sweep. Verification
	// facts are collected by the supplied loader, never by a private handler
	// path. RC-RESUME-024 is the one prescribed execution-time branch: its
	// criterion id marks the recorded in-flight criterion which needs a
	// terminal observation after the refreshed verification.
	subject := execution.Input.Observations.AcceptanceSubject
	if subject == recovery.SubjectUnverified {
		return errors.New("post-sweep acceptance subject is unverified")
	}
	if action.CriterionID == "" {
		return nil
	}
	if subject == recovery.SubjectMismatched {
		mismatch := action
		mismatch.FailureReason = "recovery_subject_mismatch"
		return appendAcceptanceFailure(ctx, execution, mismatch)
	}
	recovered := action
	recovered.FailureReason = "criterion_errored"
	if err := synthesizeCriterionError(ctx, execution, recovered); err != nil {
		return err
	}
	return appendAcceptanceFailure(ctx, execution, recovered)
}

func synthesizeCriterionError(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	acceptance := state.Acceptances[action.AttemptID]
	criterion, ok := acceptance.Criteria[action.CriterionID]
	if !ok || !criterion.Started || criterion.Completed {
		return fmt.Errorf("criterion %q is not in flight", action.CriterionID)
	}
	versions, err := identityVersions(canonical.DomainCriterionSpec)
	if err != nil {
		return err
	}
	return appendEvent(execution, state, action, runstate.EventCriterionCompleted, map[string]any{
		"criterion_id": action.CriterionID, "criterion_spec_hash": criterion.SpecHash,
		"subject_tree": criterion.SubjectTree, "outcome": "ERROR",
		"error_detail": "recovered_without_observed_completion", "identity_versions": versions,
	})
}

func appendAcceptanceFailure(_ context.Context, execution HandlerContext, action recovery.Action) error {
	disposition, err := classify(execution.Input, action, successor.FailureCase{AcceptanceReason: action.FailureReason})
	if err != nil {
		return err
	}
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	acceptance, ok := state.Acceptances[action.AttemptID]
	if !ok {
		return fmt.Errorf("acceptance for %q is absent", action.AttemptID)
	}
	payload := map[string]any{"reason": action.FailureReason, "subject_tree": acceptance.SubjectTree, "disposition": dispositionPayload(disposition)}
	if action.CriterionID != "" && action.FailureReason != "recovery_subject_mismatch" {
		payload["failed_criterion_id"] = action.CriterionID
	}
	return appendEvent(execution, state, action, runstate.EventAcceptanceFailed, payload)
}

func appendAttemptCompleted(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	return appendEvent(execution, state, action, runstate.EventAttemptCompleted, map[string]any{})
}

func appendMovementSucceeded(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	movementID, err := actionMovement(state, action)
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
	versions, err := identityVersions()
	if err != nil {
		return err
	}
	payload := map[string]any{
		"approved_artifact_instance_ids": artifactIDs,
		"identity_versions":              versions,
		"run_succeeded":                  state.FinalMovements[movementID],
	}
	if state.RepoWriteMovements[movementID] {
		changeSet, ok := state.ChangeSets[action.AttemptID]
		if !ok {
			return fmt.Errorf("change set for repo-write attempt %q is absent", action.AttemptID)
		}
		payload["approved_change_set_id"] = changeSet.ChangeSetID
	}
	return appendEvent(execution, state, withMovement(action, movementID), runstate.EventMovementSucceeded, payload)
}

func appendMovementBudgetFailure(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	movementID, err := actionMovement(state, action)
	if err != nil {
		return err
	}
	if err := appendEvent(execution, state, withMovement(action, movementID), runstate.EventMovementFailed, map[string]any{
		"reason": "budget_exhausted", "run_failed": false,
	}); err != nil {
		return err
	}
	execution.Store.Reached(faultpoint.PointLifecycleMovementFailed)
	return nil
}

func appendRunFailed(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	reason := action.FailureReason
	if reason == "" {
		reason = "movement_failed"
	}
	if err := appendEvent(execution, state, action, runstate.EventRunFailed, map[string]any{"reason": reason}); err != nil {
		return err
	}
	execution.Store.Reached(faultpoint.PointLifecycleRunFailed)
	return nil
}

func returnWaitingHuman(context.Context, HandlerContext, recovery.Action) error { return nil }

func refuseResume(context.Context, HandlerContext, recovery.Action) error { return nil }

func stabilizeUnjournaledLaunch(ctx context.Context, execution HandlerContext, action recovery.Action) error {
	directory, err := unjournaledLaunchDirectory(execution, action.AttemptID)
	if err != nil || directory == "" {
		return err
	}
	return stabilizeLaunchDirectory(ctx, directory)
}

func removeUnjournaledLaunch(_ context.Context, execution HandlerContext, action recovery.Action) error {
	directory, err := unjournaledLaunchDirectory(execution, action.AttemptID)
	if err != nil || directory == "" {
		return err
	}
	return os.RemoveAll(directory)
}

func classify(input recovery.Input, action recovery.Action, failure successor.FailureCase) (runstate.Disposition, error) {
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
	return successor.Classify(successor.ClassificationInput{
		Failure: failure, HasUnvisitedFallback: hasUnvisitedFallback,
		RetriesConsumed: facts.RetriesConsumed, RetriesPerMovement: facts.RetriesPerMovement,
		RemainingTimeMS: facts.RemainingTimeMS,
	})
}

func appendEvent(execution HandlerContext, state runstate.State, action recovery.Action, eventType runstate.EventType, payload any) error {
	if execution.Driver == nil {
		return ErrAuthorityRequired
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	event := runstate.Event{
		RunID: execution.Driver.RunID(), ScoreRevision: state.ScoreHead.Revision,
		AttemptID: action.AttemptID, Type: eventType, Payload: encoded,
	}
	if eventType != runstate.EventRunFailed && eventType != runstate.EventExecutionStopped {
		movementID, err := actionMovement(state, action)
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
	case runstate.EventExecutionStopped:
		if state.OpenExecution == nil {
			return "", errors.New("recovered interval source is absent")
		}
		return latestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventExecutionStarted && payloadString(event.Payload, "interval_id") == string(state.OpenExecution.ID)
		})
	case runstate.EventAttemptFailed:
		source := runstate.EventPerformerSelected
		switch action.FailureReason {
		case "probe_terminated_incomplete":
			source = runstate.EventAttemptStarted
		case "attempt_terminated_incomplete":
			source = runstate.EventAdapterProbed
		case "worktree_lost":
			source = runstate.EventPerformerCompleted
		}
		return latestEventID(journal.Events, func(event runstate.Event) bool { return event.Type == source && match(event) })
	case runstate.EventCriterionCompleted:
		return latestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventCriterionStarted && match(event) && payloadString(event.Payload, "criterion_id") == string(action.CriterionID)
		})
	case runstate.EventAcceptanceFailed:
		if action.FailureReason == "recovery_subject_mismatch" {
			return latestEventID(journal.Events, func(event runstate.Event) bool { return event.Type == runstate.EventAcceptanceStarted && match(event) })
		}
		return latestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventCriterionCompleted && match(event) && payloadString(event.Payload, "criterion_id") == string(action.CriterionID)
		})
	case runstate.EventAttemptCompleted:
		source := runstate.EventAcceptanceEvaluationCompleted
		if execution.Input.Projection.Acceptance != nil && execution.Input.Projection.Acceptance.Gate.Required {
			source = runstate.EventDecisionResolved
		}
		return latestEventID(journal.Events, func(event runstate.Event) bool { return event.Type == source && match(event) })
	case runstate.EventMovementSucceeded:
		return latestEventID(journal.Events, func(event runstate.Event) bool { return event.Type == runstate.EventAttemptCompleted && match(event) })
	case runstate.EventMovementFailed:
		if action.FailureReason == "budget_exhausted" {
			return latestEventID(journal.Events, func(event runstate.Event) bool {
				return event.Type == runstate.EventExecutionStopped && payloadString(event.Payload, "reason") == "budget_exhausted"
			})
		}
		if source, err := latestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventAttemptFailed && match(event)
		}); err == nil {
			return source, nil
		}
		movementID, err := actionMovement(state, action)
		if err != nil {
			return "", err
		}
		return latestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventMovementStarted && event.MovementID == movementID
		})
	case runstate.EventRunFailed:
		if action.FailureReason == "budget_exhausted" {
			return latestEventID(journal.Events, func(event runstate.Event) bool {
				return event.Type == runstate.EventExecutionStopped && payloadString(event.Payload, "reason") == "budget_exhausted"
			})
		}
		if action.MovementID != "" || action.AttemptID != "" {
			movementID, err := actionMovement(state, action)
			if err != nil {
				return "", err
			}
			return latestEventID(journal.Events, func(event runstate.Event) bool {
				return event.Type == runstate.EventMovementFailed && event.MovementID == movementID
			})
		}
		return latestEventID(journal.Events, func(event runstate.Event) bool {
			return event.Type == runstate.EventMovementFailed
		})
	default:
		return "", fmt.Errorf("no recovery causation source for %s", eventType)
	}
}

func latestEventID(events []runstate.Event, matches func(runstate.Event) bool) (string, error) {
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

func actionMovement(state runstate.State, action recovery.Action) (runstate.MovementID, error) {
	if action.MovementID != "" {
		return action.MovementID, nil
	}
	if attempt, ok := state.Attempts[action.AttemptID]; ok {
		return attempt.MovementID, nil
	}
	return "", fmt.Errorf("movement for recovery action %s is absent", action.Kind)
}

func withMovement(action recovery.Action, movementID runstate.MovementID) recovery.Action {
	action.MovementID = movementID
	return action
}

func dispositionPayload(disposition runstate.Disposition) map[string]any {
	payload := map[string]any{"charged": disposition.Charged, "movement_terminal": disposition.MovementTerminal}
	if disposition.TerminalReason != "" {
		payload["terminal_reason"] = disposition.TerminalReason
	}
	return payload
}

func identityVersions(domains ...canonical.Domain) (map[string]any, error) {
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

func attemptLaunchDirectory(execution HandlerContext, attemptID runstate.AttemptID) (string, error) {
	if execution.Store == nil || execution.Driver == nil {
		return "", errors.New("recovery executor requires store access for handoff stabilization")
	}
	root := filepath.Join(execution.Store.RepositoryRoot(), ".partitur", "work", string(execution.Driver.RunID()), string(attemptID))
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != "worktree" && entry.Name() != "output" {
			return filepath.Join(root, entry.Name()), nil
		}
	}
	return "", nil
}

func unjournaledLaunchDirectory(execution HandlerContext, attemptID runstate.AttemptID) (string, error) {
	directory, err := attemptLaunchDirectory(execution, attemptID)
	if err != nil || directory == "" {
		return directory, err
	}
	state, err := execution.Driver.State()
	if err != nil {
		return "", err
	}
	observation, err := launch.ObserveHandoff(directory)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrHandoffUnverifiable, err)
	}
	if adapterLaunch, ok := state.AdapterLaunches[attemptID]; ok && observation.HasIdentity && adapterLaunch.Process == observation.Identity {
		return "", nil
	}
	return directory, nil
}

func stabilizeLaunchDirectory(ctx context.Context, directory string) error {
	deadline := time.Now().Add(recoverySweepGrace)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		observation, err := launch.ObserveHandoff(directory)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrHandoffUnverifiable, err)
		}
		if observation.HasIdentity {
			if err := adapter.SweepSession(observation.Identity, recoverySweepGrace); err != nil {
				return fmt.Errorf("%w: %v", runstate.ErrSweepUnverifiable, err)
			}
			return nil
		}
		if observation.MarkerFree {
			return nil
		}
		if !observation.MarkerHeld || !time.Now().Before(deadline) {
			return ErrHandoffUnverifiable
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
