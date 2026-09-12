package runstore

import (
	"encoding/json"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// Site C (control-channel close): controlStopEvent clamps a cancellation close to
// the opener's latest checkpoint plus grace and records the audit fields, rather
// than the wall-clock gap between wall_start and now (#442).
func TestControlStopEventClampsToCheckpointBaseline(t *testing.T) {
	store := recoveryStore(t)
	appendRecoveryMovementStarted(t, store)
	appendRecoveryEvent(t, store, runstate.Event{
		RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted,
		Payload: recoveryPayload(t, map[string]any{
			"interval_id": "adapter-1", "phase": "adapter",
			"wall_start": "2026-07-28T00:00:00.000Z", "remaining_at_start": 600000,
		}),
	})
	checkpoint := appendRecoveryEvent(t, store, runstate.Event{
		RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionElapsedCheckpointed,
		Payload: recoveryPayload(t, map[string]any{
			"interval_id": "adapter-1", "cumulative_elapsed_ms": 60000,
		}),
	})

	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	state := input.Projection.State
	if state.OpenExecution == nil || state.OpenExecution.LatestCheckpointMS != 60000 {
		t.Fatalf("projected open interval = %+v, want checkpoint 60000", state.OpenExecution)
	}

	var payload map[string]any
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		event, err := controlStopEvent(transaction, state, "run-1", "cancelled")
		if err != nil {
			return err
		}
		if event.CausationID == "" {
			t.Fatalf("control stop event missing causation to execution.started")
		}
		return json.Unmarshal(event.Payload, &payload)
	}); err != nil {
		t.Fatal(err)
	}

	if payload["reason"] != "cancelled" || payload["charging"] != "clamped" {
		t.Fatalf("control stop payload = %v, want cancelled/clamped", payload)
	}
	if payload["charged_duration"] != float64(95000) {
		t.Fatalf("charged_duration = %v, want 95000 (checkpoint 60000 + grace 35000)", payload["charged_duration"])
	}
	if payload["accounting_grace_ms"] != float64(35000) {
		t.Fatalf("accounting_grace_ms = %v, want 35000", payload["accounting_grace_ms"])
	}
	if payload["elapsed_checkpoint_event_id"] != checkpoint.Mutation.EventID {
		t.Fatalf("elapsed_checkpoint_event_id = %v, want %q", payload["elapsed_checkpoint_event_id"], checkpoint.Mutation.EventID)
	}
	if _, present := payload["observed_at"]; present {
		t.Fatalf("clamped control close still carries observed_at: %v", payload)
	}
}
