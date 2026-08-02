package score

import (
	"bytes"
	"slices"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

func TestValidFixturesCompile(t *testing.T) {
	t.Parallel()
	for name, fixture := range map[string]func() map[string]any{
		"finalized": finalizedFixture,
		"draft":     draftFixture,
	} {
		t.Run(name, func(t *testing.T) {
			assertCompiles(t, fixture())
		})
	}
}

func TestRuleConformance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		accept func() map[string]any
		mutate func(map[string]any)
		want   Diagnostic
	}{
		{
			name:   "1a_finalized_question_resolved",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				delete(objectAt(arrayAt(document, "open_questions")[0]), "resolution")
			},
			want: Diagnostic{Rule01, "/open_questions/0", "finalized_question_unresolved"},
		},
		{
			name:   "1b_finalized_intent",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				delete(expectationAt(document), "intent")
			},
			want: Diagnostic{Rule01, "/verification/expectation/intent", "finalized_intent_missing"},
		},
		{
			name:   "1c_apply_gate_xor",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				delete(gateAt(document), "require")
			},
			want: Diagnostic{Rule01, "/verification/expectation/apply_gate", "apply_gate_not_xor"},
		},
		{
			name:   "1c_apply_gate_rejects_both_arms",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				gate := gateAt(document)
				gate["waived"] = true
				gate["reason"] = "cannot combine with require"
			},
			want: Diagnostic{Rule01, "/verification/expectation/apply_gate", "apply_gate_not_xor"},
		},
		{
			name:   "1d_apply_gate_required",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				delete(expectationAt(document), "apply_gate")
			},
			want: Diagnostic{Rule01, "/verification/expectation/apply_gate", "finalized_apply_gate_missing"},
		},
		{
			name:   "1e_waiver_reason",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				gate := gateAt(document)
				delete(gate, "require")
				gate["waived"] = true
			},
			want: Diagnostic{Rule01, "/verification/expectation/apply_gate/reason", "waiver_reason_missing"},
		},
		{
			name:   "1f_waiver_is_true",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				gateAt(document)["waived"] = false
			},
			want: Diagnostic{Rule01, "/verification/expectation/apply_gate/waived", "waiver_must_be_true"},
		},
		{
			name:   "2_write_acceptance_floor",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				acceptance := acceptanceAt(document, 1)
				acceptance["hard"] = []any{}
				acceptance["human_gate"] = "never"
			},
			want: Diagnostic{Rule02, "/movements/1/acceptance", "write_acceptance_missing"},
		},
		{
			name:   "3a_grant_subset",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 1)["grants"] =
					[]any{"repo_read", "repo_write", "shell", "network"}
			},
			want: Diagnostic{Rule03, "/movements/1/grants/3", "grant_not_capability"},
		},
		{
			name:   "3b_read_only_write",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				objectAtKey(document, "parts", "plan")["read_only"] = true
				movementAt(document, 0)["grants"] = []any{"repo_read", "repo_write"}
			},
			want: Diagnostic{Rule03, "/movements/0/grants/1", "read_only_repo_write"},
		},
		{
			name:   "4a_movement_ids_unique",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 2)["id"] = "build"
			},
			want: Diagnostic{Rule04, "/movements/2/id", "duplicate_movement_id"},
		},
		{
			name:   "4c_question_ids_unique",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				document["open_questions"] = append(arrayAt(document, "open_questions"), map[string]any{
					"id":         "q-1",
					"question":   "Second question",
					"resolution": "Resolved",
				})
			},
			want: Diagnostic{Rule04, "/open_questions/1/id", "duplicate_question_id"},
		},
		{
			name:   "4d_needs_exist",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 0)["needs"] = []any{"missing"}
			},
			want: Diagnostic{Rule04, "/movements/0/needs/0", "need_missing"},
		},
		{
			name:   "4e_needs_dag",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 0)["needs"] = []any{"verify"}
			},
			want: Diagnostic{Rule04, "/movements/0/needs/0", "needs_cycle"},
		},
		{
			name:   "4f_part_reference",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 0)["part"] = "missing"
			},
			want: Diagnostic{Rule04, "/movements/0/part", "part_missing"},
		},
		{
			name:   "4g_input_declared",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 1)["inputs"] = []any{"missing"}
			},
			want: Diagnostic{Rule04, "/movements/1/inputs/0", "input_output_missing"},
		},
		{
			name:   "4h_input_reachable",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 1)["inputs"] = []any{"review-findings"}
			},
			want: Diagnostic{Rule04, "/movements/1/inputs/0", "input_not_reachable"},
		},
		{
			name:   "4i_output_ids_unique",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				outputAt(document, 2, 0)["id"] = "design-note"
			},
			want: Diagnostic{Rule04, "/movements/2/outputs/0/id", "duplicate_output_id"},
		},
		{
			name:   "4j_reserved_output_id",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				outputAt(document, 0, 0)["id"] = "partitur.input"
				movementAt(document, 1)["inputs"] = []any{"partitur.input"}
			},
			want: Diagnostic{Rule04, "/movements/0/outputs/0/id", "reserved_output_id"},
		},
		{
			name:   "6a_unknown_core_field",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 0)["vendor"] = "opaque"
			},
			want: Diagnostic{Rule06, "/movements/0/vendor", "unknown_field"},
		},
		{
			name: "6b_extensions_namespace",
			accept: func() map[string]any {
				document := finalizedFixture()
				document["extensions"] = map[string]any{
					"vendor": map[string]any{"fraction": 1.25, "nested": nil},
				}
				return document
			},
			mutate: func(document map[string]any) {
				document["vendor"] = document["extensions"]
				delete(document, "extensions")
			},
			want: Diagnostic{Rule06, "/vendor", "unknown_field"},
		},
		{
			name:   "7a_review_output_same_movement",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				reviewAt(document, 2, 0)["findings"] = "design-note"
			},
			want: Diagnostic{Rule07, "/movements/2/acceptance/review/0/findings", "review_findings_missing"},
		},
		{
			name:   "7b_review_output_kind",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				outputAt(document, 2, 0)["kind"] = "document"
			},
			want: Diagnostic{Rule07, "/movements/2/acceptance/review/0/findings", "review_findings_wrong_kind"},
		},
		{
			name:   "7c_at_most_one_review_criterion",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				acceptance := acceptanceAt(document, 2)
				acceptance["review"] = append(arrayAt(acceptance, "review"), map[string]any{
					"id": "second-review", "findings": "review-findings", "rubric": []any{"regression"},
				})
			},
			want: Diagnostic{Rule07, "/movements/2/acceptance/review/1", "multiple_review_criteria"},
		},
		{
			name:   "7d_review_movement_cannot_write_repository",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movement := movementAt(document, 1)
				movement["outputs"] = append(arrayAt(movement, "outputs"), map[string]any{"id": "writer-findings", "kind": "findings"})
				acceptanceAt(document, 1)["review"] = []any{map[string]any{
					"id": "writer-review", "findings": "writer-findings", "rubric": []any{"coverage"},
				}}
			},
			want: Diagnostic{Rule07, "/movements/1/grants/1", "review_repo_write"},
		},
		{
			name: "8a_at_most_one_draft",
			accept: func() map[string]any {
				document := draftFixture()
				document["movements"] = append(arrayAt(document, "movements"), map[string]any{
					"id":          "ordinary",
					"part":        "plan",
					"instruction": "ordinary",
				})
				return document
			},
			mutate: func(document map[string]any) {
				movementAt(document, 1)["phase"] = "draft"
			},
			want: Diagnostic{Rule08, "/movements/1/phase", "multiple_draft_movements"},
		},
		{
			name:   "8b_draft_phase_requires_reference",
			accept: draftFixture,
			mutate: func(document map[string]any) {
				delete(document, "draft")
			},
			want: Diagnostic{Rule08, "/draft/interview_movement", "draft_reference_missing"},
		},
		{
			name:   "8c_draft_reference_requires_phase",
			accept: draftFixture,
			mutate: func(document map[string]any) {
				delete(movementAt(document, 0), "phase")
			},
			want: Diagnostic{Rule08, "/draft/interview_movement", "draft_phase_missing"},
		},
		{
			name: "8d_draft_reference_matches",
			accept: func() map[string]any {
				document := draftFixture()
				document["movements"] = append(arrayAt(document, "movements"), map[string]any{
					"id":          "ordinary",
					"part":        "plan",
					"instruction": "ordinary",
				})
				return document
			},
			mutate: func(document map[string]any) {
				objectAt(document["draft"])["interview_movement"] = "ordinary"
			},
			want: Diagnostic{Rule08, "/draft/interview_movement", "draft_reference_mismatch"},
		},
		{
			name:   "8e_draft_status_requires_draft",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				document["status"] = "draft"
			},
			want: Diagnostic{Rule08, "/movements", "draft_status_requires_movement"},
		},
		{
			name:   "8f_draft_read_only",
			accept: draftFixture,
			mutate: func(document map[string]any) {
				objectAtKey(document, "parts", "plan")["capabilities"] =
					[]any{"repo_read", "repo_write"}
				movementAt(document, 0)["grants"] = []any{"repo_read", "repo_write"}
			},
			want: Diagnostic{Rule08, "/movements/0/grants/1", "draft_repo_write"},
		},
		{
			name:   "9a_criterion_id_required",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				delete(hardAt(document, 1, 0), "id")
			},
			want: Diagnostic{Rule09, "/movements/1/acceptance/hard/0/id", "criterion_id_missing"},
		},
		{
			name:   "9b_criterion_ids_unique",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				hardAt(document, 2, 0)["id"] = "goal-review"
			},
			want: Diagnostic{Rule09, "/movements/2/acceptance/review/0/id", "duplicate_criterion_id"},
		},
		{
			name:   "10_allowed_paths_unique",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				objectAt(document["policy"])["allowed_paths"] =
					[]any{"internal/**", "internal/**"}
			},
			want: Diagnostic{Rule10, "/policy/allowed_paths/1", "duplicate_allowed_path"},
		},
		{
			name:   "11a_verified_achievable",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				acceptanceAt(document, 2)["hard"] = []any{}
			},
			want: Diagnostic{Rule11, "/verification/expectation/apply_gate/require/0", "verified_unachievable"},
		},
		{
			name:   "11b_reviewed_achievable",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				acceptanceAt(document, 2)["review"] = []any{}
			},
			want: Diagnostic{Rule11, "/verification/expectation/apply_gate/require/1", "reviewed_unachievable"},
		},
		{
			name:   "11c_review_must_be_typed",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				outputAt(document, 2, 0)["kind"] = "document"
			},
			want: Diagnostic{Rule11, "/verification/expectation/apply_gate/require/1", "reviewed_unachievable"},
		},
		{
			name:   "11d_predicate_achievable",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				gateAt(document)["require"] = []any{"verified"}
				acceptanceAt(document, 2)["review"] = []any{}
			},
			want: Diagnostic{Rule11, "/verification/expectation/apply_gate/predicates", "predicate_unachievable"},
		},
		{
			name:   "11e_predicate_review_must_be_typed",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				gateAt(document)["require"] = []any{"verified"}
				outputAt(document, 2, 0)["kind"] = "document"
			},
			want: Diagnostic{Rule11, "/verification/expectation/apply_gate/predicates", "predicate_unachievable"},
		},
		{
			name:   "11f_approved_achievable",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				acceptanceAt(document, 2)["human_gate"] = "never"
			},
			want: Diagnostic{Rule11, "/verification/expectation/apply_gate/require/2", "approved_unachievable"},
		},
		{
			name:   "12a_waived_omits_final",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				gate := gateAt(document)
				delete(gate, "require")
				gate["waived"] = true
				gate["reason"] = "deliberately ungated"
			},
			want: Diagnostic{Rule12, "/verification/final_movement", "waived_final_movement_present"},
		},
		{
			name:   "12b_final_declared",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				delete(objectAt(document["verification"]), "final_movement")
			},
			want: Diagnostic{Rule12, "/verification/final_movement", "final_movement_missing"},
		},
		{
			name:   "12c_final_reference_exists",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				objectAt(document["verification"])["final_movement"] = "missing"
			},
			want: Diagnostic{Rule12, "/verification/final_movement", "final_movement_unknown"},
		},
		{
			name:   "12d_final_no_write",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				objectAtKey(document, "parts", "verify")["capabilities"] =
					[]any{"repo_read", "repo_write", "shell"}
				movementAt(document, 2)["grants"] =
					[]any{"repo_read", "repo_write", "shell"}
			},
			want: Diagnostic{Rule12, "/verification/final_movement", "final_movement_repo_write"},
		},
		{
			name:   "12e_final_is_sink",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				document["movements"] = append(arrayAt(document, "movements"), map[string]any{
					"id":          "after",
					"part":        "plan",
					"needs":       []any{"verify"},
					"instruction": "after final",
				})
			},
			want: Diagnostic{Rule12, "/movements/3/needs/0", "final_movement_has_downstream"},
		},
		{
			name:   "12f_final_closure",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				document["movements"] = append(arrayAt(document, "movements"), map[string]any{
					"id":          "orphan",
					"part":        "plan",
					"instruction": "outside closure",
				})
			},
			want: Diagnostic{Rule12, "/movements/3", "outside_final_movement_closure"},
		},
		{
			name:   "13a_schema_number_safe",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				document["revision"] = 1.5
			},
			want: Diagnostic{Rule13, "/revision", "not_safe_integer"},
		},
		{
			name: "13c_extensions_are_opaque",
			accept: func() map[string]any {
				document := finalizedFixture()
				document["extensions"] = map[string]any{
					"vendor": map[string]any{"fraction": 1.5},
				}
				return document
			},
			mutate: func(document map[string]any) {
				document["revision"] = 1.5
			},
			want: Diagnostic{Rule13, "/revision", "not_safe_integer"},
		},
		{
			name:   "14a_draft_no_artifact_output",
			accept: draftFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 0)["outputs"] = []any{
					map[string]any{"id": "note", "kind": "document"},
				}
			},
			want: Diagnostic{Rule14, "/movements/0/outputs/0", "draft_artifact_output"},
		},
		{
			name:   "14b_draft_no_change_set",
			accept: draftFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 0)["outputs"] = []any{
					map[string]any{"id": "change", "kind": "change_set"},
				}
			},
			want: Diagnostic{Rule14, "/movements/0/outputs/0", "draft_change_set_output"},
		},
		{
			name:   "15a_write_has_one_change_set",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 1)["outputs"] = []any{}
			},
			want: Diagnostic{Rule15, "/movements/1/outputs", "write_change_set_count"},
		},
		{
			name:   "15b_write_has_at_most_one_change_set",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 1)["outputs"] = append(
					arrayAt(movementAt(document, 1), "outputs"),
					map[string]any{"id": "second-change", "kind": "change_set"},
				)
			},
			want: Diagnostic{Rule15, "/movements/1/outputs", "write_change_set_count"},
		},
		{
			name:   "15c_nonwrite_has_no_change_set",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 0)["outputs"] = append(
					arrayAt(movementAt(document, 0), "outputs"),
					map[string]any{"id": "extra-change", "kind": "change_set"},
				)
			},
			want: Diagnostic{Rule15, "/movements/0/outputs", "nonwrite_change_set"},
		},
		{
			name:   "16_identifier_grammar",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 0)["id"] = "bad id"
				movementAt(document, 1)["needs"] = []any{"bad id"}
			},
			want: Diagnostic{Rule16, "/movements/0/id", "invalid_identifier"},
		},
		{
			name:   "17_one_artifact_criterion",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				acceptanceAt(document, 0)["hard"] = []any{
					map[string]any{"id": "design-exists", "artifact": "design-note"},
					map[string]any{"id": "design-hash", "artifact": "design-note"},
				}
			},
			want: Diagnostic{Rule17, "/movements/0/acceptance/hard/1/artifact", "duplicate_artifact_criterion"},
		},
		{
			name:   "18_no_change_set_artifact_criterion",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				acceptanceAt(document, 1)["hard"] = append(
					arrayAt(acceptanceAt(document, 1), "hard"),
					map[string]any{"id": "change-exists", "artifact": "change-set"},
				)
			},
			want: Diagnostic{Rule18, "/movements/1/acceptance/hard/1/artifact", "change_set_artifact_criterion"},
		},
		{
			name:   "18_change_set_reference_is_global",
			accept: finalizedFixture,
			mutate: func(document map[string]any) {
				acceptanceAt(document, 2)["hard"] = append(
					arrayAt(acceptanceAt(document, 2), "hard"),
					map[string]any{"id": "cross-change", "artifact": "change-set"},
				)
			},
			want: Diagnostic{Rule18, "/movements/2/acceptance/hard/1/artifact", "change_set_artifact_criterion"},
		},
		{
			name:   "19b_draft_explicit_false",
			accept: draftFixture,
			mutate: func(document map[string]any) {
				movementAt(document, 0)["may_propose"] = false
			},
			want: Diagnostic{Rule19, "/movements/0/may_propose", "draft_may_propose_false"},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			accepted := test.accept()
			assertCompiles(t, accepted)
			rejected := cloneFixture(accepted)
			test.mutate(rejected)
			diagnostics := compileFixture(t, rejected)
			if !slices.Contains(diagnostics, test.want) {
				t.Fatalf("missing targeted diagnostic %#v\ngot: %#v", test.want, diagnostics)
			}
		})
	}
}

func TestRule04PartIDUniquenessIsCoveredByIngress(t *testing.T) {
	t.Parallel()
	source := []byte(`
score: "0.2"
name: duplicate-parts
revision: 1
status: draft
goal: prove duplicate mapping keys are rejected
draft:
  interview_movement: clarify
parts:
  plan:
    capabilities: [repo_read]
  plan:
    capabilities: [repo_read]
movements:
  - id: clarify
    phase: draft
    part: plan
    instruction: clarify
policy:
  budget:
    active_wall_clock_min: 1
`)
	score, diagnostics := Compile(source)
	if score != nil {
		t.Fatal("duplicate part key compiled")
	}
	want := []Diagnostic{{Rule: RuleIngress, Pointer: "", Detail: "invalid_restricted_yaml"}}
	if !slices.Equal(diagnostics, want) {
		t.Fatalf("diagnostics = %#v, want %#v", diagnostics, want)
	}
}

func TestRule13CollectsEverySchemaControlledNumber(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	document["revision"] = 1.5
	objectAtKey(document, "policy", "budget")["active_wall_clock_min"] = 2.5
	objectAtKey(document, "policy", "budget")["retries_per_movement"] = 3.5
	hardAt(document, 1, 0)["timeout_min"] = 4.5
	document["extensions"] = map[string]any{
		"vendor": map[string]any{"fraction": 5.5},
	}
	diagnostics := compileFixture(t, document)
	var got []Diagnostic
	for _, diagnostic := range diagnostics {
		if diagnostic.Rule == Rule13 {
			got = append(got, diagnostic)
		}
	}
	want := []Diagnostic{
		{Rule13, "/movements/1/acceptance/hard/0/timeout_min", "not_safe_integer"},
		{Rule13, "/policy/budget/active_wall_clock_min", "not_safe_integer"},
		{Rule13, "/policy/budget/retries_per_movement", "not_safe_integer"},
		{Rule13, "/revision", "not_safe_integer"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Rule13 diagnostics = %#v, want %#v", got, want)
	}
}

func assertCompiles(t *testing.T, document map[string]any) *Score {
	t.Helper()
	source, err := canonical.Encode(document)
	if err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := Compile(source)
	if len(diagnostics) != 0 || compiled == nil {
		t.Fatalf("valid fixture failed: %#v", diagnostics)
	}
	return compiled
}

func compileFixture(t *testing.T, document map[string]any) []Diagnostic {
	t.Helper()
	source, err := canonical.Encode(document)
	if err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := Compile(source)
	if compiled != nil {
		t.Fatal("rejecting fixture compiled")
	}
	return diagnostics
}

func cloneFixture(document map[string]any) map[string]any {
	return cloneJSON(document).(map[string]any)
}

func finalizedFixture() map[string]any {
	return map[string]any{
		"score":    "0.2",
		"name":     "compiler-fixture",
		"revision": float64(1),
		"status":   "finalized",
		"goal":     "Compile a complete score.",
		"open_questions": []any{
			map[string]any{
				"id":         "q-1",
				"question":   "Is the intent complete?",
				"resolution": "Yes.",
			},
		},
		"verification": map[string]any{
			"expectation": map[string]any{
				"intent": "pass-existing-tests",
				"apply_gate": map[string]any{
					"require":    []any{"verified", "reviewed", "approved"},
					"predicates": []any{"no_unresolved_blocking_findings"},
				},
			},
			"final_movement": "verify",
		},
		"parts": map[string]any{
			"plan": map[string]any{
				"capabilities": []any{"repo_read"},
			},
			"write": map[string]any{
				"capabilities": []any{"repo_read", "repo_write", "shell"},
			},
			"verify": map[string]any{
				"capabilities": []any{"repo_read", "shell"},
				"read_only":    true,
			},
		},
		"movements": []any{
			map[string]any{
				"id":          "plan",
				"part":        "plan",
				"grants":      []any{"repo_read"},
				"instruction": "Plan the work.",
				"outputs": []any{
					map[string]any{"id": "design-note", "kind": "document"},
				},
			},
			map[string]any{
				"id":          "build",
				"part":        "write",
				"needs":       []any{"plan"},
				"grants":      []any{"repo_read", "repo_write", "shell"},
				"instruction": "Implement the work.",
				"inputs":      []any{"design-note"},
				"outputs": []any{
					map[string]any{"id": "change-set", "kind": "change_set"},
				},
				"acceptance": map[string]any{
					"hard": []any{
						map[string]any{"id": "unit-tests", "run": []any{"go", "test", "./..."}},
					},
				},
			},
			map[string]any{
				"id":          "verify",
				"part":        "verify",
				"needs":       []any{"build"},
				"grants":      []any{"repo_read", "shell"},
				"instruction": "Verify the result.",
				"inputs":      []any{"change-set"},
				"outputs": []any{
					map[string]any{"id": "review-findings", "kind": "findings"},
				},
				"acceptance": map[string]any{
					"hard": []any{
						map[string]any{"id": "full-suite", "run": []any{"go", "test", "./..."}},
					},
					"review": []any{
						map[string]any{
							"id":       "goal-review",
							"findings": "review-findings",
							"rubric":   []any{"coverage", "risk"},
						},
					},
					"human_gate": "always",
				},
			},
		},
		"policy": map[string]any{
			"allowed_paths": []any{"internal/**", "cmd/**"},
			"side_effects":  []any{},
			"budget": map[string]any{
				"active_wall_clock_min": float64(90),
				"retries_per_movement":  float64(2),
			},
			"amendment": map[string]any{"auto": "off"},
		},
	}
}

func draftFixture() map[string]any {
	return map[string]any{
		"score":    "0.2",
		"name":     "draft-fixture",
		"revision": float64(1),
		"status":   "draft",
		"goal":     "Clarify the score.",
		"draft": map[string]any{
			"interview_movement": "clarify",
		},
		"parts": map[string]any{
			"plan": map[string]any{
				"capabilities": []any{"repo_read"},
			},
		},
		"movements": []any{
			map[string]any{
				"id":          "clarify",
				"phase":       "draft",
				"part":        "plan",
				"grants":      []any{"repo_read"},
				"instruction": "Clarify unresolved requirements.",
			},
		},
		"policy": map[string]any{
			"budget": map[string]any{
				"active_wall_clock_min": float64(10),
			},
		},
	}
}

func objectAt(value any) map[string]any {
	return value.(map[string]any)
}

func arrayAt(object map[string]any, key string) []any {
	return object[key].([]any)
}

func objectAtKey(object map[string]any, keys ...string) map[string]any {
	current := object
	for _, key := range keys {
		current = objectAt(current[key])
	}
	return current
}

func movementAt(document map[string]any, index int) map[string]any {
	return objectAt(arrayAt(document, "movements")[index])
}

func outputAt(document map[string]any, movementIndex, outputIndex int) map[string]any {
	return objectAt(arrayAt(movementAt(document, movementIndex), "outputs")[outputIndex])
}

func acceptanceAt(document map[string]any, movementIndex int) map[string]any {
	movement := movementAt(document, movementIndex)
	if value, exists := movement["acceptance"]; exists {
		return objectAt(value)
	}
	acceptance := map[string]any{}
	movement["acceptance"] = acceptance
	return acceptance
}

func hardAt(document map[string]any, movementIndex, hardIndex int) map[string]any {
	return objectAt(arrayAt(acceptanceAt(document, movementIndex), "hard")[hardIndex])
}

func reviewAt(document map[string]any, movementIndex, reviewIndex int) map[string]any {
	return objectAt(arrayAt(acceptanceAt(document, movementIndex), "review")[reviewIndex])
}

func expectationAt(document map[string]any) map[string]any {
	return objectAtKey(document, "verification", "expectation")
}

func gateAt(document map[string]any) map[string]any {
	return objectAt(expectationAt(document)["apply_gate"])
}

func TestFixtureEncodingIsStable(t *testing.T) {
	t.Parallel()
	left, err := canonical.Encode(finalizedFixture())
	if err != nil {
		t.Fatal(err)
	}
	right, err := canonical.Encode(finalizedFixture())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatal("fixture encoding is not stable")
	}
}
