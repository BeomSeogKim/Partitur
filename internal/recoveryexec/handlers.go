package recoveryexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/successor"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

const recoverySweepGrace = 30 * time.Second

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
		recovery.ActionAppendMovementSucceeded: appendMovementSucceeded,
		recovery.ActionAppendRunFailed:         appendRunFailed,
		recovery.ActionAppendBudgetFailure:     appendMovementBudgetFailure,
	}
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
				return fmt.Errorf("%w: %v", ErrSweepUnverifiable, err)
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
		return fmt.Errorf("%w: %v", ErrSweepUnverifiable, err)
	}
	return nil
}

func closeAdapterInterval(_ context.Context, execution HandlerContext, action recovery.Action) error {
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	interval := state.OpenExecution
	if interval == nil {
		return nil
	}
	if interval.Phase != "adapter" {
		return fmt.Errorf("open execution interval %q is %s, not adapter", interval.ID, interval.Phase)
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
		"charged_duration": duration, "observed_at": observed.Format(time.RFC3339Nano),
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
		return fmt.Errorf("%w: %v", ErrSweepUnverifiable, err)
	}
	return nil
}

func verifyAcceptanceSubject(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil {
		return errors.New("recovery executor requires store access for subject verification")
	}
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	acceptance, ok := state.Acceptances[action.AttemptID]
	if !ok || !acceptance.Started {
		return fmt.Errorf("acceptance for %q is not started", action.AttemptID)
	}
	matched, err := workspace.VerifyRecoverySubject(
		execution.Store.RepositoryRoot(),
		filepath.Join(execution.Store.RepositoryRoot(), ".partitur", "work", string(execution.Driver.RunID()), string(action.AttemptID), "worktree"),
		acceptance.SubjectTree,
	)
	if err != nil {
		return err
	}
	if !matched {
		return nil // Replan reloads the observed mismatch and selects C.3's failure row.
	}
	return nil
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
	if action.CriterionID != "" {
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
	versions, err := identityVersions(canonical.DomainChangeSet)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"approved_artifact_instance_ids": artifactIDs,
		"identity_versions":              versions,
		"run_succeeded":                  allOtherMovementsFinished(state, movementID),
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
	return appendEvent(execution, state, withMovement(action, movementID), runstate.EventMovementFailed, map[string]any{
		"reason": "budget_exhausted", "run_failed": false,
	})
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
	return appendEvent(execution, state, action, runstate.EventRunFailed, map[string]any{"reason": reason})
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
	_, err = execution.Driver.Append(event, faultpoint.ReceiptAddress("recovery."+string(eventType)))
	return err
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

func allOtherMovementsFinished(state runstate.State, target runstate.MovementID) bool {
	for movementID, lifecycle := range state.Movements {
		if movementID != target && lifecycle != runstate.MovementSucceeded && lifecycle != runstate.MovementInapplicable {
			return false
		}
	}
	return true
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
