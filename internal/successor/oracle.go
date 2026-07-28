package successor

import (
	"errors"
	"fmt"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// Charge records the only two kinds of authorized successor attempt.
type Charge string

const (
	ChargeQualityRetry Charge = "quality_retry"
	ChargeFallback     Charge = "fallback"
	ChargeNone         Charge = "none"
)

const (
	KindAdapterUnavailable = "adapter_unavailable"
	KindModelUnavailable   = "model_unavailable"
	KindProviderTimeout    = "provider_timeout"
	KindRateLimited        = "rate_limited"
	KindAuthentication     = "authentication"
	KindTaskFailed         = "task_failed"
	KindProtocolError      = "protocol_error"
	KindGrantDenied        = "grant_denied"
	KindBudgetExhausted    = "budget_exhausted"
)

var (
	ErrUnknownFailureCase  = errors.New("successor: unknown failure case")
	ErrInvalidInput        = errors.New("successor: invalid input")
	ErrInvalidDisposition  = errors.New("successor: invalid disposition")
	ErrFallbackUnavailable = errors.New("successor: recorded fallback is unavailable")
)

// FailureCase is either an attempt.failed kind or an acceptance.failed reason.
// The latter is quality-classified regardless of its specific reason.
type FailureCase struct {
	AttemptKind      string
	AcceptanceReason string
}

// ClassificationInput is the complete journal-projected input to Arm 1.
// RemainingTimeMS is non-negative and is zero exactly when no new attempt may
// start. Retry counts are movement-wide across the fallback chain.
type ClassificationInput struct {
	Failure              FailureCase
	HasUnvisitedFallback bool
	RetriesConsumed      int
	RetriesPerMovement   int
	RemainingTimeMS      int64
}

// Classify is Arm 1 of the successor oracle. It decides a failure's durable
// disposition before its event is appended. It does not append anything.
func Classify(input ClassificationInput) (runstate.Disposition, error) {
	if input.RemainingTimeMS < 0 || input.RetriesConsumed < 0 || input.RetriesPerMovement < 0 {
		return runstate.Disposition{}, fmt.Errorf("%w: negative budget input", ErrInvalidInput)
	}
	class, terminalReason, err := classifyFailure(input.Failure)
	if err != nil {
		return runstate.Disposition{}, err
	}

	switch class {
	case failureInfrastructure:
		if input.HasUnvisitedFallback && input.RemainingTimeMS > 0 {
			return retryDisposition(ChargeFallback), nil
		}
		return terminalDisposition(reasonForBudget(input.RemainingTimeMS, "fallbacks_exhausted")), nil
	case failureQuality:
		if input.RetriesConsumed < input.RetriesPerMovement && input.RemainingTimeMS > 0 {
			return retryDisposition(ChargeQualityRetry), nil
		}
		return terminalDisposition(reasonForBudget(input.RemainingTimeMS, "retries_exhausted")), nil
	case failureImmediatelyTerminal:
		return terminalDisposition(terminalReason), nil
	default:
		return runstate.Disposition{}, fmt.Errorf("%w: unhandled failure class", ErrUnknownFailureCase)
	}
}

type failureClass uint8

const (
	failureInfrastructure failureClass = iota + 1
	failureQuality
	failureImmediatelyTerminal
)

func classifyFailure(failure FailureCase) (failureClass, string, error) {
	if failure.AttemptKind != "" && failure.AcceptanceReason != "" {
		return 0, "", fmt.Errorf("%w: failure cannot be both attempt and acceptance", ErrInvalidInput)
	}
	if failure.AcceptanceReason != "" {
		return failureQuality, "", nil
	}
	switch failure.AttemptKind {
	case KindAdapterUnavailable, KindModelUnavailable, KindProviderTimeout, KindRateLimited, KindAuthentication:
		return failureInfrastructure, "", nil
	case KindTaskFailed:
		return failureQuality, "", nil
	case KindGrantDenied, KindProtocolError, KindBudgetExhausted:
		return failureImmediatelyTerminal, failure.AttemptKind, nil
	default:
		return 0, "", fmt.Errorf("%w: %q", ErrUnknownFailureCase, failure.AttemptKind)
	}
}

func retryDisposition(charge Charge) runstate.Disposition {
	return runstate.Disposition{Charged: string(charge)}
}

func terminalDisposition(reason string) runstate.Disposition {
	return runstate.Disposition{
		Charged:          string(ChargeNone),
		MovementTerminal: true,
		TerminalReason:   reason,
	}
}

func reasonForBudget(remainingTimeMS int64, exhausted string) string {
	if remainingTimeMS == 0 {
		return KindBudgetExhausted
	}
	return exhausted
}

// Action is the durable action Arm 2 selects. PendingSuccessor deliberately
// does not imply performer.selected or an adapter launch.
type Action string

const (
	ActionPendingSuccessor Action = "pending_successor"
	ActionMovementFailed   Action = "movement_failed"
)

// RealizationInput is the complete input to Arm 2. Binding is a durable cast
// projection view; no budget or admissibility input is accepted here.
type RealizationInput struct {
	Disposition       runstate.Disposition
	CurrentPerformer  string
	Binding           cast.BindingView
	VisitedPerformers []string
}

// Realization is the deterministic result of replaying a recorded
// disposition. Pending successors are not themselves durable selection events.
type Realization struct {
	Action         Action
	Performer      string
	Charge         Charge
	TerminalReason string
}

// Realize is Arm 2 of the successor oracle. It never reads a budget and never
// decides whether another attempt is admissible.
func Realize(input RealizationInput) (Realization, error) {
	switch Charge(input.Disposition.Charged) {
	case ChargeQualityRetry:
		if err := validateRetryDisposition(input.Disposition); err != nil {
			return Realization{}, err
		}
		if input.CurrentPerformer == "" {
			return Realization{}, fmt.Errorf("%w: quality retry has no current performer", ErrInvalidInput)
		}
		return Realization{
			Action:    ActionPendingSuccessor,
			Performer: input.CurrentPerformer,
			Charge:    ChargeQualityRetry,
		}, nil
	case ChargeFallback:
		if err := validateRetryDisposition(input.Disposition); err != nil {
			return Realization{}, err
		}
		performer, ok := nextUnvisitedFallback(
			input.Binding.Fallbacks,
			input.CurrentPerformer,
			input.VisitedPerformers,
		)
		if !ok {
			return Realization{}, ErrFallbackUnavailable
		}
		return Realization{
			Action:    ActionPendingSuccessor,
			Performer: performer,
			Charge:    ChargeFallback,
		}, nil
	case ChargeNone:
		if !input.Disposition.MovementTerminal || input.Disposition.TerminalReason == "" {
			return Realization{}, fmt.Errorf("%w: terminal none requires terminal_reason", ErrInvalidDisposition)
		}
		return Realization{
			Action:         ActionMovementFailed,
			Charge:         ChargeNone,
			TerminalReason: input.Disposition.TerminalReason,
		}, nil
	default:
		return Realization{}, fmt.Errorf("%w: unknown charge %q", ErrInvalidDisposition, input.Disposition.Charged)
	}
}

func validateRetryDisposition(disposition runstate.Disposition) error {
	if disposition.MovementTerminal || disposition.TerminalReason != "" {
		return fmt.Errorf("%w: charged successor cannot be terminal", ErrInvalidDisposition)
	}
	return nil
}

func nextUnvisitedFallback(fallbacks []string, current string, visited []string) (string, bool) {
	seen := make(map[string]struct{}, len(visited))
	for _, performer := range visited {
		seen[performer] = struct{}{}
	}
	seen[current] = struct{}{}
	for _, performer := range fallbacks {
		if _, alreadyVisited := seen[performer]; !alreadyVisited {
			return performer, true
		}
	}
	return "", false
}
