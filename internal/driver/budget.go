package driver

import (
	"context"
	"fmt"
)

// ActiveBudgetExhaustedError names the run's own active wall-clock budget as
// the deadline that fired.
type ActiveBudgetExhaustedError struct {
	RemainingAtStartMS int64
}

func (err *ActiveBudgetExhaustedError) Error() string {
	return fmt.Sprintf("active execution budget expired: remaining_at_start_ms=%d; live budget terminalization was not recorded", err.RemainingAtStartMS)
}

func (err *ActiveBudgetExhaustedError) Unwrap() error {
	return context.DeadlineExceeded
}
