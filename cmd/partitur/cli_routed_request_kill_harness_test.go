package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// TestCLIRoutedProposalToDecisionRequestedKillCuts cuts the two durable sides
// of a CLI route-to-request sequence. CLI proposals deliberately provide no
// attempt.blocked alternative: recovery can append the missing request only by
// reading amendment.routed_human.
func TestCLIRoutedProposalToDecisionRequestedKillCuts(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("proposal.routed_to_decision_requested/routed", func(t *testing.T) {
		repository, environment, runID, patch := cliRoutedRequestKillRepository(t, partitur, bin, vendor)
		child := pauseCommandAtReceipt(t, partitur, repository, environment, "amendment.routed_human", "amend", string(runID), "--patch", patch, "--reason", "CLI routed request fixture")
		routed := routedProposalEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)
		assertNoRoutedDecisionRequest(t, repository, runID, routed)
		assertNoAttemptBlocked(t, repository, runID)
		killPausedRun(t, child)

		assertRoutedProposalFixedPoint(t, partitur, repository, environment, runID)
		assertNoAttemptBlocked(t, repository, runID)
		assertRoutedDecisionRequest(t, repository, runID, routed)
	})

	t.Run("proposal.routed_to_decision_requested/requested", func(t *testing.T) {
		repository, environment, runID, patch := cliRoutedRequestKillRepository(t, partitur, bin, vendor)
		child := pauseCommandAtReceipt(t, partitur, repository, environment, "amendment.decision.requested", "amend", string(runID), "--patch", patch, "--reason", "CLI routed request fixture")
		routed := routedProposalEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)
		assertNoAttemptBlocked(t, repository, runID)
		assertRoutedDecisionRequest(t, repository, runID, routed)
		killPausedRun(t, child)

		assertRoutedProposalFixedPoint(t, partitur, repository, environment, runID)
		assertNoAttemptBlocked(t, repository, runID)
		assertRoutedDecisionRequest(t, repository, runID, routed)
	})
}

func cliRoutedRequestKillRepository(t *testing.T, partitur, bin, vendor string) (string, []string, runstate.RunID, string) {
	t.Helper()
	repository, environment := killHarnessRepository(t, bin, vendor)
	child := pauseCommandAtReceipt(t, partitur, repository, environment, "authority.driver_lease", "run")
	killPausedRun(t, child)
	runID := routedProposalRunID(t, repository)
	patch := filepath.Join(repository, "cli-routed-request.json")
	if err := os.WriteFile(patch, []byte(`[{"op":"replace","path":"/goal","value":"needs-review"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	return repository, environment, runID, patch
}

func assertNoAttemptBlocked(t *testing.T, repository string, runID runstate.RunID) {
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
		if event.Type == runstate.EventAttemptBlocked {
			t.Fatalf("CLI route journal has attempt.blocked: %+v", event)
		}
	}
}

func assertRoutedDecisionRequest(t *testing.T, repository string, runID runstate.RunID, routed runstate.Event) {
	t.Helper()
	var routedPayload, requestPayload map[string]any
	if err := json.Unmarshal(routed.Payload, &routedPayload); err != nil {
		t.Fatal(err)
	}
	request := routedDecisionRequest(t, repository, runID, routedPayload["proposal_id"])
	requestPayload = request
	for requestField, routedField := range map[string]string{
		"decision_id":   "decision_id",
		"decision_type": "decision_type",
		"proposal_id":   "proposal_id",
		"routed_reason": "reason",
		"blocking":      "blocking",
	} {
		if got, want := requestPayload[requestField], routedPayload[routedField]; got != want {
			t.Fatalf("request %s=%#v, want routed %s=%#v", requestField, got, routedField, want)
		}
	}
	if _, present := routedPayload["emitted_id"]; present {
		t.Fatalf("CLI routed payload unexpectedly has emitted_id: %#v", routedPayload)
	}
	if _, present := requestPayload["emitted_id"]; present {
		t.Fatalf("CLI request unexpectedly has emitted_id: %#v", requestPayload)
	}
}

func assertNoRoutedDecisionRequest(t *testing.T, repository string, runID runstate.RunID, routed runstate.Event) {
	t.Helper()
	var routedPayload map[string]any
	if err := json.Unmarshal(routed.Payload, &routedPayload); err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type != runstate.EventDecisionRequested {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["decision_type"] == "amendment" && payload["proposal_id"] == routedPayload["proposal_id"] {
			t.Fatalf("CLI route already has its decision request: %#v", payload)
		}
	}
}

func routedDecisionRequest(t *testing.T, repository string, runID runstate.RunID, proposalID any) map[string]any {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	for _, event := range journal.Events {
		if event.Type != runstate.EventDecisionRequested {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["decision_type"] != "amendment" || payload["proposal_id"] != proposalID {
			continue
		}
		if request != nil {
			t.Fatalf("CLI route has more than one decision request for proposal %#v", proposalID)
		}
		request = payload
	}
	if request == nil {
		t.Fatalf("CLI route has no decision request for proposal %#v", proposalID)
	}
	return request
}
