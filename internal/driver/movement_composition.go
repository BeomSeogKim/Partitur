package driver

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

// ErrCompositionTerminalized reports that composition appended the movement's
// terminal event, so callers must not try to create an attempt for it.
var ErrCompositionTerminalized = errors.New("driver: composition terminal event appended")

// ErrCompositionCancelled reports that a durable cancellation preempted a
// composition outcome before it could become terminal.
var ErrCompositionCancelled = errors.New("driver: composition cancelled before terminal append")

type MovementBase struct {
	Commit string
	Tree   string
	Hash   string
}

// stableTopologicalMovementIDs orders a score subset with declaration order as
// the tie breaker. MovementView.Needs is canonicalized and is therefore not an
// ordering source. Both movement-base and candidate composition use it before
// handing contributors to workspace.Compose.
func stableTopologicalMovementIDs(movements []score.MovementView, included map[runstate.MovementID]bool) ([]runstate.MovementID, error) {
	byID := make(map[runstate.MovementID]score.MovementView, len(movements))
	for _, movement := range movements {
		byID[runstate.MovementID(movement.ID)] = movement
	}
	indegree := make(map[runstate.MovementID]int, len(included))
	children := make(map[runstate.MovementID][]runstate.MovementID, len(included))
	for id := range included {
		movement, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("driver: dependency movement %q is absent from pinned score", id)
		}
		for _, need := range movement.Needs {
			dependency := runstate.MovementID(need)
			if included[dependency] {
				indegree[id]++
				children[dependency] = append(children[dependency], id)
			}
		}
	}
	ordered := make([]runstate.MovementID, 0, len(included))
	emitted := make(map[runstate.MovementID]bool, len(included))
	for len(ordered) != len(included) {
		selected := runstate.MovementID("")
		for _, movement := range movements {
			id := runstate.MovementID(movement.ID)
			if included[id] && indegree[id] == 0 && !emitted[id] {
				selected = id
				break
			}
		}
		if selected == "" {
			return nil, errors.New("driver: dependency graph is not acyclic")
		}
		ordered = append(ordered, selected)
		emitted[selected] = true
		for _, child := range children[selected] {
			indegree[child]--
		}
	}
	return ordered, nil
}

// movementCompositionContributors bridges each dependency's approved result
// through its producing attempt id.
func movementCompositionContributors(compiled *score.Score, state runstate.State, target runstate.MovementID) ([]workspace.CompositionContributor, error) {
	if compiled == nil || target == "" {
		return nil, errors.New("driver: incomplete movement composition input")
	}
	movements := compiled.Movements()
	byID := make(map[runstate.MovementID]score.MovementView, len(movements))
	for _, movement := range movements {
		byID[runstate.MovementID(movement.ID)] = movement
	}
	targetMovement, ok := byID[target]
	if !ok {
		return nil, fmt.Errorf("driver: movement %q is absent from pinned score", target)
	}
	closure := map[runstate.MovementID]bool{}
	var visit func(runstate.MovementID) error
	visit = func(id runstate.MovementID) error {
		if closure[id] {
			return nil
		}
		movement, ok := byID[id]
		if !ok {
			return fmt.Errorf("driver: dependency movement %q is absent from pinned score", id)
		}
		closure[id] = true
		for _, need := range movement.Needs {
			if err := visit(runstate.MovementID(need)); err != nil {
				return err
			}
		}
		return nil
	}
	for _, need := range targetMovement.Needs {
		if err := visit(runstate.MovementID(need)); err != nil {
			return nil, err
		}
	}
	ordered, err := stableTopologicalMovementIDs(movements, closure)
	if err != nil {
		return nil, err
	}
	contributors := make([]workspace.CompositionContributor, 0, len(ordered))
	for _, movementID := range ordered {
		result, succeeded := state.MovementResults[movementID]
		if !succeeded {
			return nil, fmt.Errorf("driver: dependency movement %q has no approved result", movementID)
		}
		if result.ApprovedChangeSetID == "" {
			continue
		}
		changeSet, recorded := state.ChangeSets[result.AttemptID]
		if !recorded {
			return nil, fmt.Errorf("driver: dependency movement %q approved change set is absent for attempt %q", movementID, result.AttemptID)
		}
		if changeSet.ChangeSetID != result.ApprovedChangeSetID {
			return nil, fmt.Errorf("driver: dependency movement %q approved change set does not match attempt record", movementID)
		}
		contributors = append(contributors, workspace.CompositionContributor{MovementID: movementID, ChangeSetID: changeSet.ChangeSetID, BaseTree: changeSet.BaseTree, ResultTree: changeSet.ResultTree})
	}
	return contributors, nil
}

func PrepareMovementBase(store *runstore.Store, authority *runstore.Driver, input runstore.RunInput, movementID runstate.MovementID, remainingMS int64, now func() time.Time, newID func() (string, error)) (MovementBase, error) {
	contributors, err := movementCompositionContributors(input.Score, input.Projection.State, movementID)
	if err != nil {
		return MovementBase{}, err
	}
	if len(contributors) == 0 {
		hash, err := movementCompositionDependencyHash(string(movementID), input.BaseTree)
		return MovementBase{Tree: input.BaseTree, Hash: hash}, err
	}
	return composeMovementBase(store, authority, input, movementID, contributors, remainingMS, now, newID)
}

func composeMovementBase(store *runstore.Store, authority *runstore.Driver, input runstore.RunInput, movementID runstate.MovementID, contributors []workspace.CompositionContributor, remainingMS int64, now func() time.Time, newID func() (string, error)) (MovementBase, error) {
	return composeMovementBaseWithAfterEvidence(store, authority, input, movementID, contributors, remainingMS, now, newID, nil)
}

func composeMovementBaseWithAfterEvidence(store *runstore.Store, authority *runstore.Driver, input runstore.RunInput, movementID runstate.MovementID, contributors []workspace.CompositionContributor, remainingMS int64, now func() time.Time, newID func() (string, error), afterEvidence func()) (MovementBase, error) {
	if err := incompleteMovementCompositionExecution(store, authority, input, contributors, now, newID); err != nil {
		return MovementBase{}, err
	}
	intervalID, err := newID()
	if err != nil {
		return MovementBase{}, err
	}
	opened := now()
	if _, err := authority.Append(runstate.Event{RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision, Type: runstate.EventExecutionStarted, Payload: mustCompositionPayload(map[string]any{"interval_id": intervalID, "phase": "composition", "wall_start": formatTime(opened), "remaining_at_start": remainingMS})}, "execution.composition.started"); err != nil {
		return MovementBase{}, err
	}
	result := workspace.Compose(workspace.CompositionInput{RepositoryRoot: store.RepositoryRoot(), BaseTree: input.BaseTree, Contributors: contributors})
	charged := now().Sub(opened).Milliseconds()
	if charged < 0 {
		charged = 0
	}
	stopped := runstate.Event{RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision, Type: runstate.EventExecutionStopped, Payload: mustCompositionPayload(map[string]any{"interval_id": intervalID, "reason": "normal", "charging": "measured", "charged_duration": charged})}
	if result.ResultTree != "" {
		if err := appendCompositionStopped(authority, stopped); err != nil {
			return MovementBase{}, err
		}
		hash, err := movementCompositionMergeDependencyHash(movementID, input.BaseTree, contributors, result.EnvironmentHash)
		if err != nil {
			return MovementBase{}, err
		}
		commit, err := workspace.PinMovementBase(store, authority, input, movementID, result.ResultTree)
		if err != nil {
			return MovementBase{}, err
		}
		return MovementBase{Commit: commit, Tree: result.ResultTree, Hash: hash}, nil
	}
	subject, err := compositionSubjectHash("movement", string(movementID), contributors, result.EnvironmentHash)
	if err != nil {
		return MovementBase{}, err
	}
	versions, err := identityVersions(canonical.DomainCompositionSubject)
	if err != nil {
		return MovementBase{}, err
	}
	contributorsValue := compositionContributorsValue(contributors)
	if result.Conflict != nil {
		evidence := runstate.Event{RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision, MovementID: movementID, Type: runstate.EventCompositionConflicted, Payload: mustCompositionPayload(map[string]any{"scope": "movement", "target_id": string(movementID), "composition_subject_hash": subject, "contributors": contributorsValue, "conflicted_paths": stringsToAny(result.Conflict.Paths), "composition_algorithm_version": fmt.Sprint(canonical.CompositionAlgorithmVersion), "identity_versions": versions})}
		terminal := runstate.Event{RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision, MovementID: movementID, Type: runstate.EventMovementFailed, Payload: mustCompositionPayload(map[string]any{"reason": "composition_unresolvable", "run_failed": false})}
		if err := appendMovementCompositionTerminal(store, authority, stopped, evidence, terminal, "execution.composition.stopped", "composition.conflicted", "movement.failed.composition_unresolvable", afterEvidence); err != nil {
			return MovementBase{}, err
		}
		return MovementBase{}, ErrCompositionTerminalized
	}
	payload := map[string]any{"scope": "movement", "target_id": string(movementID), "composition_subject_hash": subject, "cause": compositionFailureCause(result), "diagnostic": result.Failure.Diagnostic, "contributors": contributorsValue, "composition_algorithm_version": fmt.Sprint(canonical.CompositionAlgorithmVersion), "identity_versions": versions}
	if result.Failure.ExitStatus != nil {
		payload["git_exit_code"] = *result.Failure.ExitStatus
	}
	evidence := runstate.Event{RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision, MovementID: movementID, Type: runstate.EventCompositionFailed, Payload: mustCompositionPayload(payload)}
	terminal := runstate.Event{RunID: authority.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision, MovementID: movementID, Type: runstate.EventMovementFailed, Payload: mustCompositionPayload(map[string]any{"reason": "composition_failed", "run_failed": false})}
	if err := appendMovementCompositionTerminal(store, authority, stopped, evidence, terminal, "execution.composition.stopped", "composition.failed", "movement.failed.composition_failed", afterEvidence); err != nil {
		return MovementBase{}, err
	}
	return MovementBase{}, ErrCompositionTerminalized
}

func appendCompositionStopped(authority *runstore.Driver, stopped runstate.Event) error {
	return authority.Mutate(func(transaction *runstore.Txn, state runstate.State) error {
		if state.CancelRequested {
			return ErrCompositionCancelled
		}
		if _, err := runstate.Apply(state, stopped); err != nil {
			return err
		}
		_, err := transaction.At("execution.composition.stopped").Append(stopped)
		return err
	})
}

func appendMovementCompositionTerminal(store *runstore.Store, authority *runstore.Driver, stopped, evidence, terminal runstate.Event, stoppedAddress, evidenceAddress, terminalAddress faultpoint.ReceiptAddress, afterEvidence func()) error {
	return authority.Mutate(func(transaction *runstore.Txn, state runstate.State) error {
		if state.CancelRequested {
			return ErrCompositionCancelled
		}
		next, err := runstate.Apply(state, stopped)
		if err != nil {
			return err
		}
		if _, err := transaction.At(stoppedAddress).Append(stopped); err != nil {
			return err
		}
		state = next
		next, err = runstate.Apply(state, evidence)
		if err != nil {
			return err
		}
		evidenceReceipt, err := transaction.At(evidenceAddress).Append(evidence)
		if err != nil {
			return err
		}
		store.Reached(faultpoint.PointCompositionMovementEvidence)
		if afterEvidence != nil {
			afterEvidence()
		}
		terminal.CausationID = evidenceReceipt.Mutation.EventID
		if _, err := runstate.Apply(next, terminal); err != nil {
			return err
		}
		if _, err := transaction.At(terminalAddress).Append(terminal); err != nil {
			return err
		}
		store.Reached(faultpoint.PointCompositionMovementTerminal)
		return nil
	})
}

func incompleteMovementCompositionExecution(store *runstore.Store, authority *runstore.Driver, input runstore.RunInput, contributors []workspace.CompositionContributor, now func() time.Time, newID func() (string, error)) error {
	missing := make([]string, 0, 5)
	if store == nil {
		missing = append(missing, "store")
	}
	if authority == nil {
		missing = append(missing, "authority")
	}
	if len(contributors) == 0 {
		missing = append(missing, "contributors")
	}
	if now == nil {
		missing = append(missing, "now")
	}
	if newID == nil {
		missing = append(missing, "newID")
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("driver: incomplete movement composition execution: missing %s", strings.Join(missing, ", "))
}

func compositionFailureCause(result workspace.CompositionResult) string {
	if result.Failure.ExitStatus != nil {
		return "git_exit"
	}
	if result.Environment == nil {
		return "spawn_failed"
	}
	if result.Failure.Diagnostic == "reject merge drivers: external merge driver is unsupported" {
		return "driver_rejected"
	}
	return "git_signalled"
}

func compositionSubjectHash(scope, target string, contributors []workspace.CompositionContributor, environment string) (string, error) {
	value := map[string]any{"scope": scope, "target_id": target, "contributors": make([]any, len(contributors)), "composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion)}
	for i, contributor := range contributors {
		value["contributors"].([]any)[i] = contributor.ChangeSetID
	}
	if environment != "" {
		value["composition_environment_hash"] = environment
	}
	return canonical.Hash(canonical.DomainCompositionSubject, value)
}

func compositionContributorsValue(contributors []workspace.CompositionContributor) []any {
	value := make([]any, len(contributors))
	for i, contributor := range contributors {
		value[i] = map[string]any{"movement_id": string(contributor.MovementID), "change_set_id": contributor.ChangeSetID}
	}
	return value
}
func mustCompositionPayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
