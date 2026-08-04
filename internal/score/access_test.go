package score

import (
	"reflect"
	"testing"
)

func TestExecutionReadSurface(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	document["context"] = "Repository context."
	acceptanceAt(document, 0)["hard"] = []any{
		map[string]any{
			"id":            "design-exists",
			"artifact":      "design-note",
			"expected_hash": "sha256:ABCD",
		},
	}
	compiled := assertCompiles(t, document)
	if got := compiled.Revision(); got != 1 {
		t.Fatalf("Revision() = %d, want 1", got)
	}

	if got, want := compiled.Execution(), (ExecutionView{
		Goal:                           "Compile a complete score.",
		Context:                        "Repository context.",
		ContextPresent:                 true,
		VerificationExpectation:        "pass-existing-tests",
		VerificationExpectationPresent: true,
		FinalMovementID:                "verify",
		GateWaived:                     false,
	}); got != want {
		t.Fatalf("Execution() = %#v, want %#v", got, want)
	}

	waived := finalizedFixture()
	waivedVerification := objectAt(waived["verification"])
	delete(waivedVerification, "final_movement")
	waivedExpectation := objectAt(waivedVerification["expectation"])
	waivedExpectation["apply_gate"] = map[string]any{
		"waived": true,
		"reason": "fixture waiver",
	}
	if !assertCompiles(t, waived).Execution().GateWaived {
		t.Fatal("waived apply gate was hidden from the execution view")
	}

	questions := append(arrayAt(document, "open_questions"), map[string]any{
		"id": "a-waived", "question": "May this be waived?", "waived": true,
	})
	document["open_questions"] = questions
	if got, want := assertCompiles(t, document).ResolvedQuestions(), []ResolvedQuestionView{
		{ID: "a-waived", Question: "May this be waived?"},
		{ID: "q-1", Question: "Is the intent complete?", Resolution: "Yes.", ResolutionPresent: true},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolvedQuestions() = %#v, want %#v", got, want)
	}

	movements := compiled.Movements()
	if got, want := movements[0], (MovementView{
		ID:          "plan",
		PartID:      "plan",
		Needs:       []string{},
		Grants:      []string{"repo_read"},
		Instruction: "Plan the work.",
		Inputs:      []string{},
		Outputs: []OutputView{{
			ArtifactID: "design-note",
			Kind:       "document",
		}},
		Acceptance: AcceptanceView{
			ArtifactCriteria: []ArtifactCriterionView{{
				ID:           "design-exists",
				ArtifactID:   "design-note",
				ExpectedHash: "sha256:ABCD",
			}},
			HumanGate: "never",
		},
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan movement = %#v, want %#v", got, want)
	}
	if got, want := movements[1].Inputs, []string{"design-note"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("build inputs = %#v, want %#v", got, want)
	}
	if got, want := movements[1].Needs, []string{"plan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("build needs = %#v, want %#v", got, want)
	}
}

func TestExecutionReadSurfaceDoesNotHideUnsupportedAcceptance(t *testing.T) {
	t.Parallel()
	compiled := assertCompiles(t, finalizedFixture())
	final := compiled.Movements()[2]
	if len(final.Acceptance.RunCriteria) != 1 {
		t.Fatal("hard.run criterion was hidden from the execution view")
	}
	if !final.Acceptance.HasReviewCriteria {
		t.Fatal("review criterion was hidden from the execution view")
	}
	if final.Acceptance.HumanGate != "always" {
		t.Fatalf("human gate = %q, want always", final.Acceptance.HumanGate)
	}
	if len(final.Acceptance.ArtifactCriteria) != 0 {
		t.Fatalf("artifact criteria = %#v, want none", final.Acceptance.ArtifactCriteria)
	}
}

func TestExecutionReadSurfacePreservesOmissions(t *testing.T) {
	t.Parallel()
	compiled := assertCompiles(t, draftFixture())
	execution := compiled.Execution()
	if execution.ContextPresent || execution.Context != "" {
		t.Fatalf("context was manufactured: %#v", execution)
	}
	if execution.VerificationExpectationPresent ||
		execution.VerificationExpectation != "" {
		t.Fatalf("verification expectation was manufactured: %#v", execution)
	}
	if execution.FinalMovementID != "" {
		t.Fatalf("final movement was manufactured: %#v", execution)
	}

	var nilScore *Score
	if got := nilScore.Revision(); got != 0 {
		t.Fatalf("nil score revision = %d", got)
	}
	if got := nilScore.Execution(); got != (ExecutionView{}) {
		t.Fatalf("nil score execution = %#v", got)
	}
}

func TestExecutionReadSurfaceIsDefensive(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	acceptanceAt(document, 0)["hard"] = []any{
		map[string]any{"id": "design-exists", "artifact": "design-note"},
	}
	compiled := assertCompiles(t, document)

	first := compiled.Movements()
	first[0].Grants[0] = "network"
	first[0].Outputs[0].ArtifactID = "mutated-output"
	first[0].Acceptance.ArtifactCriteria[0].ArtifactID = "mutated-criterion"
	first[1].Inputs[0] = "mutated-input"
	first[1].Needs[0] = "mutated-need"

	second := compiled.Movements()
	if second[0].Grants[0] != "repo_read" {
		t.Fatalf("grants aliased compiler state: %#v", second[0].Grants)
	}
	if second[0].Outputs[0].ArtifactID != "design-note" {
		t.Fatalf("outputs aliased compiler state: %#v", second[0].Outputs)
	}
	if second[0].Acceptance.ArtifactCriteria[0].ArtifactID != "design-note" {
		t.Fatalf(
			"criteria aliased compiler state: %#v",
			second[0].Acceptance.ArtifactCriteria,
		)
	}
	if second[1].Inputs[0] != "design-note" {
		t.Fatalf("inputs aliased compiler state: %#v", second[1].Inputs)
	}
	if second[1].Needs[0] != "plan" {
		t.Fatalf("needs aliased compiler state: %#v", second[1].Needs)
	}
	if second[0].Acceptance.ArtifactCriteria[0].ExpectedHash != "" {
		t.Fatalf(
			"absent expected hash was manufactured: %#v",
			second[0].Acceptance.ArtifactCriteria,
		)
	}
}
