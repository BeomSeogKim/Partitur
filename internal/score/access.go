package score

import (
	"slices"
	"strings"
)

// Revision returns the validated positive score revision.
func (s *Score) Revision() uint64 {
	if s == nil {
		return 0
	}
	return uint64(s.document.Revision)
}

// Status returns the validated score lifecycle status.
func (s *Score) Status() string {
	if s == nil {
		return ""
	}
	return s.document.Status
}

// DraftInterviewMovement returns the movement named by the draft contract.
func (s *Score) DraftInterviewMovement() string {
	if s == nil || s.document.Draft == nil {
		return ""
	}
	return s.document.Draft.InterviewMovement
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
		expectation := s.document.Verification.Expectation
		if expectation == nil {
			return result
		}
		if expectation.ApplyGate == nil {
			return result
		}
		// A require-path gate carries no `waived` key at all, so its demands have
		// to survive that absence: reading them before the waiver check is what
		// stops the apply judgment from looping over an empty require list.
		result.ApplyGateRequire = slices.Clone(expectation.ApplyGate.Require)
		result.ApplyGatePredicates = slices.Clone(expectation.ApplyGate.Predicates)
		if expectation.ApplyGate.Waived == nil {
			return result
		}
		result.GateWaived = *expectation.ApplyGate.Waived
		if expectation.ApplyGate.Reason != nil {
			result.WaiverReason = *expectation.ApplyGate.Reason
		}
	}
	return result
}

// ResolvedQuestions returns finalized question dispositions sorted by id.
// Every returned value is detached from the compiled score.
func (s *Score) ResolvedQuestions() []ResolvedQuestionView {
	if s == nil {
		return nil
	}
	questions := slices.Clone(s.document.OpenQuestions)
	slices.SortFunc(questions, func(left, right question) int {
		return strings.Compare(left.ID, right.ID)
	})
	result := make([]ResolvedQuestionView, 0, len(questions))
	for _, question := range questions {
		if question.Resolution != nil {
			result = append(result, ResolvedQuestionView{
				ID:                question.ID,
				Question:          question.Question,
				Resolution:        *question.Resolution,
				ResolutionPresent: true,
			})
			continue
		}
		if question.Waived != nil && *question.Waived {
			result = append(result, ResolvedQuestionView{
				ID:       question.ID,
				Question: question.Question,
			})
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
			timeoutMin := int64(0)
			if criterion.TimeoutMin != nil {
				timeoutMin = int64(*criterion.TimeoutMin)
			}
			acceptance.RunCriteria = append(acceptance.RunCriteria, RunCriterionView{
				SourceIndex: criterion.SourceIndex, ID: criterion.ID, Argv: append([]string(nil), criterion.Run...), TimeoutMin: timeoutMin,
			})
			continue
		}
		expectedHash := ""
		if criterion.ExpectedHash != nil {
			expectedHash = *criterion.ExpectedHash
		}
		acceptance.ArtifactCriteria = append(
			acceptance.ArtifactCriteria,
			ArtifactCriterionView{
				SourceIndex:  criterion.SourceIndex,
				ID:           criterion.ID,
				ArtifactID:   *criterion.Artifact,
				ExpectedHash: expectedHash,
			},
		)
	}
	if len(value.Acceptance.Review) != 0 {
		acceptance.ReviewCriteria = make([]ReviewCriterionView, 0, len(value.Acceptance.Review))
	}
	for _, criterion := range value.Acceptance.Review {
		acceptance.ReviewCriteria = append(acceptance.ReviewCriteria, ReviewCriterionView{
			SourceIndex: criterion.SourceIndex,
			ID:          criterion.ID,
			Findings:    criterion.Findings,
			Rubrics:     sortedStrings(criterion.Rubric),
		})
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
