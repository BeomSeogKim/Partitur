// Package recoveryexec executes recovery decisions selected by recovery.
//
// It deliberately sits outside internal/recovery: planning remains a pure
// projection over supplied facts, while this package owns ordered effects.
package recoveryexec

import (
	"context"
	"errors"
	"fmt"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

var (
	ErrIncompleteExecutor  = errors.New("recovery executor is incomplete")
	ErrAuthorityRequired   = errors.New("recovery executor requires established driver authority")
	ErrInvalidDecision     = errors.New("recovery executor received invalid decision")
	ErrUnreachableAction   = errors.New("recovery action is unreachable in this slice")
	ErrUnreachableStep     = errors.New("recovery action step is unreachable in this slice")
	ErrHandoffUnverifiable = errors.New("recovery spawn handoff is unverifiable")
	// errMovementCompositionTerminalized reports that selecting a recovered
	// movement's initial performer durably failed that movement before an
	// attempt could be created.
	errMovementCompositionTerminalized = errors.New("recovery movement composition terminalized")
	// ErrRunCancelledDuringRecovery reports that a recovery-owned attempt observed a
	// cancellation and terminalized through the §6 oracle. It is not a failure: the
	// executor replans so C.1's terminal row supplies the outcome, rather than a second
	// exit path inventing one.
	ErrRunCancelledDuringRecovery = errors.New("recovery attempt was cancelled and terminalized")
	ErrRecoveryReplan             = errors.New("recovery action must replan before mutation")
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
	RunID  runstate.RunID
	Input  recovery.Input

	// afterCompositionEvidence is a deterministic interleave seam for package
	// tests. Production contexts leave it nil.
	afterCompositionEvidence func()
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
	RunID  runstate.RunID
	Load   LoadInput

	// Steps is a test seam. A nil map selects the package's default handlers;
	// a non-nil map is intentionally exact so tests can expose missing steps.
	Steps map[recovery.ActionStep]StepHandler

	// authorize exists so tests can prove every effect path is fenced. It is not
	// an authority preflight: real handlers fence their own Append/Mutate call.
	authorize func(*runstore.Driver) error
}

// Outcome is recovery's command-visible result. The command translates it to
// an exit code without inspecting planner actions or run state.
type Outcome string

const (
	OutcomeSucceeded Outcome = "SUCCEEDED"
	OutcomeFailed    Outcome = "FAILED"
	OutcomeCancelled Outcome = "CANCELLED"
	OutcomeQuiescent Outcome = "QUIESCENT"
	OutcomeRefused   Outcome = "REFUSED"
	OutcomeHalted    Outcome = "HALTED"
)

// Result reports the final selected decision and the effects actually run.
type Result struct {
	Decision recovery.Decision
	Outcome  Outcome
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
	input, halted, err := executor.loadInput(ctx, recovery.Decision{}, "load recovery input")
	if err != nil {
		return Result{}, err
	}
	if halted.Halt != "" {
		return Result{Decision: halted, Outcome: OutcomeHalted}, nil
	}
	return executor.execute(ctx, input, recovery.Plan(input))
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
			result.Outcome = OutcomeHalted
			return result, nil
		}

		action := *decision.Action
		// A named unimplemented zero-step action is a refusal, not a recovery
		// effect. It must not acquire a driver or append an authority event.
		// Nonempty Steps select step dispatch before kind handling below.
		if len(action.Steps) == 0 {
			if unit, ok := namedUnimplementedActionOwners[action.Kind]; ok {
				return result, fmt.Errorf("%w: %s is owned by unit %s", ErrUnreachableAction, action.Kind, unit)
			}
		}
		if action.Kind == recovery.ActionReclaimAuthority || (actionRequiresDriver(action) && executor.Driver == nil) {
			if err := executor.acquireAuthority(input); err != nil {
				if halted, ok := haltDecision(decision, err); ok {
					result.Decision = halted
					result.Outcome = OutcomeHalted
					return result, nil
				}
				return result, err
			}
			refreshed, halted, err := executor.loadInput(ctx, decision, "reload recovery input after authority acquisition")
			if err != nil {
				return result, err
			}
			if halted.Halt != "" {
				result.Decision = halted
				result.Outcome = OutcomeHalted
				return result, nil
			}
			input = refreshed
			decision = recovery.Plan(input)
			result.Replans++
			continue
		}
		if isContinuation(action) {
			decision = continuePlan(input, action.Continuation)
			continue
		}
		if actionRequiresDriver(action) {
			if err := executor.authorizeDriver(); err != nil {
				return result, err
			}
		}
		cancelledMidStep := false
		if len(action.Steps) != 0 {
			for index, step := range action.Steps {
				handler, ok := executor.stepHandler(step)
				if !ok || handler == nil {
					return result, fmt.Errorf("%w: %s", ErrUnreachableStep, step)
				}
				handlerContext := HandlerContext{Store: executor.Store, Driver: executor.Driver, RunID: executor.RunID, Input: input}
				if err := handler(ctx, handlerContext, action); err != nil {
					if errors.Is(err, ErrRunCancelledDuringRecovery) {
						// The step ran and terminalized the run; Result records effects that
						// actually happened, so it is recorded before the replan even though
						// the remaining steps are skipped.
						result.Steps = append(result.Steps, step)
						refreshed, halted, reloadErr := executor.reloadAfterEffect(ctx, input, decision)
						if reloadErr != nil {
							return result, reloadErr
						}
						if halted.Halt != "" {
							result.Decision = halted
							result.Outcome = OutcomeHalted
							return result, nil
						}
						input = refreshed
						result.Replans++
						decision = recovery.Plan(input)
						cancelledMidStep = true
						break
					}
					if halted, ok := haltDecision(decision, err); ok {
						result.Decision = halted
						result.Outcome = OutcomeHalted
						return result, nil
					}
					return result, fmt.Errorf("execute recovery step %s: %w", step, err)
				}
				result.Steps = append(result.Steps, step)
				if index+1 < len(action.Steps) || action.Continuation != "" || action.Replan {
					refreshed, halted, err := executor.reloadAfterEffect(ctx, input, decision)
					if err != nil {
						return result, err
					}
					if halted.Halt != "" {
						result.Decision = halted
						result.Outcome = OutcomeHalted
						return result, nil
					}
					input = refreshed
				}
			}
			if cancelledMidStep {
				continue
			}
		} else {
			handlerContext := HandlerContext{Store: executor.Store, Driver: executor.Driver, RunID: executor.RunID, Input: input}
			handler, ok := executor.kindHandler(action.Kind)
			if !ok {
				return result, fmt.Errorf("%w: %s", ErrUnreachableAction, action.Kind)
			}
			if err := handler(ctx, handlerContext, action); err != nil {
				if errors.Is(err, errMovementCompositionTerminalized) {
					result.Outcome = OutcomeFailed
					return result, nil
				}
				if errors.Is(err, ErrRecoveryReplan) {
					refreshed, halted, reloadErr := executor.reloadAfterEffect(ctx, input, decision)
					if reloadErr != nil {
						return result, reloadErr
					}
					if halted.Halt != "" {
						result.Decision = halted
						result.Outcome = OutcomeHalted
						return result, nil
					}
					input = refreshed
					result.Replans++
					decision = recovery.Plan(input)
					continue
				}
				if errors.Is(err, ErrRunCancelledDuringRecovery) {
					// As above: the action ran before it reported the cancellation.
					result.Kinds = append(result.Kinds, action.Kind)
					refreshed, halted, reloadErr := executor.reloadAfterEffect(ctx, input, decision)
					if reloadErr != nil {
						return result, reloadErr
					}
					if halted.Halt != "" {
						result.Decision = halted
						result.Outcome = OutcomeHalted
						return result, nil
					}
					input = refreshed
					result.Replans++
					decision = recovery.Plan(input)
					continue
				}
				if halted, ok := haltDecision(decision, err); ok {
					result.Decision = halted
					result.Outcome = OutcomeHalted
					return result, nil
				}
				return result, fmt.Errorf("execute recovery action %s: %w", action.Kind, err)
			}
			result.Kinds = append(result.Kinds, action.Kind)
			if action.Continuation != "" || action.Replan {
				refreshed, halted, err := executor.reloadAfterEffect(ctx, input, decision)
				if err != nil {
					return result, err
				}
				if halted.Halt != "" {
					result.Decision = halted
					result.Outcome = OutcomeHalted
					return result, nil
				}
				input = refreshed
			}
		}
		if action.Continuation != "" {
			if action.PendingSuccessor != nil {
				pending := *action.PendingSuccessor
				input.Projection.Scheduler.PendingSuccessor = &pending
			}
			decision = continuePlan(input, action.Continuation)
			continue
		}
		if !action.Replan {
			result.Outcome = outcomeFor(action, input)
			return result, nil
		}
		result.Replans++
		decision = recovery.Plan(input)
	}
}

func (executor *Executor) acquireAuthority(input recovery.Input) error {
	if executor.Driver != nil {
		return nil
	}
	if executor.Store == nil || executor.RunID == "" {
		return ErrAuthorityRequired
	}
	var driver *runstore.Driver
	var err error
	lease := input.Observations.Lease
	if lease.Owner == recovery.OwnerDead && lease.Identity != nil {
		driver, err = executor.Store.ReclaimDeadRecoveryDriver(executor.RunID, runstore.LeaseIdentity{
			Epoch: lease.Identity.Epoch,
			Token: lease.Identity.Token,
			PID:   lease.Identity.PID,
			Start: lease.Identity.Start,
		})
	} else {
		driver, err = executor.Store.AcquireRecoveryDriver(executor.RunID)
	}
	if err != nil {
		return fmt.Errorf("acquire recovery authority: %w", err)
	}
	executor.Driver = driver
	return nil
}

func actionRequiresDriver(action recovery.Action) bool {
	switch action.Kind {
	case recovery.ActionTerminalCleanup,
		recovery.ActionRemoveStaleLease,
		recovery.ActionQuarantineOrphanLease,
		recovery.ActionRefuseResume,
		recovery.ActionReturnWaitingHuman,
		recovery.ActionExecuteCancellation:
		return false
	default:
		return true
	}
}

func outcomeFor(action recovery.Action, input recovery.Input) Outcome {
	switch action.Kind {
	case recovery.ActionExecuteCancellation:
		return OutcomeCancelled
	case recovery.ActionRefuseResume:
		return OutcomeRefused
	case recovery.ActionReturnWaitingHuman:
		return OutcomeQuiescent
	case recovery.ActionTerminalCleanup:
		switch input.Projection.State.Run {
		case runstate.RunSucceeded:
			return OutcomeSucceeded
		case runstate.RunFailed:
			return OutcomeFailed
		case runstate.RunCancelled:
			return OutcomeCancelled
		}
	}
	return ""
}

func (executor *Executor) loadInput(ctx context.Context, decision recovery.Decision, description string) (recovery.Input, recovery.Decision, error) {
	input, err := executor.Load(ctx)
	if err != nil {
		if halted, ok := haltDecision(decision, err); ok {
			return recovery.Input{}, halted, nil
		}
		return recovery.Input{}, recovery.Decision{}, fmt.Errorf("%s: %w", description, err)
	}
	input = executor.canonicalizeDriverLease(input)
	return input, recovery.Decision{}, nil
}

func (executor *Executor) canonicalizeDriverLease(input recovery.Input) recovery.Input {
	if executor == nil || executor.Driver == nil || executor.Driver.RunID() != executor.RunID {
		return input
	}
	identity := input.Observations.Lease.Identity
	if identity == nil || !executor.Driver.MatchesLease(runstore.LeaseIdentity{
		Epoch: identity.Epoch,
		Token: identity.Token,
		PID:   identity.PID,
		Start: identity.Start,
	}) {
		return input
	}
	input.Observations.Lease.Owner = recovery.OwnerCurrentDriver
	return input
}

// reloadAfterEffect is the sole transition from an executed recovery effect
// to further planning. No planning input may predate this executor's last
// durable mutation.

func (executor *Executor) reloadAfterEffect(ctx context.Context, previous recovery.Input, decision recovery.Decision) (recovery.Input, recovery.Decision, error) {
	if executor.Load == nil {
		return previous, recovery.Decision{}, nil
	}
	return executor.loadInput(ctx, decision, "reload recovery input after recovery effect")
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
	case errors.Is(err, runstore.ErrJournalIdempotencyConflict):
		return recovery.Decision{CaseID: decision.CaseID, Halt: recovery.HaltJournalIdempotencyConflict}, true
	case errors.Is(err, canonical.ErrUnsupportedRunFormat):
		return recovery.Decision{CaseID: decision.CaseID, Halt: recovery.HaltUnsupportedRunFormat}, true
	case errors.Is(err, runstore.ErrMissingPinnedSnapshot):
		return recovery.Decision{CaseID: decision.CaseID, Halt: recovery.HaltMissingSnapshotFile}, true
	case errors.Is(err, runstore.ErrMissingResolvedCast):
		return recovery.Decision{CaseID: decision.CaseID, Halt: recovery.HaltMissingResolvedCast}, true
	case errors.Is(err, workspace.ErrGitUnverifiable):
		return recovery.Decision{CaseID: decision.CaseID, Halt: recovery.HaltGitUnverifiable}, true
	case errors.Is(err, runstate.ErrSweepUnverifiable):
		return recovery.Decision{CaseID: decision.CaseID, Halt: recovery.HaltSweepUnverifiable}, true
	case errors.Is(err, ErrHandoffUnverifiable):
		return recovery.Decision{CaseID: decision.CaseID, Halt: recovery.HaltSpawnHandoffUnverifiable}, true
	case errors.Is(err, runstore.ErrJournalCorrupt):
		return recovery.Decision{CaseID: decision.CaseID, Halt: recovery.HaltJournalCorrupt}, true
	default:
		return recovery.Decision{}, false
	}
}
