package recoveryexec

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

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

func TestDefaultDirectKindsAppendToRealStore(t *testing.T) {
	t.Run("budget failure", func(t *testing.T) {
		store, driver := handlerStore(t, false)
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

func TestDefaultAcceptanceHandlersAppendToRealStore(t *testing.T) {
	t.Run("criterion recovery records error then acceptance failure", func(t *testing.T) {
		store, driver := handlerStore(t, true)
		advanceHandlerAcceptance(t, driver, true)
		state, err := driver.State()
		if err != nil {
			t.Fatal(err)
		}
		input := recovery.Input{Projection: recovery.Projection{State: state, CurrentHeadAttempt: handlerAttempt(state)}}
		executor := &Executor{Store: store, Driver: driver}
		_, err = executor.execute(context.Background(), input, recovery.Decision{CaseID: recovery.CaseIncompleteCriterion, Action: &recovery.Action{
			Kind: recovery.ActionRecoverIncompleteCriterion, AttemptID: "attempt-1", CriterionID: "criterion-1", FailureReason: "criterion_errored",
			Steps: []recovery.ActionStep{recovery.StepSynthesizeCriterionError, recovery.StepClassifyAcceptanceFailure},
		}})
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := driver.State()
		if err != nil || replayed.Acceptances["attempt-1"].Criteria["criterion-1"].Outcome != "ERROR" || replayed.Attempts["attempt-1"].State != runstate.AttemptFailed {
			t.Fatalf("state=%+v error=%v", replayed, err)
		}
		assertLastEventType(t, store, runstate.EventAcceptanceFailed)
	})

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

func TestExecutorMapsSweepFailureToHaltWithoutJournalWrite(t *testing.T) {
	store, driver := handlerStore(t, true)
	journalPath := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "journal.jsonl")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	executor := &Executor{Store: store, Driver: driver, Steps: map[recovery.ActionStep]StepHandler{
		recovery.StepSweepRecordedSession: func(context.Context, HandlerContext, recovery.Action) error { return ErrSweepUnverifiable },
	}}
	result, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{CaseID: recovery.CaseUnprobedAttempt, Action: &recovery.Action{
		Kind: recovery.ActionRecoverUnprobedAttempt, AttemptID: "attempt-1", Steps: []recovery.ActionStep{recovery.StepSweepRecordedSession},
	}})
	if err != nil || result.Decision.Halt != recovery.HaltSweepUnverifiable {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("journal changed=%t error=%v", string(after) != string(before), err)
	}
}

func advanceHandlerAcceptance(t *testing.T, driver *runstore.Driver, criterion bool) {
	t.Helper()
	appendDriverEvent := func(eventType runstate.EventType, payload any) {
		t.Helper()
		if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "write", AttemptID: "attempt-1", Type: eventType, Payload: handlerPayload(t, payload)}, faultpoint.ReceiptAddress("test."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	appendDriverEvent(runstate.EventAttemptStarted, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "boot", "start_ticks": "1"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{}, "shell": false, "network": false}, "identity_versions": versions})
	appendDriverEvent(runstate.EventAdapterProbed, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})
	appendDriverEvent(runstate.EventPerformerCompleted, map[string]any{"session_hint_stored": false})
	appendDriverEvent(runstate.EventVerificationPassed, map[string]any{})
	planned := []any{}
	if criterion {
		planned = []any{"criterion-1"}
	}
	appendDriverEvent(runstate.EventAcceptanceStarted, map[string]any{"subject_tree": "git-sha1:subject", "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": planned, "identity_versions": versions})
	if criterion {
		appendDriverEvent(runstate.EventCriterionStarted, map[string]any{"criterion_id": "criterion-1", "criterion_spec_hash": "sha256:criterion", "subject_tree": "git-sha1:subject", "identity_versions": versions})
		return
	}
	appendDriverEvent(runstate.EventAcceptanceEvaluationCompleted, map[string]any{"subject_tree": "git-sha1:subject", "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{}, "identity_versions": versions})
}

func handlerAttempt(state runstate.State) *recovery.AttemptRecovery {
	attempt := state.Attempts["attempt-1"]
	return &recovery.AttemptRecovery{AttemptID: "attempt-1", MovementID: "write", ScoreRevision: 1, State: attempt.State,
		FailureClassification: recovery.FailureClassification{CurrentPerformer: "writer", VisitedPerformers: []string{"writer"}, RetriesPerMovement: 1, RemainingTimeMS: 1}}
}

func handlerStore(t *testing.T, selectAttempt bool) (*runstore.Store, *runstore.Driver) {
	t.Helper()
	root := t.TempDir()
	store, err := runstore.New(root, faultpoint.Nop{})
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
	driver, err := store.AcquireDriver("run-1", []runstate.MovementSeed{{ID: "write", Initial: runstate.MovementPending}, {ID: "read", Initial: runstate.MovementPending}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Release() })
	return store, driver
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
		recovery.StepSynthesizeCriterionError,
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
		recovery.StepSynthesizeCriterionError,
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
	if err != nil || loads != 3 || handoffCalls != 1 || result.Replans != 1 || result.Decision.Halt != recovery.HaltRootSnapshotDivergence {
		t.Fatalf("result=%+v loads=%d handoffCalls=%d error=%v", result, loads, handoffCalls, err)
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
