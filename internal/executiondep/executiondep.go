// Package executiondep recomputes the Appendix A.5 request identity for an
// already-recorded attempt. It is deliberately independent of the driver so
// amendment evaluation and live execution cannot form an import cycle.
package executiondep

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

// Attempt is the durable, attempt-specific input that is intentionally not
// retained by runstate's E-scoped projection. A collector reads these values
// from performer.selected and attempt.started; delivered selections and the
// recorded tuple come from adapter.probed.
type Attempt struct {
	ID                   runstate.AttemptID
	MovementID           runstate.MovementID
	AdapterID            string
	Model                string
	Extensions           map[string]any
	GrantedAuthority     protocol.Grants
	BaseCompositionHash  runstate.Hash
	Inputs               []protocol.ArtifactRef
	DeliveredResolutions []runstate.DeliveredResolution
	DeliveredFeedback    []runstate.DeliveredFeedback
	IdentityVersions     json.RawMessage
	RecordedHash         runstate.Hash
}

// Recompute applies the projector selected by the recorded A.5 tuple to a
// patched score. This binary implements v3 only; an unsupported outer or inner
// projector fails closed rather than being relabelled with current rules.
func Recompute(compiled *score.Score, attempt Attempt) (runstate.Hash, error) {
	if compiled == nil || attempt.ID == "" || attempt.MovementID == "" ||
		attempt.AdapterID == "" || attempt.Model == "" || len(attempt.IdentityVersions) == 0 {
		return "", errors.New("executiondep: incomplete attempt input")
	}
	tuple, err := decodeRecordedTuple(attempt.IdentityVersions)
	if err != nil {
		return "", err
	}
	switch tuple.Projections[string(canonical.DomainExecutionDependency)] {
	case canonical.ProjectionVersionExecutionDependency:
		return recomputeV3(compiled, attempt, tuple)
	default:
		return "", unsupportedTuple(canonical.DomainExecutionDependency, tuple)
	}
}

type recordedTuple struct {
	CanonicalEncoding int            `json:"canonical_encoding"`
	Projections       map[string]int `json:"projections"`
}

func decodeRecordedTuple(recorded json.RawMessage) (recordedTuple, error) {
	var tuple recordedTuple
	if err := json.Unmarshal(recorded, &tuple); err != nil {
		return recordedTuple{}, fmt.Errorf("executiondep: decode identity_versions: %w", err)
	}
	if tuple.Projections == nil {
		return recordedTuple{}, fmt.Errorf("executiondep: decode identity_versions: projections is absent")
	}
	return tuple, nil
}

func recomputeV3(compiled *score.Score, attempt Attempt, tuple recordedTuple) (runstate.Hash, error) {
	movement, ok := movementByID(compiled, attempt.MovementID)
	if !ok {
		return "", fmt.Errorf("executiondep: movement %q is absent", attempt.MovementID)
	}
	part, ok := partByID(compiled, movement.PartID)
	if !ok {
		return "", fmt.Errorf("executiondep: part %q is absent", movement.PartID)
	}
	plan, err := acceptance.Compile(movement)
	if err != nil {
		return "", fmt.Errorf("executiondep: compile acceptance: %w", err)
	}
	value, err := projection(compiled, movement, part, attempt, plan.Hash())
	if err != nil {
		return "", err
	}
	domains, err := V3ProjectionDomains(value)
	if err != nil {
		return "", err
	}
	if err := tuple.requireV3(domains); err != nil {
		return "", err
	}
	// This call is reached only through the recorded v3 dispatcher above. All
	// reached inner projectors have already been checked against that tuple.
	hash, err := canonical.Hash(canonical.DomainExecutionDependency, value)
	return runstate.Hash(hash), err
}

func (tuple recordedTuple) requireV3(domains []canonical.Domain) error {
	for _, domain := range domains {
		want, err := canonical.CurrentVersions(domain)
		if err != nil {
			return err
		}
		if tuple.CanonicalEncoding != want.CanonicalEncoding || tuple.Projections[string(domain)] != want.Projection {
			return unsupportedTuple(domain, tuple)
		}
	}
	return nil
}

func unsupportedTuple(domain canonical.Domain, tuple recordedTuple) error {
	return fmt.Errorf("%w: domain=%q canonical_encoding=%d projection=%d", canonical.ErrUnsupportedRunFormat,
		domain, tuple.CanonicalEncoding, tuple.Projections[string(domain)])
}

func projection(compiled *score.Score, movement score.MovementView, part score.PartView, attempt Attempt, acceptanceHash runstate.Hash) (map[string]any, error) {
	inputs := make([]any, len(attempt.Inputs))
	for index, input := range attempt.Inputs {
		inputs[index] = map[string]any{"artifact_id": input.ArtifactID, "kind": input.Kind, "instance_id": input.InstanceID, "content_hash": input.Hash}
	}
	outputs := make([]any, len(movement.Outputs))
	for index, output := range movement.Outputs {
		outputs[index] = map[string]any{"artifact_id": output.ArtifactID, "kind": output.Kind}
	}
	resolutions := make([]any, len(attempt.DeliveredResolutions))
	for index, value := range attempt.DeliveredResolutions {
		resolutions[index] = map[string]any{"decision_id": value.DecisionID, "kind": value.Kind, "digest": string(value.Digest)}
	}
	feedback := make([]any, len(attempt.DeliveredFeedback))
	for index, value := range attempt.DeliveredFeedback {
		feedback[index] = map[string]any{"previous_attempt_id": string(value.PreviousAttemptID), "kind": value.Kind, "artifact_instance_id": string(value.ArtifactInstanceID), "content_hash": string(value.ContentHash)}
	}
	sort.Slice(feedback, func(left, right int) bool {
		l, r := feedback[left].(map[string]any), feedback[right].(map[string]any)
		if l["previous_attempt_id"] == r["previous_attempt_id"] {
			return l["artifact_instance_id"].(string) < r["artifact_instance_id"].(string)
		}
		return l["previous_attempt_id"].(string) < r["previous_attempt_id"].(string)
	})
	movementValue := map[string]any{
		"id": movement.ID, "part": movement.PartID, "instruction": movement.Instruction,
		"needs": stringsValue(movement.Needs), "inputs": inputs, "outputs": outputs,
		"grants": stringsValue(movement.Grants), "may_propose": movement.MayPropose,
		"acceptance": string(acceptanceHash),
	}
	if movement.MayPropose {
		hash, err := compiled.Hash()
		if err != nil {
			return nil, err
		}
		movementValue["score_base_hash"] = hash
	}
	if movement.Phase == "draft" {
		movementValue["phase"] = "draft"
	}
	if attempt.BaseCompositionHash != "" {
		movementValue["base_composition_hash"] = string(attempt.BaseCompositionHash)
	}
	execution := compiled.Execution()
	global := map[string]any{
		"resolved_questions":     resolvedQuestions(compiled.ResolvedQuestions()),
		"effective_paths":        map[string]any{"rw": stringsValue(attempt.GrantedAuthority.PathsRW), "ro": stringsValue(attempt.GrantedAuthority.PathsRO)},
		"side_effects_permitted": []any{},
		"protected_paths":        []any{".partitur/**", "partitur.yaml", "refs/partitur/**"},
	}
	scoreValue := map[string]any{"goal": execution.Goal, "global_invariants": global, "verification_expectation_intent": execution.VerificationExpectation}
	if execution.ContextPresent {
		scoreValue["context"] = execution.Context
	}
	value := map[string]any{
		"actual_adapter_id": attempt.AdapterID,
		"movement":          movementValue,
		"part":              map[string]any{"capabilities": stringsValue(part.Capabilities), "read_only": part.ReadOnly},
		"model":             attempt.Model,
		"authority":         map[string]any{"paths_rw": stringsValue(attempt.GrantedAuthority.PathsRW), "paths_ro": stringsValue(attempt.GrantedAuthority.PathsRO), "shell": attempt.GrantedAuthority.Shell, "network": attempt.GrantedAuthority.Network, "side_effects": stringsValue(compiled.EffectivePolicy().SideEffects)},
		"score":             scoreValue, "resolved_decisions": resolutions, "feedback": feedback,
	}
	if extension, ok := attempt.Extensions[attempt.AdapterID]; ok {
		value["extensions"] = extension
	}
	return value, nil
}

// V3ProjectionDomains derives the recorded closure from the exact A.5 value.
// Driver uses this same selector when it writes adapter.probed, so the producer
// and step-8 dispatcher cannot carry independent domain lists.
func V3ProjectionDomains(value map[string]any) ([]canonical.Domain, error) {
	movement, ok := value["movement"].(map[string]any)
	if !ok {
		return nil, errors.New("executiondep: v3 projection has no movement")
	}
	resolutions, ok := value["resolved_decisions"].([]any)
	if !ok {
		return nil, errors.New("executiondep: v3 projection has no resolved decisions")
	}

	// A.5 always reaches the outer identity and the compiled acceptance chain.
	// Optional domains follow the fields that the v3 projection actually emits.
	domains := []canonical.Domain{
		canonical.DomainExecutionDependency,
		canonical.DomainAcceptanceSpec,
		canonical.DomainCriterionSpec,
	}
	// A composition hash is absent when this attempt has no composed base.
	if _, present := movement["base_composition_hash"]; present {
		domains = append(domains, canonical.DomainMovementComposition)
	}
	// Score is reached only when A.5 emitted may_propose's score-base hash.
	if _, present := movement["score_base_hash"]; present {
		domains = append(domains, canonical.DomainScore)
	}
	// Resolution bodies are reached only when their delivered digests replay.
	if len(resolutions) != 0 {
		domains = append(domains, canonical.DomainResolutionBody)
	}
	return domains, nil
}

func movementByID(compiled *score.Score, id runstate.MovementID) (score.MovementView, bool) {
	for _, value := range compiled.Movements() {
		if value.ID == string(id) {
			return value, true
		}
	}
	return score.MovementView{}, false
}
func partByID(compiled *score.Score, id string) (score.PartView, bool) {
	for _, value := range compiled.Parts() {
		if value.ID == id {
			return value, true
		}
	}
	return score.PartView{}, false
}
func stringsValue(values []string) []any {
	output := make([]any, len(values))
	for index := range values {
		output[index] = values[index]
	}
	return output
}
func resolvedQuestions(values []score.ResolvedQuestionView) []any {
	output := make([]any, len(values))
	for index, value := range values {
		entry := map[string]any{"id": value.ID, "question": value.Question}
		if value.ResolutionPresent {
			entry["disposition"] = "resolved"
			entry["resolution"] = value.Resolution
		} else {
			entry["disposition"] = "waived"
		}
		output[index] = entry
	}
	return output
}

// Equal reports whether a recorded attempt remains compatible with compiled.
func Equal(compiled *score.Score, attempt Attempt) (bool, error) {
	got, err := Recompute(compiled, attempt)
	return got == attempt.RecordedHash, err
}
