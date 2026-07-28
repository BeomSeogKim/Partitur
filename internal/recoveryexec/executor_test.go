package recoveryexec

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

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
				criterion := journal.Events[len(journal.Events)-2]
				var criterionPayload map[string]any
				if err := json.Unmarshal(criterion.Payload, &criterionPayload); err != nil {
					t.Fatal(err)
				}
				for _, absent := range []string{"exit_code", "duration_ms", "output_ref"} {
					if _, ok := criterionPayload[absent]; ok {
						t.Fatalf("criterion completion unexpectedly carries %s: %s", absent, criterion.Payload)
					}
				}
				if criterionPayload["error_detail"] != "recovered_without_observed_completion" {
					t.Fatalf("criterion completion payload = %s", criterion.Payload)
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
		{name: "recorded session sweep", step: recovery.StepSweepRecordedSession, err: ErrSweepUnverifiable, wantHalt: recovery.HaltSweepUnverifiable},
		{name: "spawn handoff", step: recovery.StepStabilizeHandoff, err: ErrHandoffUnverifiable, wantHalt: recovery.HaltSpawnHandoffUnverifiable},
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

func TestExecutorRejectsControlActionsBeforeAcquiringAuthority(t *testing.T) {
	for _, test := range []struct {
		action recovery.ActionKind
		unit   string
	}{
		{action: recovery.ActionExecuteCancellation, unit: "2.1"},
		{action: recovery.ActionCompleteOrAbandonPrepare, unit: "4.2"},
	} {
		t.Run(string(test.action), func(t *testing.T) {
			store := acquirableRecoveryStore(t)
			journalPath := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "journal.jsonl")
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}

			input := recovery.Input{Projection: recovery.Projection{State: runstate.State{Run: runstate.RunRunning, CancelRequested: test.action == recovery.ActionExecuteCancellation}}}
			if test.action == recovery.ActionCompleteOrAbandonPrepare {
				input.Projection.State.PendingPrepare = &runstate.PendingPrepare{}
				input.Observations.Prepare = recovery.PrepareObservation{PlanPresent: true, SnapshotPresent: true}
			}
			executor := &Executor{Store: store, RunID: "run-1", Load: func(context.Context) (recovery.Input, error) { return input, nil }}
			_, err = executor.execute(context.Background(), recovery.Input{}, recovery.Decision{
				CaseID: "RC-test",
				Action: &recovery.Action{Kind: test.action},
			})
			if !errors.Is(err, ErrUnreachableAction) || !strings.Contains(err.Error(), "unit "+test.unit) {
				t.Fatalf("error=%v", err)
			}
			if _, present, leaseErr := store.ReadLease("run-1"); leaseErr != nil || present {
				t.Fatalf("lease present=%t error=%v", present, leaseErr)
			}
			after, err := os.ReadFile(journalPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("journal changed=%t error=%v", string(after) != string(before), err)
			}
		})
	}
}

func TestReclaimAuthorityIsTheOnlyAuthorityBoundary(t *testing.T) {
	for _, action := range []recovery.ActionKind{
		recovery.ActionTerminalCleanup,
		recovery.ActionRemoveStaleLease,
		recovery.ActionQuarantineOrphanLease,
		recovery.ActionRefuseResume,
		recovery.ActionReturnWaitingHuman,
		recovery.ActionExecuteCancellation,
		recovery.ActionCompleteOrAbandonPrepare,
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
	snapshot := []byte("score: \"0.2\"\nname: executor-fixture\nrevision: 1\nstatus: finalized\ngoal: fixture\nverification:\n  expectation:\n    intent: pass-existing-tests\n    apply_gate:\n      waived: true\n      reason: fixture\nparts: {}\nmovements: []\npolicy:\n  allowed_paths: [\"**\"]\n  budget:\n    active_wall_clock_min: 10\n")
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
