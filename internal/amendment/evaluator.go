// Package amendment implements the pure §9 admissibility and approval-policy
// evaluation. Durable proposal records and preparation are intentionally left
// to its callers.
package amendment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/executiondep"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

type Kind string

const (
	Rejected Kind = "rejected"
	Routed   Kind = "routed"
	Approved Kind = "approved"
)

type EnvelopeClass string

const (
	NarrowPaths    EnvelopeClass = "NARROW_PATHS"
	NarrowGrants   EnvelopeClass = "NARROW_GRANTS"
	BudgetDecrease EnvelopeClass = "BUDGET_DECREASE"
)

// Input is the complete value boundary for one §9 evaluation. State is the
// E-scoped projection. Attempts are a collector-owned supplement: it joins
// attempt state to journaled selected-performer and attempt-start facts.
type Input struct {
	State         runstate.State
	Base          *score.Score
	BaseRevision  uint64
	BaseHash      runstate.Hash
	Operations    []any
	ClaimedImpact score.Impact
	// HasClaimedImpact distinguishes an absent optional claim from a present,
	// deliberately empty claim. Only the latter participates in containment.
	HasClaimedImpact bool
	Attempts         []executiondep.Attempt
	// HumanDecision selects the decision-time §9 re-run. It preserves steps
	// 1–9, but records envelope guards as audit facts instead of routing or
	// blocking the already-deciding human.
	HumanDecision bool
}

// Outcome is one deterministic result. Patched and Impact are populated once
// steps 4–7 complete, including on later feasibility rejections and routes.
type Outcome struct {
	Kind      Kind
	Reason    string
	Condition string
	Class     EnvelopeClass
	GuardPass bool
	Patched   *score.Score
	Impact    score.Impact
}

func Evaluate(input Input) (Outcome, error) {
	if input.Base == nil {
		return Outcome{}, fmt.Errorf("amendment: base score is required")
	}
	if input.State.Run.Terminal() {
		return Outcome{Kind: Rejected, Reason: "run_terminal"}, nil
	}
	if input.State.Run != runstate.RunRunning && input.State.Run != runstate.RunWaitingHuman {
		return Outcome{Kind: Rejected, Reason: "run_terminal"}, nil
	}
	if input.State.CancelRequested {
		return Outcome{Kind: Rejected, Reason: "run_cancelling"}, nil
	}
	if input.State.ScoreHead.Revision != input.BaseRevision || input.State.ScoreHead.SemanticHash != input.BaseHash {
		return Outcome{Kind: Rejected, Reason: "stale"}, nil
	}
	if touchesReserved(input.Operations) {
		return Outcome{Kind: Rejected, Reason: "reserved_field"}, nil
	}
	baseValue, baseBytes, err := scoreValue(input.Base)
	if err != nil {
		return Outcome{}, err
	}
	patchedValue, err := score.ApplyPatch(baseValue, input.Operations)
	if err != nil {
		return Outcome{Kind: Rejected, Reason: "patch_error"}, nil
	}
	patchedBytes, err := canonical.Encode(patchedValue)
	if err != nil {
		return Outcome{}, fmt.Errorf("amendment: encode patched score: %w", err)
	}
	if bytes.Equal(baseBytes, patchedBytes) {
		return Outcome{Kind: Rejected, Reason: "no_op"}, nil
	}
	patched, diagnostics := score.CompileValue(patchedValue)
	if len(diagnostics) != 0 {
		return Outcome{Kind: Rejected, Reason: "invalid_score"}, nil
	}
	impact, err := score.Compare(input.Base, patched)
	if err != nil {
		return Outcome{}, err
	}
	if input.HasClaimedImpact && !input.ClaimedImpact.Contains(impact) {
		return Outcome{Kind: Rejected, Reason: "claim_narrower", Patched: patched, Impact: impact}, nil
	}
	if changed, err := executedDependencyChanged(patched, input.State, input.Attempts); err != nil {
		return Outcome{}, err
	} else if changed {
		return Outcome{Kind: Rejected, Reason: "executed_dependency_changed", Patched: patched, Impact: impact}, nil
	}
	if condition, err := candidateCondition(patched, input.State); err != nil {
		return Outcome{}, err
	} else if condition != "" {
		return Outcome{Kind: Rejected, Reason: "candidate_incompatible", Condition: condition, Patched: patched, Impact: impact}, nil
	}
	if input.HumanDecision {
		class := classify(impact)
		return Outcome{Kind: Approved, Class: class, GuardPass: class != "" && guardPasses(class, input.Base, patched, input.State, impact), Patched: patched, Impact: impact}, nil
	}

	// Status is intentionally checked on the authenticated base, not the patched
	// score; /status is reserved at step 3.
	if baseStatus(input.Base) == "draft" {
		return Outcome{Kind: Routed, Reason: "draft_phase", Patched: patched, Impact: impact}, nil
	}
	if input.Base.EffectivePolicy().AmendmentAuto == "off" {
		return Outcome{Kind: Routed, Reason: "auto_disabled", Patched: patched, Impact: impact}, nil
	}
	if amendmentPolicyChanged(impact) {
		return Outcome{Kind: Routed, Reason: "recognized_non_monotone", Patched: patched, Impact: impact}, nil
	}
	class := classify(impact)
	if class == "" {
		return Outcome{Kind: Routed, Reason: "unclassified_change", Patched: patched, Impact: impact}, nil
	}
	guard := guardPasses(class, input.Base, patched, input.State, impact)
	if !guard {
		return Outcome{Kind: Routed, Reason: "runtime_scope_started", Class: class, GuardPass: false, Patched: patched, Impact: impact}, nil
	}
	return Outcome{Kind: Approved, Class: class, GuardPass: true, Patched: patched, Impact: impact}, nil
}

func scoreValue(value *score.Score) (any, []byte, error) {
	bytes, err := value.ProjectionBytes()
	if err != nil {
		return nil, nil, err
	}
	var result any
	if err := json.Unmarshal(bytes, &result); err != nil {
		return nil, nil, err
	}
	return result, bytes, nil
}

func touchesReserved(operations []any) bool {
	for _, raw := range operations {
		op, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := op["op"].(string)
		if name == "test" {
			continue
		}
		for _, key := range []string{"path", "from"} {
			if key == "from" && name != "move" && name != "copy" {
				continue
			}
			pointer, _ := op[key].(string)
			if reservedPointer(pointer) {
				return true
			}
		}
	}
	return false
}

func reservedPointer(pointer string) bool {
	// The empty RFC 6901 pointer is the document root and therefore an
	// ancestor of both reserved fields. "/" instead names the empty member,
	// not the root.
	if pointer == "" {
		return true
	}
	for _, reserved := range []string{"/revision", "/status"} {
		if pointer == reserved || strings.HasPrefix(pointer, reserved+"/") {
			return true
		}
	}
	return false
}

func baseStatus(value *score.Score) string {
	raw, _, err := scoreValue(value)
	if err != nil {
		return ""
	}
	return raw.(map[string]any)["status"].(string)
}
func amendmentPolicyChanged(value score.Impact) bool {
	for _, change := range value.ScoreChanges {
		if change.Selector == "/policy/amendment" {
			return true
		}
	}
	return false
}

func classify(value score.Impact) EnvelopeClass {
	if onlyPaths(value) {
		return NarrowPaths
	}
	if onlyOneGrantRemoval(value) {
		return NarrowGrants
	}
	if onlyOneBudgetDecrease(value) {
		return BudgetDecrease
	}
	return ""
}
func onlyPaths(value score.Impact) bool {
	return len(value.Authority.AllowedPaths.Added) == 0 && len(value.Authority.AllowedPaths.Removed) > 0 && len(value.Authority.Grants) == 0 && len(value.Authority.SideEffects.Added) == 0 && len(value.Authority.SideEffects.Removed) == 0 && value.Budget.ActiveWallClockMin == nil && value.Budget.RetriesPerMovement == nil && changesOnly(value, "/policy/allowed_paths")
}
func onlyOneGrantRemoval(value score.Impact) bool {
	return len(value.Authority.AllowedPaths.Added) == 0 && len(value.Authority.AllowedPaths.Removed) == 0 && len(value.Authority.Grants) == 1 && len(value.Authority.Grants[0].Added) == 0 && len(value.Authority.Grants[0].Removed) > 0 && len(value.Authority.SideEffects.Added) == 0 && len(value.Authority.SideEffects.Removed) == 0 && value.Budget.ActiveWallClockMin == nil && value.Budget.RetriesPerMovement == nil && changesOnly(value, "/movements[id="+value.Authority.Grants[0].MovementID+"]/grants")
}
func onlyOneBudgetDecrease(value score.Impact) bool {
	count := 0
	if value.Budget.ActiveWallClockMin != nil {
		if value.Budget.ActiveWallClockMin.To >= value.Budget.ActiveWallClockMin.From {
			return false
		}
		count++
	}
	if value.Budget.RetriesPerMovement != nil {
		if value.Budget.RetriesPerMovement.To >= value.Budget.RetriesPerMovement.From {
			return false
		}
		count++
	}
	return count == 1 && len(value.Authority.AllowedPaths.Added) == 0 && len(value.Authority.AllowedPaths.Removed) == 0 && len(value.Authority.Grants) == 0 && len(value.Authority.SideEffects.Added) == 0 && len(value.Authority.SideEffects.Removed) == 0 && changesOnlyBudget(value)
}
func changesOnly(value score.Impact, selector string) bool {
	return len(value.ScoreChanges) == 1 && value.ScoreChanges[0].Selector == selector && value.ScoreChanges[0].Operation == "replace"
}
func changesOnlyBudget(value score.Impact) bool {
	if len(value.ScoreChanges) != 1 {
		return false
	}
	return value.ScoreChanges[0].Selector == "/policy/budget/active_wall_clock_min" || value.ScoreChanges[0].Selector == "/policy/budget/retries_per_movement"
}

func guardPasses(class EnvelopeClass, before, after *score.Score, state runstate.State, impact score.Impact) bool {
	switch class {
	case NarrowPaths:
		for _, movement := range before.Movements() {
			if effectivePathsChanged(movement, before.EffectivePolicy(), after.EffectivePolicy()) && movementStarted(state, runstate.MovementID(movement.ID)) {
				return false
			}
		}
		return true
	case NarrowGrants:
		id := runstate.MovementID(impact.Authority.Grants[0].MovementID)
		return !movementOrDownstreamStarted(after, state, id)
	case BudgetDecrease:
		return true
	default:
		return false
	}
}
func effectivePathsChanged(movement score.MovementView, before, after score.PolicyView) bool {
	if !has(movement.Grants, "repo_read") && !has(movement.Grants, "repo_write") {
		return false
	}
	return !slices.Equal(before.AllowedPaths, after.AllowedPaths)
}
func has(values []string, want string) bool { return slices.Contains(values, want) }
func movementStarted(state runstate.State, movementID runstate.MovementID) bool {
	for _, attempt := range state.Attempts {
		if attempt.MovementID == movementID {
			return true
		}
	}
	return false
}
func movementOrDownstreamStarted(compiled *score.Score, state runstate.State, id runstate.MovementID) bool {
	changed := true
	selected := map[runstate.MovementID]bool{id: true}
	for changed {
		changed = false
		for _, movement := range compiled.Movements() {
			mid := runstate.MovementID(movement.ID)
			if selected[mid] {
				continue
			}
			for _, need := range movement.Needs {
				if selected[runstate.MovementID(need)] {
					selected[mid] = true
					changed = true
					break
				}
			}
		}
	}
	for movement := range selected {
		if movementStarted(state, movement) {
			return true
		}
	}
	return false
}

func executedDependencyChanged(patched *score.Score, state runstate.State, attempts []executiondep.Attempt) (bool, error) {
	byID := make(map[runstate.AttemptID]executiondep.Attempt, len(attempts))
	for _, attempt := range attempts {
		byID[attempt.ID] = attempt
	}
	for id, projected := range state.Attempts {
		if projected.State != runstate.AttemptCompleted || state.Movements[projected.MovementID] != runstate.MovementSucceeded {
			continue
		}
		if !scoreHasMovement(patched, projected.MovementID) {
			return true, nil
		}
		attempt, ok := byID[id]
		if !ok {
			return false, fmt.Errorf("amendment: completed attempt %q has no collector input", id)
		}
		if attempt.RecordedHash == "" {
			return false, fmt.Errorf("amendment: completed attempt %q has no recorded execution dependency hash", id)
		}
		same, err := executiondep.Equal(patched, attempt)
		if err != nil {
			return false, err
		}
		if !same {
			return true, nil
		}
	}
	return false, nil
}

func scoreHasMovement(compiled *score.Score, id runstate.MovementID) bool {
	for _, movement := range compiled.Movements() {
		if movement.ID == string(id) {
			return true
		}
	}
	return false
}

func candidateCondition(patched *score.Score, state runstate.State) (string, error) {
	candidate := state.ApplicationCandidate
	if candidate == nil {
		return "", nil
	}
	contributors, err := candidateContributors(patched, state)
	if err != nil {
		return "composition_changed", nil
	}
	hash, err := workspace.CandidateCompositionHash(candidate.BaseTree, contributors, string(candidate.CompositionEnvironmentHash))
	if err != nil {
		// A patched contributor set can legitimately switch between A.4's
		// identity and merge forms. Its recorded environment then makes the
		// recomputed form invalid, which is the condition failing—not an
		// evaluator input failure.
		return "composition_changed", nil
	}
	if runstate.Hash(hash) != candidate.CompositionDependencyHash {
		return "composition_changed", nil
	}
	execution := patched.Execution()
	if execution.GateWaived {
		return "verification_mode_changed", nil
	}
	if movementStarted(state, runstate.MovementID(execution.FinalMovementID)) && state.Movements[runstate.MovementID(execution.FinalMovementID)] == runstate.MovementSucceeded {
		return "verification_episode_finished", nil
	}
	return "", nil
}
func candidateContributors(compiled *score.Score, state runstate.State) ([]workspace.CompositionContributor, error) {
	movements := compiled.Movements()
	included := make(map[runstate.MovementID]bool, len(movements))
	for _, movement := range movements {
		included[runstate.MovementID(movement.ID)] = true
	}
	ordered, err := topological(movements, included)
	if err != nil {
		return nil, err
	}
	output := []workspace.CompositionContributor{}
	for _, id := range ordered {
		movement := findMovement(movements, id)
		if !has(movement.Grants, "repo_write") || state.Movements[id] == runstate.MovementInapplicable {
			continue
		}
		if state.Movements[id] != runstate.MovementSucceeded {
			return nil, fmt.Errorf("writer %q has not succeeded", id)
		}
		result, ok := state.MovementResults[id]
		if !ok || result.ApprovedChangeSetID == "" {
			return nil, fmt.Errorf("writer %q has no approved change set", id)
		}
		change, ok := state.ChangeSets[result.AttemptID]
		if !ok || change.ChangeSetID != result.ApprovedChangeSetID {
			return nil, fmt.Errorf("writer %q has no matching change set", id)
		}
		output = append(output, workspace.CompositionContributor{MovementID: id, ChangeSetID: change.ChangeSetID, BaseTree: change.BaseTree, ResultTree: change.ResultTree})
	}
	return output, nil
}
func findMovement(values []score.MovementView, id runstate.MovementID) score.MovementView {
	for _, value := range values {
		if value.ID == string(id) {
			return value
		}
	}
	return score.MovementView{}
}
func topological(values []score.MovementView, included map[runstate.MovementID]bool) ([]runstate.MovementID, error) {
	byID := map[runstate.MovementID]score.MovementView{}
	for _, value := range values {
		byID[runstate.MovementID(value.ID)] = value
	}
	indegree := map[runstate.MovementID]int{}
	children := map[runstate.MovementID][]runstate.MovementID{}
	for id := range included {
		movement, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("missing movement %q", id)
		}
		for _, need := range movement.Needs {
			dependency := runstate.MovementID(need)
			if included[dependency] {
				indegree[id]++
				children[dependency] = append(children[dependency], id)
			}
		}
	}
	output := make([]runstate.MovementID, 0, len(included))
	done := map[runstate.MovementID]bool{}
	for len(output) != len(included) {
		selected := runstate.MovementID("")
		for _, movement := range values {
			id := runstate.MovementID(movement.ID)
			if included[id] && indegree[id] == 0 && !done[id] {
				selected = id
				break
			}
		}
		if selected == "" {
			return nil, fmt.Errorf("movement graph is cyclic")
		}
		output = append(output, selected)
		done[selected] = true
		for _, child := range children[selected] {
			indegree[child]--
		}
	}
	return output, nil
}
