package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// TestRoutedAmendmentHumanRejectionKillCut proves the command's amendment
// branch against a real blocking adapter proposal. It fails if approve emits
// decision.resolved, loses a routed binding, permits an empty reason to append,
// or recovery stops treating the released blocked attempt as decision_resume.
func TestRoutedAmendmentHumanRejectionKillCut(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	repository, environment := routedProposalKillRepository(t, bin, vendor)
	blocked := pauseRunAtReceipt(t, partitur, repository, environment, "attempt.blocked")
	killPausedRun(t, blocked)
	runID := routedProposalRunID(t, repository)

	routed := pauseCommandAtReceipt(t, partitur, repository, environment, "recovery.amendment.routed_human", "resume", string(runID))
	killPausedRun(t, routed)
	assertRoutedProposalFixedPoint(t, partitur, repository, environment, runID)

	route := routedProposalEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)
	requestPayload := routedDecisionRequest(t, repository, runID, amendmentHumanRejectionPayload(t, route)["proposal_id"])
	decisionID, ok := requestPayload["decision_id"].(string)
	if !ok || decisionID == "" || requestPayload["decision_type"] != "amendment" || requestPayload["blocking"] != true {
		t.Fatalf("routed decision request = %#v, want blocking amendment", requestPayload)
	}
	assertAmendmentRejectionRequiresReason(t, partitur, repository, environment, runID, decisionID)

	rejected := pauseCommandAtReceipt(t, partitur, repository, environment, "amendment.human_rejected", "approve", decisionID, "--reject", "--reason", "operator rejected the change")
	killPausedRun(t, rejected)

	rejection := routedProposalEvent(t, repository, runID, runstate.EventAmendmentHumanRejected)
	assertAmendmentHumanRejection(t, route, requestPayload, rejection)
	assertNoDecisionResolved(t, repository, runID)
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Projection.State.PendingDecisions) != 0 || input.Projection.State.Run != runstate.RunRunning {
		t.Fatalf("rejected amendment projection = run:%s pending:%+v, want RUNNING without pending decisions", input.Projection.State.Run, input.Projection.State.PendingDecisions)
	}

	resumed := pauseCommandAtReceipt(t, partitur, repository, environment, "attempt.performer_selected", "resume", string(runID))
	assertDecisionResumeSelected(t, repository, runID, rejection.EventID)
	killPausedRun(t, resumed)
}

func assertAmendmentRejectionRequiresReason(t *testing.T, binary, repository string, environment []string, runID runstate.RunID, decisionID string) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCommandBinary(t, binary, repository, environment, "approve", decisionID, "--reject")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "usage error:") || !strings.Contains(stderr, "amendment decision") {
		t.Fatalf("reasonless amendment rejection exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	after, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events) {
		t.Fatalf("reasonless amendment rejection appended %d events", len(after.Events)-len(before.Events))
	}
}

func amendmentHumanRejectionPayload(t *testing.T, event runstate.Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func assertAmendmentHumanRejection(t *testing.T, routed runstate.Event, requestPayload map[string]any, rejection runstate.Event) {
	t.Helper()
	routePayload := amendmentHumanRejectionPayload(t, routed)
	rejectionPayload := amendmentHumanRejectionPayload(t, rejection)
	for _, field := range []string{"proposal_id", "decision_id"} {
		if got, want := rejectionPayload[field], routePayload[field]; got != want {
			t.Fatalf("rejection %s=%#v, want routed %s=%#v", field, got, field, want)
		}
	}
	if got, want := rejectionPayload["decision_id"], requestPayload["decision_id"]; got != want {
		t.Fatalf("rejection decision_id=%#v, want requested decision_id=%#v", got, want)
	}
	for _, field := range []string{"base_revision", "base_hash", "classifier_version", "identity_versions"} {
		if got, want := rejectionPayload[field], routePayload[field]; !reflect.DeepEqual(got, want) {
			t.Fatalf("rejection %s=%#v, want routed %s=%#v", field, got, field, want)
		}
	}
	if rejectionPayload["human_reason"] != "operator rejected the change" {
		t.Fatalf("rejection human_reason=%#v", rejectionPayload["human_reason"])
	}
}

func assertNoDecisionResolved(t *testing.T, repository string, runID runstate.RunID) {
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
		if event.Type == runstate.EventDecisionResolved {
			t.Fatalf("amendment rejection appended forbidden decision.resolved: %+v", event)
		}
	}
}

func assertDecisionResumeSelected(t *testing.T, repository string, runID runstate.RunID, rejectionEventID string) {
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
		if event.Type != runstate.EventPerformerSelected {
			continue
		}
		payload := amendmentHumanRejectionPayload(t, event)
		if payload["reason"] == "decision_resume" && event.CausationID == rejectionEventID {
			return
		}
	}
	t.Fatalf("recovery did not select RC-RESUME-041 from amendment.human_rejected %q: %v", rejectionEventID, eventKinds(journal.Events))
}
