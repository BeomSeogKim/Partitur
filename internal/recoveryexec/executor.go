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
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

var (
	ErrIncompleteExecutor  = errors.New("recovery executor is incomplete")
	ErrAuthorityRequired   = errors.New("recovery executor requires established driver authority")
	ErrInvalidDecision     = errors.New("recovery executor received invalid decision")
	ErrUnreachableAction   = errors.New("recovery action is unreachable in this slice")
	ErrUnreachableStep     = errors.New("recovery action step is unreachable in this slice")
	ErrSweepUnverifiable   = errors.New("recovery session sweep is unverifiable")
	ErrHandoffUnverifiable = errors.New("recovery spawn handoff is unverifiable")
)

// LoadInput returns a fresh, fully observed recovery input. It is called again
// only when the selected action says Replan, so the executor never invents an
// iteration policy of its own.
type LoadInput func(context.Context) (recovery.Input, error)

// HandlerContext gives one effect handler the selected action's run-owned
// inputs and the authority it must use for durable mutation. It deliberately
// contains no live judgement callback: classification facts come from Input.
type HandlerContext struct {
	Store  *runstore.Store
	Driver *runstore.Driver
	Input  recovery.Input
}

// StepHandler performs one planner-selected, order-sensitive recovery step.
// A handler that mutates durable state must use Context.Driver for that
// mutation; Append and Mutate recheck authority under the state lock.
type StepHandler func(context.Context, HandlerContext, recovery.Action) error

// Executor runs the step sequence exactly as supplied by the planner.
//
// Driver is deliberately supplied by the caller rather than acquired here.
// The resume command owns lease acquisition/reclamation; this executor accepts
// only the already-established authority that authorizes execution mutation.
// Planning and halts need no driver and never invoke an effect handler.
type Executor struct {
	Driver *runstore.Driver
	Store  *runstore.Store
	Load   LoadInput

	// Steps is a test seam. A nil map selects the package's default handlers;
	// a non-nil map is intentionally exact so tests can expose missing steps.
	Steps map[recovery.ActionStep]StepHandler

	// authorize exists so tests can prove every effect path is fenced. It is not
	// an authority preflight: real handlers fence their own Append/Mutate call.
	authorize func(*runstore.Driver) error
}

// Result reports the final selected decision and the effects actually run.
type Result struct {
	Decision recovery.Decision
	Steps    []recovery.ActionStep
	Kinds    []recovery.ActionKind
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
		if err := executor.authorizeDriver(); err != nil {
			return result, err
		}
		if len(action.Steps) != 0 {
			for _, step := range action.Steps {
				handler, ok := executor.stepHandler(step)
				if !ok || handler == nil {
					return result, fmt.Errorf("%w: %s", ErrUnreachableStep, step)
				}
				handlerContext := HandlerContext{Store: executor.Store, Driver: executor.Driver, Input: input}
				if err := handler(ctx, handlerContext, action); err != nil {
					if halted, ok := haltDecision(decision, err); ok {
						result.Decision = halted
						return result, nil
					}
					return result, fmt.Errorf("execute recovery step %s: %w", step, err)
				}
				result.Steps = append(result.Steps, step)
				if stepRefreshesInput(step) {
					refreshed, err := executor.refreshInput(ctx, input)
					if err != nil {
						return result, err
					}
					input = refreshed
				}
			}
		} else {
			handlerContext := HandlerContext{Store: executor.Store, Driver: executor.Driver, Input: input}
			handler, ok := executor.kindHandler(action.Kind)
			if !ok {
				return result, fmt.Errorf("%w: %s", ErrUnreachableAction, action.Kind)
			}
			if err := handler(ctx, handlerContext, action); err != nil {
				if halted, ok := haltDecision(decision, err); ok {
					result.Decision = halted
					return result, nil
				}
				return result, fmt.Errorf("execute recovery action %s: %w", action.Kind, err)
			}
			result.Kinds = append(result.Kinds, action.Kind)
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

// stepRefreshesInput marks a boundary after which the next step must use a
// newly-derived input. The close changes durable budget facts; the criterion
// sweep changes which subject observation is admissible. Neither handler
// performs its own replacement read.
func stepRefreshesInput(step recovery.ActionStep) bool {
	return step == recovery.StepCloseAdapterInterval || step == recovery.StepSweepCriterionSession
}

func (executor *Executor) refreshInput(ctx context.Context, previous recovery.Input) (recovery.Input, error) {
	if executor.Load == nil {
		return previous, nil
	}
	input, err := executor.Load(ctx)
	if err != nil {
		return recovery.Input{}, fmt.Errorf("reload recovery input after ordered step: %w", err)
	}
	return input, nil
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
	return nil
}

func (executor *Executor) stepHandler(step recovery.ActionStep) (StepHandler, bool) {
	if executor.Steps != nil {
		handler, ok := executor.Steps[step]
		return handler, ok
	}
	handler, ok := defaultSteps()[step]
	return handler, ok
}

func (executor *Executor) kindHandler(kind recovery.ActionKind) (StepHandler, bool) {
	handler, ok := defaultKinds()[kind]
	return handler, ok
}

func haltDecision(decision recovery.Decision, err error) (recovery.Decision, bool) {
	switch {
	case errors.Is(err, ErrSweepUnverifiable):
		return recovery.Decision{CaseID: decision.CaseID, Halt: recovery.HaltSweepUnverifiable}, true
	case errors.Is(err, ErrHandoffUnverifiable):
		return recovery.Decision{CaseID: decision.CaseID, Halt: recovery.HaltSpawnHandoffUnverifiable}, true
	default:
		return recovery.Decision{}, false
	}
}
