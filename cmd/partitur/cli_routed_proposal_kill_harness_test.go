package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// TestCLIProposalPublishedToRoutedKillCuts drives the ordinary CLI amendment
// route through the two durable transactions named by the E.2 edge. Unlike an
// adapter proposal, this route has no blocking descriptor that could retain an
// otherwise unreferenced proposal record.
func TestCLIProposalPublishedToRoutedKillCuts(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("proposal.published_to_routed/published", func(t *testing.T) {
		repository, environment, runID, patch := cliRoutedProposalKillRepository(t, partitur, bin, vendor)
		child := pauseCommandAtReceipt(t, partitur, repository, environment, "proposal.record.published", "amend", string(runID), "--patch", patch, "--reason", "CLI routed proposal fixture")
		record := proposalPublicationRecord(t, repository, runID)
		assertNoEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)
		assertNoBlockingDescriptorForProposalRecord(t, repository, runID, record)
		killPausedRun(t, child)

		assertRoutedProposalFixedPoint(t, partitur, repository, environment, runID)
		assertNoEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)
		assertNoBlockingDescriptorForProposalRecord(t, repository, runID, record)
		assertProposalPublicationQuarantined(t, record)
	})

	t.Run("proposal.published_to_routed/routed", func(t *testing.T) {
		repository, environment, runID, patch := cliRoutedProposalKillRepository(t, partitur, bin, vendor)
		child := pauseCommandAtReceipt(t, partitur, repository, environment, "amendment.routed_human", "amend", string(runID), "--patch", patch, "--reason", "CLI routed proposal fixture")
		record := proposalPublicationRecord(t, repository, runID)
		routed := routedProposalEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)
		assertNoBlockingDescriptorForProposalRecord(t, repository, runID, record)
		assertCLIRouteBindsProposalRecord(t, routed, record)
		killPausedRun(t, child)

		assertRoutedProposalFixedPoint(t, partitur, repository, environment, runID)
		assertProposalPublicationRetained(t, record)
	})
}

func cliRoutedProposalKillRepository(t *testing.T, partitur, bin, vendor string) (string, []string, runstate.RunID, string) {
	t.Helper()
	repository, environment := humanGateKillHarnessRepository(t, bin, vendor)
	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
	runID := strings.TrimSpace(stdout)
	if code != 0 || runID == "" || stdout != runID+"\n" || stderr != "" {
		t.Fatalf("CLI routed fixture run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	patch := filepath.Join(repository, "cli-routed-proposal.json")
	if err := os.WriteFile(patch, []byte(`[{"op":"replace","path":"/goal","value":"needs-review"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	return repository, environment, runstate.RunID(runID), patch
}

func assertCLIRouteBindsProposalRecord(t *testing.T, routed runstate.Event, record proposalPublicationArtifact) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(routed.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if got, want := payload["proposal_record_hash"], proposalPublicationHash(record.contents); got != want {
		t.Fatalf("CLI route proposal_record_hash=%#v, want %q", got, want)
	}
	if blocking, _ := payload["blocking"].(bool); blocking {
		t.Fatalf("CLI route blocking=%t, want false", blocking)
	}
}

func assertNoBlockingDescriptorForProposalRecord(t *testing.T, repository string, runID runstate.RunID, record proposalPublicationArtifact) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	proposalID := strings.TrimSuffix(filepath.Base(record.path), ".json")
	recordHash := proposalPublicationHash(record.contents)
	for _, event := range journal.Events {
		if event.Type != runstate.EventAttemptBlocked {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		for _, raised := range payload["raised"].([]any) {
			proposal, ok := raised.(map[string]any)
			if !ok || proposal["kind"] != "proposal" || proposal["blocking"] != true || proposal["proposal_id"] != proposalID {
				continue
			}
			route, ok := proposal["route"].(map[string]any)
			if ok && route["proposal_record_hash"] == recordHash {
				t.Fatalf("blocking descriptor retains CLI proposal %q", proposalID)
			}
		}
	}
}
