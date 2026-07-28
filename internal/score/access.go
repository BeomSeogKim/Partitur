package score

import "slices"

// Revision returns the validated positive score revision.
func (s *Score) Revision() uint64 {
	if s == nil {
		return 0
	}
	return uint64(s.document.Revision)
}

// Parts returns effective parts sorted by id. Every returned slice is a copy.
func (s *Score) Parts() []PartView {
	if s == nil {
		return nil
	}
	result := make([]PartView, 0, len(s.document.Parts))
	for id, part := range s.document.Parts {
		result = append(result, PartView{
			ID:           id,
			Capabilities: sortedStrings(part.Capabilities),
			ReadOnly:     part.ReadOnly,
		})
	}
	slices.SortFunc(result, func(left, right PartView) int {
		switch {
		case left.ID < right.ID:
			return -1
		case left.ID > right.ID:
			return 1
		default:
			return 0
		}
	})
	return result
}

// Movements returns effective movements in declaration order. Every returned
// slice is a copy.
func (s *Score) Movements() []MovementView {
	if s == nil {
		return nil
	}
	result := make([]MovementView, 0, len(s.document.Movements))
	for _, movement := range s.document.Movements {
		result = append(result, movementView(movement))
	}
	return result
}

// Execution returns the score-level fields needed to build an execute brief
// and identify the terminal movement.
func (s *Score) Execution() ExecutionView {
	if s == nil {
		return ExecutionView{}
	}
	result := ExecutionView{Goal: s.document.Goal}
	if s.document.Context != nil {
		result.Context = *s.document.Context
		result.ContextPresent = true
	}
	if s.document.Verification != nil {
		if s.document.Verification.FinalMovement != nil {
			result.FinalMovementID = *s.document.Verification.FinalMovement
		}
		if expectation := s.document.Verification.Expectation; expectation != nil &&
			expectation.Intent != nil {
			result.VerificationExpectation = *expectation.Intent
			result.VerificationExpectationPresent = true
		}
		if expectation := s.document.Verification.Expectation; expectation != nil &&
			expectation.ApplyGate != nil && expectation.ApplyGate.Waived != nil {
			result.GateWaived = *expectation.ApplyGate.Waived
		}
	}
	return result
}

// EffectivePolicy returns a defensive view of defaulted policy.
func (s *Score) EffectivePolicy() PolicyView {
	if s == nil {
		return PolicyView{}
	}
	return PolicyView{
		AllowedPaths:       sortedStrings(s.document.Policy.AllowedPaths),
		SideEffects:        sortedStrings(s.document.Policy.SideEffects),
		ActiveWallClockMin: int64(s.document.Policy.Budget.ActiveWallClockMin),
		RetriesPerMovement: int64(s.document.Policy.Budget.RetriesPerMovement),
		AmendmentAuto:      s.document.Policy.Amendment.Auto,
	}
}

func movementView(value movement) MovementView {
	phase := ""
	if value.Phase != nil {
		phase = *value.Phase
	}
	outputs := make([]OutputView, len(value.Outputs))
	for index, output := range value.Outputs {
		outputs[index] = OutputView{
			ArtifactID: output.ID,
			Kind:       output.Kind,
		}
	}
	acceptance := AcceptanceView{
		ArtifactCriteria:  make([]ArtifactCriterionView, 0, len(value.Acceptance.Hard)),
		HasReviewCriteria: len(value.Acceptance.Review) != 0,
		HumanGate:         value.Acceptance.HumanGate,
	}
	for _, criterion := range value.Acceptance.Hard {
		if criterion.Artifact == nil {
			acceptance.HasRunCriteria = true
			continue
		}
		expectedHash := ""
		if criterion.ExpectedHash != nil {
			expectedHash = *criterion.ExpectedHash
		}
		acceptance.ArtifactCriteria = append(
			acceptance.ArtifactCriteria,
			ArtifactCriterionView{
				ID:           criterion.ID,
				ArtifactID:   *criterion.Artifact,
				ExpectedHash: expectedHash,
			},
		)
	}
	return MovementView{
		ID:          value.ID,
		PartID:      value.PartID,
		Phase:       phase,
		Needs:       sortedStrings(value.Needs),
		Grants:      sortedStrings(value.Grants),
		MayPropose:  value.MayPropose,
		Instruction: value.Instruction,
		Inputs:      sortedStrings(value.Inputs),
		Outputs:     outputs,
		Acceptance:  acceptance,
	}
}
