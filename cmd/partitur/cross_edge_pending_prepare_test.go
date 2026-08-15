package main

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

type crossEdgePrepareObservation struct {
	prepared  int
	terminal  int
	approved  int
	pendingID runstate.ProposalID
}

// observeCrossEdgePrepareState replays the ordered journal domain used by the
// cross-edge check. It deliberately retains proposal identity: approvals for
// two different proposals are not a duplicate approval.
func observeCrossEdgePrepareState(events []runstate.Event) (crossEdgePrepareObservation, error) {
	observation := crossEdgePrepareObservation{}
	approvalCounts := make(map[runstate.ProposalID]int)
	for index, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return observation, fmt.Errorf("event %d %s payload: %w", index, event.Type, err)
		}
		proposalID, _ := payload["proposal_id"].(string)
		switch event.Type {
		case runstate.EventAmendmentApprovalPrepared, runstate.EventAmendmentApprovalAbandoned, runstate.EventAmendmentApproved:
			if proposalID == "" {
				return observation, fmt.Errorf("event %d %s has empty proposal_id", index, event.Type)
			}
		}
		switch event.Type {
		case runstate.EventAmendmentApprovalPrepared:
			observation.prepared++
			if observation.pendingID != "" {
				return observation, fmt.Errorf("event %d prepares proposal %q while proposal %q is pending", index, proposalID, observation.pendingID)
			}
			observation.pendingID = runstate.ProposalID(proposalID)
		case runstate.EventAmendmentApprovalAbandoned:
			observation.terminal++
			if observation.pendingID != runstate.ProposalID(proposalID) {
				return observation, fmt.Errorf("event %d abandons proposal %q while proposal %q is pending", index, proposalID, observation.pendingID)
			}
			observation.pendingID = ""
		case runstate.EventAmendmentApproved:
			observation.terminal++
			observation.approved++
			if observation.pendingID != runstate.ProposalID(proposalID) {
				return observation, fmt.Errorf("event %d approves proposal %q while proposal %q is pending", index, proposalID, observation.pendingID)
			}
			approvalCounts[runstate.ProposalID(proposalID)]++
			if approvalCounts[runstate.ProposalID(proposalID)] > 1 {
				return observation, fmt.Errorf("event %d is approval %d for proposal %q", index, approvalCounts[runstate.ProposalID(proposalID)], proposalID)
			}
			observation.pendingID = ""
		}
	}
	return observation, nil
}

func TestCrossEdgePrepareCheckProductionSubprocess(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	repository, environment, runID, driver, approver := preparedHumanApproval(t, partitur, bin, vendor)
	defer driver.stop(t)
	defer approver.stop(t)
	assertCrossEdgePrepareObservation(t, repository, runID, 1, 0, 0, true)

	driver.kill(t)
	killPausedRun(t, approver)
	assertCrossEdgePrepareObservation(t, repository, runID, 1, 0, 0, true)

	assertRecoveryFixedPoint(t, partitur, repository, environment, string(runID), nil, fixedPointNoneFixture)
	assertCrossEdgePrepareObservation(t, repository, runID, 1, 1, 1, false)
}

func assertCrossEdgePrepareObservation(t *testing.T, repository string, runID runstate.RunID, prepared, terminal, approved int, pending bool) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := observeCrossEdgePrepareState(journal.Events)
	if err != nil {
		t.Fatal(err)
	}
	if observation.prepared != prepared || observation.terminal != terminal || observation.approved != approved || (observation.pendingID != "") != pending {
		t.Fatalf("cross-edge observation=%+v, want prepared=%d terminal=%d approved=%d pending=%t", observation, prepared, terminal, approved, pending)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if (input.Projection.State.PendingPrepare != nil) != pending {
		t.Fatalf("exported pending prepare=%+v, want pending=%t", input.Projection.State.PendingPrepare, pending)
	}
}

func TestPendingPrepareCrossEdgeOracleFixtures(t *testing.T) {
	valid := []runstate.Event{
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalPrepared, "proposal-1"),
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalAbandoned, "proposal-1"),
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalPrepared, "proposal-2"),
		crossEdgePrepareEvent(runstate.EventAmendmentApproved, "proposal-2"),
	}
	observation, err := observeCrossEdgePrepareState(valid)
	if err != nil || observation.prepared != 2 || observation.terminal != 2 || observation.pendingID != "" {
		t.Fatalf("positive control observation=%+v error=%v", observation, err)
	}
	invalid := []runstate.Event{
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalPrepared, "proposal-1"),
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalPrepared, "proposal-2"),
	}
	if _, err := observeCrossEdgePrepareState(invalid); err == nil {
		t.Fatal("two simultaneously pending prepares passed")
	}
}

func TestCrossEdgePreparePayloadDecodeOracleFixture(t *testing.T) {
	invalid := []runstate.Event{{
		Type:    runstate.EventAmendmentApprovalPrepared,
		Payload: json.RawMessage(`{"proposal_id":`),
	}}
	if _, err := observeCrossEdgePrepareState(invalid); err == nil {
		t.Fatal("malformed cross-edge payload passed")
	}
}

func TestCrossEdgePrepareNonemptyProposalIDOracleFixtures(t *testing.T) {
	fixtures := map[string][]runstate.Event{
		"prepare": {
			crossEdgePrepareEvent(runstate.EventAmendmentApprovalPrepared, ""),
		},
		"abandon": {
			crossEdgePrepareEvent(runstate.EventAmendmentApprovalAbandoned, ""),
		},
		"approve": {
			crossEdgePrepareEvent(runstate.EventAmendmentApproved, ""),
		},
	}
	for name, invalid := range fixtures {
		t.Run(name, func(t *testing.T) {
			if _, err := observeCrossEdgePrepareState(invalid); err == nil {
				t.Fatal("empty proposal_id passed")
			}
		})
	}
}

func TestCrossEdgePrepareAbandonOrderingOracleFixture(t *testing.T) {
	invalid := []runstate.Event{
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalPrepared, "proposal-1"),
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalAbandoned, "proposal-2"),
	}
	if _, err := observeCrossEdgePrepareState(invalid); err == nil {
		t.Fatal("abandon for a different proposal passed")
	}
}

func TestCrossEdgePrepareApprovalOrderingOracleFixture(t *testing.T) {
	invalid := []runstate.Event{
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalPrepared, "proposal-1"),
		crossEdgePrepareEvent(runstate.EventAmendmentApproved, "proposal-2"),
	}
	if _, err := observeCrossEdgePrepareState(invalid); err == nil {
		t.Fatal("approval for a different proposal passed")
	}
}

func TestApprovalPerProposalCrossEdgeOracleFixtures(t *testing.T) {
	valid := []runstate.Event{
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalPrepared, "proposal-1"),
		crossEdgePrepareEvent(runstate.EventAmendmentApproved, "proposal-1"),
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalPrepared, "proposal-2"),
		crossEdgePrepareEvent(runstate.EventAmendmentApproved, "proposal-2"),
	}
	observation, err := observeCrossEdgePrepareState(valid)
	if err != nil || observation.approved != 2 || observation.prepared != 2 || observation.terminal != 2 {
		t.Fatalf("different-proposal positive control observation=%+v error=%v", observation, err)
	}
	invalid := append([]runstate.Event{}, valid[:2]...)
	invalid = append(invalid,
		crossEdgePrepareEvent(runstate.EventAmendmentApprovalPrepared, "proposal-1"),
		crossEdgePrepareEvent(runstate.EventAmendmentApproved, "proposal-1"),
	)
	if _, err := observeCrossEdgePrepareState(invalid); err == nil {
		t.Fatal("two approvals for one proposal passed")
	}
}

func crossEdgePrepareEvent(eventType runstate.EventType, proposalID string) runstate.Event {
	payload, _ := json.Marshal(map[string]any{"proposal_id": proposalID})
	return runstate.Event{Type: eventType, Payload: payload}
}
