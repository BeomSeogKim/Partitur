package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

func TestStatusNoOperandSkipsUnreadableDiscoveredRun(t *testing.T) {
	root, _ := resumeAttemptFixture(t)
	writeStatusLegacyUnreadableRun(t, root)
	t.Chdir(root)

	code, stdout, stderr := invokeCommand("status")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "Run: run-1 (RUNNING)") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestStatusSelectedUnreadableRunStillHaltsWithExitFive(t *testing.T) {
	root, _ := resumeAttemptFixture(t)
	legacyID := writeStatusLegacyUnreadableRun(t, root)
	t.Chdir(root)

	code, stdout, stderr := invokeCommand("status", legacyID)
	if code != 5 || stdout != "" || !strings.HasPrefix(stderr, "recovery halted:") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

// Application preconditions load every discovered run before treating it as inactive,
// so an unreadable run must continue to block the mutation-adjacent apply scan.
func TestUnreadableRunStillBlocksApplicationPreconditionScan(t *testing.T) {
	root, _ := resumeAttemptFixture(t)
	legacyID := writeStatusLegacyUnreadableRun(t, root)
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.LoadRunInput(runstate.RunID(legacyID))
	if !errors.Is(err, runstore.ErrJournalCorrupt) {
		t.Fatalf("error = %v, want journal corrupt", err)
	}
}

func writeStatusLegacyUnreadableRun(t *testing.T, root string) string {
	t.Helper()
	const legacyID = "legacy-run"
	readableJournal, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	firstLine := strings.SplitN(string(readableJournal), "\n", 2)[0]
	var started map[string]any
	if err := json.Unmarshal([]byte(firstLine), &started); err != nil {
		t.Fatal(err)
	}
	started["run_id"] = legacyID
	started["event_id"] = "11111111111111111111111111111111"
	encodedStarted, err := json.Marshal(started)
	if err != nil {
		t.Fatal(err)
	}
	legacyStopped := `{"event_id":"eae0817fd0427dc951e4d9c2edac15e5","seq":2,"ts":"2026-09-09T11:35:37.138Z","run_id":"legacy-run","score_revision":1,"type":"execution.stopped","causation_id":"a26cbf0304893d0dda4c69c1d0653c5a","payload":{"charged_duration":9000000,"charging":"clamped","interval_id":"01a08513-60dd-78ce-a481-f973e3e78b5d","observed_at":"2026-09-09T11:35:37.135Z","reason":"recovered"}}`
	legacyFailed := `{"event_id":"0996139e3e0eeb784b092d927338e9ed","seq":3,"ts":"2026-09-09T11:35:37.172Z","run_id":"legacy-run","score_revision":1,"type":"run.failed","causation_id":"4b6b0b0a1b56fd14dc7ee53f84b48cca","payload":{"reason":"movement_failed"}}`
	runRoot := filepath.Join(root, ".partitur", "runs", legacyID)
	if err := os.MkdirAll(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := append(encodedStarted, '\n')
	journal = append(journal, legacyStopped...)
	journal = append(journal, '\n')
	journal = append(journal, legacyFailed...)
	journal = append(journal, '\n')
	if err := os.WriteFile(filepath.Join(runRoot, "journal.jsonl"), journal, 0o600); err != nil {
		t.Fatal(err)
	}
	return legacyID
}
