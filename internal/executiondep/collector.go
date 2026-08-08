package executiondep

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

// CollectedAttempts is the only amendment input for recorded execution
// dependencies. Its storage is private so callers cannot assemble a
// partially journaled attempt and accidentally make §9 fail open.
type CollectedAttempts struct {
	attempts map[runstate.AttemptID]Attempt
}

// Collect reconstructs the §9 execution-dependency inputs from a run's
// durable journal. It intentionally does not add them to runstate: they are
// historical request facts, not E-scoped replay output.
func Collect(store *runstore.Store, runID runstate.RunID) (CollectedAttempts, error) {
	if store == nil {
		return CollectedAttempts{}, errors.New("executiondep: nil store")
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		return CollectedAttempts{}, err
	}
	if journal.TailUnparseable {
		// A torn tail is handled by recovery; §9 must not treat its absent facts
		// as zero values while that recovery work remains outstanding.
		return CollectedAttempts{}, errors.New("executiondep: journal tail requires recovery")
	}
	initial, err := store.LoadInitialScore(runID)
	if err != nil {
		return CollectedAttempts{}, fmt.Errorf("executiondep: load initial score: %w", err)
	}
	run, err := store.LoadRunInput(runID)
	if err != nil {
		return CollectedAttempts{}, fmt.Errorf("executiondep: load resolved cast: %w", err)
	}

	state := runstate.NewState(movementSeed(initial))
	facts := make(map[runstate.AttemptID]*collectedAttempt)
	scores := map[uint64]*score.Score{initial.Revision(): initial}
	for _, event := range journal.Events {
		switch event.Type {
		case runstate.EventPerformerSelected:
			value, err := selectedAttempt(event, run.Cast, state, scores, store, runID)
			if err != nil {
				return CollectedAttempts{}, err
			}
			facts[event.AttemptID] = value
		case runstate.EventAttemptStarted:
			value, ok := facts[event.AttemptID]
			if !ok {
				return CollectedAttempts{}, missing(event.AttemptID, "performer.selected")
			}
			if err := value.started(event); err != nil {
				return CollectedAttempts{}, err
			}
		case runstate.EventAdapterProbed:
			value, ok := facts[event.AttemptID]
			if !ok {
				return CollectedAttempts{}, missing(event.AttemptID, "performer.selected")
			}
			if err := value.probed(event); err != nil {
				return CollectedAttempts{}, err
			}
		}

		next, err := runstate.Apply(state, event)
		if err != nil {
			return CollectedAttempts{}, fmt.Errorf("executiondep: replay seq=%d: %w", event.Seq, err)
		}
		state = next
	}

	result := CollectedAttempts{attempts: make(map[runstate.AttemptID]Attempt)}
	for id := range state.Attempts {
		if !eligible(state, id) {
			continue
		}
		value, ok := facts[id]
		if !ok {
			return CollectedAttempts{}, missing(id, "performer.selected")
		}
		attempt, err := value.complete()
		if err != nil {
			return CollectedAttempts{}, err
		}
		result.attempts[id] = attempt
	}
	return result, nil
}

// Changed applies §9's recorded-attempt comparison. A zero CollectedAttempts
// is valid only when replay contains no eligible attempt; otherwise absence is
// a recovery halt rather than evidence that no dependency changed.
func (collected CollectedAttempts) Changed(patched *score.Score, state runstate.State) (bool, error) {
	for id, projected := range state.Attempts {
		if !eligible(state, id) {
			continue
		}
		if !scoreHasMovement(patched, projected.MovementID) {
			return true, nil
		}
		attempt, ok := collected.attempts[id]
		if !ok {
			return false, missing(id, "collected execution dependency")
		}
		if attempt.RecordedHash == "" {
			return false, missing(id, "adapter.probed.execution_dependency_hash")
		}
		same, err := Equal(patched, attempt)
		if err != nil {
			return false, err
		}
		if !same {
			return true, nil
		}
	}
	return false, nil
}

// eligible is DESIGN §9's completed successful non-superseded predicate:
// COMPLETED on a SUCCEEDED movement. SUPERSEDED, FAILED, BLOCKED, and a
// completed-but-unsuccessful attempt all fail this one predicate.
func eligible(state runstate.State, attemptID runstate.AttemptID) bool {
	attempt, ok := state.Attempts[attemptID]
	return ok && attempt.State == runstate.AttemptCompleted && state.Movements[attempt.MovementID] == runstate.MovementSucceeded
}

type collectedAttempt struct {
	attempt              Attempt
	startSeen, probeSeen bool
	reviewRequired       bool
}

func selectedAttempt(
	event runstate.Event,
	resolved *cast.Cast,
	state runstate.State,
	scores map[uint64]*score.Score,
	store *runstore.Store,
	runID runstate.RunID,
) (*collectedAttempt, error) {
	payload, err := payload(event)
	if err != nil {
		return nil, err
	}
	compiled, ok := scores[event.ScoreRevision]
	if !ok {
		compiled, err = store.LoadScoreSnapshot(runID, event.ScoreRevision)
		if err != nil {
			return nil, fmt.Errorf("executiondep: attempt %q score revision %d: %w", event.AttemptID, event.ScoreRevision, err)
		}
		scores[event.ScoreRevision] = compiled
	}
	movement, ok := movementByID(compiled, event.MovementID)
	if !ok {
		return nil, fmt.Errorf("executiondep: attempt %q movement %q is absent from revision %d", event.AttemptID, event.MovementID, event.ScoreRevision)
	}
	performerID, _ := payload["performer_id"].(string)
	adapterID, _ := payload["adapter_id"].(string)
	model, _ := payload["model"].(string)
	performer, ok := resolved.Performer(performerID)
	if !ok {
		return nil, missing(event.AttemptID, "resolved cast performer")
	}
	if performer.Adapter != adapterID || performer.Model != model {
		return nil, fmt.Errorf("executiondep: attempt %q performer.selected disagrees with resolved cast", event.AttemptID)
	}
	inputs, err := inputsAtSelection(compiled, movement, state)
	if err != nil {
		return nil, fmt.Errorf("executiondep: attempt %q: %w", event.AttemptID, err)
	}
	value := &collectedAttempt{
		attempt:        Attempt{ID: event.AttemptID, MovementID: event.MovementID, AdapterID: adapterID, Model: model, Inputs: inputs},
		reviewRequired: movement.Acceptance.HasReviewCriteria,
	}
	// Read the selected performer's adapter entry, not score.extensions. The
	// similarly named score block is not what the driver sent and would hash a
	// different request identity without any type or replay error.
	if extension, present := performer.Extensions[adapterID]; present {
		value.attempt.Extensions = map[string]any{adapterID: extension}
	}
	return value, nil
}

func (value *collectedAttempt) started(event runstate.Event) error {
	payload, err := payload(event)
	if err != nil {
		return err
	}
	granted, err := decodeGrants(payload["granted_authority"])
	if err != nil {
		return fmt.Errorf("executiondep: attempt %q granted_authority: %w", event.AttemptID, err)
	}
	value.attempt.GrantedAuthority = granted
	if hash, present := payload["base_composition_hash"].(string); present {
		value.attempt.BaseCompositionHash = runstate.Hash(hash)
	}
	if value.reviewRequired {
		review, err := reviewInput(event, payload)
		if err != nil {
			return err
		}
		value.attempt.Inputs = append(value.attempt.Inputs, review)
		slices.SortFunc(value.attempt.Inputs, func(left, right protocol.ArtifactRef) int {
			return strings.Compare(left.ArtifactID, right.ArtifactID)
		})
	}
	value.startSeen = true
	return nil
}

func (value *collectedAttempt) probed(event runstate.Event) error {
	payload, err := payload(event)
	if err != nil {
		return err
	}
	observed, _ := payload["execution_dependency_hash"].(string)
	versions, err := json.Marshal(payload["identity_versions"])
	if err != nil {
		return fmt.Errorf("executiondep: attempt %q identity_versions: %w", event.AttemptID, err)
	}
	value.attempt.RecordedHash = runstate.Hash(observed)
	value.attempt.IdentityVersions = versions
	value.attempt.DeliveredResolutions = deliveredResolutions(payload["delivered_resolutions"])
	value.attempt.DeliveredFeedback = deliveredFeedback(payload["delivered_feedback"])
	value.probeSeen = true
	return nil
}

func (value *collectedAttempt) complete() (Attempt, error) {
	if !value.startSeen {
		return Attempt{}, missing(value.attempt.ID, "attempt.started")
	}
	if !value.probeSeen {
		return Attempt{}, missing(value.attempt.ID, "adapter.probed")
	}
	if value.attempt.RecordedHash == "" {
		return Attempt{}, missing(value.attempt.ID, "adapter.probed.execution_dependency_hash")
	}
	if len(value.attempt.IdentityVersions) == 0 {
		return Attempt{}, missing(value.attempt.ID, "adapter.probed.identity_versions")
	}
	return value.attempt, nil
}

func inputsAtSelection(compiled *score.Score, movement score.MovementView, state runstate.State) ([]protocol.ArtifactRef, error) {
	outputs := make(map[string]score.OutputView)
	producers := make(map[string]runstate.MovementID)
	for _, candidate := range compiled.Movements() {
		for _, output := range candidate.Outputs {
			outputs[output.ArtifactID] = output
			producers[output.ArtifactID] = runstate.MovementID(candidate.ID)
		}
	}
	inputs := make([]protocol.ArtifactRef, 0, len(movement.Inputs))
	for _, artifactID := range movement.Inputs {
		output, exists := outputs[artifactID]
		if !exists {
			return nil, fmt.Errorf("input %q has no declared output", artifactID)
		}
		if output.Kind == "change_set" {
			continue // A.5 excludes change-set inputs, as does the execute request.
		}
		result, exists := state.MovementResults[producers[artifactID]]
		if !exists || result.AttemptID == "" {
			return nil, fmt.Errorf("input %q has no successful producer at selection", artifactID)
		}
		instanceID := runstate.ArtifactInstanceID(artifactID + "@" + string(result.AttemptID))
		record, exists := state.Artifacts[instanceID]
		if !exists || record.LogicalOutputID != artifactID || record.Kind != output.Kind {
			return nil, fmt.Errorf("input %q has no selected artifact instance at selection", artifactID)
		}
		inputs = append(inputs, protocol.ArtifactRef{ArtifactID: artifactID, Kind: output.Kind, InstanceID: string(instanceID), Hash: string(record.ContentHash)})
	}
	slices.SortFunc(inputs, func(left, right protocol.ArtifactRef) int {
		return strings.Compare(left.ArtifactID, right.ArtifactID)
	})
	return inputs, nil
}

func reviewInput(event runstate.Event, payload map[string]any) (protocol.ArtifactRef, error) {
	raw, ok := payload["review_subject_input"].(map[string]any)
	if !ok {
		// A review criterion requires this attempt.started commitment (§4); it
		// is not an empty input and therefore halts collection when absent.
		return protocol.ArtifactRef{}, missing(event.AttemptID, "attempt.started.review_subject_input")
	}
	instanceID, _ := raw["instance_id"].(string)
	hash, _ := raw["hash"].(string)
	if instanceID == "" || hash == "" {
		return protocol.ArtifactRef{}, missing(event.AttemptID, "attempt.started.review_subject_input")
	}
	return protocol.ArtifactRef{ArtifactID: "partitur.subject-tree", Kind: "partitur/subject-tree+json;v=1", InstanceID: instanceID, Hash: hash}, nil
}

func decodeGrants(raw any) (protocol.Grants, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return protocol.Grants{}, err
	}
	var grants protocol.Grants
	if err := json.Unmarshal(encoded, &grants); err != nil {
		return protocol.Grants{}, err
	}
	return grants, nil
}

func payload(event runstate.Event) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal(event.Payload, &value); err != nil {
		return nil, fmt.Errorf("executiondep: attempt %q payload: %w", event.AttemptID, err)
	}
	return value, nil
}

func deliveredResolutions(raw any) []runstate.DeliveredResolution {
	values, _ := raw.([]any)
	result := make([]runstate.DeliveredResolution, 0, len(values))
	for _, raw := range values {
		value, _ := raw.(map[string]any)
		decisionID, _ := value["decision_id"].(string)
		kind, _ := value["kind"].(string)
		digest, _ := value["digest"].(string)
		result = append(result, runstate.DeliveredResolution{DecisionID: decisionID, Kind: kind, Digest: runstate.Hash(digest)})
	}
	return result
}

func deliveredFeedback(raw any) []runstate.DeliveredFeedback {
	values, _ := raw.([]any)
	result := make([]runstate.DeliveredFeedback, 0, len(values))
	for _, raw := range values {
		value, _ := raw.(map[string]any)
		attemptID, _ := value["previous_attempt_id"].(string)
		kind, _ := value["kind"].(string)
		instanceID, _ := value["artifact_instance_id"].(string)
		hash, _ := value["content_hash"].(string)
		result = append(result, runstate.DeliveredFeedback{PreviousAttemptID: runstate.AttemptID(attemptID), Kind: kind, ArtifactInstanceID: runstate.ArtifactInstanceID(instanceID), ContentHash: runstate.Hash(hash)})
	}
	return result
}

func movementSeed(compiled *score.Score) []runstate.MovementSeed {
	execution := compiled.Execution()
	result := make([]runstate.MovementSeed, 0, len(compiled.Movements()))
	for _, movement := range compiled.Movements() {
		result = append(result, runstate.MovementSeed{
			ID: runstate.MovementID(movement.ID), Initial: runstate.InitialMovementState(compiled.Status(), movement.Phase),
			RepoWrite: hasGrant(movement.Grants, "repo_write"), HasDependencies: len(movement.Needs) != 0,
			Final: movement.ID == execution.FinalMovementID,
		})
	}
	return result
}

func hasGrant(grants []string, want string) bool {
	return slices.Contains(grants, want)
}

func scoreHasMovement(compiled *score.Score, id runstate.MovementID) bool {
	for _, movement := range compiled.Movements() {
		if movement.ID == string(id) {
			return true
		}
	}
	return false
}

func missing(attemptID runstate.AttemptID, fact string) error {
	return fmt.Errorf("executiondep: attempt %q is missing durable %s", attemptID, fact)
}
