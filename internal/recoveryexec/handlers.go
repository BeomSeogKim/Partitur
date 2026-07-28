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
		recovery.ActionAppendMovementSucceeded:    appendMovementSucceeded,
		recovery.ActionAppendRunFailed:            appendRunFailed,
		recovery.ActionAppendBudgetFailure:        appendMovementBudgetFailure,
		recovery.ActionReturnWaitingHuman:         returnWaitingHuman,
		recovery.ActionRefuseResume:               refuseResume,
		recovery.ActionStabilizeUnjournaledLaunch: stabilizeUnjournaledLaunch,
		recovery.ActionRemoveUnjournaledLaunch:    removeUnjournaledLaunch,
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
		return fmt.Errorf("%w: %v", ErrSweepUnverifiable, err)
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
