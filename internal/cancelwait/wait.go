// Package cancelwait owns cancel's bounded wait for a live lease owner.
package cancelwait

import (
	"context"
	"errors"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryexec"
)

// AcknowledgementDeadline exceeds the 30 second outer termination grace. It
// gives a conforming driver enough time to drain before cancel takes ownership.
const AcknowledgementDeadline = 35 * time.Second

// cancellationRecoveryPassInterval spaces full recovery passes. One pass
// replays the journal and collects external state (including a Git probe), so
// 250 ms avoids treating it like the driver's cheap journal-tail watch while
// adding acceptable acknowledgement latency to a command that already waits
// up to AcknowledgementDeadline. Run caps its final wait at the deadline.
const cancellationRecoveryPassInterval = 250 * time.Millisecond

// Owner is the current lease observation needed only to decide whether the
// deadline may target a process. Planning still remains recovery.Plan's job.
type Owner struct {
	Current  bool
	State    recovery.OwnerState
	Identity recovery.LeaseIdentity
}

// Waiter re-enters recovery from the top until the live owner acknowledges or
// the acknowledgement deadline transfers responsibility to cancel.
//
// The function fields are deliberately narrow test seams. Production supplies
// the real recovery pass and process terminator; tests can drive every window
// without waiting for wall clock time or spawning partitur.
type Waiter struct {
	Execute   func(context.Context) (recoveryexec.Result, error)
	Observe   func(context.Context) (Owner, error)
	Terminate func(context.Context, recovery.LeaseIdentity) error
	Wait      func(context.Context, time.Duration) error
	Now       func() time.Time
	Deadline  time.Duration
}

// Run executes recovery passes until their normal outcome is available. A
// current owner that becomes unverifiable remains in the non-mutating wait
// until the deadline; the final pass then owns the sole owner_unverifiable
// halt site in recovery.Plan.
func (waiter Waiter) Run(ctx context.Context) (recoveryexec.Result, error) {
	if waiter.Execute == nil || waiter.Observe == nil || waiter.Terminate == nil {
		return recoveryexec.Result{}, errors.New("cancel acknowledgement waiter is incomplete")
	}
	if waiter.Wait == nil {
		waiter.Wait = wait
	}
	if waiter.Now == nil {
		waiter.Now = time.Now
	}
	if waiter.Deadline <= 0 {
		waiter.Deadline = AcknowledgementDeadline
	}

	deadline := waiter.Now().Add(waiter.Deadline)
	for {
		result, err := waiter.Execute(ctx)
		if err != nil {
			return recoveryexec.Result{}, err
		}
		if result.Outcome != recoveryexec.OutcomeRefused &&
			!(result.Outcome == recoveryexec.OutcomeHalted && result.Decision.Halt == recovery.HaltOwnerUnverifiable) {
			return result, nil
		}

		now := waiter.Now()
		if now.Before(deadline) {
			remaining := deadline.Sub(now)
			if remaining > cancellationRecoveryPassInterval {
				remaining = cancellationRecoveryPassInterval
			}
			if err := waiter.Wait(ctx, remaining); err != nil {
				return recoveryexec.Result{}, err
			}
			continue
		}

		// Owner identity affects only escalation, which cannot occur before the
		// acknowledgement deadline. Avoid a second recovery observation on each
		// pre-deadline pass.
		owner, err := waiter.Observe(ctx)
		if err != nil {
			return recoveryexec.Result{}, err
		}

		if owner.Current && owner.State == recovery.OwnerLive {
			// Terminate re-verifies the identity before every signal. A changed
			// lease or gone owner is resolved by the final recovery pass below.
			if err := waiter.Terminate(ctx, owner.Identity); err != nil {
				return recoveryexec.Result{}, err
			}
		}
		// The final pass is deliberately the only post-escalation action: it
		// selects RC-RESUME-006 or RC-RESUME-005 from fresh durable facts.
		return waiter.Execute(ctx)
	}
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
