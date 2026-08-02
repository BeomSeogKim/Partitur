package recoveryexec

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/cancellation"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

func TestLiveAndResumeSuccessorSelectionJournalIdentity(t *testing.T) {
	for _, test := range []struct {
		name, charged, kind, wantPerformer string
	}{
		{name: "quality retry", charged: "quality_retry", kind: "task_failed", wantPerformer: "worker"},
		{name: "ordered fallback", charged: "fallback", kind: "rate_limited", wantPerformer: "backup-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			live := successorIdentityFixture(t, test.charged, test.kind, "")
			defer live.driver.Release()
			attempt, err := workspace.CreateRecoveredAttempt(live.store, live.driver, live.input, "inspect")
			if err != nil {
				t.Fatal(err)
			}
			candidate := live.input.Projection.State.ApplicationCandidate
			result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
				RepositoryRoot: live.store.RepositoryRoot(), Score: live.input.Score, Cast: live.input.Cast,
				RunID: live.runID, Attempt: attempt, BaseTree: live.input.BaseTree, CandidateTree: candidate.ResultTree, Authority: live.driver,
				PerformerID: test.wantPerformer, SelectionReason: test.charged, SelectionCausationID: live.failure.EventID,
				RemainingMS: live.input.Projection.Scheduler.RemainingTime,
			}, driver.DefaultExecutionDependencies(faultpoint.Nop{}))
			if result.Err == nil {
				t.Fatal("live fixture unexpectedly executed beyond performer.selected")
			}

			resumed := successorIdentityFixture(t, test.charged, test.kind, "")
			defer resumed.driver.Release()
			pending := resumed.input.Projection.Scheduler.PendingSuccessor
			if pending == nil {
				t.Fatal("resume fixture has no pending successor")
			}
			err = materializeSuccessor(context.Background(), HandlerContext{
				Store: resumed.store, Driver: resumed.driver, RunID: resumed.runID,
			}, recovery.Action{Kind: recovery.ActionMaterializeSuccessor, MovementID: pending.MovementID, PendingSuccessor: pending})
			if err == nil {
				t.Fatal("resume fixture unexpectedly executed beyond performer.selected")
			}

			liveSelection, liveState := successorSelection(t, live.store, live.driver, live.runID)
			resumeSelection, resumeState := successorSelection(t, resumed.store, resumed.driver, resumed.runID)
			if liveSelection.Type != runstate.EventPerformerSelected || resumeSelection.Type != runstate.EventPerformerSelected {
				t.Fatalf("selections = %s, %s", liveSelection.Type, resumeSelection.Type)
			}
			if liveSelection.CausationID != live.failure.EventID || resumeSelection.CausationID != resumed.failure.EventID {
				t.Fatalf("selection causation = %q, %q; failure ids = %q, %q", liveSelection.CausationID, resumeSelection.CausationID, live.failure.EventID, resumed.failure.EventID)
			}
			if normalizedSuccessorSelection(liveSelection, live.runID, live.failure.EventID) != normalizedSuccessorSelection(resumeSelection, resumed.runID, resumed.failure.EventID) {
				t.Fatalf("normalized live selection=%+v resume selection=%+v", normalizedSuccessorSelection(liveSelection, live.runID, live.failure.EventID), normalizedSuccessorSelection(resumeSelection, resumed.runID, resumed.failure.EventID))
			}
			if liveState != runstate.AttemptStarting || resumeState != runstate.AttemptStarting {
				t.Fatalf("successor states = %s, %s; want STARTING", liveState, resumeState)
			}
		})
	}
}

func TestRecoveryFinalMovementAcceptanceStartsAtCandidateResultTree(t *testing.T) {
	const candidateTree = "git-sha1:final-candidate-result"
	fixture := successorIdentityFixture(t, "quality_retry", "task_failed", candidateTree)
	defer fixture.driver.Release()
	const attemptID = runstate.AttemptID("attempt-final")
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	appendEvent := func(eventType runstate.EventType, payload any) {
		t.Helper()
		if _, err := fixture.driver.Append(runstate.Event{
			RunID: fixture.runID, ScoreRevision: 1, MovementID: "inspect", AttemptID: attemptID, Type: eventType,
			Payload: handlerPayload(t, payload),
		}, faultpoint.ReceiptAddress("test.recovery_final_acceptance."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(runstate.EventPerformerSelected, map[string]any{"reason": "quality_retry", "performer_id": "worker", "adapter_id": "missing-successor-identity-adapter", "model": "model"})
	appendEvent(runstate.EventAttemptStarted, map[string]any{
		"attempt_number":    2,
		"adapter_process":   map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}},
		"granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false},
		"identity_versions": versions,
	})
	appendEvent(runstate.EventAdapterProbed, map[string]any{
		"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false},
		"enforcement":         map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true},
		"negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions,
	})
	appendEvent(runstate.EventPerformerCompleted, map[string]any{"session_hint_stored": false})
	appendEvent(runstate.EventVerificationPassed, map[string]any{})

	if err := appendAcceptanceStarted(context.Background(), HandlerContext{Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID}, recovery.Action{Kind: recovery.ActionAppendAcceptanceStarted, AttemptID: attemptID}); err != nil {
		t.Fatal(err)
	}
	state, err := fixture.driver.State()
	if err != nil {
		t.Fatal(err)
	}
	if _, recorded := state.ChangeSets[attemptID]; recorded {
		t.Fatal("final verification attempt unexpectedly has a change set")
	}
	if got := state.Acceptances[attemptID].SubjectTree; got != candidateTree {
		t.Fatalf("final acceptance subject tree = %q, want candidate result %q", got, candidateTree)
	}
	ref := "refs/partitur/runs/" + string(fixture.runID) + "/attempts/" + string(attemptID) + "/subject"
	if output, err := exec.Command("git", "-C", fixture.store.RepositoryRoot(), "rev-parse", "--verify", ref).CombinedOutput(); err == nil {
		t.Fatalf("reader unexpectedly created subject ref %q: %s", ref, output)
	}
}

func TestCaptureChangeSetRecoveryHandlerRecordsOnce(t *testing.T) {
	fixture := recoveryChangeSetFixture(t)
	defer fixture.driver.Release()
	action := recovery.Action{Kind: recovery.ActionCaptureChangeSet, AttemptID: fixture.attemptID}
	handlerContext := HandlerContext{Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID}
	if err := captureChangeSet(context.Background(), handlerContext, action); err != nil {
		t.Fatal(err)
	}
	if err := captureChangeSet(context.Background(), handlerContext, action); err != nil {
		t.Fatal(err)
	}
	journal, err := fixture.store.ReadJournal(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	matching := 0
	for _, event := range journal.Events {
		if event.Type == runstate.EventChangeSetRecorded && event.AttemptID == fixture.attemptID {
			matching++
		}
	}
	if matching == 0 {
		t.Fatal("recovery capture journal inspection observed zero matching change_set.recorded events")
	}
	if matching != 1 {
		t.Fatalf("change_set.recorded events = %d, want 1", matching)
	}
	state, err := fixture.driver.State()
	if err != nil {
		t.Fatal(err)
	}
	recorded, ok := state.ChangeSets[fixture.attemptID]
	if !ok || recorded.ChangeSetID == "" || recorded.BaseTree != recorded.ResultTree {
		t.Fatalf("recorded recovery change set = %#v", recorded)
	}
}

func TestRecoveryWriterAcceptanceCreatesDurableSubjectRefForIgnoredProtectedFile(t *testing.T) {
	fixture := recoveryChangeSetFixture(t)
	defer fixture.driver.Release()
	worktree := filepath.Join(fixture.store.RepositoryRoot(), ".partitur", "work", string(fixture.runID), string(fixture.attemptID), "worktree")
	if err := os.MkdirAll(filepath.Join(worktree, ".partitur", "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".partitur", "runs", "x"), []byte("ignored but protected\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	action := recovery.Action{Kind: recovery.ActionCaptureChangeSet, AttemptID: fixture.attemptID}
	handlerContext := HandlerContext{Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID}
	if err := captureChangeSet(context.Background(), handlerContext, action); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.driver.Append(runstate.Event{
		RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", PartID: "writer", AttemptID: fixture.attemptID,
		Type: runstate.EventVerificationPassed, Payload: []byte(`{}`),
	}, "test.recovery_writer_subject.verification"); err != nil {
		t.Fatal(err)
	}
	if err := appendAcceptanceStarted(context.Background(), handlerContext, recovery.Action{Kind: recovery.ActionAppendAcceptanceStarted, AttemptID: fixture.attemptID}); err != nil {
		t.Fatal(err)
	}
	state, err := fixture.driver.State()
	if err != nil {
		t.Fatal(err)
	}
	subjectTree := state.Acceptances[fixture.attemptID].SubjectTree
	ref := "refs/partitur/runs/" + string(fixture.runID) + "/attempts/" + string(fixture.attemptID) + "/subject"
	output, err := exec.Command("git", "-C", fixture.store.RepositoryRoot(), "rev-parse", ref+"^{tree}").Output()
	if err != nil {
		t.Fatalf("resolve recovery subject ref: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != strings.TrimPrefix(subjectTree, "git-sha1:") {
		t.Fatalf("recovery subject ref tree = %q, want recorded %q", got, subjectTree)
	}
	output, err = exec.Command("git", "-C", worktree, "show", strings.TrimPrefix(subjectTree, "git-sha1:")+":.partitur/runs/x").Output()
	if err != nil || string(output) != "ignored but protected\n" {
		t.Fatalf("recovery subject tree omitted ignored protected file: output=%q err=%v", output, err)
	}
	matched, err := workspace.VerifyRecoverySubject(fixture.store.RepositoryRoot(), worktree, subjectTree)
	if err != nil || !matched {
		t.Fatalf("VerifyRecoverySubject(recorded recovery subject) = (%v, %v), want (true, nil)", matched, err)
	}
	input, err := fixture.store.LoadRunInput(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := workspace.CaptureRecoveredAcceptanceSubject(fixture.store, fixture.driver, input, fixture.attemptID)
	if err != nil || verified.Tree != subjectTree || verified.Ref != ref {
		t.Fatalf("recovery subject ref verification = (%#v, %v), want tree %q and ref %q", verified, err, subjectTree, ref)
	}
}

func TestRecoveryCompositionTerminalStopsBeforeCreatingTargetAttempt(t *testing.T) {
	fixture := recoveryChangeSetFixture(t)
	defer fixture.driver.Release()
	input, err := fixture.store.LoadRunInput(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	for _, event := range []runstate.Event{
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventChangeSetRecorded, Payload: handlerPayload(t, map[string]any{"change_set_id": "sha256:write", "base_tree": input.BaseTree, "result_tree": "git-sha1:missing-tree", "commit": input.BaseCommit, "ref": "refs/partitur/runs/" + string(fixture.runID) + "/attempts/" + string(fixture.attemptID) + "/changeset", "identity_versions": versions})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventVerificationPassed, Payload: handlerPayload(t, map[string]any{})},
		{RunID: fixture.runID, ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{"interval_id": "acceptance-write", "phase": "acceptance", "wall_start": "2026-07-30T00:00:00.000Z", "remaining_at_start": 600000})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventAcceptanceStarted, Payload: handlerPayload(t, map[string]any{"subject_tree": "git-sha1:missing-tree", "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{"tests"}, "identity_versions": versions})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventCriterionStarted, Payload: handlerPayload(t, map[string]any{"criterion_id": "tests", "criterion_spec_hash": "sha256:criterion", "subject_tree": "git-sha1:missing-tree", "identity_versions": versions})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventCriterionCompleted, Payload: handlerPayload(t, map[string]any{"criterion_id": "tests", "criterion_spec_hash": "sha256:criterion", "subject_tree": "git-sha1:missing-tree", "outcome": "PASS", "duration_ms": 1, "identity_versions": versions})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventAcceptanceEvaluationCompleted, Payload: handlerPayload(t, map[string]any{"subject_tree": "git-sha1:missing-tree", "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{map[string]any{"criterion_id": "tests", "criterion_spec_hash": "sha256:criterion", "outcome": "PASS"}}, "identity_versions": versions})},
		{RunID: fixture.runID, ScoreRevision: 1, Type: runstate.EventExecutionStopped, Payload: handlerPayload(t, map[string]any{"interval_id": "acceptance-write", "reason": "normal", "charging": "measured", "charged_duration": 1})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventAttemptCompleted, Payload: handlerPayload(t, map[string]any{})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventMovementSucceeded, Payload: handlerPayload(t, map[string]any{"approved_artifact_instance_ids": []any{}, "approved_change_set_id": "sha256:write", "identity_versions": versions, "run_succeeded": false})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "target", Type: runstate.EventMovementReady, Payload: handlerPayload(t, map[string]any{})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "target", Type: runstate.EventMovementStarted, Payload: handlerPayload(t, map[string]any{})},
	} {
		if _, err := fixture.driver.Append(event, faultpoint.ReceiptAddress("test.recovery_composition_terminal."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
	executor := &Executor{Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID}
	result, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: recovery.CaseScheduler, Action: &recovery.Action{Kind: recovery.ActionSelectInitialPerformer, MovementID: "target"}})
	if err != nil || result.Outcome != OutcomeFailed {
		t.Fatalf("recovery result = %+v error=%v, want failed terminal", result, err)
	}
	journal, err := fixture.store.ReadJournal(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.MovementID == "target" && event.Type == runstate.EventPerformerSelected {
			t.Fatal("recovery created a target attempt after composition terminalized it")
		}
	}
}

func TestRecoveryFanInSuccessorMaterializesAtComposedBase(t *testing.T) {
	fixture := recoveryChangeSetFixture(t)
	defer fixture.driver.Release()
	completeRecoveryWriter(t, fixture)
	startRecoveryTargetFailure(t, fixture)
	input, err := fixture.store.LoadRunInput(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	pending := input.Projection.Scheduler.PendingSuccessor
	if pending == nil {
		t.Fatal("recovery fan-in retry is not pending")
	}
	installRecoverySuccessAdapter(t)
	if err := materializeSuccessor(context.Background(), HandlerContext{Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID}, recovery.Action{Kind: recovery.ActionMaterializeSuccessor, MovementID: pending.MovementID, PendingSuccessor: pending}); err != nil {
		t.Fatal(err)
	}
	ref := "refs/partitur/runs/" + string(fixture.runID) + "/movements/target/base"
	assertRecoveredSuccessorBase(t, fixture, ref, "git-sha1:"+recoveryGitText(t, fixture.store.RepositoryRoot(), "rev-parse", ref+"^{tree}"))
}

func TestRecoveryFinalSuccessorMaterializesAtCandidateBase(t *testing.T) {
	fixture := recoveryChangeSetFixtureWithFinal(t, true)
	defer fixture.driver.Release()
	completeRecoveryWriter(t, fixture)
	input, err := fixture.store.LoadRunInput(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.ComposeCandidate(fixture.store, fixture.driver, input, input.Projection.Scheduler.RemainingTime, time.Now, workspace.NewID); err != nil {
		t.Fatal(err)
	}
	startRecoveryTargetFailure(t, fixture)
	input, err = fixture.store.LoadRunInput(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	pending := input.Projection.Scheduler.PendingSuccessor
	if pending == nil {
		t.Fatal("recovery final retry is not pending")
	}
	installRecoverySuccessAdapter(t)
	if err := materializeSuccessor(context.Background(), HandlerContext{Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID}, recovery.Action{Kind: recovery.ActionMaterializeSuccessor, MovementID: pending.MovementID, PendingSuccessor: pending}); err != nil {
		t.Fatal(err)
	}
	candidate := input.Projection.State.ApplicationCandidate
	if candidate == nil {
		t.Fatal("recorded candidate is absent")
	}
	assertRecoveredSuccessorBase(t, fixture, "refs/partitur/runs/"+string(fixture.runID)+"/candidate", candidate.ResultTree)
}

func TestRecoverySuccessorWithNeedsAndNoWritersKeepsBaseCompositionHash(t *testing.T) {
	fixture := recoveryZeroWriterSuccessorFixture(t)
	defer fixture.driver.Release()
	base, err := driver.PrepareSuccessorBase(fixture.store, fixture.driver, fixture.input, "target", fixture.input.Projection.Scheduler.RemainingTime, time.Now, workspace.NewID)
	if err != nil {
		t.Fatal(err)
	}
	if base.Hash == "" {
		t.Fatal("zero-writer dependent successor base composition hash is absent")
	}
	installRecoverySuccessAdapter(t)
	if err := materializeSuccessor(context.Background(), HandlerContext{Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID}, recovery.Action{Kind: recovery.ActionMaterializeSuccessor, MovementID: fixture.input.Projection.Scheduler.PendingSuccessor.MovementID, PendingSuccessor: fixture.input.Projection.Scheduler.PendingSuccessor}); err != nil {
		t.Fatal(err)
	}
	assertRecoveredSuccessorBase(t, fixture, "refs/partitur/runs/"+string(fixture.runID)+"/base", fixture.input.BaseTree)
}

func TestCaptureChangeSetRecoveryHandlerRecapturesPinnedSurvivingWorktree(t *testing.T) {
	fixture := recoveryChangeSetFixture(t)
	defer fixture.driver.Release()
	input, err := fixture.store.LoadRunInput(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := workspace.CaptureRecoveredChangeSet(fixture.store, fixture.driver, input, fixture.attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if got := recoveryGitText(t, fixture.store.RepositoryRoot(), "show-ref", "--hash", pinned.Ref); got != pinned.Commit {
		t.Fatalf("pinned change set ref = %q, want %q", got, pinned.Commit)
	}
	worktree := filepath.Join(fixture.store.RepositoryRoot(), ".partitur", "work", string(fixture.runID), string(fixture.attemptID), "worktree")
	if err := os.WriteFile(filepath.Join(worktree, "surviving.txt"), []byte("later worktree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	action := recovery.Action{Kind: recovery.ActionCaptureChangeSet, AttemptID: fixture.attemptID}
	handlerContext := HandlerContext{Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID}
	if err := captureChangeSet(context.Background(), handlerContext, action); err != nil {
		t.Fatal(err)
	}
	state, err := fixture.driver.State()
	if err != nil {
		t.Fatal(err)
	}
	recorded, ok := state.ChangeSets[fixture.attemptID]
	if !ok {
		t.Fatal("recovery capture did not record the change set")
	}
	if recorded.ChangeSetID == pinned.ID || recorded.Commit == pinned.Commit || recorded.ResultTree == pinned.ResultTree {
		t.Fatalf("recorded change set = %#v, want later worktree checkpoint after %#v", recorded, pinned)
	}
	if got := recoveryGitText(t, fixture.store.RepositoryRoot(), "show-ref", "--hash", recorded.Ref); got != recorded.Commit {
		t.Fatalf("recorded change set ref = %q, want later commit %q", got, recorded.Commit)
	}
	if got := recoveryGitText(t, fixture.store.RepositoryRoot(), "show", recorded.Commit+":surviving.txt"); got != "later worktree" {
		t.Fatalf("recorded checkpoint content = %q, want surviving worktree content", got)
	}
}

func recoveryGitText(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

func completeRecoveryWriter(t *testing.T, fixture recoveryChangeSetFixtureState) {
	t.Helper()
	input, err := fixture.store.LoadRunInput(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	dependencyPath := filepath.Join(fixture.store.RepositoryRoot(), "dependency.txt")
	if err := os.WriteFile(dependencyPath, []byte("dependency\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"add", "dependency.txt"}, {"commit", "-m", "recovery dependency"}} {
		command := exec.Command("git", arguments...)
		command.Dir = fixture.store.RepositoryRoot()
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	dependencyTree := "git-sha1:" + recoveryGitText(t, fixture.store.RepositoryRoot(), "rev-parse", "HEAD^{tree}")
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	for _, event := range []runstate.Event{
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventChangeSetRecorded, Payload: handlerPayload(t, map[string]any{"change_set_id": "sha256:write", "base_tree": input.BaseTree, "result_tree": dependencyTree, "commit": input.BaseCommit, "ref": "refs/partitur/runs/" + string(fixture.runID) + "/attempts/" + string(fixture.attemptID) + "/changeset", "identity_versions": versions})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventVerificationPassed, Payload: handlerPayload(t, map[string]any{})},
		{RunID: fixture.runID, ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{"interval_id": "acceptance-write", "phase": "acceptance", "wall_start": "2026-07-30T00:00:00.000Z", "remaining_at_start": 600000})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventAcceptanceStarted, Payload: handlerPayload(t, map[string]any{"subject_tree": dependencyTree, "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{"tests"}, "identity_versions": versions})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventCriterionStarted, Payload: handlerPayload(t, map[string]any{"criterion_id": "tests", "criterion_spec_hash": "sha256:criterion", "subject_tree": dependencyTree, "identity_versions": versions})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventCriterionCompleted, Payload: handlerPayload(t, map[string]any{"criterion_id": "tests", "criterion_spec_hash": "sha256:criterion", "subject_tree": dependencyTree, "outcome": "PASS", "duration_ms": 1, "identity_versions": versions})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventAcceptanceEvaluationCompleted, Payload: handlerPayload(t, map[string]any{"subject_tree": dependencyTree, "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{map[string]any{"criterion_id": "tests", "criterion_spec_hash": "sha256:criterion", "outcome": "PASS"}}, "identity_versions": versions})},
		{RunID: fixture.runID, ScoreRevision: 1, Type: runstate.EventExecutionStopped, Payload: handlerPayload(t, map[string]any{"interval_id": "acceptance-write", "reason": "normal", "charging": "measured", "charged_duration": 1})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventAttemptCompleted, Payload: handlerPayload(t, map[string]any{})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", AttemptID: fixture.attemptID, Type: runstate.EventMovementSucceeded, Payload: handlerPayload(t, map[string]any{"approved_artifact_instance_ids": []any{}, "approved_change_set_id": "sha256:write", "identity_versions": versions, "run_succeeded": false})},
	} {
		if _, err := fixture.driver.Append(event, faultpoint.ReceiptAddress("test.recovery_successor."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
}

func startRecoveryTargetFailure(t *testing.T, fixture recoveryChangeSetFixtureState) {
	t.Helper()
	input, err := fixture.store.LoadRunInput(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	base, err := driver.PrepareSuccessorBase(fixture.store, fixture.driver, input, "target", input.Projection.Scheduler.RemainingTime, time.Now, workspace.NewID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := workspace.CreateRecoveredAttemptAtBase(fixture.store, fixture.driver, input, "target", base.Commit)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []runstate.Event{
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "target", Type: runstate.EventMovementReady, Payload: handlerPayload(t, map[string]any{})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "target", Type: runstate.EventMovementStarted, Payload: handlerPayload(t, map[string]any{})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "target", AttemptID: attempt.AttemptID, Type: runstate.EventPerformerSelected, Payload: handlerPayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "codex", "model": "fixture"})},
		{RunID: fixture.runID, ScoreRevision: 1, MovementID: "target", AttemptID: attempt.AttemptID, Type: runstate.EventAttemptFailed, Payload: handlerPayload(t, map[string]any{"kind": "task_failed", "disposition": map[string]any{"charged": "quality_retry", "movement_terminal": false}})},
	} {
		if _, err := fixture.driver.Append(event, faultpoint.ReceiptAddress("test.recovery_successor."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
}

func assertRecoveredSuccessorBase(t *testing.T, fixture recoveryChangeSetFixtureState, ref, wantTree string) {
	t.Helper()
	journal, err := fixture.store.ReadJournal(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	var started runstate.Event
	for index := len(journal.Events) - 1; index >= 0; index-- {
		event := journal.Events[index]
		if event.MovementID == "target" && event.Type == runstate.EventAttemptStarted {
			started = event
			break
		}
	}
	if started.AttemptID == "" {
		t.Fatal("recovered successor attempt.started is absent")
	}
	payload := map[string]any{}
	if err := json.Unmarshal(started.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if hash, _ := payload["base_composition_hash"].(string); hash == "" {
		t.Fatal("recovered dependent successor omitted base_composition_hash")
	}
	worktree := filepath.Join(fixture.store.RepositoryRoot(), ".partitur", "work", string(fixture.runID), string(started.AttemptID), "worktree")
	if got := recoveryGitText(t, worktree, "rev-parse", "HEAD"); got != recoveryGitText(t, fixture.store.RepositoryRoot(), "rev-parse", ref) {
		t.Fatalf("recovered successor worktree HEAD = %q, want pinned base %q", got, ref)
	}
	if got := "git-sha1:" + recoveryGitText(t, worktree, "rev-parse", "HEAD^{tree}"); got != wantTree {
		t.Fatalf("recovered successor execution base tree = %q, want %q", got, wantTree)
	}
	state, err := fixture.driver.State()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Acceptances[started.AttemptID].SubjectTree; got != wantTree {
		t.Fatalf("recovered successor recorded subject = %q, want execution base %q", got, wantTree)
	}
}

func installRecoverySuccessAdapter(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	const adapterScript = `#!/bin/sh
IFS= read -r ignored
printf '%s\n' '{"jsonrpc":"2.0","id":"probe","result":{"protocol":2,"adapter":{"id":"codex","version":"fixture"},"capabilities":{"repo_read":true,"repo_write":true,"shell":false,"network":false,"resumable_sessions":false,"models":[{"id":"fixture","aliases":[]}]},"enforcement":{"path_grants":true,"read_only":true,"network_grants":true,"shell_grants":true,"read_grants":true}}}'
IFS= read -r request
output=$(printf '%s' "$request" | sed -n 's/.*"output_dir":"\([^"]*\)".*/\1/p')
printf 'fixture report\n' > "$output/report"
printf '%s\n' '{"jsonrpc":"2.0","method":"event","params":{"type":"artifact","artifact_id":"report","path":"report"}}'
printf '%s\n' '{"jsonrpc":"2.0","id":"execute","result":{"outcome":"completed"}}'
`
	if err := os.WriteFile(filepath.Join(directory, "partitur-adapter-codex"), []byte(adapterScript), 0o700); err != nil {
		t.Fatal(err)
	}
	trampoline := filepath.Join(directory, "partitur-trampoline")
	command := exec.Command("go", "build", "-o", trampoline, "./cmd/partitur-trampoline")
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate recovery executor test source")
	}
	command.Dir = filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build trampoline: %v\n%s", err, output)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type successorIdentityFixtureState struct {
	store   *runstore.Store
	driver  *runstore.Driver
	runID   runstate.RunID
	input   runstore.RunInput
	failure runstate.Event
}

type recoveryChangeSetFixtureState struct {
	store     *runstore.Store
	driver    *runstore.Driver
	runID     runstate.RunID
	attemptID runstate.AttemptID
	input     runstore.RunInput
}

func recoveryChangeSetFixture(t *testing.T) recoveryChangeSetFixtureState {
	return recoveryChangeSetFixtureWithFinal(t, false)
}

func recoveryChangeSetFixtureWithFinal(t *testing.T, withFinal bool) recoveryChangeSetFixtureState {
	t.Helper()
	root := t.TempDir()
	verification := map[string]any{"expectation": map[string]any{"intent": "pass-existing-tests", "apply_gate": map[string]any{"waived": true, "reason": "fixture"}}}
	if withFinal {
		verification = map[string]any{"expectation": map[string]any{"intent": "pass-existing-tests", "apply_gate": map[string]any{"require": []any{"verified"}}}, "final_movement": "target"}
	}
	scoreDocument := map[string]any{
		"score": "0.2", "name": "recovery-change-set", "revision": 1, "status": "finalized", "goal": "fixture",
		"verification": verification,
		"parts":        map[string]any{"writer": map[string]any{"capabilities": []any{"repo_read", "repo_write"}}},
		"movements": []any{
			map[string]any{"id": "write", "part": "writer", "grants": []any{"repo_read", "repo_write"}, "instruction": "fixture", "outputs": []any{map[string]any{"id": "change-set", "kind": "change_set"}, map[string]any{"id": "writer-report", "kind": "artifact"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "writer-report-present", "artifact": "writer-report"}}}},
			map[string]any{"id": "target", "part": "writer", "needs": []any{"write"}, "grants": []any{"repo_read"}, "instruction": "fixture", "outputs": []any{map[string]any{"id": "report", "kind": "artifact"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "report-present", "artifact": "report"}}}},
		},
		"policy": map[string]any{"allowed_paths": []any{"**"}, "budget": map[string]any{"active_wall_clock_min": 10}},
	}
	castDocument := map[string]any{"cast": "0.1", "performers": map[string]any{"worker": map[string]any{"adapter": "codex", "model": "fixture"}}, "bindings": map[string]any{"writer": map[string]any{"performer": "worker"}}}
	writeFixtureJSON(t, filepath.Join(root, "partitur.yaml"), scoreDocument)
	if err := os.Mkdir(filepath.Join(root, ".partitur"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureJSON(t, filepath.Join(root, ".partitur", "cast.yaml"), castDocument)
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".partitur/runs/\n.partitur/work/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"init"}, {"config", "user.name", "Partitur Test"}, {"config", "user.email", "partitur@example.invalid"}, {"add", "partitur.yaml", ".partitur/cast.yaml", ".gitignore"}, {"commit", "-m", "fixture"}} {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	preparation, validation := validate.Prepare()
	if validation.Refusal != nil || validation.HasDiagnostics() || preparation == nil {
		t.Fatalf("preparation=%+v validation=%+v", preparation, validation)
	}
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(started.RunID, []runstate.MovementSeed{{ID: "write", Initial: runstate.MovementPending, RepoWrite: true}, {ID: "target", Initial: runstate.MovementPending, HasDependencies: true, Final: withFinal}})
	if err != nil {
		t.Fatal(err)
	}
	if err := started.Run.BindDriver(authority); err != nil {
		authority.Release()
		t.Fatal(err)
	}
	attempt, err := started.Run.CreateAttempt("write")
	if err != nil {
		authority.Release()
		t.Fatal(err)
	}
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	for _, event := range []runstate.Event{
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "write", Type: runstate.EventMovementReady, Payload: handlerPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "write", Type: runstate.EventMovementStarted, Payload: handlerPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "write", PartID: "writer", AttemptID: attempt.AttemptID, Type: runstate.EventPerformerSelected, Payload: handlerPayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "codex", "model": "fixture"})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "write", PartID: "writer", AttemptID: attempt.AttemptID, Type: runstate.EventAttemptStarted, Payload: handlerPayload(t, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}}, "granted_authority": map[string]any{"paths_rw": []any{"**"}, "paths_ro": []any{}, "shell": false, "network": false}, "identity_versions": versions})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "write", PartID: "writer", AttemptID: attempt.AttemptID, Type: runstate.EventAdapterProbed, Payload: handlerPayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": true, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "write", PartID: "writer", AttemptID: attempt.AttemptID, Type: runstate.EventPerformerCompleted, Payload: handlerPayload(t, map[string]any{"session_hint_stored": false})},
	} {
		if _, err := authority.Append(event, faultpoint.ReceiptAddress("test.recovery_change_set."+string(event.Type))); err != nil {
			authority.Release()
			t.Fatal(err)
		}
	}
	return recoveryChangeSetFixtureState{store: store, driver: authority, runID: started.RunID, attemptID: attempt.AttemptID}
}

func recoveryZeroWriterSuccessorFixture(t *testing.T) recoveryChangeSetFixtureState {
	t.Helper()
	root := t.TempDir()
	scoreDocument := map[string]any{
		"score": "0.2", "name": "recovery-zero-writer-successor", "revision": 1, "status": "finalized", "goal": "fixture",
		"verification": map[string]any{"expectation": map[string]any{"intent": "pass-existing-tests", "apply_gate": map[string]any{"waived": true, "reason": "fixture"}}},
		"parts":        map[string]any{"reader": map[string]any{"capabilities": []any{"repo_read"}, "read_only": true}},
		"movements": []any{
			map[string]any{"id": "read", "part": "reader", "grants": []any{"repo_read"}, "instruction": "fixture", "outputs": []any{map[string]any{"id": "read-report", "kind": "artifact"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "read-report-present", "artifact": "read-report"}}}},
			map[string]any{"id": "target", "part": "reader", "needs": []any{"read"}, "grants": []any{"repo_read"}, "instruction": "fixture", "outputs": []any{map[string]any{"id": "report", "kind": "artifact"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "report-present", "artifact": "report"}}}},
		},
		"policy": map[string]any{"allowed_paths": []any{"**"}, "budget": map[string]any{"active_wall_clock_min": 10}},
	}
	castDocument := map[string]any{"cast": "0.1", "performers": map[string]any{"worker": map[string]any{"adapter": "codex", "model": "fixture"}}, "bindings": map[string]any{"reader": map[string]any{"performer": "worker"}}}
	writeFixtureJSON(t, filepath.Join(root, "partitur.yaml"), scoreDocument)
	if err := os.Mkdir(filepath.Join(root, ".partitur"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureJSON(t, filepath.Join(root, ".partitur", "cast.yaml"), castDocument)
	for _, arguments := range [][]string{{"init"}, {"config", "user.name", "Partitur Test"}, {"config", "user.email", "partitur@example.invalid"}, {"add", "partitur.yaml", ".partitur/cast.yaml"}, {"commit", "-m", "fixture"}} {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	preparation, validation := validate.Prepare()
	if validation.Refusal != nil || validation.HasDiagnostics() || preparation == nil {
		t.Fatalf("preparation=%+v validation=%+v", preparation, validation)
	}
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(started.RunID, []runstate.MovementSeed{{ID: "read", Initial: runstate.MovementPending}, {ID: "target", Initial: runstate.MovementPending, HasDependencies: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := started.Run.BindDriver(authority); err != nil {
		authority.Release()
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	for _, event := range []runstate.Event{
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", Type: runstate.EventMovementReady, Payload: handlerPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", Type: runstate.EventMovementStarted, Payload: handlerPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", AttemptID: "read-attempt", Type: runstate.EventPerformerSelected, Payload: handlerPayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "codex", "model": "fixture"})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", AttemptID: "read-attempt", Type: runstate.EventAttemptStarted, Payload: handlerPayload(t, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false}, "identity_versions": versions})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", AttemptID: "read-attempt", Type: runstate.EventAdapterProbed, Payload: handlerPayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", AttemptID: "read-attempt", Type: runstate.EventPerformerCompleted, Payload: handlerPayload(t, map[string]any{"session_hint_stored": false})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", AttemptID: "read-attempt", Type: runstate.EventVerificationPassed, Payload: handlerPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{"interval_id": "acceptance-read", "phase": "acceptance", "wall_start": "2026-07-30T00:00:00.000Z", "remaining_at_start": 600000})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", AttemptID: "read-attempt", Type: runstate.EventAcceptanceStarted, Payload: handlerPayload(t, map[string]any{"subject_tree": input.BaseTree, "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{}, "identity_versions": versions})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", AttemptID: "read-attempt", Type: runstate.EventAcceptanceEvaluationCompleted, Payload: handlerPayload(t, map[string]any{"subject_tree": input.BaseTree, "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{}, "identity_versions": versions})},
		{RunID: started.RunID, ScoreRevision: 1, Type: runstate.EventExecutionStopped, Payload: handlerPayload(t, map[string]any{"interval_id": "acceptance-read", "reason": "normal", "charging": "measured", "charged_duration": 1})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", AttemptID: "read-attempt", Type: runstate.EventAttemptCompleted, Payload: handlerPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "read", AttemptID: "read-attempt", Type: runstate.EventMovementSucceeded, Payload: handlerPayload(t, map[string]any{"approved_artifact_instance_ids": []any{}, "identity_versions": versions, "run_succeeded": false})},
	} {
		if _, err := authority.Append(event, faultpoint.ReceiptAddress("test.recovery_zero_writer."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
	input, err = store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := workspace.CreateRecoveredAttemptAtBase(store, authority, input, "target", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []runstate.Event{
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "target", Type: runstate.EventMovementReady, Payload: handlerPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "target", Type: runstate.EventMovementStarted, Payload: handlerPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "target", AttemptID: attempt.AttemptID, Type: runstate.EventPerformerSelected, Payload: handlerPayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "codex", "model": "fixture"})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "target", AttemptID: attempt.AttemptID, Type: runstate.EventAttemptFailed, Payload: handlerPayload(t, map[string]any{"kind": "task_failed", "disposition": map[string]any{"charged": "quality_retry", "movement_terminal": false}})},
	} {
		if _, err := authority.Append(event, faultpoint.ReceiptAddress("test.recovery_zero_writer."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
	input, err = store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.Scheduler.PendingSuccessor == nil {
		t.Fatal("zero-writer successor is not pending")
	}
	return recoveryChangeSetFixtureState{store: store, driver: authority, runID: started.RunID, input: input}
}

func successorIdentityFixture(t *testing.T, charged, kind, candidateResultTree string) successorIdentityFixtureState {
	t.Helper()
	root := t.TempDir()
	scoreDocument := map[string]any{
		"score": "0.2", "name": "successor-identity", "revision": 1, "status": "finalized", "goal": "fixture",
		"verification": map[string]any{"expectation": map[string]any{"intent": "pass-existing-tests", "apply_gate": map[string]any{"require": []any{"verified"}}}, "final_movement": "inspect"},
		"parts":        map[string]any{"reader": map[string]any{"capabilities": []any{"repo_read"}, "read_only": true}},
		"movements":    []any{map[string]any{"id": "inspect", "part": "reader", "grants": []any{"repo_read"}, "instruction": "fixture", "outputs": []any{map[string]any{"id": "report", "kind": "artifact"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "report-present", "artifact": "report"}}}}},
		"policy":       map[string]any{"allowed_paths": []any{"**"}, "budget": map[string]any{"active_wall_clock_min": 10, "retries_per_movement": 3}},
	}
	castDocument := map[string]any{
		"cast": "0.1", "performers": map[string]any{
			"worker":   map[string]any{"adapter": "missing-successor-identity-adapter", "model": "model"},
			"backup-a": map[string]any{"adapter": "missing-successor-identity-adapter", "model": "model"},
		},
		"bindings": map[string]any{"reader": map[string]any{"performer": "worker", "fallbacks": []any{"backup-a"}}},
	}
	writeFixtureJSON(t, filepath.Join(root, "partitur.yaml"), scoreDocument)
	if err := os.Mkdir(filepath.Join(root, ".partitur"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureJSON(t, filepath.Join(root, ".partitur", "cast.yaml"), castDocument)
	for _, arguments := range [][]string{{"init"}, {"config", "user.name", "Partitur Test"}, {"config", "user.email", "partitur@example.invalid"}, {"add", "partitur.yaml", ".partitur/cast.yaml"}, {"commit", "-m", "fixture"}} {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	preparation, validation := validate.Prepare()
	if validation.Refusal != nil || validation.HasDiagnostics() || preparation == nil {
		t.Fatalf("preparation=%+v validation=%+v", preparation, validation)
	}
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := store.AcquireDriver(started.RunID, []runstate.MovementSeed{{ID: "inspect", Initial: runstate.MovementPending}})
	if err != nil {
		t.Fatal(err)
	}
	if err := started.Run.BindDriver(driver); err != nil {
		t.Fatal(err)
	}
	if candidateResultTree == "" {
		if _, err := started.Run.RecordZeroWriterCandidate(); err != nil {
			t.Fatal(err)
		}
	} else {
		input, err := store.LoadRunInput(started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Append(runstate.Event{
			RunID: started.RunID, ScoreRevision: 1, Type: runstate.EventApplicationCandidateRecorded,
			Payload: handlerPayload(t, map[string]any{
				"candidate_id": "candidate-1", "base_tree": input.BaseTree, "result_tree": candidateResultTree,
				"ordered_change_sets": []any{}, "contributors": []any{}, "candidate_composition_dependency_hash": "sha256:composition",
				"identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
			}),
		}, "test.application_candidate_recorded"); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []runstate.Event{
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementReady, Payload: handlerPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementStarted, Payload: handlerPayload(t, map[string]any{})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: handlerPayload(t, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "missing-successor-identity-adapter", "model": "model"})},
		{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "attempt-1", Type: runstate.EventAttemptFailed, Payload: handlerPayload(t, map[string]any{"kind": kind, "disposition": map[string]any{"charged": charged, "movement_terminal": false}})},
	} {
		if _, err := driver.Append(event, faultpoint.ReceiptAddress("test."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	return successorIdentityFixtureState{store: store, driver: driver, runID: started.RunID, input: input, failure: journal.Events[len(journal.Events)-1]}
}

func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func successorSelection(t *testing.T, store *runstore.Store, authority *runstore.Driver, runID runstate.RunID) (runstate.Event, runstate.AttemptState) {
	t.Helper()
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	selection := journal.Events[len(journal.Events)-1]
	state, err := authority.State()
	if err != nil {
		t.Fatal(err)
	}
	attempt := state.Attempts[selection.AttemptID]
	return selection, attempt.State
}

func normalizedSuccessorSelection(event runstate.Event, runID runstate.RunID, failureID string) string {
	payload := map[string]any{}
	_ = json.Unmarshal(event.Payload, &payload)
	return fmt.Sprintf("run=%t revision=%d movement=%s part=%s attempt=%t type=%s causation_failure=%t payload=%v", event.RunID == runID, event.ScoreRevision, event.MovementID, event.PartID, event.AttemptID != "", event.Type, event.CausationID == failureID, payload)
}

func TestExecutorDoesNotCanonicalizeDifferentLiveLease(t *testing.T) {
	store, driver := handlerStore(t, false)
	process := exec.Command("sleep", "30")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Process.Kill()
		_ = process.Wait()
	})
	start, err := procid.Read(process.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	owned, present, err := store.ReadLease("run-1")
	if err != nil || !present {
		t.Fatalf("owned lease = %+v present=%t error=%v", owned, present, err)
	}
	other := runstore.Lease{Epoch: owned.Epoch, Token: "different-live-owner", PID: process.Process.Pid, Start: start}
	if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		if _, err := transaction.At("test.remove_owner").CompareRemoveLease(owned.Identity()); err != nil {
			return err
		}
		_, err := transaction.At("test.install_other").CreateLease(true, other)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	state, err := driver.State()
	if err != nil {
		t.Fatal(err)
	}
	input := recovery.Input{
		Projection: recovery.Projection{State: state},
		Observations: recovery.Observations{Lease: recovery.LeaseObservation{
			Exists: true, Readable: true, Epoch: other.Epoch, Owner: recovery.OwnerLive,
			Identity: &recovery.LeaseIdentity{Epoch: other.Epoch, Token: other.Token, PID: other.PID, Start: other.Start},
		}},
	}
	got := (&Executor{Store: store, RunID: "run-1", Driver: driver}).canonicalizeDriverLease(input)
	if got.Observations.Lease.Owner != recovery.OwnerLive {
		t.Fatalf("owner = %s, want live", got.Observations.Lease.Owner)
	}
	if decision := recovery.Plan(got); decision.CaseID != recovery.CaseLiveOwner {
		t.Fatalf("decision = %s, want %s", decision.CaseID, recovery.CaseLiveOwner)
	}
}

func TestDefaultHandlersAppendRecoveredAttemptFailureToRealStore(t *testing.T) {
	store, driver := handlerStore(t, true)
	state, err := driver.State()
	if err != nil {
		t.Fatal(err)
	}
	first := recovery.Input{Projection: recovery.Projection{
		State: state,
		CurrentHeadAttempt: &recovery.AttemptRecovery{
			AttemptID: "attempt-1", MovementID: "write", ScoreRevision: 1, State: runstate.AttemptStarting,
			FailureClassification: recovery.FailureClassification{CurrentPerformer: "writer", VisitedPerformers: []string{"writer"}, RetriesPerMovement: 1, RemainingTimeMS: 1},
		},
	}}
	executor := &Executor{Store: store, Driver: driver}
	result, err := executor.execute(context.Background(), first, recovery.Decision{CaseID: recovery.CaseUnstartedAttempt, Action: &recovery.Action{
		Kind: recovery.ActionRecoverUnstartedAttempt, AttemptID: "attempt-1", FailureKind: "task_failed", FailureReason: "attempt_never_started",
		Steps: []recovery.ActionStep{recovery.StepStabilizeHandoff, recovery.StepCloseAdapterInterval, recovery.StepClassifyAndAppendFailure},
	}})
	if err != nil || len(result.Steps) != 3 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	replayed, err := driver.State()
	if err != nil {
		t.Fatal(err)
	}
	failure := replayed.Attempts["attempt-1"].Failure
	if failure == nil || failure.Kind != "task_failed" || failure.Reason != "attempt_never_started" || failure.Disposition.Charged != "quality_retry" {
		t.Fatalf("replayed failure = %+v", failure)
	}
	assertLastEventType(t, store, runstate.EventAttemptFailed)
}

func TestClampedCloseRefreshesBudgetBeforeFailureClassification(t *testing.T) {
	store, driver := handlerStore(t, true)
	appendMeasuredExecution(t, driver, "spent", "adapter", 600000, 599999)
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{
		"interval_id": "last-millisecond", "phase": "adapter", "wall_start": time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "remaining_at_start": 1,
	})}, "test.last_millisecond.started"); err != nil {
		t.Fatal(err)
	}
	load := func(context.Context) (recovery.Input, error) {
		state, err := driver.State()
		if err != nil {
			return recovery.Input{}, err
		}
		remaining := int64(600000) - state.ConsumedBudgetMS
		attempt := handlerAttempt(state)
		attempt.FailureClassification.RemainingTimeMS = remaining
		return recovery.Input{Projection: recovery.Projection{State: state, CurrentHeadAttempt: attempt}}, nil
	}
	input, err := load(context.Background())
	if err != nil || input.Projection.CurrentHeadAttempt.FailureClassification.RemainingTimeMS != 1 {
		t.Fatalf("pre-close input=%+v error=%v", input, err)
	}
	executor := &Executor{Store: store, Driver: driver, Load: load}
	_, err = executor.execute(context.Background(), input, recovery.Decision{CaseID: recovery.CaseUnstartedAttempt, Action: &recovery.Action{
		Kind: recovery.ActionRecoverUnstartedAttempt, AttemptID: "attempt-1", FailureKind: "task_failed", FailureReason: "attempt_never_started",
		Steps: []recovery.ActionStep{recovery.StepCloseAdapterInterval, recovery.StepClassifyAndAppendFailure},
	}})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	last := journal.Events[len(journal.Events)-1]
	var payload map[string]any
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	disposition, _ := payload["disposition"].(map[string]any)
	if disposition["charged"] != "none" || disposition["terminal_reason"] != "budget_exhausted" {
		t.Fatalf("post-close failure disposition = %s, want exhausted budget", last.Payload)
	}
}

// A recovery-owned attempt that observes a cancellation terminalizes through the §6 oracle,
// so the run is already terminal when the handler returns. Reporting that as an execution
// error made `resume` call a cancelled run an operational interruption; §7 gives it exit 4.
// The executor replans instead, so C.1's terminal row supplies the outcome and there is no
// second way out.
func TestCancellationDuringRecoveryReplansToTheTerminalRow(t *testing.T) {
	store, driver := handlerStore(t, true)
	load := func(context.Context) (recovery.Input, error) {
		state, err := driver.State()
		if err != nil {
			return recovery.Input{}, err
		}
		return recovery.Input{Projection: recovery.Projection{State: state, CurrentHeadAttempt: handlerAttempt(state)}}, nil
	}
	cancelled := false
	executor := &Executor{
		Store:  store,
		RunID:  "run-1",
		Driver: driver,
		Load:   load,
		Steps: map[recovery.ActionStep]StepHandler{
			recovery.StepCloseAdapterInterval: func(_ context.Context, execution HandlerContext, _ recovery.Action) error {
				// Stand in for the driver observing the request mid-attempt and running the
				// oracle: the run is terminal by the time the handler returns. The oracle's
				// own ordering is pinned in internal/runstore; what is under test here is
				// that the executor replans instead of calling this an execution failure.
				current, err := driver.State()
				if err != nil {
					return err
				}
				if _, err := driver.Append(runstate.Event{
					RunID: "run-1", ScoreRevision: 1, Type: runstate.EventRunCancelled,
					Payload: handlerPayload(t, runstate.CancellationPayload(current, nil)),
				}, "test.run.cancelled"); err != nil {
					return err
				}
				cancelled = true
				return ErrRunCancelledDuringRecovery
			},
		},
	}
	input, err := load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.execute(context.Background(), input, recovery.Decision{
		CaseID: recovery.CaseUnstartedAttempt,
		Action: &recovery.Action{
			Kind:      recovery.ActionRecoverUnstartedAttempt,
			AttemptID: "attempt-1",
			Steps:     []recovery.ActionStep{recovery.StepCloseAdapterInterval, recovery.StepClassifyAndAppendFailure},
		},
	})
	if err != nil {
		t.Fatalf("cancellation was reported as an execution failure: %v", err)
	}
	if !cancelled {
		t.Fatal("the injected step never ran")
	}
	if result.Outcome != OutcomeCancelled {
		t.Fatalf("outcome = %q, want %q", result.Outcome, OutcomeCancelled)
	}
	// Result records effects that actually happened: the step ran and terminalized the run
	// before it reported the cancellation, and the steps after it did not.
	if !slices.Equal(result.Steps, []recovery.ActionStep{recovery.StepCloseAdapterInterval}) {
		t.Fatalf("recorded steps = %v, want only the one that ran", result.Steps)
	}
	if result.Replans != 1 {
		t.Fatalf("replans = %d, want exactly one to C.1's terminal row", result.Replans)
	}
}

// The test above injects the sentinel, so it cannot see whether the attempt handler still
// produces it. This pins that mapping directly: it is the line a review round found
// reporting a cancelled run as an execution failure.
func TestRecoveredAttemptOutcomeMapsCancellationToTheSentinel(t *testing.T) {
	for _, test := range []struct {
		outcome driver.Outcome
		want    error
	}{
		{outcome: driver.OutcomeSucceeded, want: nil},
		{outcome: driver.OutcomeCancelled, want: ErrRunCancelledDuringRecovery},
	} {
		t.Run(string(test.outcome), func(t *testing.T) {
			if err := recoveredAttemptOutcome(test.outcome); !errors.Is(err, test.want) {
				t.Fatalf("outcome %s mapped to %v, want %v", test.outcome, err, test.want)
			}
		})
	}
	// Everything else stays an execution failure rather than silently succeeding.
	for _, outcome := range []driver.Outcome{
		driver.OutcomeFailed, driver.OutcomeHalted, driver.OutcomeInterrupted,
	} {
		err := recoveredAttemptOutcome(outcome)
		if err == nil || errors.Is(err, ErrRunCancelledDuringRecovery) {
			t.Fatalf("outcome %s mapped to %v", outcome, err)
		}
	}
}

func appendMeasuredExecution(t *testing.T, driver *runstore.Driver, intervalID, phase string, remaining, charged int64) {
	t.Helper()
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{
		"interval_id": intervalID, "phase": phase, "wall_start": "2026-07-28T00:00:00.000Z", "remaining_at_start": remaining,
	})}, faultpoint.ReceiptAddress("test."+intervalID+".started")); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStopped, Payload: handlerPayload(t, map[string]any{
		"interval_id": intervalID, "reason": "normal", "charging": "measured", "charged_duration": charged,
	})}, faultpoint.ReceiptAddress("test."+intervalID+".stopped")); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultDirectKindsAppendToRealStore(t *testing.T) {
	t.Run("budget failure", func(t *testing.T) {
		store, driver := handlerStore(t, false)
		appendBudgetExhaustedInterval(t, store, driver)
		executor := &Executor{Store: store, Driver: driver}
		result, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: "RC-test", Action: &recovery.Action{
			Kind: recovery.ActionAppendBudgetFailure, MovementID: "write", FailureReason: "budget_exhausted",
		}})
		if err != nil || !slices.Equal(result.Kinds, []recovery.ActionKind{recovery.ActionAppendBudgetFailure}) {
			t.Fatalf("result=%+v error=%v", result, err)
		}
		state, err := driver.State()
		if err != nil || state.Movements["write"] != runstate.MovementFailed {
			t.Fatalf("state=%+v error=%v", state.Movements, err)
		}
		assertLastEventType(t, store, runstate.EventMovementFailed)
	})

	t.Run("run failed", func(t *testing.T) {
		store, driver := handlerStore(t, false)
		if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventMovementFailed, Payload: handlerPayload(t, map[string]any{"reason": "budget_exhausted", "run_failed": false})}, "test.movement_failed"); err != nil {
			t.Fatal(err)
		}
		executor := &Executor{Store: store, Driver: driver}
		_, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: "RC-test", Action: &recovery.Action{Kind: recovery.ActionAppendRunFailed}})
		if err != nil {
			t.Fatal(err)
		}
		state, err := driver.State()
		if err != nil || state.Run != runstate.RunFailed {
			t.Fatalf("run=%s error=%v", state.Run, err)
		}
		assertLastEventType(t, store, runstate.EventRunFailed)
	})
}

func TestRecoveryMovementFailureCutPointsAreReached(t *testing.T) {
	probe := &recoveryPointProbe{}
	store, driver := handlerStoreWithSeedsAndProbe(t, false, []runstate.MovementSeed{
		{ID: "write", Initial: runstate.MovementPending},
	}, probe)
	appendBudgetExhaustedInterval(t, store, driver)
	executor := &Executor{Store: store, Driver: driver}
	_, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: recovery.CaseBudgetExhausted, Action: &recovery.Action{
		Kind:          recovery.ActionAppendBudgetFailure,
		MovementID:    "write",
		FailureReason: "budget_exhausted",
		Steps: []recovery.ActionStep{
			recovery.StepAppendMovementBudgetFailure,
			recovery.StepAppendRunFailed,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []faultpoint.PointID{
		faultpoint.PointLifecycleMovementFailed,
		faultpoint.PointLifecycleRunFailed,
	}
	if !slices.Equal(probe.movementFailurePoints(), want) {
		t.Fatalf("movement-failure points=%v, want %v", probe.movementFailurePoints(), want)
	}
}

func TestBudgetFailureCitesBudgetExhaustingExecutionStop(t *testing.T) {
	t.Run("movement fan-in binds both terminal events to its closed interval", func(t *testing.T) {
		store, driver := handlerStore(t, false)
		stopID := appendBudgetExhaustedInterval(t, store, driver)
		executor := &Executor{Store: store, Driver: driver}
		_, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: recovery.CaseBudgetExhausted, Action: &recovery.Action{
			Kind: recovery.ActionAppendBudgetFailure, MovementID: "write", FailureReason: "budget_exhausted",
			Steps: []recovery.ActionStep{recovery.StepAppendMovementBudgetFailure, recovery.StepAppendRunFailed},
		}})
		if err != nil {
			t.Fatal(err)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range journal.Events {
			if event.Type == runstate.EventMovementFailed || event.Type == runstate.EventRunFailed {
				if event.CausationID != stopID {
					t.Fatalf("%s causation = %q, want budget execution stop %q", event.Type, event.CausationID, stopID)
				}
			}
		}
	})

	t.Run("candidate composition fails the run directly from its closed interval", func(t *testing.T) {
		store, driver := handlerStore(t, false)
		stopID := appendBudgetExhaustedInterval(t, store, driver)
		executor := &Executor{Store: store, Driver: driver}
		_, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: recovery.CaseBudgetExhausted, Action: &recovery.Action{
			Kind: recovery.ActionAppendRunFailed, FailureReason: "budget_exhausted",
		}})
		if err != nil {
			t.Fatal(err)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		last := journal.Events[len(journal.Events)-1]
		if last.Type != runstate.EventRunFailed || last.CausationID != stopID {
			t.Fatalf("run failure = %+v, want direct causation %q", last, stopID)
		}
	})
}

func TestAppendCompositionTerminalKeepsEvidenceStateNeutralUntilTerminal(t *testing.T) {
	store, authority := handlerStore(t, false)
	appendHandlerEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventCompositionConflicted, Payload: handlerPayload(t, map[string]any{
		"scope": "movement", "target_id": "write", "composition_subject_hash": "sha256:subject",
		"contributors": []any{map[string]any{"movement_id": "dependency", "change_set_id": "sha256:change"}}, "conflicted_paths": []any{"file"},
		"composition_algorithm_version": "1", "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{"partitur/composition-subject": 2}},
	})})
	state, err := authority.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Movements["write"] != runstate.MovementRunning {
		t.Fatalf("composition evidence projected movement state %s, want RUNNING", state.Movements["write"])
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	evidence := journal.Events[len(journal.Events)-1]
	// A newer, same-target evidence event must not replace the evidence selected
	// by the planner as the terminal's causation source.
	appendHandlerEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventCompositionFailed, Payload: handlerPayload(t, map[string]any{
		"scope": "movement", "target_id": "write", "composition_subject_hash": "sha256:newer",
		"cause": "git_exit", "git_exit_code": 2, "diagnostic": "newer evidence", "contributors": []any{},
		"composition_algorithm_version": "1", "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{"partitur/composition-subject": 2}},
	})})
	if err := appendCompositionTerminal(context.Background(), HandlerContext{Store: store, Driver: authority, RunID: "run-1"}, recovery.Action{CompositionTerminal: &recovery.CompositionTerminal{Scope: "movement", TargetID: "write", Reason: "composition_unresolvable", EvidenceEventID: evidence.EventID, ScoreRevision: evidence.ScoreRevision}}); err != nil {
		t.Fatal(err)
	}
	journal, err = store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	matching := 0
	for _, event := range journal.Events {
		if event.Type == runstate.EventMovementFailed && event.MovementID == "write" {
			if event.CausationID != evidence.EventID {
				t.Fatalf("composition terminal causation = %q, want evidence %q", event.CausationID, evidence.EventID)
			}
			matching++
		}
	}
	if matching == 0 {
		t.Fatal("composition terminal inspection observed zero matching movement.failed events")
	}
	state, err = authority.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Movements["write"] != runstate.MovementFailed {
		t.Fatalf("terminal movement state = %s, want FAILED", state.Movements["write"])
	}
}

func TestAppendCompositionTerminalReachesScopeTerminalPoint(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		scope string
		point faultpoint.PointID
	}{
		{name: "movement", scope: "movement", point: faultpoint.PointCompositionMovementTerminal},
		{name: "candidate", scope: "candidate", point: faultpoint.PointCompositionCandidateTerminal},
	} {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			probe := &recoveryPointProbe{}
			store, authority := handlerStoreWithSeedsAndProbe(t, false, []runstate.MovementSeed{{ID: "write", Initial: runstate.MovementPending}}, probe)
			evidence := runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCompositionFailed, Payload: handlerPayload(t, map[string]any{
				"scope": scenario.scope, "target_id": "run-1", "composition_subject_hash": "sha256:subject",
				"cause": "spawn_failed", "diagnostic": "fixture", "contributors": []any{},
				"composition_algorithm_version": "1", "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{"partitur/composition-subject": 2}},
			})}
			if scenario.scope == "movement" {
				evidence.MovementID = "write"
				evidence.Payload = handlerPayload(t, map[string]any{
					"scope": scenario.scope, "target_id": "write", "composition_subject_hash": "sha256:subject",
					"cause": "spawn_failed", "diagnostic": "fixture", "contributors": []any{},
					"composition_algorithm_version": "1", "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{"partitur/composition-subject": 2}},
				})
			}
			appendHandlerEvent(t, store, evidence)
			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			evidence = journal.Events[len(journal.Events)-1]
			if err := appendCompositionTerminal(context.Background(), HandlerContext{Store: store, Driver: authority, RunID: "run-1"}, recovery.Action{CompositionTerminal: &recovery.CompositionTerminal{
				Scope: scenario.scope, TargetID: map[bool]string{true: "write", false: "run-1"}[scenario.scope == "movement"], Reason: "composition_failed", EvidenceEventID: evidence.EventID, ScoreRevision: evidence.ScoreRevision,
			}}); err != nil {
				t.Fatal(err)
			}
			if len(probe.points) == 0 || probe.points[len(probe.points)-1] != scenario.point {
				t.Fatalf("recovery probe points = %v, want final %q", probe.points, scenario.point)
			}
		})
	}
}

func TestAppendCompositionTerminalYieldsToDurableCancellation(t *testing.T) {
	store, authority := handlerStore(t, false)
	appendHandlerEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventCompositionFailed, Payload: handlerPayload(t, map[string]any{
		"scope": "movement", "target_id": "write", "composition_subject_hash": "sha256:subject", "cause": "spawn_failed", "diagnostic": "spawn", "contributors": []any{map[string]any{"movement_id": "dependency", "change_set_id": "sha256:change"}},
		"composition_algorithm_version": "1", "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{"partitur/composition-subject": 2}},
	})})
	if _, err := authority.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCancelRequested, Payload: handlerPayload(t, map[string]any{"requested_by": "cli"})}, "test.cancel.requested"); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	evidence := journal.Events[len(journal.Events)-2]
	err = appendCompositionTerminal(context.Background(), HandlerContext{Store: store, Driver: authority, RunID: "run-1"}, recovery.Action{CompositionTerminal: &recovery.CompositionTerminal{Scope: "movement", TargetID: "write", Reason: "composition_failed", EvidenceEventID: evidence.EventID, ScoreRevision: evidence.ScoreRevision}})
	if !errors.Is(err, ErrRecoveryReplan) {
		t.Fatalf("terminal under cancellation error = %v, want ErrRecoveryReplan", err)
	}
	journal, err = store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type == runstate.EventMovementFailed {
			t.Fatalf("cancellation lost C.1 precedence; appended %s", event.Type)
		}
	}
}

func TestAppendCompositionTerminalSerializesCancellationAfterEvidence(t *testing.T) {
	store, authority := handlerStore(t, false)
	appendHandlerEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventCompositionFailed, Payload: handlerPayload(t, map[string]any{
		"scope": "movement", "target_id": "write", "composition_subject_hash": "sha256:subject", "cause": "spawn_failed", "diagnostic": "spawn", "contributors": []any{map[string]any{"movement_id": "dependency", "change_set_id": "sha256:change"}},
		"composition_algorithm_version": "1", "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{"partitur/composition-subject": 2}},
	})})
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	evidence := journal.Events[len(journal.Events)-1]
	requestStarted := make(chan struct{})
	requestDone := make(chan error, 1)
	err = appendCompositionTerminal(context.Background(), HandlerContext{
		Store: store, Driver: authority, RunID: "run-1",
		afterCompositionEvidence: func() {
			go func() {
				close(requestStarted)
				requestDone <- store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
					_, err := transaction.At("test.cancel.requested").Append(runstate.Event{
						RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCancelRequested,
						Payload: handlerPayload(t, map[string]any{"requested_by": "cli"}),
					})
					return err
				})
			}()
			<-requestStarted
		},
	}, recovery.Action{CompositionTerminal: &recovery.CompositionTerminal{
		Scope: "movement", TargetID: "write", Reason: "composition_failed", EvidenceEventID: evidence.EventID, ScoreRevision: evidence.ScoreRevision,
	}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-requestDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation request did not complete after recovery terminalized")
	}
	journal, err = store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	evidenceIndex, terminalIndex, cancellationIndex := -1, -1, -1
	for index, event := range journal.Events {
		switch event.Type {
		case runstate.EventCompositionFailed:
			if event.EventID == evidence.EventID {
				evidenceIndex = index
			}
		case runstate.EventMovementFailed:
			terminalIndex = index
		case runstate.EventCancelRequested:
			cancellationIndex = index
		}
	}
	if evidenceIndex < 0 || terminalIndex < 0 || cancellationIndex < 0 {
		t.Fatalf("composition/cancellation journal sequence is incomplete: %+v", journal.Events)
	}
	if !(evidenceIndex < terminalIndex && terminalIndex < cancellationIndex) {
		t.Fatalf("cancellation interleaved with recovery terminal: evidence=%d terminal=%d cancellation=%d", evidenceIndex, terminalIndex, cancellationIndex)
	}
}

func appendBudgetExhaustedInterval(t *testing.T, store *runstore.Store, driver *runstore.Driver) string {
	t.Helper()
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{
		"interval_id": "composition-1", "phase": "composition", "wall_start": "2026-07-28T00:00:00.000Z", "remaining_at_start": 1,
	})}, "test.execution_started"); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStopped, Payload: handlerPayload(t, map[string]any{
		"interval_id": "composition-1", "reason": "budget_exhausted", "charging": "clamped", "charged_duration": 1, "observed_at": "2026-07-28T00:00:00.001Z",
	})}, "test.execution_stopped"); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	return journal.Events[len(journal.Events)-1].EventID
}

func TestDefaultAcceptanceHandlersAppendToRealStore(t *testing.T) {
	t.Run("acceptance completion records attempt then movement", func(t *testing.T) {
		store, driver := handlerStore(t, true)
		advanceHandlerAcceptance(t, driver, false)
		state, err := driver.State()
		if err != nil {
			t.Fatal(err)
		}
		input := recovery.Input{Projection: recovery.Projection{State: state, CurrentHeadAttempt: handlerAttempt(state)}}
		executor := &Executor{Store: store, Driver: driver}
		_, err = executor.execute(context.Background(), input, recovery.Decision{CaseID: recovery.CaseGateFreeCompletion, Action: &recovery.Action{
			Kind: recovery.ActionAppendAcceptanceSuccess, AttemptID: "attempt-1", MovementID: "write",
			Steps: []recovery.ActionStep{recovery.StepAppendAttemptCompleted, recovery.StepAppendMovementSucceeded},
		}})
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := driver.State()
		if err != nil || replayed.Attempts["attempt-1"].State != runstate.AttemptCompleted || replayed.Movements["write"] != runstate.MovementSucceeded {
			t.Fatalf("attempt=%+v movement=%s error=%v", replayed.Attempts["attempt-1"], replayed.Movements["write"], err)
		}
		assertLastEventType(t, store, runstate.EventMovementSucceeded)
	})
}

func TestRCResume033ExecutorResumesOnlySelectedCriterion(t *testing.T) {
	fixture := resumeCriterionFixture(t)
	defer fixture.driver.Release()

	loaded, err := fixture.store.LoadRunInput(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	input := recovery.Input{
		Projection: loaded.Projection,
		Observations: recovery.Observations{
			AcceptanceSubject: recovery.SubjectMatched,
			Lease: recovery.LeaseObservation{
				Exists: true, Readable: true,
				Epoch: loaded.Projection.State.Authority.Epoch, Owner: recovery.OwnerCurrentDriver,
			},
		},
	}
	selection := recovery.PlanAcceptance(input)
	if selection.CaseID != recovery.CaseNextCriterion || selection.Action == nil ||
		selection.Action.Kind != recovery.ActionResumeCriterion ||
		selection.Action.CriterionID != "second" {
		t.Fatalf("acceptance decision=%+v, want RC-RESUME-033 resuming second", selection)
	}
	decision := recovery.Plan(input)

	stopAfterResume := errors.New("stop after selected criterion resume")
	executor := &Executor{
		Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID,
		Load: func(context.Context) (recovery.Input, error) {
			return recovery.Input{}, stopAfterResume
		},
	}
	if _, err := executor.execute(context.Background(), input, decision); !errors.Is(err, stopAfterResume) {
		t.Fatalf("resume error = %v, want reload stop after selected criterion", err)
	}

	state, err := fixture.driver.State()
	if err != nil {
		t.Fatal(err)
	}
	acceptance := state.Acceptances[fixture.attemptID]
	if !acceptance.Criteria["first"].Completed ||
		!acceptance.Criteria["second"].Completed ||
		acceptance.EvaluationCompleted {
		t.Fatalf("acceptance after selected resume = %+v", acceptance)
	}

	journal, err := fixture.store.ReadJournal(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if got := journal.Events[len(journal.Events)-2:]; got[0].Type != runstate.EventCriterionStarted ||
		got[1].Type != runstate.EventCriterionCompleted ||
		!bytes.Contains(got[0].Payload, []byte(`"criterion_id":"second"`)) ||
		!bytes.Contains(got[1].Payload, []byte(`"criterion_id":"second"`)) {
		t.Fatalf("resume events = %+v, want second criterion start then completion", got)
	}
}

func TestRCResume033ReachesAcceptanceEvaluationCompletedAfterSelectedCriterion(t *testing.T) {
	fixture := resumeCriterionFixture(t)
	defer fixture.driver.Release()
	stopAfterCompletion := errors.New("stop after acceptance evaluation completion")
	loads := 0
	load := func(context.Context) (recovery.Input, error) {
		loads++
		if loads == 3 {
			return recovery.Input{}, stopAfterCompletion
		}
		loaded, err := fixture.store.LoadRunInput(fixture.runID)
		if err != nil {
			return recovery.Input{}, err
		}
		return recovery.Input{
			Projection: loaded.Projection,
			Observations: recovery.Observations{
				AcceptanceSubject: recovery.SubjectMatched,
				Lease: recovery.LeaseObservation{
					Exists: true, Readable: true,
					Epoch: loaded.Projection.State.Authority.Epoch, Owner: recovery.OwnerCurrentDriver,
				},
			},
		}, nil
	}
	executor := &Executor{Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID, Load: load}
	if _, err := executor.Execute(context.Background()); !errors.Is(err, stopAfterCompletion) {
		t.Fatalf("resume error = %v, want stop after acceptance completion", err)
	}

	journal, err := fixture.store.ReadJournal(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type == runstate.EventAcceptanceEvaluationCompleted {
			return
		}
	}
	t.Fatalf("journal has no %s: %+v", runstate.EventAcceptanceEvaluationCompleted, journal.Events)
}

func TestRCResume033BudgetExhaustionUsesSharedAcceptanceTerminal(t *testing.T) {
	installRecoverySuccessAdapter(t)
	fixture := resumeCriterionFixture(t, true)
	defer fixture.driver.Release()
	input, err := fixture.store.LoadRunInput(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.Scheduler.RemainingTime != 0 {
		t.Fatalf("recovery budget fixture remaining=%d, want 0", input.Projection.Scheduler.RemainingTime)
	}

	if err := resumeCriterion(context.Background(), HandlerContext{
		Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID,
	}, recovery.Action{Kind: recovery.ActionResumeCriterion, AttemptID: fixture.attemptID, CriterionID: "second"}); err != nil {
		t.Fatal(err)
	}
	journal, err := fixture.store.ReadJournal(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	start := -1
	for index, event := range journal.Events {
		if event.Type == runstate.EventCriterionStarted && bytes.Contains(event.Payload, []byte(`"criterion_id":"second"`)) {
			start = index
			break
		}
	}
	if start < 0 {
		t.Fatal("resumed run criterion did not start")
	}
	got := make([]runstate.EventType, 0, len(journal.Events)-start)
	for _, event := range journal.Events[start:] {
		got = append(got, event.Type)
	}
	want := []runstate.EventType{
		runstate.EventCriterionStarted, runstate.EventCriterionCompleted, runstate.EventExecutionStopped,
		runstate.EventAttemptFailed, runstate.EventMovementFailed, runstate.EventRunFailed,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("recovered budget terminal events = %v, want %v", got, want)
	}
	for _, event := range journal.Events[start:] {
		if event.Type != runstate.EventExecutionStopped && event.Type != runstate.EventAttemptFailed {
			continue
		}
		payload := map[string]any{}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if event.Type == runstate.EventExecutionStopped && payload["reason"] != "budget_exhausted" {
			t.Fatalf("recovered budget execution.stop = %#v", payload)
		}
		if event.Type == runstate.EventAttemptFailed && payload["kind"] != "budget_exhausted" {
			t.Fatalf("recovered budget attempt.failed = %#v", payload)
		}
	}
}

func TestRCResume019MatchesLiveMovementSuccessPayload(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(fmt.Sprintf("final=%t", final), func(t *testing.T) {
			store, driver := handlerStoreWithSeeds(t, true, []runstate.MovementSeed{{
				ID: "write", Initial: runstate.MovementPending, Final: final,
			}})
			appendHandlerCandidate(t, driver)
			advanceHandlerAcceptance(t, driver, false)
			if _, err := driver.Append(runstate.Event{
				RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1",
				Type: runstate.EventAttemptCompleted, Payload: handlerPayload(t, map[string]any{}),
			}, "test.attempt_completed"); err != nil {
				t.Fatal(err)
			}
			state, err := driver.State()
			if err != nil {
				t.Fatal(err)
			}
			if err := appendMovementSucceeded(context.Background(), HandlerContext{
				Store: store, Driver: driver, RunID: "run-1",
			}, recovery.Action{Kind: recovery.ActionAppendMovementSucceeded, AttemptID: "attempt-1"}); err != nil {
				t.Fatal(err)
			}
			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(journal.Events[len(journal.Events)-1].Payload, &got); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"approved_artifact_instance_ids": []any{},
				"identity_versions":              map[string]any{"canonical_encoding": float64(1), "projections": map[string]any{}},
				"run_succeeded":                  final,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("RC-RESUME-019 payload = %#v, want live payload %#v", got, want)
			}
			if state.FinalMovements["write"] != final {
				t.Fatalf("seeded finality = %#v, want %t", state.FinalMovements, final)
			}
		})
	}
}

// TestProjectorAcceptsRunSucceededAfterNonFinalMovementSucceeded pins the event
// shape the projector now accepts, and nothing more. It is deliberately NOT a
// waived-runtime test: it pre-records application_candidate.recorded, whereas §8
// gives an active waived run no recorded candidate -- the candidate is folded
// into run.succeeded. The live and recovery scheduler reach waived completion
// through ActionComposeCandidate; this test does not pin that runtime path.
func TestProjectorAcceptsRunSucceededAfterNonFinalMovementSucceeded(t *testing.T) {
	store, driver := handlerStoreWithSeeds(t, true, []runstate.MovementSeed{{
		ID: "write", Initial: runstate.MovementPending,
	}})
	appendHandlerCandidate(t, driver)
	advanceHandlerAcceptance(t, driver, false)
	if _, err := driver.Append(runstate.Event{
		RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1",
		Type: runstate.EventAttemptCompleted, Payload: handlerPayload(t, map[string]any{}),
	}, "test.attempt_completed"); err != nil {
		t.Fatal(err)
	}
	if err := appendMovementSucceeded(context.Background(), HandlerContext{
		Store: store, Driver: driver, RunID: "run-1",
	}, recovery.Action{Kind: recovery.ActionAppendMovementSucceeded, AttemptID: "attempt-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Append(runstate.Event{
		RunID: "run-1", ScoreRevision: 1, Type: runstate.EventRunSucceeded,
		Payload: handlerPayload(t, map[string]any{
			"candidate": map[string]any{
				"candidate_id": "candidate-1", "base_tree": "git-sha1:tree", "result_tree": "git-sha1:tree",
				"ordered_change_sets": []any{}, "contributors": []any{},
				"candidate_composition_dependency_hash": "sha256:composition",
			},
			"waiver":            map[string]any{"reason": "fixture waiver"},
			"identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
		}),
	}, "test.run_succeeded"); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	got := []runstate.EventType{journal.Events[len(journal.Events)-2].Type, journal.Events[len(journal.Events)-1].Type}
	if want := []runstate.EventType{runstate.EventMovementSucceeded, runstate.EventRunSucceeded}; !slices.Equal(got, want) {
		t.Fatalf("terminal durable events = %v, want %v", got, want)
	}
	var payload map[string]any
	if err := json.Unmarshal(journal.Events[len(journal.Events)-1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["candidate"] == nil || payload["waiver"].(map[string]any)["reason"] != "fixture waiver" {
		t.Fatalf("run.succeeded payload = %#v", payload)
	}
	state, err := driver.State()
	if err != nil || state.Run != runstate.RunSucceeded {
		t.Fatalf("state=%+v error=%v", state.Run, err)
	}
}

func TestResumeSchedulesNextDurableMovementInDeclarationOrder(t *testing.T) {
	store, driver := handlerStore(t, true)
	advanceHandlerAcceptance(t, driver, false)
	if _, err := driver.Append(runstate.Event{
		RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1",
		Type: runstate.EventAttemptCompleted, Payload: handlerPayload(t, map[string]any{}),
	}, "test.attempt_completed"); err != nil {
		t.Fatal(err)
	}
	if err := appendMovementSucceeded(context.Background(), HandlerContext{
		Store: store, Driver: driver, RunID: "run-1",
	}, recovery.Action{Kind: recovery.ActionAppendMovementSucceeded, AttemptID: "attempt-1"}); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("observed durable movement.ready")
	executor := &Executor{Store: store, RunID: "run-1", Driver: driver}
	executor.Load = func(context.Context) (recovery.Input, error) {
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			return recovery.Input{}, err
		}
		for _, event := range journal.Events {
			if event.Type == runstate.EventMovementReady && event.MovementID == "read" {
				return recovery.Input{}, stop
			}
		}
		state, err := driver.State()
		if err != nil {
			return recovery.Input{}, err
		}
		input := recovery.Input{Projection: recovery.Projection{
			State: state,
			// This fixture declares write before read, and read depends only on
			// write. The expected next event is therefore fixed here as read,
			// not derived by invoking the scheduler under test.
			Scheduler: recovery.Scheduler{RemainingTime: 600000, Movements: []recovery.ScheduledMovement{
				{ID: "write"}, {ID: "read", Needs: []runstate.MovementID{"write"}},
			}},
		}}
		lease, present, err := store.ReadLease("run-1")
		if err != nil {
			return recovery.Input{}, err
		}
		if present {
			input.Observations.Lease = recovery.LeaseObservation{
				Exists: true, Readable: true, Epoch: lease.Epoch, Owner: recovery.OwnerLive,
				Identity: &recovery.LeaseIdentity{Epoch: lease.Epoch, Token: lease.Token, PID: lease.PID, Start: lease.Start},
			}
		}
		return input, nil
	}
	result, err := executor.Execute(context.Background())
	if !errors.Is(err, stop) {
		t.Fatalf("resume result=%+v error=%v, want durable ready stop", result, err)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	ready := 0
	for _, event := range journal.Events {
		if event.Type == runstate.EventMovementReady && event.MovementID == "read" {
			ready++
		}
	}
	if ready != 1 {
		t.Fatalf("read movement.ready count=%d, want one", ready)
	}
}

func TestExecutorRecoversIncompleteCriterionAfterSweep(t *testing.T) {
	tests := []struct {
		name              string
		subject           recovery.SubjectVerification
		wantEventTypes    []runstate.EventType
		wantFailureReason string
	}{
		{
			name:              "matched post-sweep subject records an unobserved completion then fails acceptance",
			subject:           recovery.SubjectMatched,
			wantEventTypes:    []runstate.EventType{runstate.EventCriterionCompleted, runstate.EventAcceptanceFailed},
			wantFailureReason: "criterion_errored",
		},
		{
			name:              "mismatched post-sweep subject fails acceptance without inventing a completion",
			subject:           recovery.SubjectMismatched,
			wantEventTypes:    []runstate.EventType{runstate.EventAcceptanceFailed},
			wantFailureReason: "recovery_subject_mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, driver := handlerStore(t, true)
			advanceHandlerAcceptance(t, driver, true)
			stop := errors.New("stop after recovery replan")
			loads := 0
			executor := &Executor{Store: store, Driver: driver}
			executor.Load = func(context.Context) (recovery.Input, error) {
				loads++
				if loads == 3 {
					return recovery.Input{}, stop
				}
				state, err := driver.State()
				if err != nil {
					return recovery.Input{}, err
				}
				return incompleteCriterionInput(state, test.subject), nil
			}

			result, err := executor.Execute(context.Background())
			if !errors.Is(err, stop) || result.Replans != 0 || loads != 3 {
				t.Fatalf("result=%+v loads=%d error=%v", result, loads, err)
			}
			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			got := make([]runstate.EventType, 0, len(test.wantEventTypes))
			for _, event := range journal.Events {
				if event.Type == runstate.EventCriterionCompleted || event.Type == runstate.EventAcceptanceFailed {
					got = append(got, event.Type)
				}
			}
			if !slices.Equal(got, test.wantEventTypes) {
				t.Fatalf("terminal recovery events = %v, want %v", got, test.wantEventTypes)
			}
			last := journal.Events[len(journal.Events)-1]
			var payload map[string]any
			if err := json.Unmarshal(last.Payload, &payload); err != nil || payload["reason"] != test.wantFailureReason {
				t.Fatalf("acceptance failure payload = %s error=%v", last.Payload, err)
			}
			if test.subject == recovery.SubjectMatched {
				if last.Type != runstate.EventAcceptanceFailed {
					t.Fatalf("final journal event type = %s, want %s", last.Type, runstate.EventAcceptanceFailed)
				}
				criterion := journal.Events[len(journal.Events)-2]
				var criterionPayload map[string]any
				if err := json.Unmarshal(criterion.Payload, &criterionPayload); err != nil {
					t.Fatal(err)
				}
				if criterionPayload["outcome"] != "ERROR" {
					t.Fatalf("criterion completion outcome = %v, want ERROR", criterionPayload["outcome"])
				}
				for _, absent := range []string{"exit_code", "duration_ms", "output_ref"} {
					if _, ok := criterionPayload[absent]; ok {
						t.Fatalf("criterion completion unexpectedly carries %s: %s", absent, criterion.Payload)
					}
				}
				if criterionPayload["error_detail"] != "recovered_without_observed_completion" {
					t.Fatalf("criterion completion payload = %s", criterion.Payload)
				}
				state, err := driver.State()
				if err != nil {
					t.Fatal(err)
				}
				if state.Attempts["attempt-1"].State != runstate.AttemptFailed {
					t.Fatalf("replayed attempt state = %s, want %s", state.Attempts["attempt-1"].State, runstate.AttemptFailed)
				}
				if state.Acceptances["attempt-1"].Criteria["criterion-1"].Outcome != "ERROR" {
					t.Fatalf("replayed criterion outcome = %s, want ERROR", state.Acceptances["attempt-1"].Criteria["criterion-1"].Outcome)
				}
			}
			if test.subject == recovery.SubjectMismatched {
				state, err := driver.State()
				if err != nil {
					t.Fatal(err)
				}
				if state.Attempts["attempt-1"].State != runstate.AttemptFailed {
					t.Fatalf("replayed attempt state = %s, want %s", state.Attempts["attempt-1"].State, runstate.AttemptFailed)
				}
				if last.Type != runstate.EventAcceptanceFailed {
					t.Fatalf("final journal event type = %s, want %s", last.Type, runstate.EventAcceptanceFailed)
				}
				if _, ok := payload["failed_criterion_id"]; ok {
					t.Fatalf("acceptance failure unexpectedly carries failed_criterion_id: %s", last.Payload)
				}
			}
		})
	}
}

func incompleteCriterionInput(state runstate.State, subject recovery.SubjectVerification) recovery.Input {
	state.Authority.Epoch = 0
	attempt := handlerAttempt(state)
	attempt.AcceptanceStarted = true
	return recovery.Input{Projection: recovery.Projection{
		State:              state,
		CurrentHeadAttempt: attempt,
		Acceptance:         &recovery.AcceptanceRecovery{},
	}, Observations: recovery.Observations{AcceptanceSubject: subject}}
}

func TestExecutorMapsSweepFailureToHaltWithoutJournalWrite(t *testing.T) {
	for _, test := range []struct {
		name     string
		step     recovery.ActionStep
		err      error
		wantHalt recovery.HaltReason
	}{
		{name: "recorded session sweep", step: recovery.StepSweepRecordedSession, err: runstate.ErrSweepUnverifiable, wantHalt: recovery.HaltSweepUnverifiable},
		{name: "spawn handoff", step: recovery.StepStabilizeHandoff, err: ErrHandoffUnverifiable, wantHalt: recovery.HaltSpawnHandoffUnverifiable},
		{name: "git invocation", step: recovery.StepSweepRecordedSession, err: workspace.ErrGitUnverifiable, wantHalt: recovery.HaltGitUnverifiable},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, driver := handlerStore(t, true)
			journalPath := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "journal.jsonl")
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			executor := &Executor{Store: store, Driver: driver, Steps: map[recovery.ActionStep]StepHandler{
				test.step: func(context.Context, HandlerContext, recovery.Action) error { return test.err },
			}}
			result, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: recovery.CaseUnprobedAttempt, Action: &recovery.Action{
				Kind: recovery.ActionRecoverUnprobedAttempt, AttemptID: "attempt-1", Steps: []recovery.ActionStep{test.step},
			}})
			if err != nil || result.Outcome != OutcomeHalted || result.Decision.Halt != test.wantHalt {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			after, err := os.ReadFile(journalPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("journal changed=%t error=%v", string(after) != string(before), err)
			}
		})
	}
}

func TestExecutorMapsAppendErrorsToAppendixDHalts(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want recovery.HaltReason
	}{
		{name: "append idempotency conflict", err: runstore.ErrJournalIdempotencyConflict, want: recovery.HaltJournalIdempotencyConflict},
		{name: "append unsupported format", err: canonical.ErrUnsupportedRunFormat, want: recovery.HaltUnsupportedRunFormat},
	} {
		t.Run(test.name, func(t *testing.T) {
			step := recovery.StepAppendRunFailed
			executor := &Executor{Steps: map[recovery.ActionStep]StepHandler{
				step: func(context.Context, HandlerContext, recovery.Action) error { return test.err },
			}}
			result, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{
				CaseID: "RC-test",
				Action: &recovery.Action{Kind: recovery.ActionReturnWaitingHuman, Steps: []recovery.ActionStep{step}},
			})
			if err != nil || result.Outcome != OutcomeHalted || result.Decision.Halt != test.want {
				t.Fatalf("result=%+v error=%v, want halt=%q", result, err, test.want)
			}
		})
	}
}

func TestExecutorMapsLoadErrorsToAppendixDHalts(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want recovery.HaltReason
	}{
		{name: "load corrupt journal", err: runstore.ErrJournalCorrupt, want: recovery.HaltJournalCorrupt},
		{name: "load missing pinned snapshot", err: runstore.ErrMissingPinnedSnapshot, want: recovery.HaltMissingSnapshotFile},
		{name: "load missing resolved cast", err: runstore.ErrMissingResolvedCast, want: recovery.HaltMissingResolvedCast},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &Executor{Load: func(context.Context) (recovery.Input, error) {
				return recovery.Input{}, test.err
			}}
			result, err := executor.Execute(context.Background())
			if err != nil || result.Outcome != OutcomeHalted || result.Decision.Halt != test.want {
				t.Fatalf("result=%+v error=%v, want halt=%q", result, err, test.want)
			}
		})
	}
}

func TestExecutorMapsEveryReloadSiteToAppendixDHalts(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want recovery.HaltReason
		run  func(*testing.T, error) (Result, error)
	}{
		{
			name: "post-reclaim reload maps corrupt journal",
			err:  runstore.ErrJournalCorrupt,
			want: recovery.HaltJournalCorrupt,
			run: func(t *testing.T, loadErr error) (Result, error) {
				executor := &Executor{Store: acquirableRecoveryStore(t), RunID: "run-1", Load: func(context.Context) (recovery.Input, error) {
					return recovery.Input{}, loadErr
				}}
				return executor.execute(context.Background(), recovery.Input{}, recovery.Decision{
					CaseID: recovery.CaseReclaimAuthority,
					Action: &recovery.Action{Kind: recovery.ActionReclaimAuthority},
				})
			},
		},
		{
			name: "post-effect reload maps missing pinned snapshot",
			err:  runstore.ErrMissingPinnedSnapshot,
			want: recovery.HaltMissingSnapshotFile,
			run: func(t *testing.T, loadErr error) (Result, error) {
				executor := testExecutor(map[recovery.ActionStep]StepHandler{
					recovery.StepCloseAdapterInterval: func(context.Context, HandlerContext, recovery.Action) error { return nil },
				})
				executor.Load = func(context.Context) (recovery.Input, error) { return recovery.Input{}, loadErr }
				return executor.execute(context.Background(), recovery.Input{}, stepDecision(true, recovery.StepCloseAdapterInterval))
			},
		},
		{
			name: "ordinary replan maps missing resolved cast",
			err:  runstore.ErrMissingResolvedCast,
			want: recovery.HaltMissingResolvedCast,
			run: func(t *testing.T, loadErr error) (Result, error) {
				executor := testExecutor(map[recovery.ActionStep]StepHandler{
					recovery.StepStabilizeHandoff: func(context.Context, HandlerContext, recovery.Action) error { return nil },
				})
				executor.Load = func(context.Context) (recovery.Input, error) { return recovery.Input{}, loadErr }
				return executor.execute(context.Background(), recovery.Input{}, stepDecision(true, recovery.StepStabilizeHandoff))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.run(t, test.err)
			if err != nil || result.Outcome != OutcomeHalted || result.Decision.Halt != test.want {
				t.Fatalf("result=%+v error=%v, want halt=%q", result, err, test.want)
			}
		})
	}
}

func advanceHandlerAcceptance(t *testing.T, driver *runstore.Driver, criterion bool) {
	advanceHandlerAcceptanceWithProcesses(t, driver, criterion, nil, nil)
}

func advanceHandlerAcceptanceWithProcesses(t *testing.T, driver *runstore.Driver, criterion bool, adapterProcess, criterionProcess map[string]any) {
	t.Helper()
	appendDriverEvent := func(eventType runstate.EventType, payload any) {
		t.Helper()
		if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: eventType, Payload: handlerPayload(t, payload)}, faultpoint.ReceiptAddress("test."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	if adapterProcess == nil {
		adapterProcess = map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "boot", "start_ticks": "1"}}
	}
	appendDriverEvent(runstate.EventAttemptStarted, map[string]any{"attempt_number": 1, "adapter_process": adapterProcess, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{}, "shell": false, "network": false}, "identity_versions": versions})
	appendDriverEvent(runstate.EventAdapterProbed, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})
	appendDriverEvent(runstate.EventPerformerCompleted, map[string]any{"session_hint_stored": false})
	appendDriverEvent(runstate.EventVerificationPassed, map[string]any{})
	planned := []any{}
	if criterion {
		planned = []any{"criterion-1"}
	}
	appendDriverEvent(runstate.EventAcceptanceStarted, map[string]any{"subject_tree": "git-sha1:subject", "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": planned, "identity_versions": versions})
	if criterion {
		payload := map[string]any{"criterion_id": "criterion-1", "criterion_spec_hash": "sha256:criterion", "subject_tree": "git-sha1:subject", "identity_versions": versions}
		if criterionProcess != nil {
			payload["criterion_process"] = criterionProcess
		}
		appendDriverEvent(runstate.EventCriterionStarted, payload)
		return
	}
	appendDriverEvent(runstate.EventAcceptanceEvaluationCompleted, map[string]any{"subject_tree": "git-sha1:subject", "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{}, "identity_versions": versions})
}

func appendCancellationRequest(t *testing.T, driver *runstore.Driver) {
	t.Helper()
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCancelRequested, Payload: handlerPayload(t, map[string]any{"requested_by": "cli"})}, "test.cancel.requested"); err != nil {
		t.Fatal(err)
	}
}

func sweptEmptyCancellationProcess(t *testing.T) map[string]any {
	t.Helper()
	start, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return cancellationProcessPayload(t, 999999, start)
}

func unverifiableCancellationProcess(t *testing.T) map[string]any {
	t.Helper()
	start, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return cancellationProcessPayload(t, os.Getpid(), distinctCancellationStart(t, start))
}

func cancellationProcessPayload(t *testing.T, sessionID int, start runstate.StartIdentity) map[string]any {
	t.Helper()
	identity := map[string]any{}
	switch value := start.(type) {
	case runstate.LinuxStartIdentity:
		identity = map[string]any{"platform": "linux", "boot_id": value.BootID, "start_ticks": value.StartTicks}
	case runstate.DarwinStartIdentity:
		identity = map[string]any{"platform": "darwin", "start_tvsec": value.StartTVSec, "start_tvusec": value.StartTVUsec}
	default:
		t.Fatalf("unsupported start identity %T", start)
	}
	return map[string]any{"pid": sessionID, "session_id": sessionID, "start_identity": identity}
}

func distinctCancellationStart(t *testing.T, start runstate.StartIdentity) runstate.StartIdentity {
	t.Helper()
	switch value := start.(type) {
	case runstate.LinuxStartIdentity:
		value.BootID += "-previous"
		return value
	case runstate.DarwinStartIdentity:
		value.StartTVSec++
		return value
	default:
		t.Fatalf("unsupported start identity %T", start)
		return nil
	}
}

func assertCancellationRemainsNonterminal(t *testing.T, store *runstore.Store) {
	t.Helper()
	input, err := store.LoadRunInput("run-1")
	if err != nil || input.Projection.State.Run.Terminal() {
		t.Fatalf("state=%+v error=%v", input.Projection.State.Run, err)
	}
}

func handlerAttempt(state runstate.State) *recovery.AttemptRecovery {
	attempt := state.Attempts["attempt-1"]
	return &recovery.AttemptRecovery{AttemptID: "attempt-1", MovementID: "write", ScoreRevision: 1, State: attempt.State,
		FailureClassification: recovery.FailureClassification{CurrentPerformer: "writer", VisitedPerformers: []string{"writer"}, RetriesPerMovement: 1, RemainingTimeMS: 1}}
}

func appendHandlerCandidate(t *testing.T, driver *runstore.Driver) {
	t.Helper()
	if _, err := driver.Append(runstate.Event{
		RunID: "run-1", ScoreRevision: 1, Type: runstate.EventApplicationCandidateRecorded,
		Payload: handlerPayload(t, map[string]any{
			"candidate_id": "candidate-1", "base_tree": "git-sha1:tree", "result_tree": "git-sha1:tree",
			"ordered_change_sets": []any{}, "contributors": []any{},
			"candidate_composition_dependency_hash": "sha256:composition",
			"identity_versions":                     map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
		}),
	}, "test.application_candidate_recorded"); err != nil {
		t.Fatal(err)
	}
}

func handlerStore(t *testing.T, selectAttempt bool) (*runstore.Store, *runstore.Driver) {
	return handlerStoreWithSeeds(t, selectAttempt, []runstate.MovementSeed{
		{ID: "write", Initial: runstate.MovementPending},
		{ID: "read", Initial: runstate.MovementPending},
	})
}

type resumeCriterionFixtureState struct {
	store     *runstore.Store
	driver    *runstore.Driver
	runID     runstate.RunID
	attemptID runstate.AttemptID
}

func resumeCriterionFixture(t *testing.T, budgetSecondRun ...bool) resumeCriterionFixtureState {
	t.Helper()
	secondCriterion := map[string]any{"id": "second", "artifact": "second"}
	if len(budgetSecondRun) != 0 && budgetSecondRun[0] {
		secondCriterion = map[string]any{"id": "second", "run": []any{"true"}}
	}
	root := t.TempDir()
	scoreDocument := map[string]any{
		"score": "0.2", "name": "resume-criterion", "revision": 1, "status": "finalized", "goal": "fixture",
		"verification": map[string]any{"expectation": map[string]any{"intent": "pass-existing-tests", "apply_gate": map[string]any{"require": []any{"verified"}}}, "final_movement": "write"},
		"parts":        map[string]any{"writer": map[string]any{"capabilities": []any{"repo_read"}, "read_only": true}},
		"movements": []any{map[string]any{
			"id": "write", "part": "writer", "grants": []any{"repo_read"}, "instruction": "fixture",
			"outputs":    []any{map[string]any{"id": "first", "kind": "artifact"}, map[string]any{"id": "second", "kind": "artifact"}},
			"acceptance": map[string]any{"hard": []any{map[string]any{"id": "first", "artifact": "first"}, secondCriterion}},
		}},
		"policy": map[string]any{"allowed_paths": []any{"**"}, "budget": map[string]any{"active_wall_clock_min": 10}},
	}
	castDocument := map[string]any{
		"cast": "0.1", "performers": map[string]any{"worker": map[string]any{"adapter": "fixture", "model": "fixture"}},
		"bindings": map[string]any{"writer": map[string]any{"performer": "worker"}},
	}
	writeFixtureJSON(t, filepath.Join(root, "partitur.yaml"), scoreDocument)
	if err := os.Mkdir(filepath.Join(root, ".partitur"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixtureJSON(t, filepath.Join(root, ".partitur", "cast.yaml"), castDocument)
	for _, arguments := range [][]string{{"init"}, {"config", "user.name", "Partitur Test"}, {"config", "user.email", "partitur@example.invalid"}, {"add", "partitur.yaml", ".partitur/cast.yaml"}, {"commit", "-m", "fixture"}} {
		command := exec.Command("git", arguments...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", arguments, err, output)
		}
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	preparation, validation := validate.Prepare()
	if validation.Refusal != nil || validation.HasDiagnostics() || preparation == nil {
		t.Fatalf("preparation=%+v validation=%+v", preparation, validation)
	}
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(started.RunID, []runstate.MovementSeed{{ID: "write", Initial: runstate.MovementPending}})
	if err != nil {
		t.Fatal(err)
	}
	if err := started.Run.BindDriver(authority); err != nil {
		authority.Release()
		t.Fatal(err)
	}
	attempt, err := started.Run.CreateAttempt("write")
	if err != nil {
		authority.Release()
		t.Fatal(err)
	}
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	appendEvent := func(eventType runstate.EventType, payload any) {
		t.Helper()
		if _, err := authority.Append(runstate.Event{
			RunID: started.RunID, ScoreRevision: 1, MovementID: "write", PartID: "writer", AttemptID: attempt.AttemptID,
			Type: eventType, Payload: handlerPayload(t, payload),
		}, faultpoint.ReceiptAddress("test.rc_resume_033."+string(eventType))); err != nil {
			authority.Release()
			t.Fatal(err)
		}
	}
	appendEvent(runstate.EventMovementReady, map[string]any{})
	appendEvent(runstate.EventMovementStarted, map[string]any{})
	appendEvent(runstate.EventPerformerSelected, map[string]any{"reason": "initial", "performer_id": "worker", "adapter_id": "fixture", "model": "fixture"})
	appendEvent(runstate.EventAttemptStarted, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false}, "identity_versions": versions})
	appendEvent(runstate.EventAdapterProbed, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})
	appendEvent(runstate.EventArtifactRecorded, map[string]any{"logical_output_id": "first", "kind": "artifact", "content_hash": "sha256:first", "size_bytes": 1, "source_path": "first"})
	appendEvent(runstate.EventArtifactRecorded, map[string]any{"logical_output_id": "second", "kind": "artifact", "content_hash": "sha256:second", "size_bytes": 1, "source_path": "second"})
	appendEvent(runstate.EventPerformerCompleted, map[string]any{"session_hint_stored": false})
	appendEvent(runstate.EventVerificationPassed, map[string]any{})
	if len(budgetSecondRun) != 0 && budgetSecondRun[0] {
		appendEvent(runstate.EventExecutionStarted, map[string]any{"interval_id": "spent", "phase": "adapter", "wall_start": "2026-08-01T00:00:00.000Z", "remaining_at_start": 600000})
		appendEvent(runstate.EventExecutionStopped, map[string]any{"interval_id": "spent", "reason": "normal", "charging": "measured", "charged_duration": 600000})
		appendEvent(runstate.EventExecutionStarted, map[string]any{"interval_id": "acceptance", "phase": "acceptance", "wall_start": "2026-08-01T00:00:00.000Z", "remaining_at_start": 0})
	}
	loaded, err := store.LoadRunInput(started.RunID)
	if err != nil {
		authority.Release()
		t.Fatal(err)
	}
	plan, err := acceptance.Compile(loaded.Score.Movements()[0])
	if err != nil {
		authority.Release()
		t.Fatal(err)
	}
	subjectTree := "git-sha1:" + recoveryGitText(t, root, "rev-parse", "HEAD^{tree}")
	acceptanceStarted, err := plan.StartEvent(runstate.Event{
		RunID: started.RunID, ScoreRevision: 1, MovementID: "write", PartID: "writer", AttemptID: attempt.AttemptID,
	}, subjectTree)
	if err != nil {
		authority.Release()
		t.Fatal(err)
	}
	appendEvent(runstate.EventAcceptanceStarted, acceptanceStarted.Payload)
	var firstEvents []runstate.Event
	_, err = acceptance.EvaluateStartedCriterion(plan, acceptance.Evaluation{
		RunID: started.RunID, ScoreRevision: 1, MovementID: "write", PartID: "writer", AttemptID: attempt.AttemptID,
		SubjectTree: subjectTree,
		LookupArtifact: func(id runstate.ArtifactInstanceID) (runstate.ArtifactRecord, bool, error) {
			record, ok := map[runstate.ArtifactInstanceID]runstate.ArtifactRecord{
				"first@" + runstate.ArtifactInstanceID(attempt.AttemptID):  {AttemptID: attempt.AttemptID, LogicalOutputID: "first", Kind: "artifact"},
				"second@" + runstate.ArtifactInstanceID(attempt.AttemptID): {AttemptID: attempt.AttemptID, LogicalOutputID: "second", Kind: "artifact"},
			}[id]
			return record, ok, nil
		},
		Append: func(event runstate.Event) (faultpoint.DurabilityReceipt, error) {
			firstEvents = append(firstEvents, event)
			return faultpoint.DurabilityReceipt{Mutation: faultpoint.Mutation{
				Kind: faultpoint.JournalAppend, EventType: string(event.Type), EventID: "event", Sequence: uint64(len(firstEvents)),
				Timestamp: "2026-08-01T00:00:00.000Z", Path: "journal.jsonl",
			}}, nil
		},
	}, "first")
	if err != nil {
		authority.Release()
		t.Fatal(err)
	}
	for _, event := range firstEvents {
		appendEvent(event.Type, event.Payload)
	}
	return resumeCriterionFixtureState{store: store, driver: authority, runID: started.RunID, attemptID: attempt.AttemptID}
}

func handlerStoreWithSeeds(t *testing.T, selectAttempt bool, seed []runstate.MovementSeed) (*runstore.Store, *runstore.Driver) {
	return handlerStoreWithSeedsAndProbe(t, selectAttempt, seed, faultpoint.Nop{})
}

func handlerStoreWithSeedsAndProbe(
	t *testing.T,
	selectAttempt bool,
	seed []runstate.MovementSeed,
	probe faultpoint.Probe,
) (*runstore.Store, *runstore.Driver) {
	t.Helper()
	root := t.TempDir()
	store, err := runstore.New(root, probe)
	if err != nil {
		t.Fatal(err)
	}
	appendHandlerEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventRunStarted, Payload: handlerPayload(t, map[string]any{
		"base_commit": "base", "base_tree": "tree", "score_hash": "sha256:score", "score_file_hash": "sha256:file", "resolved_cast_hash": "sha256:cast", "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
	})})
	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		appendHandlerEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: eventType, Payload: handlerPayload(t, map[string]any{})})
	}
	if selectAttempt {
		appendHandlerEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: handlerPayload(t, map[string]any{"reason": "initial", "performer_id": "writer", "adapter_id": "adapter", "model": "model"})})
	}
	driver, err := store.AcquireDriver("run-1", seed)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Release() })
	return store, driver
}

type recoveryPointProbe struct {
	points []faultpoint.PointID
}

func (probe *recoveryPointProbe) Reached(point faultpoint.PointID) {
	probe.points = append(probe.points, point)
}

func (probe *recoveryPointProbe) movementFailurePoints() []faultpoint.PointID {
	var points []faultpoint.PointID
	for _, point := range probe.points {
		switch point {
		case faultpoint.PointLifecycleMovementFailed, faultpoint.PointLifecycleRunFailed:
			points = append(points, point)
		}
	}
	return points
}

func appendHandlerEvent(t *testing.T, store *runstore.Store, event runstate.Event) {
	t.Helper()
	err := store.Mutate(event.RunID, "", func(transaction *runstore.Txn) error {
		_, err := transaction.At("test").Append(event)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func handlerPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertLastEventType(t *testing.T, store *runstore.Store, want runstate.EventType) {
	t.Helper()
	journal, err := store.ReadJournal("run-1")
	if err != nil || len(journal.Events) == 0 || journal.Events[len(journal.Events)-1].Type != want {
		t.Fatalf("last event=%+v error=%v", journal.Events, err)
	}
}

func TestExecutorExecutesEachReachableStep(t *testing.T) {
	steps := []recovery.ActionStep{
		recovery.StepStabilizeHandoff,
		recovery.StepSweepRecordedSession,
		recovery.StepCloseAdapterInterval,
		recovery.StepClassifyAndAppendFailure,
		recovery.StepSweepCriterionSession,
		recovery.StepVerifyAcceptanceSubject,
		recovery.StepClassifyAcceptanceFailure,
		recovery.StepAppendAttemptCompleted,
		recovery.StepAppendMovementSucceeded,
		recovery.StepAppendMovementBudgetFailure,
		recovery.StepAppendRunFailed,
	}
	for _, step := range steps {
		t.Run(string(step), func(t *testing.T) {
			calls := 0
			executor := testExecutor(map[recovery.ActionStep]StepHandler{
				step: func(context.Context, HandlerContext, recovery.Action) error {
					calls++
					return nil
				},
			})
			result, err := executor.execute(context.Background(), recovery.Input{}, stepDecision(false, step))
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !slices.Equal(result.Steps, []recovery.ActionStep{step}) {
				t.Fatalf("calls=%d steps=%v", calls, result.Steps)
			}
		})
	}
}

func TestExecutorStopsAtEachReachableStepFailure(t *testing.T) {
	steps := []recovery.ActionStep{
		recovery.StepStabilizeHandoff,
		recovery.StepSweepRecordedSession,
		recovery.StepCloseAdapterInterval,
		recovery.StepClassifyAndAppendFailure,
		recovery.StepSweepCriterionSession,
		recovery.StepVerifyAcceptanceSubject,
		recovery.StepClassifyAcceptanceFailure,
		recovery.StepAppendAttemptCompleted,
		recovery.StepAppendMovementSucceeded,
		recovery.StepAppendMovementBudgetFailure,
		recovery.StepAppendRunFailed,
	}
	for _, step := range steps {
		t.Run(string(step), func(t *testing.T) {
			broken := errors.New("broken " + string(step))
			calledAfterFailure := false
			executor := testExecutor(map[recovery.ActionStep]StepHandler{
				step: func(context.Context, HandlerContext, recovery.Action) error { return broken },
				"later": func(context.Context, HandlerContext, recovery.Action) error {
					calledAfterFailure = true
					return nil
				},
			})
			_, err := executor.execute(context.Background(), recovery.Input{}, stepDecision(false, step, "later"))
			if !errors.Is(err, broken) || calledAfterFailure {
				t.Fatalf("error=%v calledAfterFailure=%v", err, calledAfterFailure)
			}
		})
	}
}

func TestExecutorHaltDoesNotWriteJournal(t *testing.T) {
	root := t.TempDir()
	journal := filepath.Join(root, "journal.jsonl")
	before := []byte("durable-prefix\n")
	if err := os.WriteFile(journal, before, 0o600); err != nil {
		t.Fatal(err)
	}
	input := recovery.Input{Projection: recovery.Projection{State: runningState()}, Observations: recovery.Observations{RootSnapshotDivergence: true}}
	executor := &Executor{Load: func(context.Context) (recovery.Input, error) { return input, nil }}
	result, err := executor.Execute(context.Background())
	if err != nil || result.Decision.Halt != recovery.HaltRootSnapshotDivergence {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	after, err := os.ReadFile(journal)
	if err != nil || string(after) != string(before) {
		t.Fatalf("journal=%q error=%v", after, err)
	}
}

func TestExecutorReplansOnlyWhenPlannerRequestsIt(t *testing.T) {
	first := recovery.Input{Projection: recovery.Projection{
		State: runningState(),
		CurrentHeadAttempt: &recovery.AttemptRecovery{
			AttemptID: "attempt", MovementID: "move", ScoreRevision: 1, State: runstate.AttemptStarting,
		},
	}}
	second := first
	second.Observations.RootSnapshotDivergence = true
	loads := 0
	handoffCalls := 0
	executor := testExecutor(map[recovery.ActionStep]StepHandler{
		recovery.StepStabilizeHandoff:         func(context.Context, HandlerContext, recovery.Action) error { handoffCalls++; return nil },
		recovery.StepCloseAdapterInterval:     func(context.Context, HandlerContext, recovery.Action) error { return nil },
		recovery.StepClassifyAndAppendFailure: func(context.Context, HandlerContext, recovery.Action) error { return nil },
	})
	executor.Load = func(context.Context) (recovery.Input, error) {
		loads++
		if loads == 1 {
			return first, nil
		}
		return second, nil
	}
	result, err := executor.Execute(context.Background())
	if err != nil || loads != 4 || handoffCalls != 1 || result.Replans != 1 || result.Decision.Halt != recovery.HaltRootSnapshotDivergence {
		t.Fatalf("result=%+v loads=%d handoffCalls=%d error=%v", result, loads, handoffCalls, err)
	}
}

func TestExecutorContinuationUsesFreshInputAfterEffect(t *testing.T) {
	first := recovery.Input{Projection: recovery.Projection{
		State:     runningState(),
		Scheduler: recovery.Scheduler{RemainingTime: 1, Movements: []recovery.ScheduledMovement{{ID: "move"}}},
	}}
	second := first
	second.Projection.Scheduler.RemainingTime = 0

	stop := errors.New("observed fresh continuation input")
	loads := 0
	var observed recovery.Input
	executor := testExecutor(map[recovery.ActionStep]StepHandler{
		"initial": func(context.Context, HandlerContext, recovery.Action) error { return nil },
		recovery.StepAppendMovementBudgetFailure: func(_ context.Context, execution HandlerContext, _ recovery.Action) error {
			observed = execution.Input
			return stop
		},
	})
	executor.Load = func(context.Context) (recovery.Input, error) {
		loads++
		return second, nil
	}

	_, err := executor.execute(context.Background(), first, recovery.Decision{
		CaseID: "RC-test",
		Action: &recovery.Action{Kind: "test-step-action", Steps: []recovery.ActionStep{"initial"}, Continuation: recovery.ContinuationC4},
	})
	if !errors.Is(err, stop) || loads != 1 {
		t.Fatalf("error=%v loads=%d", err, loads)
	}
	if observed.Projection.Scheduler.RemainingTime != 0 {
		t.Fatalf("handler input remaining time = %d, want 0 from fresh continuation input", observed.Projection.Scheduler.RemainingTime)
	}
}

func TestExecutorReplanUsesFreshInputWithoutRefreshStep(t *testing.T) {
	first := recovery.Input{Projection: recovery.Projection{
		State:     runningState(),
		Scheduler: recovery.Scheduler{RemainingTime: 1, Movements: []recovery.ScheduledMovement{{ID: "move"}}},
	}}
	second := first
	second.Projection.Scheduler.RemainingTime = 0

	stop := errors.New("observed fresh replan input")
	loads := 0
	var observed recovery.Input
	executor := testExecutor(map[recovery.ActionStep]StepHandler{
		"initial": func(context.Context, HandlerContext, recovery.Action) error { return nil },
		recovery.StepAppendMovementBudgetFailure: func(_ context.Context, execution HandlerContext, _ recovery.Action) error {
			observed = execution.Input
			return stop
		},
	})
	executor.Load = func(context.Context) (recovery.Input, error) {
		loads++
		return second, nil
	}

	_, err := executor.execute(context.Background(), first, stepDecision(true, "initial"))
	if !errors.Is(err, stop) || loads != 1 {
		t.Fatalf("error=%v loads=%d", err, loads)
	}
	if observed.Projection.Scheduler.RemainingTime != 0 {
		t.Fatalf("handler input remaining time = %d, want 0 from fresh replan input", observed.Projection.Scheduler.RemainingTime)
	}
}

func TestExecutorRejectsUnreachableAction(t *testing.T) {
	executor := testExecutor(nil)
	_, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{
		CaseID: "RC-test", Action: &recovery.Action{Kind: "not-implemented"},
	})
	if !errors.Is(err, ErrUnreachableAction) {
		t.Fatalf("error=%v", err)
	}
}

func TestExecutorRequiresAuthorityBeforeEffect(t *testing.T) {
	called := false
	executor := &Executor{
		Steps: map[recovery.ActionStep]StepHandler{
			recovery.StepVerifyAcceptanceSubject: func(context.Context, HandlerContext, recovery.Action) error { called = true; return nil },
		},
	}
	_, err := executor.execute(context.Background(), recovery.Input{}, stepDecision(false, recovery.StepVerifyAcceptanceSubject))
	if !errors.Is(err, ErrAuthorityRequired) || called {
		t.Fatalf("error=%v called=%v", err, called)
	}
}

func TestExecutorRejectsNamedUnimplementedActionsBeforeAuthority(t *testing.T) {
	for action, unit := range namedUnimplementedActionOwners {
		t.Run(string(action), func(t *testing.T) {
			store := acquirableRecoveryStore(t)
			journalPath := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "journal.jsonl")
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}

			executor := &Executor{
				Store: store,
				RunID: "run-1",
				Load: func(context.Context) (recovery.Input, error) {
					return recovery.Input{}, nil
				},
			}
			_, err = executor.execute(context.Background(), recovery.Input{}, recovery.Decision{
				CaseID: "RC-test",
				Action: &recovery.Action{Kind: action},
			})
			if !errors.Is(err, ErrUnreachableAction) || !strings.Contains(err.Error(), "unit "+unit) {
				t.Fatalf("error=%v", err)
			}
			if executor.Driver != nil {
				t.Fatal("named refusal acquired a driver")
			}
			if _, present, leaseErr := store.ReadLease("run-1"); leaseErr != nil || present {
				t.Fatalf("lease present=%t error=%v", present, leaseErr)
			}

			after, err := os.ReadFile(journalPath)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatalf("journal changed=%t error=%v", !bytes.Equal(after, before), err)
			}
		})
	}
}

func TestExecutorExecutesCancellationOracle(t *testing.T) {
	t.Run("closes an open interval without a surviving lease", func(t *testing.T) {
		store := acquirableRecoveryStore(t)
		driver, err := store.AcquireRecoveryDriver("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCancelRequested, Payload: handlerPayload(t, map[string]any{"requested_by": "cli"})}, "test.cancel.requested"); err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{
			"interval_id": "unfenced-interval", "phase": "composition", "wall_start": time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "remaining_at_start": 1000,
		})}, "test.cancel.execution.started"); err != nil {
			t.Fatal(err)
		}
		lease, present, err := store.ReadLease("run-1")
		if err != nil || !present {
			t.Fatalf("lease=%+v present=%t error=%v", lease, present, err)
		}
		if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
			_, err := transaction.At("test.remove_lease").CompareRemoveLease(lease.Identity())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := (&Executor{Store: store, RunID: "run-1"}).execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: recovery.CaseCancellation, Action: &recovery.Action{Kind: recovery.ActionExecuteCancellation}}); err != nil {
			t.Fatal(err)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		var cancelled map[string]any
		if err := json.Unmarshal(journal.Events[len(journal.Events)-1].Payload, &cancelled); err != nil {
			t.Fatal(err)
		}
		if journal.Events[len(journal.Events)-2].Type != runstate.EventExecutionStopped || cancelled["fenced_epoch"] != nil {
			t.Fatalf("events=%s,%s payload=%v", journal.Events[len(journal.Events)-2].Type, journal.Events[len(journal.Events)-1].Type, cancelled)
		}
	})

	t.Run("fences a surviving lease even when its owner is verifiably gone", func(t *testing.T) {
		store := acquirableRecoveryStore(t)
		driver, err := store.AcquireRecoveryDriver("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCancelRequested, Payload: handlerPayload(t, map[string]any{"requested_by": "cli"})}, "test.cancel.requested"); err != nil {
			t.Fatal(err)
		}
		lease, present, err := store.ReadLease("run-1")
		if err != nil || !present {
			t.Fatalf("lease=%+v present=%t error=%v", lease, present, err)
		}
		gone := lease
		gone.Token = "gone-owner"
		gone.PID = 999999
		if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
			if _, err := transaction.At("test.remove_live_lease").CompareRemoveLease(lease.Identity()); err != nil {
				return err
			}
			_, err := transaction.At("test.install_gone_lease").CreateLease(true, gone)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := (&Executor{Store: store, RunID: "run-1"}).execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: recovery.CaseCancellation, Action: &recovery.Action{Kind: recovery.ActionExecuteCancellation}}); err != nil {
			t.Fatal(err)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		var cancelled map[string]any
		if err := json.Unmarshal(journal.Events[len(journal.Events)-1].Payload, &cancelled); err != nil {
			t.Fatal(err)
		}
		if cancelled["fenced_epoch"] != float64(2) {
			t.Fatalf("cancelled payload=%v", cancelled)
		}
	})

	t.Run("closes then fences then terminalizes then removes matching lease", func(t *testing.T) {
		store := acquirableRecoveryStore(t)
		driver, err := store.AcquireRecoveryDriver("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCancelRequested, Payload: handlerPayload(t, map[string]any{"requested_by": "cli"})}, "test.cancel.requested"); err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{
			"interval_id": "cancel-interval", "phase": "composition", "wall_start": time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "remaining_at_start": 1000,
		})}, "test.cancel.execution.started"); err != nil {
			t.Fatal(err)
		}

		executor := &Executor{Store: store, RunID: "run-1"}
		result, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: recovery.CaseCancellation, Action: &recovery.Action{Kind: recovery.ActionExecuteCancellation}})
		if err != nil || !slices.Equal(result.Kinds, []recovery.ActionKind{recovery.ActionExecuteCancellation}) || result.Outcome != OutcomeCancelled {
			t.Fatalf("result=%+v error=%v", result, err)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if got := []runstate.EventType{journal.Events[len(journal.Events)-2].Type, journal.Events[len(journal.Events)-1].Type}; !slices.Equal(got, []runstate.EventType{runstate.EventExecutionStopped, runstate.EventRunCancelled}) {
			t.Fatalf("terminal event order=%v", got)
		}
		var stopped, cancelled map[string]any
		if err := json.Unmarshal(journal.Events[len(journal.Events)-2].Payload, &stopped); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(journal.Events[len(journal.Events)-1].Payload, &cancelled); err != nil {
			t.Fatal(err)
		}
		if stopped["reason"] != "cancelled" || stopped["charging"] != "clamped" || cancelled["fenced_epoch"] != float64(2) {
			t.Fatalf("stopped=%v cancelled=%v", stopped, cancelled)
		}
		if _, present, err := store.ReadLease("run-1"); err != nil || present {
			t.Fatalf("lease present=%t error=%v", present, err)
		}
		state, err := store.LoadRunInput("run-1")
		if err != nil || state.Projection.State.Run != runstate.RunCancelled || state.Projection.State.OpenExecution != nil || state.Projection.State.Authority.Epoch != 2 {
			t.Fatalf("state=%+v error=%v", state.Projection.State, err)
		}
	})

	t.Run("abandons a pending prepare before terminalization", func(t *testing.T) {
		store, driver := preparedCancellationStore(t)
		executor := &Executor{Store: store, RunID: "run-1"}
		if _, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: recovery.CaseCancellation, Action: &recovery.Action{Kind: recovery.ActionExecuteCancellation}}); err != nil {
			t.Fatal(err)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		got := []runstate.EventType{journal.Events[len(journal.Events)-2].Type, journal.Events[len(journal.Events)-1].Type}
		if !slices.Equal(got, []runstate.EventType{runstate.EventAmendmentApprovalAbandoned, runstate.EventRunCancelled}) {
			t.Fatalf("terminal event order=%v", got)
		}
		var abandoned map[string]any
		if err := json.Unmarshal(journal.Events[len(journal.Events)-2].Payload, &abandoned); err != nil {
			t.Fatal(err)
		}
		if abandoned["reason"] != "cancelled" {
			t.Fatalf("abandoned payload=%v", abandoned)
		}
		runRoot := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1")
		for _, path := range []string{"scores/revision-2.yaml", "prepares/prepare-1.json"} {
			if _, err := os.Stat(filepath.Join(runRoot, path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s still present or unreadable: %v", path, err)
			}
		}
		if _, err := os.Stat(filepath.Join(runRoot, "quarantine", "cancelled_prepare", strings.TrimPrefix(preparedSnapshotHash(t), "sha256:"), "revision-2.yaml")); err != nil {
			t.Fatal(err)
		}
		state, err := store.LoadRunInput("run-1")
		if err != nil || state.Projection.State.PendingPrepare != nil || state.Projection.State.Run != runstate.RunCancelled {
			t.Fatalf("state=%+v error=%v", state.Projection.State, err)
		}
		_ = driver
	})
}

func TestCancellationAdapterSweepFailureHaltsBeforeDurableOracle(t *testing.T) {
	store, driver := cancellationHandlerStore(t)
	advanceHandlerAcceptanceWithProcesses(t, driver, false, unverifiableCancellationProcess(t), nil)
	appendCancellationRequest(t, driver)

	result, err := (&Executor{Store: store, RunID: "run-1"}).execute(context.Background(), recovery.Input{}, recovery.Decision{
		CaseID: recovery.CaseCancellation,
		Action: &recovery.Action{Kind: recovery.ActionExecuteCancellation},
	})
	if err != nil || result.Outcome != OutcomeHalted || result.Decision.Halt != recovery.HaltSweepUnverifiable {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	assertCancellationRemainsNonterminal(t, store)
}

func TestCancellationCriterionSweepFailureHaltsBeforeDurableOracle(t *testing.T) {
	store, driver := cancellationHandlerStore(t)
	advanceHandlerAcceptanceWithProcesses(t, driver, true, sweptEmptyCancellationProcess(t), unverifiableCancellationProcess(t))
	appendCancellationRequest(t, driver)

	result, err := (&Executor{Store: store, RunID: "run-1"}).execute(context.Background(), recovery.Input{}, recovery.Decision{
		CaseID: recovery.CaseCancellation,
		Action: &recovery.Action{Kind: recovery.ActionExecuteCancellation},
	})
	if err != nil || result.Outcome != OutcomeHalted || result.Decision.Halt != recovery.HaltSweepUnverifiable {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	assertCancellationRemainsNonterminal(t, store)
}

func TestCancellationSweepFailureRoutesToAppendixDHalt(t *testing.T) {
	store, driver := cancellationHandlerStore(t)
	advanceHandlerAcceptanceWithProcesses(t, driver, false, unverifiableCancellationProcess(t), nil)
	appendCancellationRequest(t, driver)

	result, err := (&Executor{Store: store, RunID: "run-1"}).execute(context.Background(), recovery.Input{}, recovery.Decision{
		CaseID: recovery.CaseCancellation,
		Action: &recovery.Action{Kind: recovery.ActionExecuteCancellation},
	})
	if err != nil || result.Outcome != OutcomeHalted || result.Decision.Halt != recovery.HaltSweepUnverifiable {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestCancellationOracleRequiresRequestBeforeSweep(t *testing.T) {
	store, driver := cancellationHandlerStore(t)
	advanceHandlerAcceptanceWithProcesses(t, driver, false, unverifiableCancellationProcess(t), nil)

	err := cancellation.Execute(context.Background(), store, "run-1")
	if !errors.Is(err, runstore.ErrLeaseConflict) {
		t.Fatalf("error=%v, want cancellation request required before sweep", err)
	}
	assertCancellationRemainsNonterminal(t, store)
}

func TestCancellationDoesNotFenceWithoutLease(t *testing.T) {
	store := acquirableRecoveryStore(t)
	if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		_, err := transaction.At("test.cancel.requested").Append(runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCancelRequested,
			Payload: handlerPayload(t, map[string]any{"requested_by": "cli"}),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := cancellation.Execute(context.Background(), store, "run-1"); err != nil {
		t.Fatal(err)
	}

	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	var cancelled map[string]any
	if err := json.Unmarshal(journal.Events[len(journal.Events)-1].Payload, &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled["fenced_epoch"] != nil {
		t.Fatalf("cancelled payload=%v, want no fence without a lease", cancelled)
	}
}

func TestCancellationDoesNotFenceStaleLease(t *testing.T) {
	store := acquirableRecoveryStore(t)
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	appendCancellationRequest(t, driver)
	lease, present, err := store.ReadLease("run-1")
	if err != nil || !present {
		t.Fatalf("lease=%+v present=%t error=%v", lease, present, err)
	}
	stale := lease
	stale.Epoch++
	if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		if _, err := transaction.At("test.remove_current_lease").CompareRemoveLease(lease.Identity()); err != nil {
			return err
		}
		_, err := transaction.At("test.install_stale_lease").CreateLease(true, stale)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := cancellation.Execute(context.Background(), store, "run-1"); err != nil {
		t.Fatal(err)
	}

	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	var cancelled map[string]any
	if err := json.Unmarshal(journal.Events[len(journal.Events)-1].Payload, &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled["fenced_epoch"] != nil {
		t.Fatalf("cancelled payload=%v, want no fence for stale lease", cancelled)
	}
	if remaining, present, err := store.ReadLease("run-1"); err != nil || !present || remaining.Epoch != stale.Epoch {
		t.Fatalf("lease=%+v present=%t error=%v, want stale lease retained", remaining, present, err)
	}
}

func TestExecuteCancellationRejectsIncompleteHandlerContext(t *testing.T) {
	action := recovery.Action{Kind: recovery.ActionExecuteCancellation}
	for _, test := range []struct {
		name    string
		context HandlerContext
	}{
		{name: "missing store", context: HandlerContext{RunID: "run-1"}},
		{name: "missing run id", context: HandlerContext{Store: acquirableRecoveryStore(t)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := executeCancellation(context.Background(), test.context, action)
			if err == nil || err.Error() != "recovery executor requires store and run id for cancellation" {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func cancellationHandlerStore(t *testing.T) (*runstore.Store, *runstore.Driver) {
	t.Helper()
	store := acquirableRecoveryStore(t)
	for _, event := range []runstate.Event{
		{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventMovementReady, Payload: handlerPayload(t, map[string]any{})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventMovementStarted, Payload: handlerPayload(t, map[string]any{})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: handlerPayload(t, map[string]any{"reason": "initial", "performer_id": "writer", "adapter_id": "adapter", "model": "model"})},
	} {
		appendHandlerEvent(t, store, event)
	}
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Release() })
	return store, driver
}

func preparedCancellationStore(t *testing.T) (*runstore.Store, *runstore.Driver) {
	t.Helper()
	store := acquirableRecoveryStore(t)
	runRoot := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1")
	firstSnapshot, err := os.ReadFile(filepath.Join(runRoot, "scores", "revision-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	secondSnapshot := []byte(strings.Replace(string(firstSnapshot), "revision: 1", "revision: 2", 1))
	secondScore, diagnostics := score.Compile(secondSnapshot)
	if len(diagnostics) != 0 {
		t.Fatalf("score diagnostics=%v", diagnostics)
	}
	secondHash, err := secondScore.Hash()
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	baseHash := current.Projection.State.ScoreHead.SemanticHash
	plan := handlerPayload(t, map[string]any{
		"proposal_id": "proposal-1", "base_revision": 1, "base_hash": baseHash,
		"new_revision": 2, "new_snapshot_hash": secondHash, "new_snapshot_file_hash": hashFixture(secondSnapshot),
		"superseded_attempt_ids": []any{}, "mode": "auto", "envelope_class": "NARROW_PATHS",
	})
	if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		if _, err := transaction.At("test.prepare.snapshot").PublishImmutable("scores/revision-2.yaml", secondSnapshot, runstore.Hash(hashFixture(secondSnapshot))); err != nil {
			return err
		}
		_, err := transaction.At("test.prepare.plan").PublishImmutable("prepares/prepare-1.json", plan, runstore.Hash(hashFixture(plan)))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	prepared := runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventAmendmentApprovalPrepared, Payload: handlerPayload(t, map[string]any{
		"prepare_id": "prepare-1", "proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS",
		"base_revision": 1, "base_hash": baseHash, "new_revision": 2, "new_snapshot_hash": secondHash,
		"new_snapshot_file_hash": hashFixture(secondSnapshot), "plan_record_hash": hashFixture(plan), "target_attempt_ids": []any{},
		"observed_authority_epoch": 1, "quiesce_deadline": "2026-07-30T00:00:00.000Z", "classifier_version": 1,
		"identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
	})}
	if _, err := driver.Append(prepared, "test.prepare"); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCancelRequested, Payload: handlerPayload(t, map[string]any{"requested_by": "cli"})}, "test.cancel.requested"); err != nil {
		t.Fatal(err)
	}
	return store, driver
}

func preparedSnapshotHash(t *testing.T) string {
	t.Helper()
	// The fixture's revision-2 snapshot differs from revision 1 only by revision.
	store := acquirableRecoveryStore(t)
	runRoot := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1")
	first, err := os.ReadFile(filepath.Join(runRoot, "scores", "revision-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return hashFixture([]byte(strings.Replace(string(first), "revision: 1", "revision: 2", 1)))
}

func TestReclaimAuthorityIsTheOnlyAuthorityBoundary(t *testing.T) {
	for _, action := range []recovery.ActionKind{
		recovery.ActionTerminalCleanup,
		recovery.ActionRemoveStaleLease,
		recovery.ActionQuarantineOrphanLease,
		recovery.ActionRefuseResume,
		recovery.ActionReturnWaitingHuman,
		recovery.ActionExecuteCancellation,
	} {
		if actionRequiresDriver(recovery.Action{Kind: action}) {
			t.Fatalf("%s unexpectedly requires authority", action)
		}
	}
	if !actionRequiresDriver(recovery.Action{Kind: recovery.ActionReclaimAuthority}) {
		t.Fatal("reclaim_authority must establish authority")
	}
}

func acquirableRecoveryStore(t *testing.T) *runstore.Store {
	t.Helper()
	store, err := runstore.New(t.TempDir(), faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := []byte("score: \"0.2\"\nname: executor-fixture\nrevision: 1\nstatus: finalized\ngoal: fixture\nverification:\n  expectation:\n    intent: pass-existing-tests\n    apply_gate:\n      waived: true\n      reason: fixture\nparts:\n  writer:\n    capabilities: [repo_read]\n    read_only: true\nmovements:\n  - id: write\n    part: writer\n    grants: [repo_read]\n    instruction: inspect\npolicy:\n  allowed_paths: [\"**\"]\n  budget:\n    active_wall_clock_min: 10\n")
	compiled, diagnostics := score.Compile(snapshot)
	if len(diagnostics) != 0 {
		t.Fatalf("score diagnostics=%v", diagnostics)
	}
	scoreHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	resolvedCast := []byte("cast: \"0.1\"\nperformers: {}\nbindings: {}\n")
	resolved, castDiagnostics := cast.Resolve([]cast.Layer{{Origin: "fixture", Data: resolvedCast}})
	if len(castDiagnostics) != 0 {
		t.Fatalf("cast diagnostics=%v", castDiagnostics)
	}
	castHash, err := resolved.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		if _, err := tx.At("fixture.score").PublishImmutable("scores/revision-1.yaml", snapshot, runstore.Hash(hashFixture(snapshot))); err != nil {
			return err
		}
		if _, err := tx.At("fixture.cast").PublishImmutable("resolved-cast.yaml", resolvedCast, runstore.Hash(hashFixture(resolvedCast))); err != nil {
			return err
		}
		_, err := tx.At("fixture.start").Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventRunStarted, Payload: handlerPayload(t, map[string]any{
			"base_commit": "base", "base_tree": "tree", "score_hash": scoreHash, "score_file_hash": hashFixture(snapshot), "resolved_cast_hash": castHash, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
		})})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func hashFixture(value []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(value))
}

func testExecutor(steps map[recovery.ActionStep]StepHandler) *Executor {
	return &Executor{
		Driver: &runstore.Driver{},
		Steps:  steps,
		authorize: func(*runstore.Driver) error {
			return nil
		},
	}
}

func stepDecision(replan bool, steps ...recovery.ActionStep) recovery.Decision {
	return recovery.Decision{
		CaseID: "RC-test",
		Action: &recovery.Action{Kind: "test-step-action", Replan: replan, Steps: steps},
	}
}

func runningState() runstate.State {
	state := runstate.NewState([]runstate.MovementSeed{{ID: "move", Initial: runstate.MovementPending}})
	state.Run = runstate.RunRunning
	state.ScoreHead.Revision = 1
	state.Movements["move"] = runstate.MovementRunning
	return state
}
