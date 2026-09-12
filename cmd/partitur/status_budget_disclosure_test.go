package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	statusprojection "github.com/BeomSeogKim/Partitur/internal/status"
)

func TestStatusBudgetDisclosureLeavesTheClampedChargeIntact(t *testing.T) {
	root, store := resumeFixture(t, "")
	if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		_, err := transaction.At("fixture.execution.started").Append(resumeEvent("run-1", runstate.EventExecutionStarted, map[string]any{
			"interval_id": "saturated-interval", "phase": "fixture",
			"wall_start":         time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339Nano),
			"remaining_at_start": 9000000,
		}))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := run([]string{"resume", "run-1"}, &stdout, &stderr)
	// Exit 6 records that after the close charged 9,000,000 ms against the
	// 600,000 ms fixture budget, recovery reached terminalization but could not
	// append run.failed without source authority.
	if code != 6 {
		t.Fatalf("resume exit code = %d, want 6; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	var stopped map[string]any
	for _, event := range journal.Events {
		if event.Type != runstate.EventExecutionStopped {
			continue
		}
		if err := json.Unmarshal(event.Payload, &stopped); err != nil {
			t.Fatal(err)
		}
	}
	if stopped == nil {
		t.Fatal("execution.stopped event not found")
	}
	if stopped["reason"] != "recovered" || stopped["charging"] != "clamped" || stopped["observed_at"] == "" || stopped["charged_duration"] != float64(9000000) {
		t.Fatalf("execution.stopped payload = %v, want recovered clamped charge of 9000000 with observed_at", stopped)
	}

	report, err := statusprojection.Read(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if report.Budget.OpenExecution != nil || report.Budget.ConsumedBudgetMS != 9000000 {
		t.Fatalf("budget disclosure = %+v, want closed execution and consumed_budget_ms=9000000", report.Budget)
	}
	// On main, the clamp half already passes; the disclosure half above is what
	// reds because main has no budget projection.
}
