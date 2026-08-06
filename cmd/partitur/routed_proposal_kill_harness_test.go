package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

// TestBlockedProposalRouteKillCuts drives the recoverable blocking-route R→R
// edge through real partitur subprocesses. The run is killed only after the
// named receipt has notified the parent; the resume is likewise killed only
// after its recovery receipt is durable.
func TestBlockedProposalRouteKillCuts(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("proposal.blocked_route_to_routed/blocked_route", func(t *testing.T) {
		repository, environment := routedProposalKillRepository(t, bin, vendor)
		child := pauseRunAtReceipt(t, partitur, repository, environment, "attempt.blocked")
		killPausedRun(t, child)
		runID := routedProposalRunID(t, repository)
		blocked := routedProposalEvent(t, repository, runID, runstate.EventAttemptBlocked)
		descriptor := blockingRouteDescriptor(t, blocked)
		assertNoEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)

		// The invocation directory is deliberately no longer a valid score. A
		// recovery that re-ran §9 from current inputs could not append the frozen
		// descriptor route below.
		if err := os.WriteFile(filepath.Join(repository, "partitur.yaml"), []byte("not: [a valid score"), 0o600); err != nil {
			t.Fatal(err)
		}
		resumed := pauseCommandAtReceipt(t, partitur, repository, environment, "recovery.amendment.routed_human", "resume", string(runID))
		killPausedRun(t, resumed)
		routed := routedProposalEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)
		assertFrozenRoute(t, descriptor, routed)
		assertSingleProposalRecord(t, repository, runID)

		assertRoutedProposalFixedPoint(t, partitur, repository, environment, runID)
	})
}

func routedProposalKillRepository(t *testing.T, bin, vendor string) (string, []string) {
	t.Helper()
	scoreDocument := runScore()
	scoreDocument["movements"].([]any)[0].(map[string]any)["may_propose"] = true
	compiled, diagnostics := score.CompileValue(scoreDocument)
	if len(diagnostics) != 0 {
		t.Fatalf("compile proposal fixture: %v", diagnostics)
	}
	baseHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	writeValidateInputs(t, repository, scoreDocument, runCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, replaceEnvironment(os.Environ(), map[string]string{
		"HOME":                               t.TempDir(),
		"PATH":                               bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN":                 vendor,
		runVendorEnvironment:                 "1",
		runVendorProposalBaseHashEnvironment: baseHash,
	})
}

func killPausedRun(t *testing.T, child *pausedRun) {
	t.Helper()
	defer child.stop(t)
	if err := child.command.Process.Kill(); err != nil {
		t.Fatalf("kill receipt-paused command: %v", err)
	}
	if err := child.command.Wait(); err == nil {
		t.Fatal("receipt-paused command exited successfully")
	}
	child.commandEnded = true
}

func routedProposalRunID(t *testing.T, repository string) runstate.RunID {
	t.Helper()
	runID, err := soleRunID(repository)
	if err != nil {
		t.Fatal(err)
	}
	return runstate.RunID(runID)
}

func routedProposalEvent(t *testing.T, repository string, runID runstate.RunID, eventType runstate.EventType) runstate.Event {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	var found runstate.Event
	for _, event := range journal.Events {
		if event.Type != eventType {
			continue
		}
		if found.Type != "" {
			t.Fatalf("%s appears more than once", eventType)
		}
		found = event
	}
	if found.Type == "" {
		t.Fatalf("%s is absent from journal: %v", eventType, eventKinds(journal.Events))
	}
	return found
}

func assertNoEvent(t *testing.T, repository string, runID runstate.RunID, eventType runstate.EventType) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type == eventType {
			t.Fatalf("unexpected %s before its cut", eventType)
		}
	}
}

func blockingRouteDescriptor(t *testing.T, blocked runstate.Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(blocked.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	raised, ok := payload["raised"].([]any)
	if !ok || len(raised) != 1 {
		t.Fatalf("blocked raised = %#v", payload["raised"])
	}
	proposal, ok := raised[0].(map[string]any)
	if !ok || proposal["blocking"] != true {
		t.Fatalf("blocked proposal = %#v", raised[0])
	}
	descriptor, ok := proposal["route"].(map[string]any)
	if !ok {
		t.Fatalf("blocked proposal has no route descriptor: %#v", proposal)
	}
	return descriptor
}

func assertFrozenRoute(t *testing.T, descriptor map[string]any, routed runstate.Event) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(routed.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"proposal_record_hash", "reason", "decision_type", "base_revision", "base_hash", "classifier_version", "typed_delta", "actual_impact", "identity_versions"} {
		if !reflect.DeepEqual(payload[key], descriptor[key]) {
			t.Fatalf("routed %s = %#v, want frozen descriptor %#v", key, payload[key], descriptor[key])
		}
	}
}

func assertSingleProposalRecord(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repository, ".partitur", "runs", string(runID), "proposals"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].Type().IsRegular() {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("proposal records = %v, want one frozen regular record", names)
	}
}

func assertRoutedProposalFixedPoint(t *testing.T, binary, repository string, environment []string, runID runstate.RunID) {
	t.Helper()
	journalPath := filepath.Join(repository, ".partitur", "runs", string(runID), "journal.jsonl")
	code, stdout, stderr := runCommandBinary(t, binary, repository, environment, "resume", string(runID))
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("recovery completion exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = runCommandBinary(t, binary, repository, environment, "resume", string(runID))
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("resume fixed point exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(string(before), string(after)) {
		t.Fatal("fixed-point resume appended a durable event")
	}
}
