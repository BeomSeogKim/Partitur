package recoveryexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/cancellation"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/criterionexec"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryconsequence"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/successor"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

const recoverySweepGrace = 30 * time.Second

var (
	appendCompositionTerminal           = recoveryconsequence.AppendCompositionTerminal
	appendAcceptanceEvaluationCompleted = recoveryconsequence.AppendAcceptanceEvaluationCompleted
	appendHumanGateRequest              = recoveryconsequence.AppendHumanGateRequest
	appendGateRejectedFailure           = recoveryconsequence.AppendGateRejectedFailure
	realizeRecordedDisposition          = recoveryconsequence.RealizeRecordedDisposition
	appendQuestionRequest               = recoveryconsequence.AppendQuestionRequest
	appendBlockedProposalRoute          = recoveryconsequence.AppendBlockedProposalRoute
	appendRoutedRequest                 = recoveryconsequence.AppendRoutedRequest
	appendAcceptanceFailure             = recoveryconsequence.AppendAcceptanceFailure
	appendAttemptCompleted              = recoveryconsequence.AppendAttemptCompleted
	appendMovementSucceeded             = recoveryconsequence.AppendMovementSucceeded
	appendRunFailed                     = recoveryconsequence.AppendRunFailed
	recoveryPayload                     = recoveryconsequence.RecoveryPayload
	appendEvent                         = recoveryconsequence.AppendEvent
	latestEventID                       = recoveryconsequence.LatestEventID
	actionMovement                      = recoveryconsequence.ActionMovement
	withMovement                        = recoveryconsequence.WithMovement
	classify                            = recoveryconsequence.Classify
	dispositionPayload                  = recoveryconsequence.DispositionPayload
	identityVersions                    = recoveryconsequence.IdentityVersions
)

func defaultSteps() map[recovery.ActionStep]StepHandler {
	return map[recovery.ActionStep]StepHandler{
		recovery.StepStabilizeHandoff:            stabilizeHandoff,
		recovery.StepSweepRecordedSession:        sweepRecordedSession,
		recovery.StepCloseAdapterInterval:        closeAdapterInterval,
		recovery.StepClassifyAndAppendFailure:    appendAttemptFailure,
		recovery.StepSweepCriterionSession:       sweepCriterionSession,
		recovery.StepVerifyAcceptanceSubject:     verifyAcceptanceSubject,
		recovery.StepClassifyAcceptanceFailure:   appendAcceptanceFailure,
		recovery.StepAppendAttemptCompleted:      appendAttemptCompleted,
		recovery.StepAppendMovementSucceeded:     appendMovementSucceeded,
		recovery.StepAppendMovementBudgetFailure: appendMovementBudgetFailure,
		recovery.StepAppendRunFailed:             appendRunFailed,
	}
}

func defaultKinds() map[recovery.ActionKind]StepHandler {
	return map[recovery.ActionKind]StepHandler{
		recovery.ActionCloseOpenExecutionInterval:   closeOpenExecutionInterval,
		recovery.ActionTerminalCleanup:              terminalCleanup,
		recovery.ActionRemoveStaleLease:             removeStaleLease,
		recovery.ActionQuarantineOrphanLease:        quarantineOrphanLease,
		recovery.ActionAppendMovementSucceeded:      appendMovementSucceeded,
		recovery.ActionAppendMovementReady:          appendMovementReady,
		recovery.ActionAppendMovementStarted:        appendMovementStarted,
		recovery.ActionAppendAcceptanceStarted:      appendAcceptanceStarted,
		recovery.ActionAppendEvaluationCompleted:    appendAcceptanceEvaluationCompleted,
		recovery.ActionAppendHumanGateRequest:       appendHumanGateRequest,
		recovery.ActionAppendGateRejectedFailure:    appendGateRejectedFailure,
		recovery.ActionAppendFinalGateFailure:       appendGateRejectedFailure,
		recovery.ActionSelectInitialPerformer:       selectInitialPerformer,
		recovery.ActionResumeCriterion:              resumeCriterion,
		recovery.ActionRealizeRecordedDisposition:   realizeRecordedDisposition,
		recovery.ActionMaterializeSuccessor:         materializeSuccessor,
		recovery.ActionSelectRevisionRestart:        selectRevisionRestart,
		recovery.ActionAppendQuestionRequest:        appendQuestionRequest,
		recovery.ActionAppendBlockedProposalRoute:   appendBlockedProposalRoute,
		recovery.ActionAppendRoutedRequest:          appendRoutedRequest,
		recovery.ActionSelectDecisionResume:         selectDecisionResume,
		recovery.ActionAppendRunFailed:              appendRunFailed,
		recovery.ActionAppendBudgetFailure:          appendMovementBudgetFailure,
		recovery.ActionReturnWaitingHuman:           returnWaitingHuman,
		recovery.ActionRefuseResume:                 refuseResume,
		recovery.ActionStabilizeUnjournaledLaunch:   stabilizeUnjournaledLaunch,
		recovery.ActionRemoveUnjournaledLaunch:      removeUnjournaledLaunch,
		recovery.ActionComposeCandidate:             composeCandidate,
		recovery.ActionRerunPostHocVerification:     rerunPostHocVerification,
		recovery.ActionAppendDraftNoBlockingFailure: appendAttemptFailure,
		recovery.ActionCaptureChangeSet:             captureChangeSet,
		recovery.ActionExecuteCancellation:          executeCancellation,
		recovery.ActionCompleteOrAbandonPrepare:     completeOrAbandonPrepare,
		recovery.ActionAppendCompositionTerminal:    appendCompositionTerminal,
		recovery.ActionRerunComposition:             rerunMovementComposition,
	}
}

func completeOrAbandonPrepare(ctx context.Context, execution HandlerContext, _ recovery.Action) error {
	if execution.Store == nil || execution.RunID == "" {
		return errors.New("recovery executor requires store and run id for prepare commit")
	}
	if err := execution.Store.CompleteOrAbandonPrepare(ctx, execution.RunID); err != nil {
		return err
	}
	return ErrRecoveryReplan
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
	if errors.Is(err, driver.ErrCompositionBudgetExhausted) {
		return ErrRecoveryReplan
	}
	return err
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
		if errors.Is(err, driver.ErrCompositionBudgetExhausted) {
			return ErrRecoveryReplan
		}
		return err
	}
	_, err = driver.PrepareMovementBase(execution.Store, execution.Driver, input, action.MovementID, input.Projection.Scheduler.RemainingTime, time.Now, workspace.NewID)
	if errors.Is(err, driver.ErrCompositionTerminalized) {
		return nil
	}
	if errors.Is(err, driver.ErrCompositionBudgetExhausted) {
		return ErrRecoveryReplan
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
		subject, err := workspace.CaptureRecoveredAcceptanceSubject(
			execution.Store,
			execution.Driver,
			input,
			action.AttemptID,
		)
		if err != nil {
			return err
		}
		if subject.Ref != "" {
			matched, err := workspace.VerifyRecoverySubject(
				execution.Store.RepositoryRoot(),
				filepath.Join(execution.Store.RepositoryRoot(), ".partitur", "work", string(execution.Driver.RunID()), string(action.AttemptID), "worktree"),
				subject.Tree,
			)
			if err != nil {
				return err
			}
			if !matched {
				return errors.New("recovery acceptance subject does not match surviving worktree")
			}
		}
		event, err := plan.StartEvent(runstate.Event{
			RunID:         execution.Driver.RunID(),
			ScoreRevision: input.Projection.State.ScoreHead.Revision,
			MovementID:    attempt.MovementID,
			PartID:        movement.PartID,
			AttemptID:     action.AttemptID,
		}, subject.Tree)
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
			if errors.Is(err, driver.ErrCompositionBudgetExhausted) {
				return ErrRecoveryReplan
			}
			return err
		}
		baseCommit, baseTree, baseHash = composed.Commit, composed.Tree, composed.Hash
		break
	}
	return executeRecoveredAttemptAtBase(ctx, execution, input, movementID, performer.ID, "initial", "", baseCommit, baseTree, baseHash)
}

func resumeCriterion(ctx context.Context, execution HandlerContext, action recovery.Action) error {
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
		evaluation, err := acceptance.EvaluateStartedCriterion(plan, acceptance.Evaluation{
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
			RunCriterion: func(request acceptance.RunCriterionRequest) acceptance.RunCriterionResult {
				trampoline, resolveErr := exec.LookPath("partitur-trampoline")
				if resolveErr != nil {
					return acceptance.RunCriterionResult{Err: fmt.Errorf("resolve partitur-trampoline: %w", resolveErr)}
				}
				trampoline, resolveErr = filepath.Abs(trampoline)
				if resolveErr != nil {
					return acceptance.RunCriterionResult{Err: resolveErr}
				}
				attemptRoot := filepath.Join(execution.Store.RepositoryRoot(), ".partitur", "work", string(execution.RunID), string(action.AttemptID))
				return criterionexec.Run(criterionexec.Config{
					RunID: execution.RunID, AttemptID: action.AttemptID, AttemptRoot: attemptRoot,
					Worktree: filepath.Join(attemptRoot, "worktree"), RepositoryRoot: execution.Store.RepositoryRoot(),
					SubjectTree: acceptanceState.SubjectTree, TrampolinePath: trampoline,
					RemainingMS: input.Projection.Scheduler.RemainingTime, Probe: faultpoint.ProbeFromEnvironment(),
				}, request)
			},
		}, string(action.CriterionID))
		if err != nil {
			return err
		}
		if evaluation.BudgetExhausted {
			terminal := driver.TerminalizeAcceptanceBudget(ctx, driver.AcceptanceBudgetTerminalization{
				RepositoryRoot: execution.Store.RepositoryRoot(),
				RunID:          execution.RunID,
				AttemptID:      action.AttemptID,
				Authority:      execution.Driver,
				Probe:          faultpoint.ProbeFromEnvironment(),
				Close: func() error {
					return closeRecoveredAcceptanceBudgetInterval(execution, action)
				},
			})
			return terminal.Err
		}
		return nil
	}
	return fmt.Errorf("recovery criterion movement %q is absent from pinned score", attempt.MovementID)
}

func closeRecoveredAcceptanceBudgetInterval(execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	interval := state.OpenExecution
	if interval == nil || interval.Phase != "acceptance" {
		return errors.New("recovery acceptance budget has no open acceptance interval")
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
		"interval_id": interval.ID, "reason": "budget_exhausted", "charging": "clamped",
		"charged_duration": duration, "observed_at": observed.Format("2006-01-02T15:04:05.000Z"),
	})
}

// recoveredExecutionDependencies gives a recovered attempt the same receipt
// seam a live one has. `DefaultExecutionDependencies` leaves the observer nil,
// and the run command fills it in on its own path — so without this, every
// durable append made by an attempt that recovery started is unobservable, and
// the cut between `attempt.completed` and `movement.succeeded` cannot be
// reached by any caller.
func recoveredExecutionDependencies() driver.ExecutionDependencies {
	execution := driver.DefaultExecutionDependencies(faultpoint.ProbeFromEnvironment())
	execution.ReceiptObserver = runstore.ReceiptObserverFromEnvironment()
	return execution
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
	causationID := pending.CausationID
	if causationID == "" {
		causationID, err = latestEventID(journal.Events, func(previous runstate.Event) bool {
			if previous.AttemptID != pending.AttemptID {
				return false
			}
			if pending.Reason == "decision_resume" {
				switch previous.Type {
				case runstate.EventDecisionResolved, runstate.EventAmendmentHumanRejected, runstate.EventAmendmentRejected:
					return true
				default:
					return false
				}
			}
			return previous.Type == runstate.EventAttemptFailed || previous.Type == runstate.EventAcceptanceFailed
		})
		if err != nil {
			return err
		}
	}
	return executeRecoveredAttempt(
		ctx, execution, input, string(pending.MovementID), performer.ID, pending.Reason, causationID,
	)
}

func selectRevisionRestart(_ context.Context, _ HandlerContext, action recovery.Action) error {
	if action.RevisionRestart == nil || action.PendingSuccessor == nil {
		return errors.New("recovery revision restart selection is incomplete")
	}
	restart := action.RevisionRestart
	pending := action.PendingSuccessor
	if restart.MovementID == "" || restart.AttemptID == "" || restart.ApprovalEventID == "" || restart.Performer == "" ||
		pending.MovementID != restart.MovementID || pending.AttemptID != restart.AttemptID || pending.Performer != restart.Performer ||
		pending.Reason != "revision_restart" || pending.CausationID != restart.ApprovalEventID || action.Continuation != recovery.ContinuationC4 {
		return errors.New("recovery revision restart selection is incomplete")
	}
	return nil
}

func selectDecisionResume(_ context.Context, _ HandlerContext, action recovery.Action) error {
	if action.PendingSuccessor == nil || action.PendingSuccessor.AttemptID != action.AttemptID || action.PendingSuccessor.Performer == "" || action.PendingSuccessor.Reason != "decision_resume" {
		return errors.New("recovery decision resume selection is incomplete")
	}
	return nil
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
	base, err := driver.PrepareSuccessorBase(execution.Store, execution.Driver, input, runstate.MovementID(movementID), input.Projection.Scheduler.RemainingTime, time.Now, workspace.NewID)
	if err != nil {
		if errors.Is(err, driver.ErrCompositionTerminalized) {
			return errMovementCompositionTerminalized
		}
		if errors.Is(err, driver.ErrCompositionCancelled) {
			return ErrRecoveryReplan
		}
		if errors.Is(err, driver.ErrCompositionBudgetExhausted) {
			return ErrRecoveryReplan
		}
		return err
	}
	return executeRecoveredAttemptAtBase(ctx, execution, input, movementID, performerID, reason, causationID, base.Commit, base.Tree, base.Hash)
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
	}, recoveredExecutionDependencies())
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
	if execution.Store == nil || execution.Driver == nil {
		return "", errors.New("recovery executor requires store access for handoff stabilization")
	}
	state, err := execution.Driver.State()
	if err != nil {
		return "", err
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
		if !entry.IsDir() || entry.Name() == "worktree" || entry.Name() == "output" {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		observation, err := launch.ObserveHandoff(directory)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrHandoffUnverifiable, err)
		}
		if observation.HasIdentity && journaledLaunchIdentity(state, observation.Identity) {
			continue
		}
		return directory, nil
	}
	return "", nil
}

func journaledLaunchIdentity(state runstate.State, identity runstate.ProcessIdentity) bool {
	for _, adapterLaunch := range state.AdapterLaunches {
		if reflect.DeepEqual(adapterLaunch.Process, identity) {
			return true
		}
	}
	for _, launch := range state.CriterionLaunches {
		if spawned, ok := launch.(runstate.SpawnedCriterionLaunch); ok && reflect.DeepEqual(spawned.Process, identity) {
			return true
		}
	}
	return false
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
