package amendment

import (
	"encoding/json"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/executiondep"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

func TestEvaluatePipelineAndPolicy(t *testing.T) {
	base := testScore(t)
	baseHash, err := base.Hash()
	if err != nil {
		t.Fatal(err)
	}
	state := runstate.NewState(nil)
	state.Run = runstate.RunRunning
	state.ScoreHead = runstate.ScoreHead{Revision: base.Revision(), SemanticHash: runstate.Hash(baseHash)}
	for _, test := range []struct {
		name       string
		operations []any
		mutate     func(*runstate.State)
		want       Kind
		reason     string
		class      EnvelopeClass
	}{
		{"budget decrease approves", []any{op("replace", "/policy/budget/active_wall_clock_min", float64(9))}, nil, Approved, "", BudgetDecrease},
		{"reserved wins before patch", []any{op("replace", "/revision", float64(2))}, nil, Rejected, "reserved_field", ""},
		{"empty member is not root", []any{op("replace", "/", float64(2))}, nil, Rejected, "patch_error", ""},
		{"test reserved is permitted", []any{op("test", "/revision", float64(1))}, nil, Rejected, "no_op", ""},
		{"unordered paths are not a canonical no-op", []any{op("replace", "/policy/allowed_paths", []any{"test/**", "src/**"})}, nil, Routed, "unclassified_change", ""},
		{"lifecycle wins stale", []any{op("replace", "/policy/budget/active_wall_clock_min", float64(9))}, func(state *runstate.State) { state.CancelRequested = true; state.ScoreHead.Revision = 99 }, Rejected, "run_cancelling", ""},
		{"path guard routes after pipeline", []any{op("replace", "/policy/allowed_paths", []any{})}, func(state *runstate.State) {
			state.Attempts["a"] = runstate.Attempt{MovementID: "inspect", State: runstate.AttemptRunning}
		}, Routed, "runtime_scope_started", NarrowPaths},
		{"policy change routes nonmonotone", []any{op("replace", "/policy/amendment/auto", "off")}, nil, Routed, "recognized_non_monotone", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := state
			current.Attempts = make(map[runstate.AttemptID]runstate.Attempt, len(state.Attempts))
			for id, attempt := range state.Attempts {
				current.Attempts[id] = attempt
			}
			if test.mutate != nil {
				test.mutate(&current)
			}
			claim := score.Impact{}
			if test.reason != "patch_error" && test.reason != "no_op" {
				claim = claimFor(t, base, test.operations)
			}
			got, err := Evaluate(Input{State: current, Base: base, BaseRevision: base.Revision(), BaseHash: runstate.Hash(baseHash), Operations: test.operations, ClaimedImpact: claim})
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != test.want || got.Reason != test.reason || got.Class != test.class {
				t.Fatalf("outcome=%+v, want kind=%s reason=%q class=%s", got, test.want, test.reason, test.class)
			}
		})
	}
}

func TestEvaluateClaimContainmentAndCandidatePrecedence(t *testing.T) {
	base := testScore(t)
	hash, err := base.Hash()
	if err != nil {
		t.Fatal(err)
	}
	state := runstate.NewState(nil)
	state.Run = runstate.RunRunning
	state.ScoreHead = runstate.ScoreHead{Revision: base.Revision(), SemanticHash: runstate.Hash(hash)}
	operations := []any{op("replace", "/policy/budget/active_wall_clock_min", float64(9))}
	got, err := Evaluate(Input{State: state, Base: base, BaseRevision: base.Revision(), BaseHash: runstate.Hash(hash), Operations: operations})
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "claim_narrower" {
		t.Fatalf("reason=%q, want claim_narrower", got.Reason)
	}
}

func TestEvaluateFeasibilityPrecedesPolicy(t *testing.T) {
	base := testScore(t)
	hash, err := base.Hash()
	if err != nil {
		t.Fatal(err)
	}
	state := runstate.NewState(nil)
	state.Run = runstate.RunRunning
	state.ScoreHead = runstate.ScoreHead{Revision: base.Revision(), SemanticHash: runstate.Hash(hash)}
	state.Movements["inspect"] = runstate.MovementSucceeded
	state.Attempts["attempt-1"] = runstate.Attempt{MovementID: "inspect", State: runstate.AttemptCompleted}
	versions := json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/execution-dependency":3}}`)
	attempt := executiondep.Attempt{ID: "attempt-1", MovementID: "inspect", AdapterID: "adapter", Model: "model", IdentityVersions: versions}
	recorded, err := executiondep.Recompute(base, attempt)
	if err != nil {
		t.Fatal(err)
	}
	attempt.RecordedHash = recorded
	operations := []any{op("replace", "/movements/0/instruction", "changed")}
	got, err := Evaluate(Input{State: state, Base: base, BaseRevision: 1, BaseHash: runstate.Hash(hash), Operations: operations, ClaimedImpact: claimFor(t, base, operations), Attempts: []executiondep.Attempt{attempt}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != Rejected || got.Reason != "executed_dependency_changed" {
		t.Fatalf("outcome=%+v", got)
	}
}

func TestEvaluateCandidateFinalityPrecedesPolicy(t *testing.T) {
	base := testScore(t)
	hash, err := base.Hash()
	if err != nil {
		t.Fatal(err)
	}
	candidateHash, err := workspace.CandidateCompositionHash("git-sha1:base", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	state := runstate.NewState(nil)
	state.Run = runstate.RunRunning
	state.ScoreHead = runstate.ScoreHead{Revision: 1, SemanticHash: runstate.Hash(hash)}
	state.Movements["inspect"] = runstate.MovementSucceeded
	state.Attempts["attempt-1"] = runstate.Attempt{MovementID: "inspect", State: runstate.AttemptCompleted}
	state.ApplicationCandidate = &runstate.ApplicationCandidate{BaseTree: "git-sha1:base", CompositionDependencyHash: runstate.Hash(candidateHash)}
	versions := json.RawMessage(`{"canonical_encoding":1,"projections":{"partitur/execution-dependency":3}}`)
	attempt := executiondep.Attempt{ID: "attempt-1", MovementID: "inspect", AdapterID: "adapter", Model: "model", IdentityVersions: versions}
	recorded, err := executiondep.Recompute(base, attempt)
	if err != nil {
		t.Fatal(err)
	}
	attempt.RecordedHash = recorded
	operations := []any{op("replace", "/policy/budget/active_wall_clock_min", float64(9))}
	got, err := Evaluate(Input{State: state, Base: base, BaseRevision: 1, BaseHash: runstate.Hash(hash), Operations: operations, ClaimedImpact: claimFor(t, base, operations), Attempts: []executiondep.Attempt{attempt}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != Rejected || got.Reason != "candidate_incompatible" || got.Condition != "verification_episode_finished" {
		t.Fatalf("outcome=%+v", got)
	}
}

func TestEvaluateHumanDecisionRecordsGuardWithoutRerouting(t *testing.T) {
	base := testScore(t)
	hash, err := base.Hash()
	if err != nil {
		t.Fatal(err)
	}
	state := runstate.NewState(nil)
	state.Run = runstate.RunRunning
	state.ScoreHead = runstate.ScoreHead{Revision: 1, SemanticHash: runstate.Hash(hash)}
	state.Attempts["attempt-1"] = runstate.Attempt{MovementID: "inspect", State: runstate.AttemptRunning}
	operations := []any{op("replace", "/policy/allowed_paths", []any{})}
	input := Input{State: state, Base: base, BaseRevision: 1, BaseHash: runstate.Hash(hash), Operations: operations, ClaimedImpact: claimFor(t, base, operations)}
	routed, err := Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if routed.Kind != Routed || routed.Reason != "runtime_scope_started" {
		t.Fatalf("initial outcome=%+v", routed)
	}
	input.HumanDecision = true
	decided, err := Evaluate(input)
	if err != nil {
		t.Fatal(err)
	}
	if decided.Kind != Approved || decided.Class != NarrowPaths || decided.GuardPass {
		t.Fatalf("decision outcome=%+v", decided)
	}
}

func op(name, path string, value any) map[string]any {
	return map[string]any{"op": name, "path": path, "value": value}
}

func claimFor(t *testing.T, base *score.Score, operations []any) score.Impact {
	t.Helper()
	value, _, err := scoreValue(base)
	if err != nil {
		t.Fatal(err)
	}
	patchedValue, err := score.ApplyPatch(value, operations)
	if err != nil {
		t.Fatal(err)
	}
	patched, diagnostics := score.CompileValue(patchedValue)
	if len(diagnostics) != 0 {
		t.Fatalf("compile diagnostics=%v", diagnostics)
	}
	impact, err := score.Compare(base, patched)
	if err != nil {
		t.Fatal(err)
	}
	return impact
}

func testScore(t *testing.T) *score.Score {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(`{"score":"0.2","name":"amendment","revision":1,"status":"finalized","goal":"goal","open_questions":[],"parts":{"reader":{"capabilities":["repo_read"],"read_only":true}},"movements":[{"id":"inspect","part":"reader","grants":["repo_read"],"instruction":"inspect","outputs":[{"id":"report","kind":"artifact"}],"acceptance":{"hard":[{"id":"report-present","artifact":"report"}]}}],"policy":{"allowed_paths":["src/**"],"budget":{"active_wall_clock_min":10,"retries_per_movement":2},"amendment":{"auto":"envelope"}},"verification":{"expectation":{"intent":"pass-existing-tests","apply_gate":{"require":["verified"]}},"final_movement":"inspect"}}`), &value); err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := score.CompileValue(value)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics=%v", diagnostics)
	}
	return compiled
}
