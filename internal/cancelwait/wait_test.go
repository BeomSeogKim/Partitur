package cancelwait

import (
	"context"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryexec"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestWaiterReplansUntilDriverAcknowledges(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	terminated := false
	result, err := Waiter{
		Execute: func(context.Context) (recoveryexec.Result, error) {
			calls++
			if calls == 1 {
				return recoveryexec.Result{Outcome: recoveryexec.OutcomeRefused}, nil
			}
			return recoveryexec.Result{Outcome: recoveryexec.OutcomeCancelled}, nil
		},
		Observe: func(context.Context) (Owner, error) {
			t.Fatal("waiter observed owner before acknowledgement deadline")
			return Owner{}, nil
		},
		Terminate: func(context.Context, recovery.LeaseIdentity) error {
			terminated = true
			return nil
		},
		Wait: func(context.Context, time.Duration) error {
			now = now.Add(time.Millisecond)
			return nil
		},
		Now:      func() time.Time { return now },
		Deadline: time.Second,
	}.Run(context.Background())
	if err != nil || result.Outcome != recoveryexec.OutcomeCancelled || calls != 2 || terminated {
		t.Fatalf("result=%+v err=%v calls=%d terminated=%t", result, err, calls, terminated)
	}
}

func TestWaiterReturnsTerminalOutcomeBeforeOwnerObservation(t *testing.T) {
	calls := 0
	result, err := Waiter{
		Execute: func(context.Context) (recoveryexec.Result, error) {
			calls++
			return recoveryexec.Result{Outcome: recoveryexec.OutcomeSucceeded}, nil
		},
		Observe: func(context.Context) (Owner, error) {
			t.Fatal("terminal recovery outcome observed an owner")
			return Owner{}, nil
		},
		Terminate: func(context.Context, recovery.LeaseIdentity) error {
			t.Fatal("terminal recovery outcome terminated an owner")
			return nil
		},
	}.Run(context.Background())
	if err != nil || result.Outcome != recoveryexec.OutcomeSucceeded || calls != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
}

func TestWaiterTerminatesOnlyLiveCurrentOwnerAtDeadlineThenReplans(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	var terminated recovery.LeaseIdentity
	result, err := Waiter{
		Execute: func(context.Context) (recoveryexec.Result, error) {
			calls++
			if calls < 3 {
				return recoveryexec.Result{Outcome: recoveryexec.OutcomeRefused}, nil
			}
			return recoveryexec.Result{Outcome: recoveryexec.OutcomeCancelled}, nil
		},
		Observe: func(context.Context) (Owner, error) {
			return Owner{Current: true, State: recovery.OwnerLive, Identity: recovery.LeaseIdentity{
				Epoch: 7, Token: "token", PID: 99, Start: runstate.LinuxStartIdentity{BootID: "boot", StartTicks: "1"},
			}}, nil
		},
		Terminate: func(_ context.Context, identity recovery.LeaseIdentity) error {
			terminated = identity
			return nil
		},
		Wait: func(context.Context, time.Duration) error {
			now = now.Add(time.Second)
			return nil
		},
		Now:      func() time.Time { return now },
		Deadline: time.Second,
	}.Run(context.Background())
	if err != nil || result.Outcome != recoveryexec.OutcomeCancelled || calls != 3 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	if terminated.Epoch != 7 || terminated.Token != "token" || terminated.PID != 99 {
		t.Fatalf("terminated identity = %+v", terminated)
	}
}

func TestWaiterKeepsUnverifiableOwnerWaitingUntilFinalPlanHalt(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	terminated := false
	result, err := Waiter{
		Execute: func(context.Context) (recoveryexec.Result, error) {
			calls++
			return recoveryexec.Result{Outcome: recoveryexec.OutcomeHalted, Decision: recovery.Decision{Halt: recovery.HaltOwnerUnverifiable}}, nil
		},
		Observe: func(context.Context) (Owner, error) {
			return Owner{Current: true, State: recovery.OwnerUnverifiable}, nil
		},
		Terminate: func(context.Context, recovery.LeaseIdentity) error {
			terminated = true
			return nil
		},
		Wait: func(context.Context, time.Duration) error {
			now = now.Add(time.Second)
			return nil
		},
		Now:      func() time.Time { return now },
		Deadline: time.Second,
	}.Run(context.Background())
	if err != nil || result.Outcome != recoveryexec.OutcomeHalted || result.Decision.Halt != recovery.HaltOwnerUnverifiable || calls != 3 || terminated {
		t.Fatalf("result=%+v err=%v calls=%d terminated=%t", result, err, calls, terminated)
	}
}

func TestWaiterReplansAfterTerminalizationRemovesLease(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	waits := 0
	result, err := Waiter{
		Execute: func(context.Context) (recoveryexec.Result, error) {
			calls++
			if calls == 1 {
				return recoveryexec.Result{Outcome: recoveryexec.OutcomeRefused}, nil
			}
			return recoveryexec.Result{Outcome: recoveryexec.OutcomeCancelled}, nil
		},
		Observe: func(context.Context) (Owner, error) {
			return Owner{Current: false}, nil
		},
		Terminate: func(context.Context, recovery.LeaseIdentity) error {
			t.Fatal("lease removed after terminalization must not be terminated")
			return nil
		},
		Wait: func(context.Context, time.Duration) error {
			waits++
			now = now.Add(time.Millisecond)
			return nil
		},
		Now:      func() time.Time { return now },
		Deadline: time.Second,
	}.Run(context.Background())
	if err != nil || result.Outcome != recoveryexec.OutcomeCancelled || calls != 2 || waits != 1 {
		t.Fatalf("result=%+v err=%v calls=%d waits=%d", result, err, calls, waits)
	}
}

func TestWaiterReplansAfterDriverReleaseWithoutTerminalization(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	waits := 0
	result, err := Waiter{
		Execute: func(context.Context) (recoveryexec.Result, error) {
			calls++
			if calls < 3 {
				return recoveryexec.Result{Outcome: recoveryexec.OutcomeRefused}, nil
			}
			return recoveryexec.Result{Outcome: recoveryexec.OutcomeCancelled}, nil
		},
		Observe: func(context.Context) (Owner, error) {
			return Owner{Current: false}, nil
		},
		Terminate: func(context.Context, recovery.LeaseIdentity) error {
			t.Fatal("released lease must not be terminated")
			return nil
		},
		Wait: func(context.Context, time.Duration) error {
			waits++
			now = now.Add(time.Millisecond)
			return nil
		},
		Now:      func() time.Time { return now },
		Deadline: time.Second,
	}.Run(context.Background())
	if err != nil || result.Outcome != recoveryexec.OutcomeCancelled || calls != 3 || waits != 2 {
		t.Fatalf("result=%+v err=%v calls=%d waits=%d", result, err, calls, waits)
	}
}

func TestWaiterDoesNotTerminateLiveOwnerOnStaleLeaseAtDeadline(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	terminated := false
	result, err := Waiter{
		Execute: func(context.Context) (recoveryexec.Result, error) {
			calls++
			if calls == 1 {
				now = now.Add(time.Second)
				return recoveryexec.Result{Outcome: recoveryexec.OutcomeRefused}, nil
			}
			return recoveryexec.Result{Outcome: recoveryexec.OutcomeCancelled}, nil
		},
		Observe: func(context.Context) (Owner, error) {
			return Owner{State: recovery.OwnerLive}, nil
		},
		Terminate: func(context.Context, recovery.LeaseIdentity) error {
			terminated = true
			return nil
		},
		Now:      func() time.Time { return now },
		Deadline: time.Second,
	}.Run(context.Background())
	if err != nil || result.Outcome != recoveryexec.OutcomeCancelled || calls != 2 || terminated {
		t.Fatalf("result=%+v err=%v calls=%d terminated=%t", result, err, calls, terminated)
	}
}

func TestWaiterBoundedWaitReportsContextInterruption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := Waiter{
		Execute: func(context.Context) (recoveryexec.Result, error) {
			return recoveryexec.Result{Outcome: recoveryexec.OutcomeRefused}, nil
		},
		Observe: func(context.Context) (Owner, error) {
			return Owner{Current: true, State: recovery.OwnerLive}, nil
		},
		Terminate: func(context.Context, recovery.LeaseIdentity) error { return nil },
		Wait: func(context.Context, time.Duration) error {
			cancel()
			return context.Canceled
		},
		Deadline: time.Second,
	}.Run(ctx)
	if err != context.Canceled || result.Outcome != "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
