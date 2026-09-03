package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestDraftFinalizationLifecycle(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment, runID := finalizationReady(t, partitur, bin, vendor)
	code, _, stderr := 0, "", ""
	finalizationRoute := pauseCommandAtReceipt(t, partitur, repository, environment, "amendment.routed_human", "resume", string(runID))
	killPausedRun(t, finalizationRoute)
	finalizationRequest := pauseCommandAtReceipt(t, partitur, repository, environment, "recovery.decision.requested.finalization", "resume", string(runID))
	killPausedRun(t, finalizationRequest)
	code, _, stderr = runCommandBinary(t, partitur, repository, environment, "resume", string(runID))
	if code != 0 || stderr != "" {
		t.Fatalf("finalization fixed-point resume exit=%d stderr=%q", code, stderr)
	}
	journal := readDraftSchedulingJournal(t, repository, runID)
	routed, request := finalizationRouteAndRequest(t, journal)
	if routed != 1 || request == "" {
		t.Fatalf("finalization route=%d decision=%q", routed, request)
	}
	code, _, stderr = runCommandBinary(t, partitur, repository, environment, "approve", request, "--approve")
	if code != 0 || stderr != expectedDecisionResumeHint(string(runID)) {
		t.Fatalf("finalization approve exit=%d stderr=%q", code, stderr)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Movements[runstate.MovementID(input.Score.DraftInterviewMovement())] != runstate.MovementSucceeded {
		t.Fatalf("interview state=%s, want SUCCEEDED", input.Projection.State.Movements[runstate.MovementID(input.Score.DraftInterviewMovement())])
	}
	for _, event := range readDraftSchedulingJournal(t, repository, runID) {
		if event.Type == runstate.EventAttemptCompleted || event.Type == runstate.EventVerificationPassed || event.Type == runstate.EventAcceptanceStarted {
			t.Fatalf("finalization appended forbidden event %s", event.Type)
		}
	}
}

func TestCoreFinalizationPublishedToRoutedKillCuts(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("published reconstructs after quarantine", func(t *testing.T) {
		repository, environment, runID := finalizationReady(t, partitur, bin, vendor)
		published := pauseCommandAtReceipt(t, partitur, repository, environment, "proposal.record.published", "resume", string(runID))
		original := proposalPublicationRecord(t, repository, runID)
		killPausedRun(t, published)
		assertNoEvent(t, repository, runID, runstate.EventAmendmentRoutedHuman)
		routed := pauseCommandAtReceipt(t, partitur, repository, environment, "amendment.routed_human", "resume", string(runID))
		killPausedRun(t, routed)
		assertProposalPublicationQuarantined(t, original)
		if got, _ := finalizationRouteAndRequest(t, readDraftSchedulingJournal(t, repository, runID)); got != 1 {
			t.Fatalf("reconstructed finalization routes=%d, want 1", got)
		}
	})

	t.Run("routed retains and second resume is fixed", func(t *testing.T) {
		repository, environment, runID := finalizationReady(t, partitur, bin, vendor)
		routed := pauseCommandAtReceipt(t, partitur, repository, environment, "amendment.routed_human", "resume", string(runID))
		record := proposalPublicationRecord(t, repository, runID)
		killPausedRun(t, routed)
		request := pauseCommandAtReceipt(t, partitur, repository, environment, "recovery.decision.requested.finalization", "resume", string(runID))
		killPausedRun(t, request)
		code, _, stderr := runCommandBinary(t, partitur, repository, environment, "resume", string(runID))
		if code != 0 || stderr != "" {
			t.Fatalf("fixed-point resume exit=%d stderr=%q", code, stderr)
		}
		assertProposalPublicationRetained(t, record)
		if got, _ := finalizationRouteAndRequest(t, readDraftSchedulingJournal(t, repository, runID)); got != 1 {
			t.Fatalf("fixed-point finalization routes=%d, want 1", got)
		}
	})
}

func TestFinalizationProducerRoutesExactlyOnce(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment, runID := finalizationReady(t, partitur, bin, vendor)
	type outcome struct {
		code   int
		stderr string
	}
	done := make(chan outcome, 1)
	go func() {
		code, _, stderr := runCommandBinary(t, partitur, repository, environment, "resume", string(runID))
		done <- outcome{code: code, stderr: stderr}
	}()
	commandResult := outcome{}
	returned := false
	select {
	case commandResult = <-done:
		returned = true
	case <-time.After(5 * time.Second):
	}
	routes, _ := finalizationRouteAndRequest(t, readDraftSchedulingJournal(t, repository, runID))
	entries, err := os.ReadDir(filepath.Join(repository, ".partitur", "runs", string(runID), "proposals"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if routes != 1 || len(entries) != 1 {
		t.Fatalf("finalization routes=%d records=%d, want 1", routes, len(entries))
	}
	if returned && (commandResult.code != 0 || commandResult.stderr != "") {
		t.Fatalf("finalization producer exit=%d stderr=%q", commandResult.code, commandResult.stderr)
	}
}

func finalizationReady(t *testing.T, partitur, bin, vendor string) (string, []string, runstate.RunID) {
	t.Helper()
	repository, environment := finalizationRepository(t, bin, vendor)
	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
	runID := runstate.RunID(strings.TrimSpace(stdout))
	if code != 0 || runID == "" || stderr != "" {
		t.Fatalf("draft run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	decisionID := finalizationQuestionDecision(t, repository, runID)
	code, _, stderr = runCommandBinary(t, partitur, repository, environment, "answer", decisionID, "--answer", "continue")
	if code != 0 || stderr != expectedDecisionResumeHint(string(runID)) {
		t.Fatalf("answer exit=%d stderr=%q", code, stderr)
	}
	return repository, environment, runID
}

func finalizationRepository(t *testing.T, bin, vendor string) (string, []string) {
	t.Helper()
	document := finalizedDraftSchedulingScore()
	document["status"] = "draft"
	interview := document["movements"].([]any)[len(document["movements"].([]any))-1].(map[string]any)
	interview["may_propose"] = true
	_, diagnostics := score.CompileValue(document)
	if len(diagnostics) != 0 {
		t.Fatalf("compile finalization fixture: %v", diagnostics)
	}
	repository := t.TempDir()
	writeValidateInputs(t, repository, document, draftSchedulingCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, replaceEnvironment(draftSchedulingEnvironment(bin, vendor), map[string]string{
		runVendorDraftResultEnvironment: "question",
	})
}

func finalizationQuestionDecision(t *testing.T, repository string, runID runstate.RunID) string {
	t.Helper()
	for _, event := range readDraftSchedulingJournal(t, repository, runID) {
		if event.Type != runstate.EventAttemptBlocked {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		for _, raised := range payload["raised"].([]any) {
			entry := raised.(map[string]any)
			if entry["kind"] == "question" {
				return entry["decision_id"].(string)
			}
		}
	}
	t.Fatal("draft question decision is absent")
	return ""
}

func finalizationRouteAndRequest(t *testing.T, events []runstate.Event) (int, string) {
	t.Helper()
	routed := 0
	decisionID := ""
	for _, event := range events {
		var payload map[string]any
		switch event.Type {
		case runstate.EventAmendmentRoutedHuman:
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["decision_type"] == "finalization" {
				routed++
			}
		case runstate.EventDecisionRequested:
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["decision_type"] == "finalization" {
				decisionID, _ = payload["decision_id"].(string)
			}
		}
	}
	return routed, decisionID
}
