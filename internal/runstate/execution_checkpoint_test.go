package runstate

import (
	"errors"
	"strings"
	"testing"
)

// openAdapterInterval returns a RUNNING state whose adapter budget interval is
// open with the given remaining budget, ready for checkpoint and close events.
func openAdapterInterval(t *testing.T, remaining int64) State {
	t.Helper()
	state := NewState([]MovementSeed{{ID: "m1", Initial: MovementPending}})
	state.Run = RunRunning
	next, err := Apply(state, fixtureEvent(EventExecutionStarted, map[string]any{
		"interval_id":        "i1",
		"phase":              "adapter",
		"wall_start":         "2026-07-26T00:00:00.000Z",
		"remaining_at_start": remaining,
	}, nil))
	if err != nil {
		t.Fatalf("open interval: %v", err)
	}
	return next
}

func TestClampedChargeIsCheckpointPlusGraceBoundedByRemaining(t *testing.T) {
	cases := []struct {
		name       string
		checkpoint int64
		remaining  int64
		want       int64
	}{
		{"checkpoint plus grace under remaining", 60_000, 600_000, 95_000},
		{"no checkpoint charges grace only", 0, 600_000, 35_000},
		{"checkpoint plus grace clamped to remaining", 600_000, 100_000, 100_000},
		{"grace alone exceeds a tiny remaining", 0, 10_000, 10_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampedCharge(tc.checkpoint, tc.remaining); got != tc.want {
				t.Fatalf("ClampedCharge(%d,%d) = %d, want %d", tc.checkpoint, tc.remaining, got, tc.want)
			}
		})
	}
	if AccountingGraceMS != 35_000 {
		t.Fatalf("AccountingGraceMS = %d, want 35000", AccountingGraceMS)
	}
}

// Dedicated single-assertion locks (no subtests) so the mutation harness can
// target exactly one failing test per source mutation.
func TestClampedChargeAddsTheAccountingGrace(t *testing.T) {
	if got := ClampedCharge(0, 600_000); got != 35_000 {
		t.Fatalf("ClampedCharge(0,600000) = %d, want 35000 (grace)", got)
	}
	if got := ClampedCharge(60_000, 600_000); got != 95_000 {
		t.Fatalf("ClampedCharge(60000,600000) = %d, want 95000 (checkpoint + grace)", got)
	}
}

func TestClampedChargeIsBoundedByRemaining(t *testing.T) {
	if got := ClampedCharge(600_000, 100_000); got != 100_000 {
		t.Fatalf("ClampedCharge(600000,100000) = %d, want 100000 (bounded by remaining)", got)
	}
}

func TestClampedCloseFieldsCarryCheckpointBaselineAndGrace(t *testing.T) {
	withCheckpoint := ClampedCloseFields(&ExecutionInterval{
		ID: "i1", RemainingAtStart: 600_000,
		LatestCheckpointMS: 60_000, LatestCheckpointEventID: "ckpt-3",
	})
	if withCheckpoint["charging"] != "clamped" {
		t.Fatalf("charging = %v, want clamped", withCheckpoint["charging"])
	}
	if withCheckpoint["charged_duration"] != int64(95_000) {
		t.Fatalf("charged_duration = %v, want 95000", withCheckpoint["charged_duration"])
	}
	if withCheckpoint["accounting_grace_ms"] != int64(35_000) {
		t.Fatalf("accounting_grace_ms = %v, want 35000", withCheckpoint["accounting_grace_ms"])
	}
	if withCheckpoint["elapsed_checkpoint_event_id"] != "ckpt-3" {
		t.Fatalf("elapsed_checkpoint_event_id = %v, want ckpt-3", withCheckpoint["elapsed_checkpoint_event_id"])
	}

	// A zero baseline (no checkpoint recorded) omits elapsed_checkpoint_event_id
	// so that its absence means exactly that (§6).
	noCheckpoint := ClampedCloseFields(&ExecutionInterval{ID: "i1", RemainingAtStart: 600_000})
	if noCheckpoint["charged_duration"] != int64(35_000) {
		t.Fatalf("no-checkpoint charged_duration = %v, want 35000", noCheckpoint["charged_duration"])
	}
	if _, present := noCheckpoint["elapsed_checkpoint_event_id"]; present {
		t.Fatalf("no-checkpoint close must omit elapsed_checkpoint_event_id: %v", noCheckpoint)
	}
	if noCheckpoint["accounting_grace_ms"] != int64(35_000) {
		t.Fatalf("no-checkpoint accounting_grace_ms = %v, want 35000", noCheckpoint["accounting_grace_ms"])
	}
}

func TestElapsedCheckpointedRequiresMatchingOpenInterval(t *testing.T) {
	// No interval open.
	state := NewState([]MovementSeed{{ID: "m1", Initial: MovementPending}})
	state.Run = RunRunning
	if _, err := Apply(state, fixtureEvent(EventExecutionElapsedCheckpointed, map[string]any{
		"interval_id": "i1", "cumulative_elapsed_ms": 5_000,
	}, nil)); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("checkpoint without open interval error = %v, want illegal transition", err)
	}

	open := openAdapterInterval(t, 600_000)
	// Mismatched interval id.
	if _, err := Apply(open, fixtureEvent(EventExecutionElapsedCheckpointed, map[string]any{
		"interval_id": "other", "cumulative_elapsed_ms": 5_000,
	}, nil)); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("checkpoint for a foreign interval error = %v, want illegal transition", err)
	}
}

func TestElapsedCheckpointedRecordsLatestBaselineAndMustStrictlyIncrease(t *testing.T) {
	state := openAdapterInterval(t, 600_000)

	first, err := Apply(state, fixtureEvent(EventExecutionElapsedCheckpointed, map[string]any{
		"interval_id": "i1", "cumulative_elapsed_ms": 5_000,
	}, func(event *Event) { event.EventID = "ckpt-1"; event.CausationID = "event-1" }))
	if err != nil {
		t.Fatalf("first checkpoint: %v", err)
	}
	if first.OpenExecution.LatestCheckpointMS != 5_000 || first.OpenExecution.LatestCheckpointEventID != "ckpt-1" {
		t.Fatalf("first checkpoint projection = %+v", first.OpenExecution)
	}
	if first.ConsumedBudgetMS != 0 {
		t.Fatalf("checkpoint changed consumed budget to %d, want 0", first.ConsumedBudgetMS)
	}

	second, err := Apply(first, fixtureEvent(EventExecutionElapsedCheckpointed, map[string]any{
		"interval_id": "i1", "cumulative_elapsed_ms": 10_000,
	}, func(event *Event) { event.EventID = "ckpt-2"; event.CausationID = "event-1" }))
	if err != nil {
		t.Fatalf("second checkpoint: %v", err)
	}
	if second.OpenExecution.LatestCheckpointMS != 10_000 || second.OpenExecution.LatestCheckpointEventID != "ckpt-2" {
		t.Fatalf("second checkpoint projection = %+v", second.OpenExecution)
	}

	// A non-increasing cumulative is illegal (strictly increases per §6).
	for _, cumulative := range []int64{10_000, 9_999} {
		if _, err := Apply(second, fixtureEvent(EventExecutionElapsedCheckpointed, map[string]any{
			"interval_id": "i1", "cumulative_elapsed_ms": cumulative,
		}, func(event *Event) { event.EventID = "ckpt-x" })); err == nil ||
			!strings.Contains(err.Error(), "strictly increase") {
			t.Fatalf("non-increasing checkpoint (%d) error = %v, want strictly-increase rejection", cumulative, err)
		}
	}
}

// The #442 scenario at the projection layer: an interval whose latest checkpoint
// is small but whose wall clock has advanced by hours is charged the checkpoint
// baseline plus grace, never the wall-clock gap. The recovery close records that
// clamped charge and replay reproduces it exactly.
func TestClampedCloseChargesCheckpointBaselineNotWallGap(t *testing.T) {
	state := openAdapterInterval(t, 600_000)
	checkpointed, err := Apply(state, fixtureEvent(EventExecutionElapsedCheckpointed, map[string]any{
		"interval_id": "i1", "cumulative_elapsed_ms": 60_000,
	}, func(event *Event) { event.EventID = "ckpt-1"; event.CausationID = "event-1" }))
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}

	fields := ClampedCloseFields(checkpointed.OpenExecution)
	fields["interval_id"] = "i1"
	fields["reason"] = "recovered"
	closed, err := Apply(checkpointed, fixtureEvent(EventExecutionStopped, fields, func(event *Event) { event.EventID = "stop-1" }))
	if err != nil {
		t.Fatalf("clamped close: %v", err)
	}
	if closed.ConsumedBudgetMS != 95_000 {
		t.Fatalf("consumed budget = %d, want 95000 (checkpoint 60000 + grace 35000), not the wall-clock gap", closed.ConsumedBudgetMS)
	}
	if closed.OpenExecution != nil {
		t.Fatalf("interval remained open after close: %+v", closed.OpenExecution)
	}
}

func TestElapsedCheckpointedPayloadValidation(t *testing.T) {
	state := openAdapterInterval(t, 600_000)
	// Negative cumulative is malformed.
	if _, err := Apply(state, fixtureEvent(EventExecutionElapsedCheckpointed, map[string]any{
		"interval_id": "i1", "cumulative_elapsed_ms": -1,
	}, nil)); err == nil || !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("negative cumulative error = %v, want non-negative rejection", err)
	}
	// Missing cumulative field is malformed.
	if _, err := Apply(state, fixtureEvent(EventExecutionElapsedCheckpointed, map[string]any{
		"interval_id": "i1",
	}, nil)); err == nil {
		t.Fatal("checkpoint without cumulative_elapsed_ms must be rejected")
	}
}

func TestExecutionStoppedClampedRequiresGraceAndForbidsMeasuredCheckpointID(t *testing.T) {
	state := openAdapterInterval(t, 600_000)

	// A clamped close without accounting_grace_ms is malformed.
	if _, err := Apply(state, fixtureEvent(EventExecutionStopped, map[string]any{
		"interval_id": "i1", "reason": "recovered", "charging": "clamped", "charged_duration": 35_000,
	}, nil)); err == nil || !strings.Contains(err.Error(), "accounting_grace_ms") {
		t.Fatalf("clamped close without grace error = %v, want grace requirement", err)
	}

	// A measured close carrying elapsed_checkpoint_event_id is malformed.
	if _, err := Apply(state, fixtureEvent(EventExecutionStopped, map[string]any{
		"interval_id": "i1", "reason": "normal", "charging": "measured", "charged_duration": 1,
		"elapsed_checkpoint_event_id": "ckpt-1",
	}, nil)); err == nil || !strings.Contains(err.Error(), "elapsed_checkpoint_event_id") {
		t.Fatalf("measured close with checkpoint id error = %v, want rejection", err)
	}

	// A measured close carrying accounting_grace_ms is malformed.
	if _, err := Apply(state, fixtureEvent(EventExecutionStopped, map[string]any{
		"interval_id": "i1", "reason": "normal", "charging": "measured", "charged_duration": 1,
		"accounting_grace_ms": 35_000,
	}, nil)); err == nil || !strings.Contains(err.Error(), "accounting_grace_ms") {
		t.Fatalf("measured close with grace error = %v, want rejection", err)
	}
}
