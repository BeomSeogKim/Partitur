package executiondep

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestRecomputeDispatchesOnlyCompleteRecordedV3Tuple(t *testing.T) {
	compiled := testScore(t)
	versions := v3IdentityVersions()
	attempt := Attempt{ID: "attempt-1", MovementID: "inspect", AdapterID: "adapter", Model: "model", IdentityVersions: versions}
	hash, err := Recompute(compiled, attempt)
	if err != nil {
		t.Fatal(err)
	}
	attempt.RecordedHash = hash
	same, err := Equal(compiled, attempt)
	if err != nil || !same {
		t.Fatalf("same=%t err=%v", same, err)
	}
	for _, versions := range []json.RawMessage{
		json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/execution-dependency":3}}`),
		json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/acceptance-spec":1,"partitur/execution-dependency":3}}`),
		json.RawMessage(`{"canonical_encoding":0,"projections":{"partitur/acceptance-spec":1,"partitur/criterion-spec":1,"partitur/execution-dependency":3}}`),
		json.RawMessage(`{"canonical_encoding":2,"projections":{"partitur/acceptance-spec":1,"partitur/criterion-spec":1,"partitur/execution-dependency":3}}`),
		json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/acceptance-spec":1,"partitur/criterion-spec":1,"partitur/execution-dependency":2}}`),
		json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/acceptance-spec":1,"partitur/criterion-spec":1,"partitur/execution-dependency":4}}`),
		json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/acceptance-spec":1,"partitur/criterion-spec":0,"partitur/execution-dependency":3}}`),
		json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/acceptance-spec":1,"partitur/criterion-spec":2,"partitur/execution-dependency":3}}`),
	} {
		attempt.IdentityVersions = versions
		if _, err := Recompute(compiled, attempt); !errors.Is(err, canonical.ErrUnsupportedRunFormat) {
			t.Fatalf("versions=%s err=%v, want unsupported_run_format", versions, err)
		}
	}
}

func TestEqualPreservesRecordedProtectedPathsHash(t *testing.T) {
	attempt := Attempt{
		ID:               "attempt-1",
		MovementID:       "inspect",
		AdapterID:        "adapter",
		Model:            "model",
		IdentityVersions: v3IdentityVersions(),
		RecordedHash:     "sha256:ccf861b5258d377220ad33caec2e020a2bb75ea72691ec7dd013bc922b6fa7c9",
	}
	same, err := Equal(testScore(t), attempt)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatal("recorded protected-path hash no longer matches recomputation")
	}
}

func TestV3ProjectionDomainsFollowReachedA5Hashes(t *testing.T) {
	compiled := testScore(t)
	movement, ok := movementByID(compiled, "inspect")
	if !ok {
		t.Fatal("inspect movement is absent")
	}
	movement.MayPropose = true
	part, ok := partByID(compiled, movement.PartID)
	if !ok {
		t.Fatalf("part %q is absent", movement.PartID)
	}
	plan, err := acceptance.Compile(movement)
	if err != nil {
		t.Fatal(err)
	}
	value, err := projection(compiled, movement, part, Attempt{
		ID: "attempt-1", MovementID: "inspect", AdapterID: "adapter", Model: "model",
		BaseCompositionHash:  "sha256:composition",
		DeliveredResolutions: []runstate.DeliveredResolution{{DecisionID: "decision-1", Kind: "answer", Digest: "sha256:resolution"}},
	}, plan.Hash())
	if err != nil {
		t.Fatal(err)
	}
	got, err := V3ProjectionDomains(value)
	if err != nil {
		t.Fatal(err)
	}
	// This expected closure is written from A.5's hash chain, not from the
	// selector under test: outer -> acceptance -> criterion, plus each reached
	// optional hash in this concrete projection.
	want := []canonical.Domain{
		canonical.DomainExecutionDependency,
		canonical.DomainAcceptanceSpec,
		canonical.DomainCriterionSpec,
		canonical.DomainMovementComposition,
		canonical.DomainScore,
		canonical.DomainResolutionBody,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("reached domains = %v, want %v", got, want)
	}
}

func TestRecomputeRequiresEachReachedConditionalV3Domain(t *testing.T) {
	base := Attempt{ID: "attempt-1", MovementID: "inspect", AdapterID: "adapter", Model: "model", IdentityVersions: v3IdentityVersions()}
	for _, test := range []struct {
		name     string
		compiled *score.Score
		attempt  Attempt
		versions json.RawMessage
	}{
		{
			name:     "composition",
			compiled: testScore(t),
			attempt:  Attempt{BaseCompositionHash: "sha256:composition"},
			versions: v3IdentityVersions(),
		},
		{
			name:     "score",
			compiled: testScoreWithMayPropose(t, true),
			versions: v3IdentityVersions(),
		},
		{
			name:     "resolution body",
			compiled: testScore(t),
			attempt:  Attempt{DeliveredResolutions: []runstate.DeliveredResolution{{DecisionID: "decision-1", Kind: "answer", Digest: "sha256:resolution"}}},
			versions: v3IdentityVersions(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempt := base
			attempt.BaseCompositionHash = test.attempt.BaseCompositionHash
			attempt.DeliveredResolutions = test.attempt.DeliveredResolutions
			attempt.IdentityVersions = test.versions
			if _, err := Recompute(test.compiled, attempt); !errors.Is(err, canonical.ErrUnsupportedRunFormat) {
				t.Fatalf("err=%v, want unsupported_run_format", err)
			}
		})
	}
}

func TestRecomputeAcceptsCompleteReachedConditionalV3Tuple(t *testing.T) {
	attempt := Attempt{
		ID: "attempt-1", MovementID: "inspect", AdapterID: "adapter", Model: "model",
		BaseCompositionHash: "sha256:composition",
		DeliveredResolutions: []runstate.DeliveredResolution{
			{DecisionID: "decision-1", Kind: "answer", Digest: "sha256:resolution"},
		},
		IdentityVersions: json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/acceptance-spec":1,"partitur/criterion-spec":1,"partitur/execution-dependency":3,"partitur/movement-composition":1,"partitur/resolution-body":1,"partitur/score":1}}`),
	}
	if _, err := Recompute(testScoreWithMayPropose(t, true), attempt); err != nil {
		t.Fatalf("complete reached tuple: %v", err)
	}
}

func TestRecomputeBindsDeliveredFeedback(t *testing.T) {
	compiled := testScore(t)
	versions := v3IdentityVersions()
	first := Attempt{ID: "a", MovementID: "inspect", AdapterID: "adapter", Model: "model", IdentityVersions: versions, DeliveredFeedback: []runstate.DeliveredFeedback{{PreviousAttemptID: "previous", Kind: "diagnostic", ArtifactInstanceID: "artifact@previous", ContentHash: "sha256:one"}}}
	second := first
	second.DeliveredFeedback = []runstate.DeliveredFeedback{{PreviousAttemptID: "previous", Kind: "diagnostic", ArtifactInstanceID: "artifact@previous", ContentHash: "sha256:two"}}
	one, err := Recompute(compiled, first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Recompute(compiled, second)
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatal("feedback content hash did not affect recomputation")
	}
}

func testScore(t *testing.T) *score.Score {
	return testScoreWithMayPropose(t, false)
}

func testScoreWithMayPropose(t *testing.T, mayPropose bool) *score.Score {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(`{"score":"0.2","name":"execution","revision":1,"status":"finalized","goal":"goal","open_questions":[],"parts":{"reader":{"capabilities":["repo_read"],"read_only":true}},"movements":[{"id":"inspect","part":"reader","grants":["repo_read"],"instruction":"inspect","outputs":[{"id":"report","kind":"artifact"}],"acceptance":{"hard":[{"id":"report-present","artifact":"report"}]}}],"policy":{"allowed_paths":["src/**"],"budget":{"active_wall_clock_min":10,"retries_per_movement":2}},"verification":{"expectation":{"intent":"pass-existing-tests","apply_gate":{"require":["verified"]}},"final_movement":"inspect"}}`), &value); err != nil {
		t.Fatal(err)
	}
	if mayPropose {
		value.(map[string]any)["movements"].([]any)[0].(map[string]any)["may_propose"] = true
	}
	compiled, diagnostics := score.CompileValue(value)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%v", diagnostics)
	}
	return compiled
}

func v3IdentityVersions() json.RawMessage {
	return json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/acceptance-spec":1,"partitur/criterion-spec":1,"partitur/execution-dependency":3}}`)
}
