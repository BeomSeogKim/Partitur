package status

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

func TestStatusDisclosesOpenExecutionInterval(t *testing.T) {
	root, runID := statusFixture(t)
	const (
		intervalID = "disclosure-interval"
		phase      = "fixture"
		wallStart  = "2026-09-11T01:02:03.000Z"
		remaining  = int64(456789)
	)
	appendBudgetEvent(t, root, runID, runstate.EventExecutionStarted, map[string]any{ // execution.started
		"interval_id": intervalID, "phase": phase, "wall_start": wallStart, "remaining_at_start": remaining,
	})

	report, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Budget.ConsumedBudgetMS != 0 {
		t.Fatalf("consumed budget = %d, want 0", report.Budget.ConsumedBudgetMS)
	}
	if report.Budget.OpenExecution == nil {
		t.Fatalf("open execution = nil, want interval_id=%q phase=%q wall_start=%q remaining_at_start_ms=%d", intervalID, phase, wallStart, remaining)
	}
	open := report.Budget.OpenExecution
	if open.IntervalID != intervalID || open.Phase != phase || open.WallStart != wallStart || open.RemainingAtStartMS != remaining {
		t.Fatalf("open execution = %+v, want interval_id=%q phase=%q wall_start=%q remaining_at_start_ms=%d", open, intervalID, phase, wallStart, remaining)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range [][]byte{[]byte(`"budget":`), []byte(`"open_execution":`), []byte(`"remaining_at_start_ms":`)} {
		if !bytes.Contains(encoded, key) {
			t.Fatalf("status JSON %s does not contain %s", encoded, key)
		}
	}
}

func TestStatusDisclosesClosedExecutionInterval(t *testing.T) {
	root, runID := statusFixture(t)
	appendBudgetEvent(t, root, runID, runstate.EventExecutionStarted, map[string]any{
		"interval_id": "closed-interval", "phase": "fixture", "wall_start": "2026-09-11T01:02:03.000Z", "remaining_at_start": 500000,
	})
	appendBudgetEvent(t, root, runID, runstate.EventExecutionStopped, map[string]any{
		"interval_id": "closed-interval", "reason": "completed", "charging": "measured", "charged_duration": 123456,
	})
	report, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Budget.OpenExecution != nil {
		t.Fatalf("open execution = %+v, want nil", report.Budget.OpenExecution)
	}
	if report.Budget.ConsumedBudgetMS != 123456 {
		t.Fatalf("consumed budget = %d, want 123456", report.Budget.ConsumedBudgetMS)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"open_execution":null`)) {
		t.Fatalf("status JSON %s does not contain open_execution:null", encoded)
	}
}

func TestStatusBudgetDisclosureTakesNoClockSample(t *testing.T) {
	root, runID := statusFixture(t)
	appendBudgetEvent(t, root, runID, runstate.EventExecutionStarted, map[string]any{
		"interval_id": "stable-interval", "phase": "fixture", "wall_start": "2026-09-11T01:02:03.000Z", "remaining_at_start": 500000,
	})
	first, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	// Byte stability makes the absence of a clock observable outside this
	// package: time.Now anywhere in the projection would change two reads of
	// the same unchanged journal.
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("unchanged status reads differ:\nfirst:  %s\nsecond: %s", firstJSON, secondJSON)
	}
}

func appendBudgetEvent(t *testing.T, root, runID string, eventType runstate.EventType, payload map[string]any) {
	t.Helper()
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate(runstate.RunID(runID), "", func(transaction *runstore.Txn) error {
		_, err := transaction.At("status.budget.fixture").Append(runstate.Event{RunID: runstate.RunID(runID), ScoreRevision: 1, Type: eventType, Payload: encoded})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
