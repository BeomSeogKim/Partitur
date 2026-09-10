package driver

import (
	"context"
	"errors"
	"fmt"
)

// ActiveBudgetExhaustedError names the run's own active wall-clock budget as
// the deadline that fired. It is a diagnostic signal only: the attempt was not
// terminalized, the outcome stays INTERRUPTED, and the terminal chain required
// by DESIGN.md:2338-2350 remains unimplemented under issue #447.
type ActiveBudgetExhaustedError struct {
	RemainingAtStartMS int64
}

func (err *ActiveBudgetExhaustedError) Error() string {
	return fmt.Sprintf("active execution budget expired: remaining_at_start_ms=%d; live budget terminalization was not recorded", err.RemainingAtStartMS)
}

func (err *ActiveBudgetExhaustedError) Unwrap() error {
	return context.DeadlineExceeded
}

// activeBudgetError names the run's own active budget as the reason the adapter context
// ended. It substitutes only when the adapter reported a deadline AND the deadline that
// fired is the one this call installed. A parent cancellation, or an enclosing deadline
// that is not ours, keeps its ordinary interruption error.
func activeBudgetError(err, cause error) error {
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var exhausted *ActiveBudgetExhaustedError
	if !errors.As(cause, &exhausted) {
		return err
	}
	return exhausted
}
