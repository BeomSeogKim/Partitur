package score

import (
	"bytes"
	"errors"
	"slices"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

// ProjectionBytes returns JCS bytes for the Appendix A.4 partitur/score value.
// The domain and version tuple are intentionally not part of these bytes.
func (s *Score) ProjectionBytes() ([]byte, error) {
	if s == nil {
		return nil, errors.New("score: nil Score")
	}
	if !s.document.DefaultsApplied {
		return nil, errors.New("score: defaults not applied")
	}
	return canonical.Encode(s.projectionValue())
}

// Hash returns the versioned partitur/score identity.
func (s *Score) Hash() (string, error) {
	if s == nil {
		return "", errors.New("score: nil Score")
	}
	if !s.document.DefaultsApplied {
		return "", errors.New("score: defaults not applied")
	}
	return canonical.Hash(canonical.DomainScore, s.projectionValue())
}

func (s *Score) projectionValue() map[string]any {
	document := &s.document
	result := map[string]any{
		"score":          document.Version,
		"name":           document.Name,
		"revision":       document.Revision,
		"status":         document.Status,
		"goal":           document.Goal,
		"open_questions": projectQuestions(document.OpenQuestions),
		"parts":          projectParts(document.Parts),
		"movements":      projectMovements(document.Movements),
		"policy":         projectPolicy(document.Policy),
	}
	if document.Context != nil {
		result["context"] = *document.Context
	}
	if document.Draft != nil {
		result["draft"] = map[string]any{
			"interview_movement": document.Draft.InterviewMovement,
		}
	}
	if document.Verification != nil {
		result["verification"] = projectVerification(*document.Verification)
	}
	if document.Extensions != nil {
		result["extensions"] = cloneJSON(document.Extensions)
	}
	return result
}

func projectQuestions(questions []question) []any {
	ordered := slices.Clone(questions)
	slices.SortFunc(ordered, func(left, right question) int {
		switch {
		case left.ID < right.ID:
			return -1
		case left.ID > right.ID:
			return 1
		default:
			return 0
		}
	})
	result := make([]any, 0, len(ordered))
	for _, question := range ordered {
		value := map[string]any{
			"id":       question.ID,
			"question": question.Question,
		}
		if question.Resolution != nil {
			value["resolution"] = *question.Resolution
		}
		if question.Waived != nil {
			value["waived"] = *question.Waived
		}
		result = append(result, value)
	}
	return result
}

func projectVerification(verification verification) map[string]any {
	result := make(map[string]any)
	if verification.Expectation != nil {
		expectation := make(map[string]any)
		if verification.Expectation.Intent != nil {
			expectation["intent"] = *verification.Expectation.Intent
		}
		if verification.Expectation.ApplyGate != nil {
			gate := verification.Expectation.ApplyGate
			value := map[string]any{
				"predicates": stringsToAny(sortedStrings(gate.Predicates)),
			}
			if gate.RequireSet {
				value["require"] = stringsToAny(sortedStrings(gate.Require))
			}
			if gate.Waived != nil {
				value["waived"] = *gate.Waived
			}
			if gate.Reason != nil {
				value["reason"] = *gate.Reason
			}
			expectation["apply_gate"] = value
		}
		result["expectation"] = expectation
	}
	if verification.FinalMovement != nil {
		result["final_movement"] = *verification.FinalMovement
	}
	return result
}

func projectParts(parts map[string]part) map[string]any {
	result := make(map[string]any, len(parts))
	for id, part := range parts {
		result[id] = map[string]any{
			"capabilities": stringsToAny(sortedStrings(part.Capabilities)),
			"read_only":    part.ReadOnly,
		}
	}
	return result
}

func projectMovements(movements []movement) []any {
	result := make([]any, 0, len(movements))
	for _, movement := range movements {
		value := map[string]any{
			"id":          movement.ID,
			"part":        movement.PartID,
			"needs":       stringsToAny(sortedStrings(movement.Needs)),
			"grants":      stringsToAny(sortedStrings(movement.Grants)),
			"instruction": movement.Instruction,
			"inputs":      stringsToAny(sortedStrings(movement.Inputs)),
			"outputs":     projectOutputs(movement.Outputs),
			"may_propose": movement.MayPropose,
			"acceptance":  projectAcceptance(movement.Acceptance),
		}
		if movement.Phase != nil {
			value["phase"] = *movement.Phase
		}
		result = append(result, value)
	}
	return result
}

func projectOutputs(outputs []output) []any {
	result := make([]any, 0, len(outputs))
	for _, output := range outputs {
		result = append(result, map[string]any{
			"id":   output.ID,
			"kind": output.Kind,
		})
	}
	return result
}

func projectAcceptance(acceptance acceptance) map[string]any {
	hard := make([]any, 0, len(acceptance.Hard))
	for _, criterion := range acceptance.Hard {
		value := map[string]any{"id": criterion.ID}
		if criterion.Artifact != nil {
			value["artifact"] = *criterion.Artifact
			if criterion.ExpectedHash != nil {
				value["expected_hash"] = *criterion.ExpectedHash
			}
		} else {
			value["run"] = stringsToAny(criterion.Run)
			if criterion.TimeoutMin != nil {
				value["timeout_min"] = *criterion.TimeoutMin
			}
		}
		hard = append(hard, value)
	}
	review := make([]any, 0, len(acceptance.Review))
	for _, criterion := range acceptance.Review {
		review = append(review, map[string]any{
			"id":       criterion.ID,
			"findings": criterion.Findings,
			"rubric":   stringsToAny(sortedStrings(criterion.Rubric)),
		})
	}
	return map[string]any{
		"hard":       hard,
		"review":     review,
		"human_gate": acceptance.HumanGate,
	}
}

func projectPolicy(policy policy) map[string]any {
	return map[string]any{
		"allowed_paths": stringsToAny(sortedStrings(policy.AllowedPaths)),
		"side_effects":  stringsToAny(sortedStrings(policy.SideEffects)),
		"budget": map[string]any{
			"active_wall_clock_min": policy.Budget.ActiveWallClockMin,
			"retries_per_movement":  policy.Budget.RetriesPerMovement,
		},
		"amendment": map[string]any{
			"auto": policy.Amendment.Auto,
		},
	}
}

func sortedStrings(values []string) []string {
	result := slices.Clone(values)
	slices.SortFunc(result, compareCanonicalStrings)
	return result
}

func compareCanonicalStrings(left, right string) int {
	if left == right {
		return 0
	}
	pair, pairErr := canonical.Encode(map[string]any{left: nil, right: nil})
	leftKey, leftErr := canonical.Encode(left)
	if pairErr != nil || leftErr != nil {
		// Values in a compiled score have already passed canonical ingress, so
		// this branch is unreachable for a Score. Keep the comparator total for
		// defensive use on an invalid zero value.
		return stringsCompare(left, right)
	}
	prefix := make([]byte, 1, len(leftKey)+1)
	prefix[0] = '{'
	prefix = append(prefix, leftKey...)
	if bytes.HasPrefix(pair, prefix) {
		return -1
	}
	return 1
}

func stringsCompare(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}
