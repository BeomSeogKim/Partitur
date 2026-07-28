package recoveryexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

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
				step: func(context.Context, *runstore.Driver, recovery.Action) error {
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
				step: func(context.Context, *runstore.Driver, recovery.Action) error { return broken },
				"later": func(context.Context, *runstore.Driver, recovery.Action) error {
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
		recovery.StepStabilizeHandoff:         func(context.Context, *runstore.Driver, recovery.Action) error { handoffCalls++; return nil },
		recovery.StepCloseAdapterInterval:     func(context.Context, *runstore.Driver, recovery.Action) error { return nil },
		recovery.StepClassifyAndAppendFailure: func(context.Context, *runstore.Driver, recovery.Action) error { return nil },
	})
	executor.Load = func(context.Context) (recovery.Input, error) {
		loads++
		if loads == 1 {
			return first, nil
		}
		return second, nil
	}
	result, err := executor.Execute(context.Background())
	if err != nil || loads != 2 || handoffCalls != 1 || result.Replans != 1 || result.Decision.Halt != recovery.HaltRootSnapshotDivergence {
		t.Fatalf("result=%+v loads=%d handoffCalls=%d error=%v", result, loads, handoffCalls, err)
	}
}

func TestExecutorRejectsUnreachableAction(t *testing.T) {
	executor := testExecutor(nil)
	_, err := executor.execute(context.Background(), recovery.Input{}, recovery.Decision{
		CaseID: "RC-test", Action: &recovery.Action{Kind: recovery.ActionAppendMovementSucceeded},
	})
	if !errors.Is(err, ErrUnreachableAction) {
		t.Fatalf("error=%v", err)
	}
}

func TestExecutorRequiresAuthorityBeforeEffect(t *testing.T) {
	called := false
	executor := &Executor{
		Steps: map[recovery.ActionStep]StepHandler{
			recovery.StepVerifyAcceptanceSubject: func(context.Context, *runstore.Driver, recovery.Action) error { called = true; return nil },
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
