package runstate

// ProjectedReviewOutcome derives the review mark from durable review evidence
// and its matching human-gate resolution. It does not alter stored evidence.
func ProjectedReviewOutcome(acceptance Acceptance, resolution HumanGateResolution, scoreRevision uint64) string {
	if acceptance.ReviewOutcome != "CONTESTED" || len(acceptance.BlockingFindings) == 0 ||
		resolution.Disposition != "approved" || resolution.Scope.SubjectTree != acceptance.SubjectTree ||
		resolution.ScoreRevision != scoreRevision ||
		!sameFindingReferences(acceptance.BlockingFindings, resolution.OverriddenFindings) {
		return acceptance.ReviewOutcome
	}
	return "OVERRIDDEN"
}

func sameFindingReferences(left, right []FindingReference) bool {
	if len(left) != len(right) {
		return false
	}
	for _, reference := range left {
		if !containsFindingReference(right, reference) {
			return false
		}
	}
	return true
}
