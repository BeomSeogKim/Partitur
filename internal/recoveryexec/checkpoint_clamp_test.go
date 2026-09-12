package recoveryexec

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// The #442 scenario end-to-end at the recovered-close site: an interval opened
// hours ago (a kill then resume-later) with a large remaining budget and a small
// opener-written checkpoint is charged min(checkpoint + 35s, remaining) — never
// the wall-clock gap — and records the audit fields rather than sampling wall time.
func TestRecoveredCloseChargesCheckpointBaselineNotWallClock(t *testing.T) {
	store, driver := handlerStore(t, true)
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{
		"interval_id": "adapter-1", "phase": "adapter",
		"wall_start":         time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339Nano),
		"remaining_at_start": int64(600_000),
	})}, "test.started"); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionElapsedCheckpointed, Payload: handlerPayload(t, map[string]any{
		"interval_id": "adapter-1", "cumulative_elapsed_ms": int64(60_000),
	})}, "test.checkpoint.60000"); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	checkpointID := journal.Events[len(journal.Events)-1].EventID

	if err := closeAdapterInterval(context.Background(),
		HandlerContext{Store: store, Driver: driver, RunID: "run-1"},
		recovery.Action{Kind: recovery.ActionCloseOpenExecutionInterval}); err != nil {
		t.Fatal(err)
	}

	stop := lastStop(t, store)
	if stop["charging"] != "clamped" {
		t.Fatalf("charging = %v, want clamped", stop["charging"])
	}
	if stop["charged_duration"] != float64(95_000) {
		t.Fatalf("charged_duration = %v, want 95000 (checkpoint 60000 + grace 35000), not the wall-clock gap", stop["charged_duration"])
	}
	if stop["accounting_grace_ms"] != float64(35_000) {
		t.Fatalf("accounting_grace_ms = %v, want 35000", stop["accounting_grace_ms"])
	}
	if stop["elapsed_checkpoint_event_id"] != checkpointID {
		t.Fatalf("elapsed_checkpoint_event_id = %v, want %q", stop["elapsed_checkpoint_event_id"], checkpointID)
	}
	if _, present := stop["observed_at"]; present {
		t.Fatalf("clamped close still carries observed_at: %v", stop)
	}
}

// With no checkpoint recorded the recovered close charges the grace alone
// (min(0 + 35s, remaining)) and omits elapsed_checkpoint_event_id, so its absence
// means a zero baseline.
func TestRecoveredCloseWithoutCheckpointChargesGraceOnly(t *testing.T) {
	store, driver := handlerStore(t, true)
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{
		"interval_id": "adapter-1", "phase": "adapter",
		"wall_start":         time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339Nano),
		"remaining_at_start": int64(600_000),
	})}, "test.started"); err != nil {
		t.Fatal(err)
	}

	if err := closeAdapterInterval(context.Background(),
		HandlerContext{Store: store, Driver: driver, RunID: "run-1"},
		recovery.Action{Kind: recovery.ActionCloseOpenExecutionInterval}); err != nil {
		t.Fatal(err)
	}

	stop := lastStop(t, store)
	if stop["charged_duration"] != float64(35_000) {
		t.Fatalf("charged_duration = %v, want 35000 (grace only)", stop["charged_duration"])
	}
	if _, present := stop["elapsed_checkpoint_event_id"]; present {
		t.Fatalf("no-checkpoint close must omit elapsed_checkpoint_event_id: %v", stop)
	}
}

// Site A (recovered acceptance-budget close) reads the same checkpoint baseline.
func TestRecoveredAcceptanceBudgetCloseChargesCheckpointBaseline(t *testing.T) {
	store, driver := handlerStore(t, true)
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionStarted, Payload: handlerPayload(t, map[string]any{
		"interval_id": "acceptance-1", "phase": "acceptance",
		"wall_start":         time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339Nano),
		"remaining_at_start": int64(600_000),
	})}, "test.started"); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventExecutionElapsedCheckpointed, Payload: handlerPayload(t, map[string]any{
		"interval_id": "acceptance-1", "cumulative_elapsed_ms": int64(60_000),
	})}, "test.checkpoint.60000"); err != nil {
		t.Fatal(err)
	}

	if err := closeRecoveredAcceptanceBudgetInterval(
		HandlerContext{Store: store, Driver: driver, RunID: "run-1"},
		recovery.Action{}); err != nil {
		t.Fatal(err)
	}

	stop := lastStop(t, store)
	if stop["reason"] != "budget_exhausted" || stop["charging"] != "clamped" {
		t.Fatalf("acceptance-budget close = %v, want budget_exhausted/clamped", stop)
	}
	if stop["charged_duration"] != float64(95_000) {
		t.Fatalf("charged_duration = %v, want 95000 (checkpoint + grace)", stop["charged_duration"])
	}
	if stop["accounting_grace_ms"] != float64(35_000) {
		t.Fatalf("accounting_grace_ms = %v, want 35000", stop["accounting_grace_ms"])
	}
}

func lastStop(t *testing.T, store *runstore.Store) map[string]any {
	t.Helper()
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	last := journal.Events[len(journal.Events)-1]
	if last.Type != runstate.EventExecutionStopped {
		t.Fatalf("last event = %s, want execution.stopped", last.Type)
	}
	var payload map[string]any
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
