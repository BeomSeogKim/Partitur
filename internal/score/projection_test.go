package score

import (
	"bytes"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

func TestDefaultsGoldenDraft(t *testing.T) {
	t.Parallel()
	omitted := draftFixture()
	delete(movementAt(omitted, 0), "grants")
	explicit := cloneFixture(omitted)
	explicit["open_questions"] = []any{}
	objectAtKey(explicit, "parts", "plan")["read_only"] = false
	movement := movementAt(explicit, 0)
	movement["needs"] = []any{}
	movement["grants"] = []any{}
	movement["inputs"] = []any{}
	movement["outputs"] = []any{}
	movement["may_propose"] = true
	movement["acceptance"] = map[string]any{
		"hard":       []any{},
		"review":     []any{},
		"human_gate": "never",
	}
	policy := objectAt(explicit["policy"])
	policy["allowed_paths"] = []any{}
	policy["side_effects"] = []any{}
	objectAt(policy["budget"])["retries_per_movement"] = float64(0)
	policy["amendment"] = map[string]any{"auto": "off"}

	omittedScore := assertCompiles(t, omitted)
	explicitScore := assertCompiles(t, explicit)
	omittedBytes, err := omittedScore.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	explicitBytes, err := explicitScore.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(omittedBytes, explicitBytes) {
		t.Fatalf("omitted and explicit defaults differ\nomitted: %s\nexplicit: %s",
			omittedBytes, explicitBytes)
	}

	const golden = `{"draft":{"interview_movement":"clarify"},"goal":"Clarify the score.","movements":[{"acceptance":{"hard":[],"human_gate":"never","review":[]},"grants":[],"id":"clarify","inputs":[],"instruction":"Clarify unresolved requirements.","may_propose":true,"needs":[],"outputs":[],"part":"plan","phase":"draft"}],"name":"draft-fixture","open_questions":[],"parts":{"plan":{"capabilities":["repo_read"],"read_only":false}},"policy":{"allowed_paths":[],"amendment":{"auto":"off"},"budget":{"active_wall_clock_min":10,"retries_per_movement":0},"side_effects":[]},"revision":1,"score":"0.2","status":"draft"}`
	if string(omittedBytes) != golden {
		t.Fatalf("projection golden changed\n got: %s\nwant: %s", omittedBytes, golden)
	}
}

func TestProjectionRequiresDefaultsPass(t *testing.T) {
	t.Parallel()
	unmaterialized := &Score{}
	if _, err := unmaterialized.ProjectionBytes(); err == nil {
		t.Fatal("ProjectionBytes accepted a score before defaults")
	}
	if _, err := unmaterialized.Hash(); err == nil {
		t.Fatal("Hash accepted a score before defaults")
	}
}

func TestApplyGatePredicatesDefault(t *testing.T) {
	t.Parallel()
	omitted := finalizedFixture()
	delete(gateAt(omitted), "predicates")
	explicit := cloneFixture(omitted)
	gateAt(explicit)["predicates"] = []any{}

	omittedBytes, err := assertCompiles(t, omitted).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	explicitBytes, err := assertCompiles(t, explicit).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(omittedBytes, explicitBytes) {
		t.Fatalf("predicate default differs\nomitted: %s\nexplicit: %s",
			omittedBytes, explicitBytes)
	}
	if !bytes.Contains(omittedBytes, []byte(`"predicates":[]`)) {
		t.Fatalf("predicate default is not materialized: %s", omittedBytes)
	}
}

func TestDefaultMayProposeFalseOutsideDraft(t *testing.T) {
	t.Parallel()
	omitted := finalizedFixture()
	explicit := cloneFixture(omitted)
	for index := range arrayAt(explicit, "movements") {
		movementAt(explicit, index)["may_propose"] = false
	}
	omittedBytes, err := assertCompiles(t, omitted).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	explicitBytes, err := assertCompiles(t, explicit).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(omittedBytes, explicitBytes) {
		t.Fatalf("ordinary may_propose default differs\nomitted: %s\nexplicit: %s",
			omittedBytes, explicitBytes)
	}
	if bytes.Count(omittedBytes, []byte(`"may_propose":false`)) != 3 {
		t.Fatalf("ordinary defaults are not materialized: %s", omittedBytes)
	}
}

func TestAbsentDefaultsOmitAndNullRejects(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fixture func() map[string]any
		absent  string
		setNull func(map[string]any)
		pointer string
	}{
		{
			name:    "context",
			fixture: finalizedFixture,
			absent:  `"context"`,
			setNull: func(document map[string]any) { document["context"] = nil },
			pointer: "/context",
		},
		{
			name:    "phase",
			fixture: finalizedFixture,
			absent:  `"phase"`,
			setNull: func(document map[string]any) { movementAt(document, 0)["phase"] = nil },
			pointer: "/movements/0/phase",
		},
		{
			name:    "timeout_min",
			fixture: finalizedFixture,
			absent:  `"timeout_min"`,
			setNull: func(document map[string]any) {
				hardAt(document, 1, 0)["timeout_min"] = nil
			},
			pointer: "/movements/1/acceptance/hard/0/timeout_min",
		},
		{
			name: "expected_hash",
			fixture: func() map[string]any {
				document := finalizedFixture()
				acceptanceAt(document, 0)["hard"] = []any{
					map[string]any{"id": "design-exists", "artifact": "design-note"},
				}
				return document
			},
			absent: `"expected_hash"`,
			setNull: func(document map[string]any) {
				hardAt(document, 0, 0)["expected_hash"] = nil
			},
			pointer: "/movements/0/acceptance/hard/0/expected_hash",
		},
		{
			name:    "draft_block",
			fixture: finalizedFixture,
			absent:  `"draft"`,
			setNull: func(document map[string]any) { document["draft"] = nil },
			pointer: "/draft",
		},
		{
			name:    "verification",
			fixture: draftFixture,
			absent:  `"verification"`,
			setNull: func(document map[string]any) { document["verification"] = nil },
			pointer: "/verification",
		},
		{
			name:    "extensions",
			fixture: finalizedFixture,
			absent:  `"extensions"`,
			setNull: func(document map[string]any) { document["extensions"] = nil },
			pointer: "/extensions",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			projection, err := assertCompiles(t, test.fixture()).ProjectionBytes()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(projection, []byte(test.absent)) {
				t.Fatalf("%s default was not omitted: %s", test.name, projection)
			}
			nullFixture := test.fixture()
			test.setNull(nullFixture)
			diagnostics := compileFixture(t, nullFixture)
			want := Diagnostic{RuleSchema, test.pointer, "must_not_be_null"}
			if !containsDiagnostic(diagnostics, want) {
				t.Fatalf("missing null diagnostic %#v\ngot: %#v", want, diagnostics)
			}
		})
	}
}

func TestMaterialDefaultFieldsRejectNull(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		fixture func() map[string]any
		setNull func(map[string]any)
		pointer string
	}{
		{
			name:    "open_questions",
			fixture: draftFixture,
			setNull: func(document map[string]any) { document["open_questions"] = nil },
			pointer: "/open_questions",
		},
		{
			name:    "part_read_only",
			fixture: draftFixture,
			setNull: func(document map[string]any) {
				objectAtKey(document, "parts", "plan")["read_only"] = nil
			},
			pointer: "/parts/plan/read_only",
		},
		{
			name:    "movement_needs",
			fixture: draftFixture,
			setNull: func(document map[string]any) { movementAt(document, 0)["needs"] = nil },
			pointer: "/movements/0/needs",
		},
		{
			name:    "movement_grants",
			fixture: draftFixture,
			setNull: func(document map[string]any) { movementAt(document, 0)["grants"] = nil },
			pointer: "/movements/0/grants",
		},
		{
			name:    "movement_inputs",
			fixture: draftFixture,
			setNull: func(document map[string]any) { movementAt(document, 0)["inputs"] = nil },
			pointer: "/movements/0/inputs",
		},
		{
			name:    "movement_outputs",
			fixture: draftFixture,
			setNull: func(document map[string]any) { movementAt(document, 0)["outputs"] = nil },
			pointer: "/movements/0/outputs",
		},
		{
			name:    "movement_may_propose",
			fixture: finalizedFixture,
			setNull: func(document map[string]any) { movementAt(document, 0)["may_propose"] = nil },
			pointer: "/movements/0/may_propose",
		},
		{
			name:    "movement_acceptance",
			fixture: finalizedFixture,
			setNull: func(document map[string]any) { movementAt(document, 0)["acceptance"] = nil },
			pointer: "/movements/0/acceptance",
		},
		{
			name:    "gate_predicates",
			fixture: finalizedFixture,
			setNull: func(document map[string]any) { gateAt(document)["predicates"] = nil },
			pointer: "/verification/expectation/apply_gate/predicates",
		},
		{
			name:    "allowed_paths",
			fixture: finalizedFixture,
			setNull: func(document map[string]any) {
				objectAt(document["policy"])["allowed_paths"] = nil
			},
			pointer: "/policy/allowed_paths",
		},
		{
			name:    "side_effects",
			fixture: finalizedFixture,
			setNull: func(document map[string]any) {
				objectAt(document["policy"])["side_effects"] = nil
			},
			pointer: "/policy/side_effects",
		},
		{
			name:    "retries",
			fixture: finalizedFixture,
			setNull: func(document map[string]any) {
				objectAtKey(document, "policy", "budget")["retries_per_movement"] = nil
			},
			pointer: "/policy/budget/retries_per_movement",
		},
		{
			name:    "amendment_auto",
			fixture: finalizedFixture,
			setNull: func(document map[string]any) {
				objectAtKey(document, "policy", "amendment")["auto"] = nil
			},
			pointer: "/policy/amendment/auto",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			document := test.fixture()
			test.setNull(document)
			diagnostics := compileFixture(t, document)
			want := Diagnostic{RuleSchema, test.pointer, "must_not_be_null"}
			if !containsDiagnostic(diagnostics, want) {
				t.Fatalf("missing null diagnostic %#v\ngot: %#v", want, diagnostics)
			}
		})
	}
}

func TestProjectionOrderingSemantics(t *testing.T) {
	t.Parallel()
	base := finalizedFixture()
	base["open_questions"] = append(arrayAt(base, "open_questions"), map[string]any{
		"id":         "q-2",
		"question":   "Is ordering semantic?",
		"resolution": "Only where specified.",
	})
	movementAt(base, 0)["outputs"] = append(
		arrayAt(movementAt(base, 0), "outputs"),
		map[string]any{"id": "design-extra", "kind": "document"},
	)
	movementAt(base, 1)["inputs"] = []any{"design-note", "design-extra"}
	movementAt(base, 2)["needs"] = []any{"build", "plan"}
	gateAt(base)["predicates"] = []any{
		"no_unresolved_blocking_findings",
		"no_blocking_findings",
	}
	acceptanceAt(base, 2)["review"] = append(
		arrayAt(acceptanceAt(base, 2), "review"),
		map[string]any{
			"id":       "risk-review",
			"findings": "review-findings",
			"rubric":   []any{"risk"},
		},
	)
	reorderedSets := cloneFixture(base)
	objectAtKey(reorderedSets, "parts", "write")["capabilities"] =
		[]any{"shell", "repo_write", "repo_read"}
	movementAt(reorderedSets, 1)["grants"] =
		[]any{"shell", "repo_write", "repo_read"}
	objectAt(reorderedSets["policy"])["allowed_paths"] =
		[]any{"cmd/**", "internal/**"}
	gateAt(reorderedSets)["require"] =
		[]any{"approved", "reviewed", "verified"}
	gateAt(reorderedSets)["predicates"] =
		[]any{"no_blocking_findings", "no_unresolved_blocking_findings"}
	reviewAt(reorderedSets, 2, 0)["rubric"] =
		[]any{"risk", "coverage"}
	questions := arrayAt(reorderedSets, "open_questions")
	questions[0], questions[1] = questions[1], questions[0]
	inputs := arrayAt(movementAt(reorderedSets, 1), "inputs")
	inputs[0], inputs[1] = inputs[1], inputs[0]
	needs := arrayAt(movementAt(reorderedSets, 2), "needs")
	needs[0], needs[1] = needs[1], needs[0]

	baseBytes, err := assertCompiles(t, base).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	reorderedBytes, err := assertCompiles(t, reorderedSets).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(baseBytes, reorderedBytes) {
		t.Fatalf("set reordering changed projection\nbase: %s\nnew:  %s",
			baseBytes, reorderedBytes)
	}

	reorderedMovements := cloneFixture(base)
	movements := arrayAt(reorderedMovements, "movements")
	movements[0], movements[1] = movements[1], movements[0]
	movementBytes, err := assertCompiles(t, reorderedMovements).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(baseBytes, movementBytes) {
		t.Fatal("movement declaration order was erased")
	}

	reorderedCriteria := cloneFixture(base)
	acceptance := acceptanceAt(reorderedCriteria, 2)
	acceptance["hard"] = append(arrayAt(acceptance, "hard"),
		map[string]any{"id": "second-suite", "run": []any{"go", "vet", "./..."}})
	firstCriteriaBytes, err := assertCompiles(t, reorderedCriteria).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	hard := arrayAt(acceptance, "hard")
	hard[0], hard[1] = hard[1], hard[0]
	secondCriteriaBytes, err := assertCompiles(t, reorderedCriteria).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstCriteriaBytes, secondCriteriaBytes) {
		t.Fatal("criterion declaration order was erased")
	}

	reorderedOutputs := cloneFixture(base)
	outputs := arrayAt(movementAt(reorderedOutputs, 0), "outputs")
	outputs[0], outputs[1] = outputs[1], outputs[0]
	outputBytes, err := assertCompiles(t, reorderedOutputs).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(baseBytes, outputBytes) {
		t.Fatal("output declaration order was erased")
	}

	reorderedReviews := cloneFixture(base)
	reviews := arrayAt(acceptanceAt(reorderedReviews, 2), "review")
	reviews[0], reviews[1] = reviews[1], reviews[0]
	reviewBytes, err := assertCompiles(t, reorderedReviews).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(baseBytes, reviewBytes) {
		t.Fatal("review declaration order was erased")
	}
}

func TestHashUsesScoreDomainProjection(t *testing.T) {
	t.Parallel()
	compiled := assertCompiles(t, finalizedFixture())
	projection, err := compiled.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	value, err := canonical.ParseJSON(projection)
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical.Hash(canonical.DomainScore, value)
	if err != nil {
		t.Fatal(err)
	}
	got, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Hash() = %q, want %q", got, want)
	}
}

func TestReadViewsAreDefensive(t *testing.T) {
	t.Parallel()
	compiled := assertCompiles(t, finalizedFixture())
	before, err := compiled.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}

	parts := compiled.Parts()
	parts[0].Capabilities[0] = "network"
	movements := compiled.Movements()
	movements[0].Grants[0] = "network"
	policy := compiled.EffectivePolicy()
	policy.AllowedPaths[0] = "**"

	after, err := compiled.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("mutating a read view changed the score projection")
	}
	if strings.Join(compiled.Parts()[0].Capabilities, ",") == "network" {
		t.Fatal("part view exposed mutable interior state")
	}
}

func TestProjectionDelegatesNonBMPKeyOrderingToCanonical(t *testing.T) {
	t.Parallel()
	document := finalizedFixture()
	document["extensions"] = map[string]any{
		"vendor": map[string]any{
			"\ue000":     "bmp",
			"\U00010000": "supplementary",
		},
	}
	objectAt(document["policy"])["allowed_paths"] = []any{"", "𐀀"}
	projection, err := assertCompiles(t, document).ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	supplementary := bytes.Index(projection, []byte("\"𐀀\""))
	bmp := bytes.Index(projection, []byte("\"\""))
	if supplementary < 0 || bmp < 0 || supplementary > bmp {
		t.Fatalf("projection did not preserve JCS UTF-16 key order: %s", projection)
	}
	if !bytes.Contains(projection, []byte(`"allowed_paths":["𐀀",""]`)) {
		t.Fatalf("set sorting did not use canonical string order: %s", projection)
	}
}

func containsDiagnostic(diagnostics []Diagnostic, want Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic == want {
			return true
		}
	}
	return false
}
