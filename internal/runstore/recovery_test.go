package runstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestLoadRecoveryInputUsesRunOwnedHistory(t *testing.T) {
	tests := []struct {
		name   string
		append func(*testing.T, *Store)
		check  func(*testing.T, RecoveryInput)
	}{
		{
			name: "fresh run", check: func(t *testing.T, input RecoveryInput) {
				if input.Projection.CurrentHeadAttempt != nil || len(input.Projection.Scheduler.Movements) != 2 {
					t.Fatalf("fresh projection = %+v", input.Projection)
				}
			},
		},
		{
			name: "failed attempt retains recorded disposition", append: appendFailedAttempt, check: func(t *testing.T, input RecoveryInput) {
				attempt := input.Projection.CurrentHeadAttempt
				if attempt == nil || attempt.State != runstate.AttemptFailed || attempt.RecordedDisposition == nil || attempt.RecordedDisposition.Charged != "quality_retry" {
					t.Fatalf("failed attempt projection = %+v", attempt)
				}
			},
		},
		{
			name: "pending decision remains durable", append: appendPendingDecision, check: func(t *testing.T, input RecoveryInput) {
				attempt := input.Projection.CurrentHeadAttempt
				if attempt == nil || attempt.State != runstate.AttemptBlocked || len(attempt.QuestionRequests) != 1 || !attempt.QuestionRequests[0].Durable {
					t.Fatalf("blocked attempt projection = %+v", attempt)
				}
				if _, ok := input.Projection.State.PendingDecisions["question-1"]; !ok {
					t.Fatalf("pending decisions = %+v", input.Projection.State.PendingDecisions)
				}
			},
		},
		{
			name: "terminal run", append: func(t *testing.T, store *Store) {
				appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventRunFailed, Payload: recoveryPayload(t, map[string]any{"reason": "movement_failed"})})
			}, check: func(t *testing.T, input RecoveryInput) {
				if input.Projection.State.Run != runstate.RunFailed {
					t.Fatalf("terminal lifecycle = %s", input.Projection.State.Run)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := recoveryStore(t)
			if test.append != nil {
				test.append(t, store)
			}
			input, err := store.LoadRecoveryInput("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if input.Score.Revision() != 1 || input.Cast == nil {
				t.Fatalf("historical inputs = score revision %d cast=%v", input.Score.Revision(), input.Cast)
			}
			test.check(t, input)
		})
	}
}

func TestLoadRecoveryInputIgnoresCurrentRootScore(t *testing.T) {
	store := recoveryStore(t)
	root := filepath.Join(store.RepositoryRoot(), "partitur.yaml")
	if err := os.WriteFile(root, recoveryScoreJSON(t, 99, "current root must not win"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRecoveryInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if input.Score.Revision() != 1 || input.Score.Execution().Goal != "pinned recovery fixture" {
		t.Fatalf("loaded score = revision %d goal %q, want run-owned revision 1", input.Score.Revision(), input.Score.Execution().Goal)
	}
}

func TestChangeSetRecordedAppendIsIdempotent(t *testing.T) {
	store := recoveryStore(t)
	appendAttemptToVerifying(t, store)
	event := runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventChangeSetRecorded, Payload: recoveryPayload(t, changeSetPayloadForRecovery())}
	first := appendRecoveryEvent(t, store, event)
	second := appendRecoveryEvent(t, store, event)
	if first.Mutation.Sequence != second.Mutation.Sequence || first.Mutation.EventID != second.Mutation.EventID {
		t.Fatalf("idempotent change-set receipts = %+v %+v", first, second)
	}
	input, err := store.LoadRecoveryInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := input.Projection.State.ChangeSets["attempt-1"]; got.ChangeSetID != "change-set-1" || input.Projection.State.VerifiedAttempts["attempt-1"] {
		t.Fatalf("change-set projection = %+v verified=%t", got, input.Projection.State.VerifiedAttempts["attempt-1"])
	}
}

func recoveryStore(t *testing.T) *Store {
	t.Helper()
	store := newTestStore(t)
	scoreBytes := recoveryScoreJSON(t, 1, "pinned recovery fixture")
	compiled, diagnostics := score.Compile(scoreBytes)
	if len(diagnostics) != 0 {
		t.Fatalf("fixture score diagnostics = %v", diagnostics)
	}
	scoreHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	castBytes := recoveryCastJSON(t)
	resolved, castDiagnostics := cast.Resolve([]cast.Layer{{Origin: "fixture", Data: castBytes}})
	if len(castDiagnostics) != 0 {
		t.Fatalf("fixture cast diagnostics = %v", castDiagnostics)
	}
	castHash, err := resolved.Hash()
	if err != nil {
		t.Fatal(err)
	}
	err = store.Mutate("run-1", "", func(transaction *Txn) error {
		if _, err := transaction.At("test/score").PublishImmutable("scores/revision-1.yaml", scoreBytes, Hash(rawHash(scoreBytes))); err != nil {
			return err
		}
		if _, err := transaction.At("test/cast").PublishImmutable("resolved-cast.yaml", castBytes, Hash(rawHash(castBytes))); err != nil {
			return err
		}
		_, err := transaction.At("test/run-started").Append(runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventRunStarted,
			Payload: recoveryPayload(t, map[string]any{
				"base_commit": "git-sha1:base", "base_tree": "git-sha1:tree", "score_hash": scoreHash,
				"score_file_hash": rawHash(scoreBytes), "resolved_cast_hash": castHash, "identity_versions": recoveryVersions(),
			}),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func appendFailedAttempt(t *testing.T, store *Store) {
	appendAttemptPrefix(t, store)
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventAttemptFailed, Payload: recoveryPayload(t, map[string]any{"kind": "task_failed", "reason": "attempt_terminated_incomplete", "disposition": map[string]any{"charged": "quality_retry", "movement_terminal": false}})})
}
func appendPendingDecision(t *testing.T, store *Store) {
	appendAttemptToRunning(t, store)
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventAttemptBlocked, Payload: recoveryPayload(t, map[string]any{"raised": []any{map[string]any{"decision_id": "question-1", "emitted_id": "emitted-1", "kind": "question", "question": "Continue?", "blocking": true}}, "pending_decision_ids": []any{"question-1"}})})
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventDecisionRequested, Payload: recoveryPayload(t, map[string]any{"decision_id": "question-1", "decision_type": "question", "question": "Continue?", "emitted_id": "emitted-1"})})
}

func appendAttemptPrefix(t *testing.T, store *Store) {
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventMovementReady, Payload: recoveryPayload(t, map[string]any{})})
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventMovementStarted, Payload: recoveryPayload(t, map[string]any{})})
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: recoveryPayload(t, map[string]any{"reason": "initial", "performer_id": "writer", "adapter_id": "adapter", "model": "model"})})
}
func appendAttemptToRunning(t *testing.T, store *Store) {
	appendAttemptPrefix(t, store)
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventAttemptStarted, Payload: recoveryPayload(t, attemptStartedPayloadForRecovery())})
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventAdapterProbed, Payload: recoveryPayload(t, adapterProbedPayloadForRecovery())})
}

func appendAttemptToVerifying(t *testing.T, store *Store) {
	appendAttemptToRunning(t, store)
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventPerformerCompleted, Payload: recoveryPayload(t, map[string]any{"session_hint_stored": false})})
}

func appendRecoveryEvent(t *testing.T, store *Store, event runstate.Event) DurabilityReceipt {
	t.Helper()
	var receipt DurabilityReceipt
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		var err error
		receipt, err = transaction.At("test/event").Append(event)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return receipt
}
func recoveryPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
func recoveryVersions() map[string]any {
	return map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
}

func recoveryScoreJSON(t *testing.T, revision int, goal string) []byte {
	t.Helper()
	return recoveryPayload(t, map[string]any{"score": "0.2", "name": "recovery-fixture", "revision": revision, "status": "finalized", "goal": goal, "verification": map[string]any{"expectation": map[string]any{"intent": "pass-existing-tests", "apply_gate": map[string]any{"waived": true, "reason": "fixture"}}}, "parts": map[string]any{"writer": map[string]any{"capabilities": []any{"repo_read", "repo_write"}}, "reader": map[string]any{"capabilities": []any{"repo_read"}}}, "movements": []any{map[string]any{"id": "write", "part": "writer", "grants": []any{"repo_read", "repo_write"}, "instruction": "write", "outputs": []any{map[string]any{"id": "change-set", "kind": "change_set"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "done", "run": []any{"true"}}}}}, map[string]any{"id": "read", "part": "reader", "needs": []any{"write"}, "grants": []any{"repo_read"}, "instruction": "read", "inputs": []any{"change-set"}}}, "policy": map[string]any{"allowed_paths": []any{"**"}, "budget": map[string]any{"active_wall_clock_min": 10}}})
}
func recoveryCastJSON(t *testing.T) []byte {
	t.Helper()
	return recoveryPayload(t, map[string]any{"cast": "0.1", "performers": map[string]any{"writer": map[string]any{"adapter": "adapter", "model": "model"}, "reader": map[string]any{"adapter": "adapter", "model": "model"}}, "bindings": map[string]any{"writer": map[string]any{"performer": "writer"}, "reader": map[string]any{"performer": "reader"}}})
}
func changeSetPayloadForRecovery() map[string]any {
	return map[string]any{"change_set_id": "change-set-1", "base_tree": "git-sha1:base", "result_tree": "git-sha1:result", "commit": "git-sha1:commit", "ref": "refs/partitur/runs/run-1/attempts/attempt-1/changeset", "identity_versions": recoveryVersions()}
}
func attemptStartedPayloadForRecovery() map[string]any {
	return map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 10, "session_id": 10, "start_identity": map[string]any{"platform": "linux", "boot_id": "boot", "start_ticks": "12"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false}, "identity_versions": recoveryVersions()}
}
func adapterProbedPayloadForRecovery() map[string]any {
	return map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": true, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": false, "shell_grants": false, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": recoveryVersions()}
}
