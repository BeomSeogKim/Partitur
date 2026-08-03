package runstate

import "testing"

func TestProjectedReviewOutcomeRequiresEveryExactApprovedOverride(t *testing.T) {
	acceptance := Acceptance{
		ReviewOutcome: "CONTESTED", SubjectTree: "git-sha1:subject",
		BlockingFindings: []FindingReference{
			{ArtifactInstanceID: "findings@a1", FindingID: "F-1"},
			{ArtifactInstanceID: "findings@a1", FindingID: "F-2"},
		},
	}
	for _, test := range []struct {
		name       string
		resolution HumanGateResolution
		want       string
	}{
		{"all overridden", HumanGateResolution{Disposition: "approved", Scope: HumanGateScope{SubjectTree: "git-sha1:subject"}, ScoreRevision: 1, OverriddenFindings: acceptance.BlockingFindings}, "OVERRIDDEN"},
		{"partial override", HumanGateResolution{Disposition: "approved", Scope: HumanGateScope{SubjectTree: "git-sha1:subject"}, ScoreRevision: 1, OverriddenFindings: acceptance.BlockingFindings[:1]}, "CONTESTED"},
		{"different blocker", HumanGateResolution{Disposition: "approved", Scope: HumanGateScope{SubjectTree: "git-sha1:subject"}, ScoreRevision: 1, OverriddenFindings: []FindingReference{{ArtifactInstanceID: "findings@a1", FindingID: "F-1"}, {ArtifactInstanceID: "findings@a1", FindingID: "F-3"}}}, "CONTESTED"},
		{"rejected", HumanGateResolution{Disposition: "rejected", Scope: HumanGateScope{SubjectTree: "git-sha1:subject"}, ScoreRevision: 1, OverriddenFindings: acceptance.BlockingFindings}, "CONTESTED"},
		{"wrong subject", HumanGateResolution{Disposition: "approved", Scope: HumanGateScope{SubjectTree: "git-sha1:other"}, ScoreRevision: 1, OverriddenFindings: acceptance.BlockingFindings}, "CONTESTED"},
		{"wrong revision", HumanGateResolution{Disposition: "approved", Scope: HumanGateScope{SubjectTree: "git-sha1:subject"}, ScoreRevision: 2, OverriddenFindings: acceptance.BlockingFindings}, "CONTESTED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ProjectedReviewOutcome(acceptance, test.resolution, 1); got != test.want {
				t.Fatalf("ProjectedReviewOutcome() = %q, want %q", got, test.want)
			}
		})
	}
}
