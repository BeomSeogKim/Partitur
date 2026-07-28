// Package recoveryexec executes recovery decisions selected by recovery.
//
// It deliberately sits outside internal/recovery: planning remains a pure
// projection over supplied facts, while this package owns ordered effects.
package recoveryexec

import (
	"context"
	"errors"
	"fmt"

	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

var (
	ErrIncompleteExecutor = errors.New("recovery executor is incomplete")
	ErrAuthorityRequired  = errors.New("recovery executor requires established driver authority")
	ErrInvalidDecision    = errors.New("recovery executor received invalid decision")
	ErrUnreachableAction  = errors.New("recovery action is unreachable in this slice")
	ErrUnreachableStep    = errors.New("recovery action step is unreachable in this slice")
)

// LoadInput returns a fresh, fully observed recovery input. It is called again
// only when the selected action says Replan, so the executor never invents an
// iteration policy of its own.
type LoadInput func(context.Context) (recovery.Input, error)

// StepHandler performs one planner-selected, order-sensitive recovery step.
// A handler that mutates durable state must use Driver for that mutation; its
// Append and Mutate methods repeat the authority fence under the state lock.
type StepHandler func(context.Context, *runstore.Driver, recovery.Action) error

// Executor runs the step sequence exactly as supplied by the planner.
//
// Driver is deliberately supplied by the caller rather than acquired here.
// The resume command owns lease acquisition/reclamation; this executor accepts
// only the already-established authority that authorizes execution mutation.
// Planning and halts need no driver and never invoke an effect handler.
type Executor struct {
	Driver *runstore.Driver
	Load   LoadInput
	Steps  map[recovery.ActionStep]StepHandler

	// authorize exists so tests can prove every effect path is fenced. Production
	// uses Driver.Mutate, which rechecks the complete lease/epoch identity tuple.
	authorize func(*runstore.Driver) error
}

// Result reports the final selected decision and the effects actually run.
type Result struct {
	Decision recovery.Decision
	Steps    []recovery.ActionStep
	Replans  int
}

// Execute starts with C.1 and follows only the continuation selected by the
// planner. It never chooses a different action when a handler fails.
func (executor *Executor) Execute(ctx context.Context) (Result, error) {
	if executor == nil || executor.Load == nil {
		return Result{}, ErrIncompleteExecutor
	}
	input, err := executor.Load(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("load recovery input: %w", err)
	}
	decision := recovery.Plan(input)
	return executor.execute(ctx, input, decision)
}

func (executor *Executor) execute(ctx context.Context, input recovery.Input, decision recovery.Decision) (Result, error) {
	result := Result{}
	for {
		if !decision.Valid() {
			return result, fmt.Errorf("%w: %+v", ErrInvalidDecision, decision)
		}
		result.Decision = decision
		if decision.Halt != "" {
			// Appendix B.7: a halt is intentionally not a journal event.
			return result, nil
		}

		action := *decision.Action
		if isContinuation(action) {
			decision = continuePlan(input, action.Continuation)
			continue
		}
		if len(action.Steps) == 0 {
			return result, fmt.Errorf("%w: %s", ErrUnreachableAction, action.Kind)
		}
		if err := executor.authorizeDriver(); err != nil {
			return result, err
		}
		for _, step := range action.Steps {
			handler, ok := executor.Steps[step]
			if !ok || handler == nil {
				return result, fmt.Errorf("%w: %s", ErrUnreachableStep, step)
			}
			if err := handler(ctx, executor.Driver, action); err != nil {
				return result, fmt.Errorf("execute recovery step %s: %w", step, err)
			}
			result.Steps = append(result.Steps, step)
		}
		if !action.Replan {
			return result, nil
		}
		input, err := executor.Load(ctx)
		if err != nil {
			return result, fmt.Errorf("reload recovery input: %w", err)
		}
		result.Replans++
		decision = recovery.Plan(input)
	}
}

func isContinuation(action recovery.Action) bool {
	switch action.Kind {
	case recovery.ActionProceedAttempt:
		return action.Continuation == recovery.ContinuationC2
	case recovery.ActionProceedAcceptance:
		return action.Continuation == recovery.ContinuationC3
	case recovery.ActionProceedScheduler:
		return action.Continuation == recovery.ContinuationC4
	default:
		return false
	}
}

func continuePlan(input recovery.Input, continuation recovery.Continuation) recovery.Decision {
	switch continuation {
	case recovery.ContinuationC2:
		return recovery.PlanAttempt(input)
	case recovery.ContinuationC3:
		return recovery.PlanAcceptance(input)
	case recovery.ContinuationC4:
		return recovery.PlanScheduler(input)
	default:
		return recovery.Decision{}
	}
}

func (executor *Executor) authorizeDriver() error {
	if executor.Driver == nil {
		return ErrAuthorityRequired
	}
	if executor.authorize != nil {
		if err := executor.authorize(executor.Driver); err != nil {
			return fmt.Errorf("authorize recovery effect: %w", err)
		}
		return nil
	}
	if err := executor.Driver.Mutate(func(*runstore.Txn, runstate.State) error { return nil }); err != nil {
		return fmt.Errorf("authorize recovery effect: %w", err)
	}
	return nil
}
