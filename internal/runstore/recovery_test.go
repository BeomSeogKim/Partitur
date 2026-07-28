package runstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestReclaimDeadRecoveryDriverAllowsOnlyOneConcurrentWinner(t *testing.T) {
	store := recoveryStore(t)
	dead := appendDeadRecoveryLease(t, store)

	start := make(chan struct{})
	results := make(chan error, 2)
	var drivers [2]*Driver
	var group sync.WaitGroup
	for index := range drivers {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			driver, err := store.ReclaimDeadRecoveryDriver("run-1", dead.Identity())
			drivers[index] = driver
			results <- err
		}(index)
	}
	close(start)
	group.Wait()
	close(results)

	winners := 0
	for err := range results {
		if err == nil {
			winners++
			continue
		}
		if !errors.Is(err, ErrLeaseConflict) {
			t.Fatalf("losing concurrent reclaim error = %v, want lease conflict", err)
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent reclaim winners = %d, want 1", winners)
	}
	var winner *Driver
	for _, driver := range drivers {
		if driver != nil {
			winner = driver
		}
	}
	state, err := winner.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Authority.Epoch != 2 {
		t.Fatalf("authority epoch = %d, want 2", state.Authority.Epoch)
	}
}

func TestReclaimDeadRecoveryDriverKeepsReusedLivePIDLease(t *testing.T) {
	store := recoveryStore(t)
	start, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	dead := Lease{Epoch: 1, Token: "dead-token", PID: os.Getpid(), Start: distinctStartIdentity(t, start)}
	appendRecoveryAuthorityAndLease(t, store, dead)
	reused := Lease{Epoch: 1, Token: "reused-token", PID: os.Getpid(), Start: start}
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		if _, err := transaction.At("test.replace_dead_lease").CompareRemoveLease(dead.Identity()); err != nil {
			return err
		}
		_, err := transaction.At("test.replace_reused_lease").CreateLease(true, reused)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	_, err = store.ReclaimDeadRecoveryDriver("run-1", dead.Identity())
	if !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("reclaim error = %v, want lease conflict", err)
	}
	remaining, present, err := store.ReadLease("run-1")
	if err != nil || !present || !leaseMatches(remaining, reused.Identity()) {
		t.Fatalf("remaining lease = %+v present=%t error=%v, want reused live lease", remaining, present, err)
	}
}

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
				read := input.Projection.Scheduler.Movements[1]
				if read.ID != "read" || len(read.Needs) != 1 || read.Needs[0] != "write" {
					t.Fatalf("recovery scheduler dependency = %+v", read)
				}
			},
		},
		{
			name: "failed attempt retains recorded disposition and classification facts", append: appendFailedAttempt, check: func(t *testing.T, input RecoveryInput) {
				attempt := input.Projection.CurrentHeadAttempt
				if attempt == nil || attempt.State != runstate.AttemptFailed || attempt.RecordedDisposition == nil || attempt.RecordedDisposition.Charged != "quality_retry" {
					t.Fatalf("failed attempt projection = %+v", attempt)
				}
				facts := attempt.FailureClassification
				if facts.CurrentPerformer != "writer" || facts.RetriesConsumed != 1 || facts.RetriesPerMovement != 0 || facts.RemainingTimeMS != 600000 {
					t.Fatalf("failure classification facts = %+v", facts)
				}
				pending := input.Projection.Scheduler.PendingSuccessor
				if pending == nil || pending.AttemptID != attempt.AttemptID || pending.Performer != "writer" || pending.Reason != "quality_retry" {
					t.Fatalf("replay-derived pending successor = %+v", pending)
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

func TestLoadRecoveryInputDoesNotFallBackToRootScore(t *testing.T) {
	store := recoveryStore(t)
	root := filepath.Join(store.RepositoryRoot(), "partitur.yaml")
	if err := os.WriteFile(root, recoveryScoreJSON(t, 99, "valid root score"), 0o600); err != nil {
		t.Fatal(err)
	}
	runScorePath := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "scores", "revision-1.yaml")
	if err := os.WriteFile(runScorePath, recoveryScoreJSON(t, 1, "changed pinned score"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := store.LoadRecoveryInput("run-1")
	if err == nil || !strings.Contains(err.Error(), "pinned score file hash does not match journal") {
		t.Fatalf("LoadRecoveryInput() error = %v, want pinned score file hash mismatch", err)
	}
}

func TestLoadRecoveryInputIgnoresCurrentCastLayer(t *testing.T) {
	store := recoveryStore(t)
	currentCast := filepath.Join(store.RepositoryRoot(), ".partitur", "cast.yaml")
	if err := os.MkdirAll(filepath.Dir(currentCast), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentCast, []byte("poisoned current cast layer"), 0o600); err != nil {
		t.Fatal(err)
	}

	input, err := store.LoadRecoveryInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	wantCast, diagnostics := cast.Resolve([]cast.Layer{{Origin: "fixture", Data: recoveryCastJSON(t)}})
	if len(diagnostics) != 0 {
		t.Fatalf("fixture cast diagnostics = %v", diagnostics)
	}
	wantHash, err := wantCast.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if input.Cast == nil {
		t.Fatal("run-owned resolved cast was not loaded")
	}
	gotHash, err := input.Cast.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != wantHash {
		t.Fatalf("resolved cast hash = %q, want run-owned hash %q", gotHash, wantHash)
	}
}

func TestLoadRecoveryInputUsesOneJournalSnapshot(t *testing.T) {
	store := recoveryStore(t)
	appendRecoveryMovementStarted(t, store)
	journalPath := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "journal.jsonl")
	growth := runstate.Event{
		EventID:       "event-grown",
		Seq:           4,
		Timestamp:     "2026-07-28T00:00:00.000Z",
		RunID:         "run-1",
		ScoreRevision: 1,
		MovementID:    "write",
		AttemptID:     "attempt-1",
		Type:          runstate.EventPerformerSelected,
		Payload: recoveryPayload(t, map[string]any{
			"reason": "initial", "performer_id": "writer", "adapter_id": "adapter", "model": "model",
		}),
	}
	line, err := json.Marshal(growth)
	if err != nil {
		t.Fatal(err)
	}
	growing := &growingJournalFS{
		recordingFS: &recordingFS{delegate: realFS{}},
		journalPath: journalPath,
		appendLine:  append(line, '\n'),
	}
	store.fs = growing

	input, err := store.LoadRecoveryInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if !growing.grew {
		t.Fatal("test seam did not grow the journal")
	}
	if len(input.Projection.State.Attempts) != 0 || input.Projection.CurrentHeadAttempt != nil {
		t.Fatalf("projection mixed journal snapshots: state=%+v head=%+v", input.Projection.State.Attempts, input.Projection.CurrentHeadAttempt)
	}
}

type growingJournalFS struct {
	*recordingFS
	journalPath string
	appendLine  []byte
	grew        bool
}

func (filesystem *growingJournalFS) ReadFile(path string) ([]byte, error) {
	contents, err := filesystem.recordingFS.ReadFile(path)
	if err != nil || path != filesystem.journalPath || filesystem.grew {
		return contents, err
	}
	filesystem.grew = true
	if err := filesystem.recordingFS.Append(path, filesystem.appendLine, 0o600); err != nil {
		return nil, err
	}
	return contents, nil
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

func TestLoadRecoveryInputProjectsCompositionRecoveryFacts(t *testing.T) {
	t.Run("terminal evidence remains visible until its movement terminal", func(t *testing.T) {
		store := recoveryStore(t)
		appendRecoveryMovementStarted(t, store)
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventCompositionFailed,
			Payload: recoveryPayload(t, map[string]any{
				"scope": "movement", "target_id": "write", "composition_subject_hash": "sha256:subject",
				"cause": "git_exit", "git_exit_code": 2, "diagnostic": "exit 2", "contributors": []any{},
				"composition_algorithm_version": "1", "identity_versions": recoveryVersions(),
			}),
		})

		input, err := store.LoadRecoveryInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if got := input.Projection.CompositionTerminals; len(got) != 1 || got[0].Scope != "movement" || got[0].TargetID != "write" || got[0].Reason != "composition_failed" {
			t.Fatalf("composition terminals = %+v", got)
		}
	})

	t.Run("movement terminal suppresses matching composition evidence", func(t *testing.T) {
		store := recoveryStore(t)
		appendRecoveryMovementStarted(t, store)
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventCompositionFailed,
			Payload: recoveryPayload(t, map[string]any{
				"scope": "movement", "target_id": "write", "composition_subject_hash": "sha256:subject",
				"cause": "git_exit", "git_exit_code": 2, "diagnostic": "exit 2", "contributors": []any{},
				"composition_algorithm_version": "1", "identity_versions": recoveryVersions(),
			}),
		})
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventMovementFailed,
			Payload: recoveryPayload(t, map[string]any{"reason": "composition_failed", "run_failed": false}),
		})

		input, err := store.LoadRecoveryInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if got := input.Projection.CompositionTerminals; len(got) != 0 {
			t.Fatalf("suppressed movement composition terminals = %+v, want none", got)
		}
	})

	t.Run("run terminal suppresses matching candidate composition evidence", func(t *testing.T) {
		store := recoveryStore(t)
		appendRecoveryWriteSucceeded(t, store)
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCompositionFailed,
			Payload: recoveryPayload(t, map[string]any{
				"scope": "candidate", "target_id": "run-1", "composition_subject_hash": "sha256:subject",
				"cause": "git_exit", "git_exit_code": 2, "diagnostic": "exit 2", "contributors": []any{},
				"composition_algorithm_version": "1", "identity_versions": recoveryVersions(),
			}),
		})
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventRunFailed,
			Payload: recoveryPayload(t, map[string]any{"reason": "composition_failed"}),
		})

		input, err := store.LoadRecoveryInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if got := input.Projection.CompositionTerminals; len(got) != 0 {
			t.Fatalf("suppressed candidate composition terminals = %+v, want none", got)
		}
	})

	t.Run("recovered composition close restarts the interrupted movement", func(t *testing.T) {
		store := recoveryStore(t)
		appendRecoveryMovementStarted(t, store)
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted,
			Payload: recoveryPayload(t, map[string]any{
				"interval_id": "composition-1", "phase": "composition", "wall_start": "2026-07-28T00:00:00.000Z", "remaining_at_start": 600000,
			}),
		})
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStopped,
			Payload: recoveryPayload(t, map[string]any{
				"interval_id": "composition-1", "reason": "recovered", "charging": "clamped", "charged_duration": 600000, "observed_at": "2026-07-28T00:10:00.000Z",
			}),
		})

		input, err := store.LoadRecoveryInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		got := input.Projection.CompositionRecovery
		if got == nil || !got.Recovered || got.Scope != "movement" || got.MovementID != "write" {
			t.Fatalf("composition recovery = %+v", got)
		}
		if input.Projection.Scheduler.RemainingTime != 0 {
			t.Fatalf("remaining time after clamped close = %d, want 0", input.Projection.Scheduler.RemainingTime)
		}
	})

	t.Run("recovered composition close restarts eligible candidate composition", func(t *testing.T) {
		store := recoveryStore(t)
		appendRecoveryWriteSucceeded(t, store)
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted,
			Payload: recoveryPayload(t, map[string]any{
				"interval_id": "composition-1", "phase": "composition", "wall_start": "2026-07-28T00:00:00.000Z", "remaining_at_start": 600000,
			}),
		})
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStopped,
			Payload: recoveryPayload(t, map[string]any{
				"interval_id": "composition-1", "reason": "recovered", "charging": "clamped", "charged_duration": 1, "observed_at": "2026-07-28T00:00:00.001Z",
			}),
		})

		input, err := store.LoadRecoveryInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if got := input.Projection.CompositionRecovery; got == nil || !got.Recovered || got.Scope != "candidate" || got.MovementID != "" {
			t.Fatalf("candidate composition recovery = %+v", got)
		}
	})

	t.Run("ordinary composition close does not restart composition", func(t *testing.T) {
		store := recoveryStore(t)
		appendRecoveryWriteSucceeded(t, store)
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted,
			Payload: recoveryPayload(t, map[string]any{
				"interval_id": "composition-1", "phase": "composition", "wall_start": "2026-07-28T00:00:00.000Z", "remaining_at_start": 600000,
			}),
		})
		appendRecoveryEvent(t, store, runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStopped,
			Payload: recoveryPayload(t, map[string]any{
				"interval_id": "composition-1", "reason": "normal", "charging": "measured", "charged_duration": 1,
			}),
		})

		input, err := store.LoadRecoveryInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if got := input.Projection.CompositionRecovery; got != nil {
			t.Fatalf("ordinary composition close recovery = %+v, want none", got)
		}
	})
}

func TestLoadRecoveryInputProjectsRevisionRestart(t *testing.T) {
	store := recoveryStore(t)
	appendAttemptToRunning(t, store)

	baseScore, diagnostics := score.Compile(recoveryScoreJSON(t, 1, "pinned recovery fixture"))
	if len(diagnostics) != 0 {
		t.Fatalf("base score diagnostics = %v", diagnostics)
	}
	baseHash, err := baseScore.Hash()
	if err != nil {
		t.Fatal(err)
	}
	updatedScore := recoveryScoreJSON(t, 2, "approved revision")
	updated, diagnostics := score.Compile(updatedScore)
	if len(diagnostics) != 0 {
		t.Fatalf("updated score diagnostics = %v", diagnostics)
	}
	updatedHash, err := updated.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		_, err := transaction.At("test/score-2").PublishImmutable("scores/revision-2.yaml", updatedScore, Hash(rawHash(updatedScore)))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	appendRecoveryEvent(t, store, runstate.Event{
		RunID: "run-1", ScoreRevision: 1, Type: runstate.EventAmendmentApprovalPrepared,
		Payload: recoveryPayload(t, map[string]any{
			"prepare_id": "prepare-1", "proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS",
			"base_revision": 1, "base_hash": baseHash, "new_revision": 2, "new_snapshot_hash": updatedHash,
			"new_snapshot_file_hash": rawHash(updatedScore), "plan_record_hash": "sha256:plan", "target_attempt_ids": []any{"attempt-1"},
			"observed_authority_epoch": 0, "quiesce_deadline": "2026-07-28T00:00:00.000Z", "classifier_version": 1, "identity_versions": recoveryVersions(),
		}),
	})
	appendRecoveryEvent(t, store, runstate.Event{
		RunID: "run-1", ScoreRevision: 2, Type: runstate.EventAmendmentApproved,
		Payload: recoveryPayload(t, map[string]any{
			"proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS", "base_revision": 1, "base_hash": baseHash,
			"classifier_version": 1, "new_revision": 2, "new_snapshot_hash": updatedHash, "new_snapshot_file_hash": rawHash(updatedScore),
			"typed_delta": []any{}, "actual_impact": recoveryActualImpact(), "superseded_attempt_ids": []any{"attempt-1"},
			"obsoleted_decision_ids": []any{}, "finalization": false, "identity_versions": recoveryVersions(),
		}),
	})

	input, err := store.LoadRecoveryInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if input.Score.Revision() != 2 {
		t.Fatalf("current pinned score revision = %d, want 2", input.Score.Revision())
	}
	if got := input.Projection.RevisionRestarts; len(got) != 1 || got[0].MovementID != "write" {
		t.Fatalf("revision restarts = %+v", got)
	}
}

func TestRevisionRestartExclusions(t *testing.T) {
	t.Run("attempt already selected on the approved revision", func(t *testing.T) {
		state := runstate.NewState([]runstate.MovementSeed{{ID: "write", Initial: runstate.MovementPending}})
		state.ScoreHead.Revision = 2
		events := []runstate.Event{
			{ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: recoveryPayload(t, map[string]any{})},
			{ScoreRevision: 2, Type: runstate.EventAmendmentApproved, Payload: recoveryPayload(t, map[string]any{
				"new_revision": 2, "finalization": false, "superseded_attempt_ids": []any{"attempt-1"},
			})},
			{ScoreRevision: 2, MovementID: "write", AttemptID: "attempt-2", Type: runstate.EventPerformerSelected, Payload: recoveryPayload(t, map[string]any{})},
		}
		if got := replayFacts(events).revisionRestarts(state); len(got) != 0 {
			t.Fatalf("restart after revision-2 selection = %+v, want none", got)
		}
	})

	t.Run("finalization approval", func(t *testing.T) {
		state := runstate.NewState([]runstate.MovementSeed{{ID: "write", Initial: runstate.MovementPending}})
		state.ScoreHead.Revision = 2
		events := []runstate.Event{
			{ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: recoveryPayload(t, map[string]any{})},
			{ScoreRevision: 2, Type: runstate.EventAmendmentApproved, Payload: recoveryPayload(t, map[string]any{
				"new_revision": 2, "finalization": true, "superseded_attempt_ids": []any{"attempt-1"},
			})},
		}
		if got := replayFacts(events).revisionRestarts(state); len(got) != 0 {
			t.Fatalf("restart after finalization approval = %+v, want none", got)
		}
	})
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

func appendDeadRecoveryLease(t *testing.T, store *Store) Lease {
	t.Helper()
	start, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	lease := Lease{Epoch: 1, Token: "dead-token", PID: os.Getpid(), Start: distinctStartIdentity(t, start)}
	appendRecoveryAuthorityAndLease(t, store, lease)
	return lease
}

func appendRecoveryAuthorityAndLease(t *testing.T, store *Store, lease Lease) {
	t.Helper()
	payload := recoveryPayload(t, map[string]any{
		"authority_epoch":      lease.Epoch,
		"owner_pid":            lease.PID,
		"owner_start_identity": encodeDriverStart(lease.Start),
	})
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		if _, err := transaction.At("test/authority_granted").Append(runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventAuthorityGranted, Payload: payload,
		}); err != nil {
			return err
		}
		_, err := transaction.At("test/dead_lease").CreateLease(true, lease)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func distinctStartIdentity(t *testing.T, identity runstate.StartIdentity) runstate.StartIdentity {
	t.Helper()
	switch value := identity.(type) {
	case runstate.LinuxStartIdentity:
		value.BootID += "-previous"
		return value
	case runstate.DarwinStartIdentity:
		value.StartTVSec++
		return value
	default:
		t.Fatalf("unsupported start identity %T", identity)
		return nil
	}
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
	appendRecoveryMovementStarted(t, store)
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: recoveryPayload(t, map[string]any{"reason": "initial", "performer_id": "writer", "adapter_id": "adapter", "model": "model"})})
}
func appendRecoveryMovementStarted(t *testing.T, store *Store) {
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventMovementReady, Payload: recoveryPayload(t, map[string]any{})})
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", Type: runstate.EventMovementStarted, Payload: recoveryPayload(t, map[string]any{})})
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

func appendRecoveryWriteSucceeded(t *testing.T, store *Store) {
	t.Helper()
	appendAttemptToVerifying(t, store)
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventChangeSetRecorded, Payload: recoveryPayload(t, changeSetPayloadForRecovery())})
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventVerificationPassed, Payload: recoveryPayload(t, map[string]any{})})
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventAcceptanceStarted, Payload: recoveryPayload(t, map[string]any{"subject_tree": "git-sha1:result", "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{}, "identity_versions": recoveryVersions()})})
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventAcceptanceEvaluationCompleted, Payload: recoveryPayload(t, map[string]any{"subject_tree": "git-sha1:result", "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{}, "identity_versions": recoveryVersions()})})
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventAttemptCompleted, Payload: recoveryPayload(t, map[string]any{})})
	appendRecoveryEvent(t, store, runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: runstate.EventMovementSucceeded, Payload: recoveryPayload(t, map[string]any{"approved_artifact_instance_ids": []any{}, "approved_change_set_id": "change-set-1", "identity_versions": recoveryVersions(), "run_succeeded": false})})
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
func recoveryActualImpact() map[string]any {
	return map[string]any{
		"score_changes": []any{},
		"authority": map[string]any{
			"allowed_paths": map[string]any{"added": []any{}, "removed": []any{}},
			"grants":        []any{},
			"side_effects":  map[string]any{"added": []any{}, "removed": []any{}},
		},
		"budget": map[string]any{},
	}
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
	return map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 10, "session_id": 10, "start_identity": map[string]any{"platform": "linux", "boot_id": "boot", "start_ticks": "12"}}, "granted_authority": map[string]any{"paths_rw": []any{"**"}, "paths_ro": []any{"**"}, "shell": false, "network": false}, "identity_versions": recoveryVersions()}
}
func adapterProbedPayloadForRecovery() map[string]any {
	return map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": true, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": false, "shell_grants": false, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": recoveryVersions()}
}
