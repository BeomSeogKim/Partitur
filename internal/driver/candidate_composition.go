package driver

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

// CandidateCompositionContributors returns every successful writer in pinned
// stable topological order, with declaration order as the tie breaker. Unlike
// the candidate identity's ordered change-set list, this intentionally retains
// duplicate and no-op writers for A.4's merge dependency identity.
func CandidateCompositionContributors(input runstore.RunInput) ([]workspace.CompositionContributor, error) {
	if input.Score == nil {
		return nil, errors.New("driver: candidate composition has no pinned score")
	}
	state := input.Projection.State
	movements := input.Score.Movements()
	included := make(map[runstate.MovementID]bool, len(movements))
	for _, movement := range movements {
		included[runstate.MovementID(movement.ID)] = true
	}
	ordered, err := stableTopologicalMovementIDs(movements, included)
	if err != nil {
		return nil, err
	}
	contributors := make([]workspace.CompositionContributor, 0)
	byID := make(map[runstate.MovementID]bool, len(movements))
	for _, movement := range movements {
		byID[runstate.MovementID(movement.ID)] = slices.Contains(movement.Grants, "repo_write")
	}
	for _, movementID := range ordered {
		if !byID[movementID] {
			continue
		}
		if state.Movements[movementID] == runstate.MovementInapplicable {
			continue
		}
		if state.Movements[movementID] != runstate.MovementSucceeded {
			return nil, fmt.Errorf("driver: writer movement %q has not succeeded", movementID)
		}
		result, succeeded := state.MovementResults[movementID]
		if !succeeded || result.ApprovedChangeSetID == "" {
			return nil, fmt.Errorf("driver: writer movement %q has no approved change set", movementID)
		}
		changeSet, recorded := state.ChangeSets[result.AttemptID]
		if !recorded {
			return nil, fmt.Errorf("driver: writer movement %q has no durable change_set.recorded evidence", movementID)
		}
		if changeSet.ChangeSetID != result.ApprovedChangeSetID {
			return nil, fmt.Errorf("driver: writer movement %q approved change set does not match recorded evidence", movementID)
		}
		contributors = append(contributors, workspace.CompositionContributor{
			MovementID: movementID, ChangeSetID: changeSet.ChangeSetID,
			BaseTree: changeSet.BaseTree, ResultTree: changeSet.ResultTree,
		})
	}
	return contributors, nil
}

func candidateOrderedChangeSets(contributors []workspace.CompositionContributor) []string {
	seen := make(map[string]struct{}, len(contributors))
	ordered := make([]string, 0, len(contributors))
	for _, contributor := range contributors {
		if _, duplicate := seen[contributor.ChangeSetID]; duplicate {
			continue
		}
		seen[contributor.ChangeSetID] = struct{}{}
		ordered = append(ordered, contributor.ChangeSetID)
	}
	return ordered
}

// ComposeCandidate materializes the run-scoped candidate. A successful merge
// records the candidate; conflict and no-verdict outcomes append their
// candidate-scoped evidence and terminal run failure atomically.
func ComposeCandidate(
	store *runstore.Store,
	authority *runstore.Driver,
	input runstore.RunInput,
	remainingMS int64,
	now func() time.Time,
	newID func() (string, error),
) error {
	if store == nil || authority == nil || input.Score == nil || now == nil || newID == nil {
		return errors.New("driver: incomplete candidate composition execution")
	}
	contributors, err := CandidateCompositionContributors(input)
	if err != nil {
		return err
	}
	if len(contributors) == 0 {
		if input.Score.Execution().GateWaived {
			err := workspace.AppendWaivedRunSucceeded(store, authority, input, "run.succeeded")
			if errors.Is(err, workspace.ErrCandidateCancelled) {
				return ErrCompositionCancelled
			}
			return err
		}
		_, err := workspace.RecordRecoveredZeroWriterCandidate(store, authority, input)
		if errors.Is(err, workspace.ErrCandidateCancelled) {
			return ErrCompositionCancelled
		}
		return err
	}
	intervalID, err := newID()
	if err != nil {
		return err
	}
	opened := now()
	if _, err := authority.Append(runstate.Event{
		RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision,
		Type: runstate.EventExecutionStarted,
		Payload: mustCompositionPayload(map[string]any{
			"interval_id": intervalID, "phase": "composition", "wall_start": formatTime(opened), "remaining_at_start": remainingMS,
		}),
	}, "execution.composition.started"); err != nil {
		return err
	}
	result := workspace.Compose(workspace.CompositionInput{
		RepositoryRoot: store.RepositoryRoot(), BaseTree: input.BaseTree, Contributors: contributors,
	})
	charged := now().Sub(opened).Milliseconds()
	if charged < 0 {
		charged = 0
	}
	stopped := runstate.Event{
		RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision,
		Type: runstate.EventExecutionStopped,
		Payload: mustCompositionPayload(map[string]any{
			"interval_id": intervalID, "reason": "normal", "charging": "measured", "charged_duration": charged,
		}),
	}
	if result.ResultTree != "" {
		if err := appendCompositionStopped(authority, stopped); err != nil {
			return err
		}
		if input.Score.Execution().GateWaived {
			err := workspace.AppendWaivedRunSucceededComposed(
				store, authority, input, result.ResultTree, candidateOrderedChangeSets(contributors), contributors,
				result.EnvironmentHash, "run.succeeded",
			)
			if errors.Is(err, workspace.ErrCandidateCancelled) {
				return ErrCompositionCancelled
			}
			return err
		}
		_, err := workspace.RecordRecoveredComposedCandidate(
			store, authority, input, result.ResultTree,
			candidateOrderedChangeSets(contributors), contributors, result.EnvironmentHash,
		)
		if errors.Is(err, workspace.ErrCandidateCancelled) {
			return ErrCompositionCancelled
		}
		return err
	}
	subject, err := compositionSubjectHash("candidate", string(authority.RunID()), contributors, result.EnvironmentHash)
	if err != nil {
		return err
	}
	versions, err := identityVersions(canonical.DomainCompositionSubject)
	if err != nil {
		return err
	}
	contributorsValue := compositionContributorsValue(contributors)
	if result.Conflict != nil {
		evidence := runstate.Event{
			RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision,
			Type: runstate.EventCompositionConflicted,
			Payload: mustCompositionPayload(map[string]any{
				"scope": "candidate", "target_id": string(authority.RunID()), "composition_subject_hash": subject,
				"contributors": contributorsValue, "conflicted_paths": stringsToAny(result.Conflict.Paths),
				"composition_algorithm_version": fmt.Sprint(canonical.CompositionAlgorithmVersion), "identity_versions": versions,
			}),
		}
		terminal := runstate.Event{RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision, Type: runstate.EventRunFailed, Payload: mustCompositionPayload(map[string]any{"reason": "composition_unresolvable"})}
		if err := appendCandidateCompositionTerminal(store, authority, stopped, evidence, terminal, "composition.conflicted", "run.failed.composition_unresolvable"); err != nil {
			return err
		}
		return ErrCompositionTerminalized
	}
	if result.Failure == nil {
		return errors.New("driver: candidate composition returned no outcome")
	}
	payload := map[string]any{
		"scope": "candidate", "target_id": string(authority.RunID()), "composition_subject_hash": subject,
		"cause": compositionFailureCause(result), "diagnostic": result.Failure.Diagnostic,
		"contributors": contributorsValue, "composition_algorithm_version": fmt.Sprint(canonical.CompositionAlgorithmVersion), "identity_versions": versions,
	}
	if result.Failure.ExitStatus != nil {
		payload["git_exit_code"] = *result.Failure.ExitStatus
	}
	evidence := runstate.Event{RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision, Type: runstate.EventCompositionFailed, Payload: mustCompositionPayload(payload)}
	terminal := runstate.Event{RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision, Type: runstate.EventRunFailed, Payload: mustCompositionPayload(map[string]any{"reason": "composition_failed"})}
	if err := appendCandidateCompositionTerminal(store, authority, stopped, evidence, terminal, "composition.failed", "run.failed.composition_failed"); err != nil {
		return err
	}
	return ErrCompositionTerminalized
}

func appendCandidateCompositionTerminal(store *runstore.Store, authority *runstore.Driver, stopped, evidence, terminal runstate.Event, evidenceAddress, terminalAddress faultpoint.ReceiptAddress) error {
	return authority.Mutate(func(transaction *runstore.Txn, state runstate.State) error {
		if state.CancelRequested {
			return ErrCompositionCancelled
		}
		next, err := runstate.Apply(state, stopped)
		if err != nil {
			return err
		}
		if _, err := transaction.At("execution.composition.stopped").Append(stopped); err != nil {
			return err
		}
		next, err = runstate.Apply(next, evidence)
		if err != nil {
			return err
		}
		evidenceReceipt, err := transaction.At(evidenceAddress).Append(evidence)
		if err != nil {
			return err
		}
		store.Reached(faultpoint.PointCompositionCandidateEvidence)
		terminal.CausationID = evidenceReceipt.Mutation.EventID
		if _, err := runstate.Apply(next, terminal); err != nil {
			return err
		}
		if _, err := transaction.At(terminalAddress).Append(terminal); err != nil {
			return err
		}
		store.Reached(faultpoint.PointCompositionCandidateTerminal)
		return nil
	})
}
