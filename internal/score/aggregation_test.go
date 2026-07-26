package score

import (
	"slices"
	"testing"
)

func TestSemanticDiagnosticsAreCompleteAndOrdered(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	delete(objectAt(arrayAt(document, "open_questions")[0]), "resolution")
	objectAt(arrayAt(document, "open_questions")[0])["id"] = "bad id"
	acceptanceAt(document, 1)["hard"] = []any{}
	movementAt(document, 1)["grants"] =
		[]any{"repo_read", "repo_write", "shell", "network"}
	document["vendor"] = map[string]any{"opaque": true}
	reviewAt(document, 2, 0)["findings"] = "design-note"
	objectAt(document["policy"])["allowed_paths"] =
		[]any{"internal/**", "internal/**"}
	document["revision"] = 1.5

	got := compileFixture(t, document)
	want := []Diagnostic{
		{Rule01, "/open_questions/0", "finalized_question_unresolved"},
		{Rule02, "/movements/1/acceptance", "write_acceptance_missing"},
		{Rule03, "/movements/1/grants/3", "grant_not_capability"},
		{Rule06, "/vendor", "unknown_field"},
		{Rule07, "/movements/2/acceptance/review/0/findings", "review_findings_missing"},
		{Rule10, "/policy/allowed_paths/1", "duplicate_allowed_path"},
		{Rule11, "/verification/expectation/apply_gate/predicates", "predicate_unachievable"},
		{Rule11, "/verification/expectation/apply_gate/require/1", "reviewed_unachievable"},
		{Rule13, "/revision", "not_safe_integer"},
		{Rule16, "/open_questions/0/id", "invalid_identifier"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("diagnostics differ\n got: %#v\nwant: %#v", got, want)
	}
}

func TestIngressEndsBeforeSemanticCompleteness(t *testing.T) {
	t.Parallel()
	compiled, diagnostics := Compile([]byte("score: [\n"))
	if compiled != nil {
		t.Fatal("invalid ingress returned a score")
	}
	want := []Diagnostic{{RuleIngress, "", "invalid_restricted_yaml"}}
	if !slices.Equal(diagnostics, want) {
		t.Fatalf("diagnostics = %#v, want %#v", diagnostics, want)
	}
}

func TestScoreIngressRejectsCanonicalNumericHazards(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"-0", "1e-9999", "1e9999", ".nan", ".inf"} {
		value := value
		t.Run(value, func(t *testing.T) {
			source := []byte("revision: " + value + "\n")
			compiled, diagnostics := Compile(source)
			if compiled != nil {
				t.Fatalf("%s returned a score", value)
			}
			want := []Diagnostic{{RuleIngress, "", "invalid_restricted_yaml"}}
			if !slices.Equal(diagnostics, want) {
				t.Fatalf("%s diagnostics = %#v, want %#v", value, diagnostics, want)
			}
		})
	}
}

func TestMalformedElementsDoNotShiftPointers(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	movementAt(document, 1)["grants"] =
		[]any{float64(1), "repo_read", "repo_write", "shell", "network"}
	diagnostics := compileFixture(t, document)
	want := Diagnostic{Rule03, "/movements/1/grants/4", "grant_not_capability"}
	if !slices.Contains(diagnostics, want) {
		t.Fatalf("semantic pointer shifted after malformed sibling\nwant: %#v\ngot:  %#v",
			want, diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Rule == Rule03 &&
			diagnostic.Pointer == "/movements/1/grants/0" {
			t.Fatalf("malformed grant invented a semantic value: %#v", diagnostics)
		}
	}
}

func TestMalformedDeclarationsDoNotCreateFalseAbsence(t *testing.T) {
	t.Parallel()
	t.Run("part_body", func(t *testing.T) {
		document := finalizedFixture()
		objectAt(document["parts"])["plan"] = "malformed"
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic == (Diagnostic{Rule04, "/movements/0/part", "part_missing"}) {
				t.Fatalf("malformed declared part was reported absent: %#v", diagnostics)
			}
		}
	})
	t.Run("movement_element", func(t *testing.T) {
		document := finalizedFixture()
		document["movements"] = append(arrayAt(document, "movements"), "malformed")
		objectAt(document["verification"])["final_movement"] = "possibly-malformed"
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic == (Diagnostic{Rule12, "/verification/final_movement", "final_movement_unknown"}) {
				t.Fatalf("incomplete movement declarations proved false absence: %#v", diagnostics)
			}
		}
	})
	t.Run("movement_id_namespace", func(t *testing.T) {
		document := finalizedFixture()
		delete(movementAt(document, 0), "id")
		movementAt(document, 1)["needs"] = []any{"possibly-malformed"}
		objectAt(document["verification"])["final_movement"] = "possibly-malformed"
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic == (Diagnostic{
				Rule04, "/movements/1/needs/0", "need_missing",
			}) || diagnostic == (Diagnostic{
				Rule12, "/verification/final_movement", "final_movement_unknown",
			}) {
				t.Fatalf("incomplete movement ids proved false absence: %#v", diagnostics)
			}
		}
	})
	t.Run("movement_id_does_not_hide_output_absence", func(t *testing.T) {
		document := finalizedFixture()
		delete(movementAt(document, 2), "id")
		movementAt(document, 1)["inputs"] = []any{"definitely-absent"}
		diagnostics := compileFixture(t, document)
		want := Diagnostic{
			Rule04, "/movements/1/inputs/0", "input_output_missing",
		}
		if !slices.Contains(diagnostics, want) {
			t.Fatalf("unrelated movement id hid known output absence: %#v", diagnostics)
		}
	})
	t.Run("movement_id_hides_reachability", func(t *testing.T) {
		document := finalizedFixture()
		delete(movementAt(document, 0), "id")
		movementAt(document, 1)["inputs"] = []any{"design-note"}
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic == (Diagnostic{
				Rule04, "/movements/1/inputs/0", "input_not_reachable",
			}) {
				t.Fatalf("invalid producer id proved non-reachability: %#v", diagnostics)
			}
		}
	})
	t.Run("draft_movement_element", func(t *testing.T) {
		document := draftFixture()
		document["movements"] = []any{"malformed"}
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic.Rule == Rule08 {
				t.Fatalf("malformed movement proved a draft absent: %#v", diagnostics)
			}
		}
	})
	t.Run("draft_phase_namespace", func(t *testing.T) {
		document := draftFixture()
		movementAt(document, 0)["phase"] = float64(1)
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic.Rule == Rule08 {
				t.Fatalf("invalid phase proved draft absence: %#v", diagnostics)
			}
		}
	})
	t.Run("unrelated_draft_phase_does_not_hide_mismatch", func(t *testing.T) {
		document := draftFixture()
		objectAt(document["draft"])["interview_movement"] = "wrong"
		document["movements"] = append(
			arrayAt(document, "movements"),
			map[string]any{
				"id":          "other",
				"phase":       float64(1),
				"part":        "interviewer",
				"instruction": "Other",
			},
		)
		diagnostics := compileFixture(t, document)
		want := Diagnostic{
			Rule08, "/draft/interview_movement", "draft_reference_mismatch",
		}
		if !slices.Contains(diagnostics, want) {
			t.Fatalf("unrelated invalid phase hid known mismatch: %#v", diagnostics)
		}
	})
	t.Run("draft_id_namespace", func(t *testing.T) {
		document := draftFixture()
		movementAt(document, 0)["id"] = float64(1)
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic == (Diagnostic{
				Rule08, "/draft/interview_movement", "draft_reference_mismatch",
			}) {
				t.Fatalf("invalid draft id proved a reference mismatch: %#v", diagnostics)
			}
		}
	})
	t.Run("output_element", func(t *testing.T) {
		document := finalizedFixture()
		movementAt(document, 0)["outputs"] =
			append(arrayAt(movementAt(document, 0), "outputs"), "malformed")
		movementAt(document, 1)["inputs"] = []any{"possibly-malformed"}
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic == (Diagnostic{Rule04, "/movements/1/inputs/0", "input_output_missing"}) {
				t.Fatalf("incomplete output declarations proved false absence: %#v", diagnostics)
			}
		}
	})
	t.Run("output_id_namespace", func(t *testing.T) {
		document := finalizedFixture()
		delete(outputAt(document, 0, 0), "id")
		delete(outputAt(document, 2, 0), "id")
		movementAt(document, 1)["inputs"] = []any{"possibly-malformed"}
		reviewAt(document, 2, 0)["findings"] = "possibly-malformed"
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic == (Diagnostic{
				Rule04, "/movements/1/inputs/0", "input_output_missing",
			}) || diagnostic == (Diagnostic{
				Rule07, "/movements/2/acceptance/review/0/findings",
				"review_findings_missing",
			}) {
				t.Fatalf("incomplete output ids proved false absence: %#v", diagnostics)
			}
		}
	})
	t.Run("part_capabilities", func(t *testing.T) {
		document := finalizedFixture()
		plan := objectAt(objectAt(document["parts"])["plan"])
		plan["capabilities"] = float64(1)
		plan["read_only"] = true
		movementAt(document, 0)["grants"] = []any{"repo_write"}
		diagnostics := compileFixture(t, document)
		want := Diagnostic{
			Rule03, "/movements/0/grants/0", "read_only_repo_write",
		}
		if !slices.Contains(diagnostics, want) {
			t.Fatalf("independent read-only check was suppressed: %#v", diagnostics)
		}
		for _, diagnostic := range diagnostics {
			if diagnostic == (Diagnostic{
				Rule03, "/movements/0/grants/0", "grant_not_capability",
			}) {
				t.Fatalf("invalid capabilities proved a grant absent: %#v", diagnostics)
			}
		}
	})
	t.Run("acceptance_body", func(t *testing.T) {
		document := finalizedFixture()
		movementAt(document, 1)["acceptance"] = nil
		movementAt(document, 2)["acceptance"] = nil
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic.Rule == Rule02 || diagnostic.Rule == Rule11 {
				t.Fatalf("malformed acceptance produced derivative semantics: %#v", diagnostics)
			}
		}
	})
	t.Run("output_count", func(t *testing.T) {
		document := finalizedFixture()
		movementAt(document, 1)["outputs"] =
			append(arrayAt(movementAt(document, 1), "outputs"), "malformed")
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic.Rule == Rule15 {
				t.Fatalf("malformed output produced a count diagnostic: %#v", diagnostics)
			}
		}
	})
	t.Run("known_write_with_invalid_grant", func(t *testing.T) {
		document := finalizedFixture()
		movementAt(document, 1)["grants"] =
			[]any{"repo_write", float64(1)}
		movementAt(document, 1)["outputs"] = []any{}
		diagnostics := compileFixture(t, document)
		want := Diagnostic{
			Rule15, "/movements/1/outputs", "write_change_set_count",
		}
		if !slices.Contains(diagnostics, want) {
			t.Fatalf("malformed grant hid known write violation: %#v", diagnostics)
		}
	})
	t.Run("unknown_write_state_suppresses_nonwrite", func(t *testing.T) {
		document := finalizedFixture()
		movementAt(document, 0)["grants"] = []any{float64(1)}
		outputAt(document, 0, 0)["kind"] = "change_set"
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic == (Diagnostic{
				Rule15, "/movements/0/outputs", "nonwrite_change_set",
			}) {
				t.Fatalf("unknown write state proved a non-write violation: %#v", diagnostics)
			}
		}
	})
	t.Run("known_change_set_counts_survive_invalid_output", func(t *testing.T) {
		document := finalizedFixture()
		movementAt(document, 1)["outputs"] = []any{
			map[string]any{"id": "change-a", "kind": "change_set"},
			map[string]any{"id": "change-b", "kind": "change_set"},
			map[string]any{"id": "unknown-kind", "kind": float64(1)},
		}
		diagnostics := compileFixture(t, document)
		want := Diagnostic{
			Rule15, "/movements/1/outputs", "write_change_set_count",
		}
		if !slices.Contains(diagnostics, want) {
			t.Fatalf("invalid output hid two known change sets: %#v", diagnostics)
		}
	})
	t.Run("allowed_path_elements", func(t *testing.T) {
		document := finalizedFixture()
		objectAt(document["policy"])["allowed_paths"] =
			[]any{float64(1), float64(1)}
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic.Rule == Rule10 {
				t.Fatalf("malformed paths invented duplicate strings: %#v", diagnostics)
			}
		}
	})
	t.Run("missing_ids", func(t *testing.T) {
		document := finalizedFixture()
		delete(movementAt(document, 0), "id")
		delete(movementAt(document, 1), "id")
		diagnostics := compileFixture(t, document)
		for _, diagnostic := range diagnostics {
			if diagnostic.Rule == Rule04 &&
				diagnostic.Detail == "duplicate_movement_id" {
				t.Fatalf("missing ids invented a duplicate: %#v", diagnostics)
			}
			if diagnostic.Rule == Rule16 &&
				(diagnostic.Pointer == "/movements/0/id" ||
					diagnostic.Pointer == "/movements/1/id") {
				t.Fatalf("missing ids produced derivative grammar diagnostics: %#v", diagnostics)
			}
		}
	})
	t.Run("missing_criterion_id", func(t *testing.T) {
		document := finalizedFixture()
		delete(hardAt(document, 1, 0), "id")
		diagnostics := compileFixture(t, document)
		want := Diagnostic{
			Rule09, "/movements/1/acceptance/hard/0/id", "criterion_id_missing",
		}
		if !slices.Contains(diagnostics, want) {
			t.Fatalf("missing criterion id lost its Rule09 owner: %#v", diagnostics)
		}
		for _, diagnostic := range diagnostics {
			if diagnostic == (Diagnostic{
				Rule16, "/movements/1/acceptance/hard/0/id", "invalid_identifier",
			}) {
				t.Fatalf("missing criterion id produced derivative Rule16: %#v", diagnostics)
			}
		}
	})
}

func TestRule19PointerSurvivesMalformedMovementSibling(t *testing.T) {
	t.Parallel()
	document := draftFixture()
	movementAt(document, 0)["may_propose"] = false
	document["movements"] = append([]any{"malformed"}, arrayAt(document, "movements")...)
	diagnostics := compileFixture(t, document)
	want := Diagnostic{Rule19, "/movements/1/may_propose", "draft_may_propose_false"}
	if !slices.Contains(diagnostics, want) {
		t.Fatalf("Rule19 pointer shifted after malformed movement\nwant: %#v\ngot:  %#v",
			want, diagnostics)
	}
}
