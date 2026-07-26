package score

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestRule01WaivedQuestionIsAccepted(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	question := objectAt(arrayAt(document, "open_questions")[0])
	delete(question, "resolution")
	question["waived"] = true
	assertCompiles(t, document)
}

func TestApplyGateSchemaConformance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   Diagnostic
	}{
		{
			"require_non_empty",
			func(document map[string]any) { gateAt(document)["require"] = []any{} },
			Diagnostic{RuleSchema, "/verification/expectation/apply_gate/require", "must_be_non_empty"},
		},
		{
			"require_unique",
			func(document map[string]any) {
				gateAt(document)["require"] = []any{"verified", "verified"}
			},
			Diagnostic{RuleSchema, "/verification/expectation/apply_gate/require/1", "duplicate_value"},
		},
		{
			"require_closed",
			func(document map[string]any) { gateAt(document)["require"] = []any{"unknown"} },
			Diagnostic{RuleSchema, "/verification/expectation/apply_gate/require/0", "invalid_value"},
		},
		{
			"predicates_closed",
			func(document map[string]any) { gateAt(document)["predicates"] = []any{"unknown"} },
			Diagnostic{RuleSchema, "/verification/expectation/apply_gate/predicates/0", "invalid_value"},
		},
		{
			"predicates_unique",
			func(document map[string]any) {
				gateAt(document)["predicates"] =
					[]any{"no_blocking_findings", "no_blocking_findings"}
			},
			Diagnostic{RuleSchema, "/verification/expectation/apply_gate/predicates/1", "duplicate_value"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			document := finalizedFixture()
			test.mutate(document)
			diagnostics := compileFixture(t, document)
			if !slices.Contains(diagnostics, test.want) {
				t.Fatalf("missing apply-gate schema diagnostic %#v\ngot: %#v",
					test.want, diagnostics)
			}
		})
	}
}

func TestNormativeTableSchemaConstraints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   Diagnostic
	}{
		{"score_version", func(document map[string]any) {
			document["score"] = "0.3"
		}, Diagnostic{RuleSchema, "/score", "invalid_value"}},
		{"revision_minimum", func(document map[string]any) {
			document["revision"] = float64(0)
		}, Diagnostic{RuleSchema, "/revision", "below_minimum"}},
		{"status_closed", func(document map[string]any) {
			document["status"] = "unknown"
		}, Diagnostic{RuleSchema, "/status", "invalid_value"}},
		{"capabilities_non_empty", func(document map[string]any) {
			objectAtKey(document, "parts", "plan")["capabilities"] = []any{}
		}, Diagnostic{RuleSchema, "/parts/plan/capabilities", "must_be_non_empty"}},
		{"capabilities_unique", func(document map[string]any) {
			objectAtKey(document, "parts", "plan")["capabilities"] =
				[]any{"repo_read", "repo_read"}
		}, Diagnostic{RuleSchema, "/parts/plan/capabilities/1", "duplicate_value"}},
		{"phase_closed", func(document map[string]any) {
			movementAt(document, 0)["phase"] = "unknown"
		}, Diagnostic{RuleSchema, "/movements/0/phase", "invalid_value"}},
		{"output_kind_non_empty", func(document map[string]any) {
			outputAt(document, 0, 0)["kind"] = ""
		}, Diagnostic{RuleSchema, "/movements/0/outputs/0/kind", "must_be_non_empty"}},
		{"human_gate_closed", func(document map[string]any) {
			acceptanceAt(document, 1)["human_gate"] = "unknown"
		}, Diagnostic{RuleSchema, "/movements/1/acceptance/human_gate", "invalid_value"}},
		{"timeout_minimum", func(document map[string]any) {
			hardAt(document, 1, 0)["timeout_min"] = float64(0)
		}, Diagnostic{RuleSchema, "/movements/1/acceptance/hard/0/timeout_min", "below_minimum"}},
		{"expected_hash_shape", func(document map[string]any) {
			acceptanceAt(document, 0)["hard"] = []any{
				map[string]any{
					"id":            "design-hash",
					"artifact":      "design-note",
					"expected_hash": "md5:bad",
				},
			}
		}, Diagnostic{RuleSchema, "/movements/0/acceptance/hard/0/expected_hash", "invalid_value"}},
		{"side_effects_empty", func(document map[string]any) {
			objectAt(document["policy"])["side_effects"] = []any{"network-write"}
		}, Diagnostic{RuleSchema, "/policy/side_effects", "must_be_empty"}},
		{"active_budget_minimum", func(document map[string]any) {
			objectAtKey(document, "policy", "budget")["active_wall_clock_min"] = float64(0)
		}, Diagnostic{RuleSchema, "/policy/budget/active_wall_clock_min", "below_minimum"}},
		{"retry_minimum", func(document map[string]any) {
			objectAtKey(document, "policy", "budget")["retries_per_movement"] = float64(-1)
		}, Diagnostic{RuleSchema, "/policy/budget/retries_per_movement", "below_minimum"}},
		{"amendment_closed", func(document map[string]any) {
			objectAtKey(document, "policy", "amendment")["auto"] = "unknown"
		}, Diagnostic{RuleSchema, "/policy/amendment/auto", "invalid_value"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			document := finalizedFixture()
			test.mutate(document)
			diagnostics := compileFixture(t, document)
			if !slices.Contains(diagnostics, test.want) {
				t.Fatalf("missing defaults-table diagnostic %#v\ngot: %#v",
					test.want, diagnostics)
			}
		})
	}
}

func TestWaivedFinalizedScoreIsAccepted(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	question := objectAt(arrayAt(document, "open_questions")[0])
	delete(question, "resolution")
	question["waived"] = true
	gate := gateAt(document)
	delete(gate, "require")
	gate["waived"] = true
	gate["reason"] = "deliberately ungated"
	delete(objectAt(document["verification"]), "final_movement")
	assertCompiles(t, document)
}

func TestRule02AlternativeSatisfiersAreAccepted(t *testing.T) {
	t.Parallel()
	t.Run("human_gate_always", func(t *testing.T) {
		document := finalizedFixture()
		acceptance := acceptanceAt(document, 1)
		acceptance["hard"] = []any{}
		acceptance["human_gate"] = "always"
		assertCompiles(t, document)
	})
	t.Run("artifact_criterion", func(t *testing.T) {
		document := finalizedFixture()
		movementAt(document, 1)["outputs"] = append(
			arrayAt(movementAt(document, 1), "outputs"),
			map[string]any{"id": "build-log", "kind": "document"},
		)
		acceptanceAt(document, 1)["hard"] = []any{
			map[string]any{"id": "build-log-exists", "artifact": "build-log"},
		}
		assertCompiles(t, document)
	})
}

func TestRule06EveryCoreObjectIsStrict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fixture func() map[string]any
		inject  func(map[string]any)
		pointer string
	}{
		{"root", finalizedFixture, func(document map[string]any) {
			document["unexpected"] = true
		}, "/unexpected"},
		{"draft", draftFixture, func(document map[string]any) {
			objectAt(document["draft"])["unexpected"] = true
		}, "/draft/unexpected"},
		{"question", finalizedFixture, func(document map[string]any) {
			objectAt(arrayAt(document, "open_questions")[0])["unexpected"] = true
		}, "/open_questions/0/unexpected"},
		{"verification", finalizedFixture, func(document map[string]any) {
			objectAt(document["verification"])["unexpected"] = true
		}, "/verification/unexpected"},
		{"expectation", finalizedFixture, func(document map[string]any) {
			expectationAt(document)["unexpected"] = true
		}, "/verification/expectation/unexpected"},
		{"apply_gate", finalizedFixture, func(document map[string]any) {
			gateAt(document)["unexpected"] = true
		}, "/verification/expectation/apply_gate/unexpected"},
		{"part", finalizedFixture, func(document map[string]any) {
			objectAtKey(document, "parts", "plan")["unexpected"] = true
		}, "/parts/plan/unexpected"},
		{"movement", finalizedFixture, func(document map[string]any) {
			movementAt(document, 0)["unexpected"] = true
		}, "/movements/0/unexpected"},
		{"output", finalizedFixture, func(document map[string]any) {
			outputAt(document, 0, 0)["unexpected"] = true
		}, "/movements/0/outputs/0/unexpected"},
		{"acceptance", finalizedFixture, func(document map[string]any) {
			acceptanceAt(document, 1)["unexpected"] = true
		}, "/movements/1/acceptance/unexpected"},
		{"hard", finalizedFixture, func(document map[string]any) {
			hardAt(document, 1, 0)["unexpected"] = true
		}, "/movements/1/acceptance/hard/0/unexpected"},
		{"review", finalizedFixture, func(document map[string]any) {
			reviewAt(document, 2, 0)["unexpected"] = true
		}, "/movements/2/acceptance/review/0/unexpected"},
		{"policy", finalizedFixture, func(document map[string]any) {
			objectAt(document["policy"])["unexpected"] = true
		}, "/policy/unexpected"},
		{"budget", finalizedFixture, func(document map[string]any) {
			objectAtKey(document, "policy", "budget")["unexpected"] = true
		}, "/policy/budget/unexpected"},
		{"amendment", finalizedFixture, func(document map[string]any) {
			objectAtKey(document, "policy", "amendment")["unexpected"] = true
		}, "/policy/amendment/unexpected"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			document := test.fixture()
			test.inject(document)
			diagnostics := compileFixture(t, document)
			want := Diagnostic{Rule06, test.pointer, "unknown_field"}
			if !containsDiagnostic(diagnostics, want) {
				t.Fatalf("missing strict-field diagnostic %#v\ngot: %#v", want, diagnostics)
			}
		})
	}
}

func TestRule08FinalizedScoreMayRetainDraftMovement(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	document["draft"] = map[string]any{"interview_movement": "clarify"}
	document["movements"] = append(arrayAt(document, "movements"), map[string]any{
		"id":          "clarify",
		"phase":       "draft",
		"part":        "plan",
		"instruction": "Retained finalized interview.",
	})
	compiled := assertCompiles(t, document)
	movements := compiled.Movements()
	if !movements[len(movements)-1].MayPropose {
		t.Fatal("retained draft movement lost implicit may_propose")
	}
}

func TestRule04TransitiveInputIsAccepted(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	movementAt(document, 2)["inputs"] = []any{"change-set", "design-note"}
	assertCompiles(t, document)
}

func TestRule09ReviewCriterionIDIsRequired(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	delete(reviewAt(document, 2, 0), "id")
	diagnostics := compileFixture(t, document)
	want := Diagnostic{Rule09, "/movements/2/acceptance/review/0/id", "criterion_id_missing"}
	if !containsDiagnostic(diagnostics, want) {
		t.Fatalf("missing review-id diagnostic %#v\ngot: %#v", want, diagnostics)
	}
}

func TestRule13SafeIntegerBoundaries(t *testing.T) {
	t.Parallel()
	accepted := finalizedFixture()
	accepted["revision"] = float64(9007199254740991)
	assertCompiles(t, accepted)

	rejected := finalizedFixture()
	rejected["revision"] = float64(9007199254740992)
	diagnostics := compileFixture(t, rejected)
	want := Diagnostic{Rule13, "/revision", "not_safe_integer"}
	if !containsDiagnostic(diagnostics, want) {
		t.Fatalf("missing safe-range diagnostic %#v\ngot: %#v", want, diagnostics)
	}
}

func TestRule13DoesNotOwnUnknownCoreFields(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	document["unexpected_number"] = 1.5
	diagnostics := compileFixture(t, document)
	want := []Diagnostic{{Rule06, "/unexpected_number", "unknown_field"}}
	if !slices.Equal(diagnostics, want) {
		t.Fatalf("unknown numeric field diagnostics = %#v, want %#v", diagnostics, want)
	}
}

func TestRule16EveryIdentifierPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fixture func() map[string]any
		mutate  func(map[string]any)
		pointer string
	}{
		{"part_key", finalizedFixture, func(document map[string]any) {
			parts := objectAt(document["parts"])
			parts["bad id"] = parts["plan"]
			delete(parts, "plan")
			movementAt(document, 0)["part"] = "bad id"
		}, "/parts/bad id"},
		{"draft_reference", draftFixture, func(document map[string]any) {
			objectAt(document["draft"])["interview_movement"] = "bad id"
			movementAt(document, 0)["id"] = "bad id"
		}, "/draft/interview_movement"},
		{"final_reference", finalizedFixture, func(document map[string]any) {
			objectAt(document["verification"])["final_movement"] = "bad id"
			movementAt(document, 2)["id"] = "bad id"
		}, "/verification/final_movement"},
		{"question", finalizedFixture, func(document map[string]any) {
			objectAt(arrayAt(document, "open_questions")[0])["id"] = "bad id"
		}, "/open_questions/0/id"},
		{"movement", finalizedFixture, func(document map[string]any) {
			movementAt(document, 0)["id"] = "bad id"
			movementAt(document, 1)["needs"] = []any{"bad id"}
		}, "/movements/0/id"},
		{"empty_movement", finalizedFixture, func(document map[string]any) {
			movementAt(document, 0)["id"] = ""
			movementAt(document, 1)["needs"] = []any{""}
		}, "/movements/0/id"},
		{"part_reference", finalizedFixture, func(document map[string]any) {
			movementAt(document, 0)["part"] = "bad id"
		}, "/movements/0/part"},
		{"need_reference", finalizedFixture, func(document map[string]any) {
			movementAt(document, 1)["needs"] = []any{"bad id"}
		}, "/movements/1/needs/0"},
		{"input_reference", finalizedFixture, func(document map[string]any) {
			movementAt(document, 1)["inputs"] = []any{"bad id"}
		}, "/movements/1/inputs/0"},
		{"output", finalizedFixture, func(document map[string]any) {
			outputAt(document, 0, 0)["id"] = "bad id"
			movementAt(document, 1)["inputs"] = []any{"bad id"}
		}, "/movements/0/outputs/0/id"},
		{"hard_id", finalizedFixture, func(document map[string]any) {
			hardAt(document, 1, 0)["id"] = "bad id"
		}, "/movements/1/acceptance/hard/0/id"},
		{"artifact_reference", finalizedFixture, func(document map[string]any) {
			acceptanceAt(document, 0)["hard"] = []any{
				map[string]any{"id": "artifact-check", "artifact": "bad id"},
			}
		}, "/movements/0/acceptance/hard/0/artifact"},
		{"review_id", finalizedFixture, func(document map[string]any) {
			reviewAt(document, 2, 0)["id"] = "bad id"
		}, "/movements/2/acceptance/review/0/id"},
		{"findings_reference", finalizedFixture, func(document map[string]any) {
			reviewAt(document, 2, 0)["findings"] = "bad id"
		}, "/movements/2/acceptance/review/0/findings"},
		{"rubric", finalizedFixture, func(document map[string]any) {
			reviewAt(document, 2, 0)["rubric"] = []any{"bad id"}
		}, "/movements/2/acceptance/review/0/rubric/0"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			document := test.fixture()
			test.mutate(document)
			diagnostics := compileFixture(t, document)
			want := Diagnostic{Rule16, test.pointer, "invalid_identifier"}
			if !containsDiagnostic(diagnostics, want) {
				t.Fatalf("missing identifier diagnostic %#v\ngot: %#v", want, diagnostics)
			}
		})
	}
}

func TestRule16GrammarBoundaries(t *testing.T) {
	t.Parallel()
	rich := finalizedFixture()
	movementAt(rich, 0)["id"] = "9A_b-c"
	movementAt(rich, 1)["needs"] = []any{"9A_b-c"}
	assertCompiles(t, rich)

	maximum := "a" + strings.Repeat("b", 127)
	accepted := finalizedFixture()
	movementAt(accepted, 0)["id"] = maximum
	movementAt(accepted, 1)["needs"] = []any{maximum}
	assertCompiles(t, accepted)

	for name, id := range map[string]string{
		"leading_underscore": "_bad",
		"leading_hyphen":     "-bad",
		"dot_reserved_shape": "partitur.bad",
		"too_long":           maximum + "c",
	} {
		name, id := name, id
		t.Run(name, func(t *testing.T) {
			document := finalizedFixture()
			movementAt(document, 0)["id"] = id
			movementAt(document, 1)["needs"] = []any{id}
			diagnostics := compileFixture(t, document)
			want := Diagnostic{Rule16, "/movements/0/id", "invalid_identifier"}
			if !slices.Contains(diagnostics, want) {
				t.Fatalf("missing grammar boundary diagnostic %#v\ngot: %#v",
					want, diagnostics)
			}
		})
	}
}

func TestRule19MayProposeIsPermittedOnOrdinaryMovement(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	movementAt(document, 0)["may_propose"] = true
	compiled := assertCompiles(t, document)
	if !compiled.Movements()[0].MayPropose {
		t.Fatal("ordinary explicit may_propose true was not effective")
	}
	projection, err := compiled.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(projection, []byte(`"may_propose":true`)) != 1 {
		t.Fatalf("ordinary true was not materialized exactly once: %s", projection)
	}
}

func TestDiagnosticPointersEscapeDynamicKeys(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	parts := objectAt(document["parts"])
	parts["bad/key~"] = parts["plan"]
	delete(parts, "plan")
	movementAt(document, 0)["part"] = "bad/key~"
	diagnostics := compileFixture(t, document)
	want := Diagnostic{Rule16, "/parts/bad~1key~0", "invalid_identifier"}
	if !slices.Contains(diagnostics, want) {
		t.Fatalf("missing escaped pointer %#v\ngot: %#v", want, diagnostics)
	}
}
