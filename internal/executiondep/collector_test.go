package executiondep

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestCollectUsesJournaledActualAdapterInputs(t *testing.T) {
	store := collectedFixture(t)
	collected, err := Collect(store, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.attempts) != 2 {
		t.Fatalf("collected attempts = %d, want 2", len(collected.attempts))
	}
	got, ok := collected.attempts["attempt-1"]
	if !ok {
		t.Fatal("attempt-1 is absent")
	}
	if got.ID != "attempt-1" || got.MovementID != "inspect" || got.AdapterID != "adapter" || got.Model != "model" ||
		got.RecordedHash != "sha256:recorded-by-fixture" || got.BaseCompositionHash != "sha256:composition" {
		t.Fatalf("envelope/probe fields = %#v", got)
	}
	if !reflect.DeepEqual(got.Extensions, map[string]any{"adapter": map[string]any{"fixture": "cast-entry"}}) {
		t.Fatalf("extensions = %#v, want resolved cast adapter entry", got.Extensions)
	}
	if !reflect.DeepEqual(got.GrantedAuthority, protocol.Grants{PathsRW: []string{}, PathsRO: []string{"src/**"}}) {
		t.Fatalf("granted authority = %#v", got.GrantedAuthority)
	}
	if string(got.IdentityVersions) != `{"canonical_encoding":1,"projections":{"partitur/acceptance-spec":1,"partitur/criterion-spec":1,"partitur/execution-dependency":3}}` {
		t.Fatalf("identity versions = %s", got.IdentityVersions)
	}
	if !reflect.DeepEqual(got.Inputs, []protocol.ArtifactRef{{ArtifactID: "report", Kind: "artifact", InstanceID: "report@source-1", Hash: "sha256:source-report"}}) ||
		len(got.DeliveredResolutions) != 0 || len(got.DeliveredFeedback) != 0 {
		t.Fatalf("durable collections = inputs=%#v resolutions=%#v feedback=%#v", got.Inputs, got.DeliveredResolutions, got.DeliveredFeedback)
	}
}

func TestEligibleIsExactlyCompletedSuccessful(t *testing.T) {
	for _, test := range []struct {
		name     string
		attempt  runstate.AttemptState
		movement runstate.MovementState
		want     bool
	}{
		{"completed successful", runstate.AttemptCompleted, runstate.MovementSucceeded, true},
		{"completed unfinished", runstate.AttemptCompleted, runstate.MovementRunning, false},
		{"superseded", runstate.AttemptSuperseded, runstate.MovementSucceeded, false},
		{"failed", runstate.AttemptFailed, runstate.MovementFailed, false},
		{"blocked", runstate.AttemptBlocked, runstate.MovementWaitingHuman, false},
	} {
		state := runstate.NewState(nil)
		state.Attempts["attempt"] = runstate.Attempt{MovementID: "movement", State: test.attempt}
		state.Movements["movement"] = test.movement
		if got := eligible(state, "attempt"); got != test.want {
			t.Fatalf("%s: eligible = %t, want %t", test.name, got, test.want)
		}
	}
}

// collectedFixture is deliberately a journal fixture: it never constructs an
// Attempt, so its expected values stay independent of collection.
func collectedFixture(t *testing.T) *runstore.Store {
	t.Helper()
	root := t.TempDir()
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	compiled := collectorScore(t)
	scoreBytes, err := compiled.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	scoreHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	castBytes := []byte(`{"cast":"0.1","performers":{"worker":{"adapter":"adapter","model":"model","extensions":{"adapter":{"fixture":"cast-entry"}}}},"bindings":{"reader":{"performer":"worker"}}}`)
	resolved, diagnostics := cast.Resolve([]cast.Layer{{Origin: "fixture", Data: castBytes}})
	if len(diagnostics) != 0 {
		t.Fatalf("cast diagnostics=%v", diagnostics)
	}
	castHash, err := resolved.Hash()
	if err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(root, ".partitur", "runs", "run-1")
	if err := os.MkdirAll(filepath.Join(runRoot, "scores"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "scores", "revision-1.yaml"), scoreBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRoot, "resolved-cast.yaml"), castBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{
		"partitur/execution-dependency": 3, "partitur/acceptance-spec": 1, "partitur/criterion-spec": 1,
	}}
	digest := sha256.Sum256(scoreBytes)
	appendFixtureEvent(t, store, runstate.EventRunStarted, map[string]any{
		"base_commit": "git-sha1:commit", "base_tree": "git-sha1:tree", "score_hash": scoreHash,
		"score_file_hash": fmt.Sprintf("sha256:%x", digest), "resolved_cast_hash": castHash, "identity_versions": versions,
	}, "", "")
	appendFixtureEvent(t, store, runstate.EventMovementReady, map[string]any{}, "source", "")
	appendFixtureEvent(t, store, runstate.EventMovementStarted, map[string]any{}, "source", "")
	appendFixtureEvent(t, store, runstate.EventPerformerSelected, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "adapter", "model": "model"}, "source", "source-1")
	appendFixtureEvent(t, store, runstate.EventAttemptStarted, map[string]any{
		"attempt_number": 1, "adapter_process": map[string]any{"pid": 9, "session_id": 9, "start_identity": map[string]any{"platform": "linux", "boot_id": "boot", "start_ticks": "11"}},
		"granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"src/**"}, "shell": false, "network": false}, "identity_versions": versions,
	}, "source", "source-1")
	appendFixtureEvent(t, store, runstate.EventAdapterProbed, map[string]any{
		"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true},
		"negotiated_features": []any{}, "truncated_resolutions": []any{}, "delivered_resolutions": []any{}, "delivered_feedback": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:source-recorded", "identity_versions": versions,
	}, "source", "source-1")
	appendFixtureEvent(t, store, runstate.EventArtifactRecorded, map[string]any{"logical_output_id": "report", "kind": "artifact", "content_hash": "sha256:source-report", "size_bytes": 1, "source_path": "report"}, "source", "source-1")
	appendFixtureEvent(t, store, runstate.EventPerformerCompleted, map[string]any{"session_hint_stored": false}, "source", "source-1")
	appendFixtureEvent(t, store, runstate.EventVerificationPassed, map[string]any{}, "source", "source-1")
	appendFixtureEvent(t, store, runstate.EventAcceptanceStarted, map[string]any{"subject_tree": "git-sha1:tree", "acceptance_spec_hash": "sha256:source-acceptance", "planned_criterion_ids": []any{}, "identity_versions": versions}, "source", "source-1")
	appendFixtureEvent(t, store, runstate.EventAcceptanceEvaluationCompleted, map[string]any{"subject_tree": "git-sha1:tree", "acceptance_spec_hash": "sha256:source-acceptance", "criterion_outcomes": []any{}, "identity_versions": versions}, "source", "source-1")
	appendFixtureEvent(t, store, runstate.EventAttemptCompleted, map[string]any{}, "source", "source-1")
	appendFixtureEvent(t, store, runstate.EventMovementSucceeded, map[string]any{"approved_artifact_instance_ids": []any{"report@source-1"}, "identity_versions": versions, "run_succeeded": false}, "source", "source-1")
	appendFixtureEvent(t, store, runstate.EventMovementReady, map[string]any{}, "inspect", "")
	appendFixtureEvent(t, store, runstate.EventMovementStarted, map[string]any{}, "inspect", "")
	appendFixtureEvent(t, store, runstate.EventPerformerSelected, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "adapter", "model": "model"}, "inspect", "attempt-1")
	appendFixtureEvent(t, store, runstate.EventAttemptStarted, map[string]any{
		"attempt_number": 1, "adapter_process": map[string]any{"pid": 10, "session_id": 10, "start_identity": map[string]any{"platform": "linux", "boot_id": "boot", "start_ticks": "12"}},
		"base_composition_hash": "sha256:composition", "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"src/**"}, "shell": false, "network": false}, "identity_versions": versions,
	}, "inspect", "attempt-1")
	appendFixtureEvent(t, store, runstate.EventAdapterProbed, map[string]any{
		"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true},
		"negotiated_features": []any{}, "truncated_resolutions": []any{}, "delivered_resolutions": []any{}, "delivered_feedback": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:recorded-by-fixture", "identity_versions": versions,
	}, "inspect", "attempt-1")
	appendFixtureEvent(t, store, runstate.EventArtifactRecorded, map[string]any{"logical_output_id": "report", "kind": "artifact", "content_hash": "sha256:report", "size_bytes": 1, "source_path": "report"}, "inspect", "attempt-1")
	appendFixtureEvent(t, store, runstate.EventPerformerCompleted, map[string]any{"session_hint_stored": false}, "inspect", "attempt-1")
	appendFixtureEvent(t, store, runstate.EventVerificationPassed, map[string]any{}, "inspect", "attempt-1")
	appendFixtureEvent(t, store, runstate.EventAcceptanceStarted, map[string]any{"subject_tree": "git-sha1:tree", "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{}, "identity_versions": versions}, "inspect", "attempt-1")
	appendFixtureEvent(t, store, runstate.EventAcceptanceEvaluationCompleted, map[string]any{"subject_tree": "git-sha1:tree", "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{}, "identity_versions": versions}, "inspect", "attempt-1")
	appendFixtureEvent(t, store, runstate.EventAttemptCompleted, map[string]any{}, "inspect", "attempt-1")
	appendFixtureEvent(t, store, runstate.EventMovementSucceeded, map[string]any{"approved_artifact_instance_ids": []any{"report@attempt-1"}, "identity_versions": versions, "run_succeeded": false}, "inspect", "attempt-1")
	return store
}

func appendFixtureEvent(t *testing.T, store *runstore.Store, eventType runstate.EventType, payload any, movementID runstate.MovementID, attemptID runstate.AttemptID) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		_, err := transaction.At("test/journal").Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: movementID, AttemptID: attemptID, Type: eventType, Payload: encoded})
		return err
	})
	if err != nil {
		t.Fatalf("append %s: %v", eventType, err)
	}
}

func collectorScore(t *testing.T) *score.Score {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(`{"score":"0.2","name":"collector","revision":1,"status":"finalized","goal":"goal","open_questions":[],"parts":{"reader":{"capabilities":["repo_read"],"read_only":true}},"movements":[{"id":"source","part":"reader","grants":["repo_read"],"instruction":"produce","outputs":[{"id":"report","kind":"artifact"}]},{"id":"inspect","part":"reader","needs":["source"],"grants":["repo_read"],"instruction":"inspect","inputs":["report"]}],"policy":{"allowed_paths":["src/**"],"budget":{"active_wall_clock_min":10,"retries_per_movement":2}},"verification":{"expectation":{"intent":"pass-existing-tests","apply_gate":{"waived":true,"reason":"fixture"}}}}`), &value); err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := score.CompileValue(value)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%v", diagnostics)
	}
	return compiled
}
