package executiondep

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestRecomputeUsesRecordedV3AndFailsClosedForOldTuple(t *testing.T) {
	compiled := testScore(t)
	versions := json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/execution-dependency":3}}`)
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
	attempt.IdentityVersions = json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/execution-dependency":2}}`)
	if _, err := Recompute(compiled, attempt); !errors.Is(err, canonical.ErrUnsupportedRunFormat) {
		t.Fatalf("err=%v, want unsupported_run_format", err)
	}
}

func TestRecomputeBindsDeliveredFeedback(t *testing.T) {
	compiled := testScore(t)
	versions := json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/execution-dependency":3}}`)
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
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(`{"score":"0.2","name":"execution","revision":1,"status":"finalized","goal":"goal","open_questions":[],"parts":{"reader":{"capabilities":["repo_read"],"read_only":true}},"movements":[{"id":"inspect","part":"reader","grants":["repo_read"],"instruction":"inspect","outputs":[{"id":"report","kind":"artifact"}],"acceptance":{"hard":[{"id":"report-present","artifact":"report"}]}}],"policy":{"allowed_paths":["src/**"],"budget":{"active_wall_clock_min":10,"retries_per_movement":2}},"verification":{"expectation":{"intent":"pass-existing-tests","apply_gate":{"require":["verified"]}},"final_movement":"inspect"}}`), &value); err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := score.CompileValue(value)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%v", diagnostics)
	}
	return compiled
}
