package main

import (
	"bytes"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// A rejection at the final movement's human gate fails the run through
// movement.failed itself: no run.failed is ever appended, so the reason the
// terminal line carries has to come from that event.
func TestResumeNamesTheGateRejectionAsTheTerminalReason(t *testing.T) {
	root, store := resumeAttemptFixture(t)
	appendFinalHumanGateRejection(t, store)
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	wantStderr := "run terminal: state=\"FAILED\" reason=\"human_gate_rejected\"\n"
	code := run([]string{"resume", "run-1"}, &stdout, &stderr)
	if code != 4 || stdout.Len() != 0 || stderr.String() != wantStderr {
		t.Fatalf(
			"exit=%d stdout=%q stderr=%q, want exit=4 stdout=%q stderr=%q",
			code, stdout.String(), stderr.String(), "", wantStderr,
		)
	}
}

// appendFinalHumanGateRejection drives the single-movement attempt fixture to
// its human gate, rejects it, and fails the run the only way that path does:
// movement.failed carrying run_failed, with no run.failed behind it.
func appendFinalHumanGateRejection(t *testing.T, store *runstore.Store) {
	t.Helper()
	const decisionID = "human_gate-1"
	const subjectTree = "git-sha1:subject"
	versions := resumeIdentityVersions()
	events := []runstate.Event{
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", Type: runstate.EventMovementReady, Payload: resumePayload(t, map[string]any{})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", Type: runstate.EventMovementStarted, Payload: resumePayload(t, map[string]any{})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: resumePayload(t, map[string]any{"reason": "initial", "performer_id": "reviewer", "adapter_id": "adapter", "model": "model"})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventAttemptStarted, Payload: resumePayload(t, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false}, "identity_versions": versions})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventAdapterProbed, Payload: resumePayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "delivered_resolutions": []any{}, "delivered_feedback": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventPerformerCompleted, Payload: resumePayload(t, map[string]any{"session_hint_stored": false})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventDecisionRequested, Payload: resumePayload(t, map[string]any{"decision_id": decisionID, "decision_type": "human_gate", "gate_id": "gate-attempt-1", "gate_mode": "always", "subject_tree": subjectTree, "blocking_findings": []any{}})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventDecisionResolved, Payload: resumePayload(t, map[string]any{"decision_id": decisionID, "decision_type": "human_gate", "disposition": "rejected", "gate_id": "gate-attempt-1", "scope": map[string]any{"subject_tree": subjectTree}, "overridden_findings": []any{}, "reason": "not ready"})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventMovementFailed, Payload: resumePayload(t, map[string]any{"reason": "human_gate_rejected", "decision_id": decisionID, "subject_tree": subjectTree, "run_failed": true})},
	}
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		for _, event := range events {
			if _, err := tx.At("fixture.gate.rejected").Append(event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if terminal := journal.Events[len(journal.Events)-1]; terminal.Type != runstate.EventMovementFailed {
		t.Fatalf("terminal event=%s, want movement.failed", terminal.Type)
	}
	if countEvents(journal.Events, runstate.EventRunFailed) != 0 {
		t.Fatal("gate rejection fixture appended run.failed; the reason must come from movement.failed")
	}
}
