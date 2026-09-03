package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestDraftResultBoundary(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("empty result writes failure", func(t *testing.T) {
		repository, environment := draftResultRepository(t, bin, vendor, "empty")
		code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
		runID := strings.TrimSpace(stdout)
		if code != 4 || runID == "" || stdout != runID+"\n" || !strings.Contains(stderr, "movement_failed") {
			t.Fatalf("run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		assertDraftNoBlockingFailure(t, repository, runstate.RunID(runID))
	})

	t.Run("empty result never starts acceptance", func(t *testing.T) {
		repository, environment := draftResultRepository(t, bin, vendor, "empty")
		_, stdout, _ := runCommandBinary(t, partitur, repository, environment, "run")
		runID := strings.TrimSpace(stdout)
		if runID == "" || stdout != runID+"\n" {
			t.Fatalf("run stdout=%q", stdout)
		}
		assertDraftAcceptanceAbsent(t, repository, runstate.RunID(runID))
	})

	for _, result := range []struct {
		kind    string
		pending int
	}{
		{kind: "question", pending: 1},
		{kind: "questions", pending: 4},
		{kind: "proposal", pending: 1},
	} {
		t.Run("blocking "+result.kind+" remains blocked", func(t *testing.T) {
			repository, environment := draftResultRepository(t, bin, vendor, result.kind)
			code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
			runID := strings.TrimSpace(stdout)
			if code != 0 || runID == "" || stdout != runID+"\n" || stderr != "" {
				t.Fatalf("run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertDraftBlockingResult(t, repository, runstate.RunID(runID), result.pending)
		})
	}
}

func TestLiveBlockingRequestsRemainFixedAfterResume(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment := draftResultRepository(t, bin, vendor, "questions")
	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
	runID := runstate.RunID(strings.TrimSpace(stdout))
	if code != 0 || runID == "" || stderr != "" {
		t.Fatalf("run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertDraftBlockingResult(t, repository, runID, 4)

	code, stdout, stderr = runCommandBinary(t, partitur, repository, environment, "resume", string(runID))
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertDraftBlockingResult(t, repository, runID, 4)
}

func TestLiveBlockingRequestCrashReachesFixedPoint(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment := draftResultRepository(t, bin, vendor, "questions")
	child := pauseRunAtReceipt(t, partitur, repository, environment, "attempt.decision.requested.question")
	runID := routedProposalRunID(t, repository)
	killPausedRun(t, child)

	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "resume", string(runID))
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertDraftBlockingResult(t, repository, runID, 4)
}

func TestRecoveredBlockingRequestCrashReachesFixedPoint(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment := draftResultRepository(t, bin, vendor, "questions")
	child := pauseRunAtReceipt(t, partitur, repository, environment, "attempt.blocked")
	runID := routedProposalRunID(t, repository)
	killPausedRun(t, child)

	child = pauseCommandAtReceipt(t, partitur, repository, environment, "recovery.decision.requested.question", "resume", string(runID))
	killPausedRun(t, child)

	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "resume", string(runID))
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertDraftBlockingResult(t, repository, runID, 4)
}

func TestDraftResultBoundaryKillCuts(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, cut := range []struct {
		name    string
		address faultpoint.ReceiptAddress
	}{
		{name: "performer_completed", address: "attempt.performer_completed"},
		{name: "attempt_failed", address: "attempt.failed"},
	} {
		t.Run("lifecycle.draft_performer_completed_to_no_blocking_failure/"+cut.name, func(t *testing.T) {
			repository, environment := draftResultRepository(t, bin, vendor, "empty")
			child := pauseRunAtReceipt(t, partitur, repository, environment, cut.address)
			runID := routedProposalRunID(t, repository)
			killPausedRun(t, child)
			assertDraftResultRecoveryFixedPoint(t, partitur, repository, environment, string(runID), expectedFailure{
				event: runstate.EventAttemptFailed, kind: "task_failed", reason: "draft_no_blocking_output",
				terminalReason: "retries_exhausted", runReason: "movement_failed",
			})
			if cut.name == "performer_completed" {
				assertDraftRecoveryFailureCausation(t, repository, runID)
			}
			assertDraftAcceptanceAbsent(t, repository, runID)
		})
	}
}

func TestResumeRecoveredAttemptWaitingHumanIsQuiescent(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment := draftResultRepository(t, bin, vendor, "question")
	child := pauseRunAtReceipt(t, partitur, repository, environment, "movement.movement.started")
	runID := routedProposalRunID(t, repository)
	killPausedRun(t, child)

	code, stdout, stderr := runCommandBinaryWithin(t, 10*time.Second, partitur, repository, environment, "resume", string(runID))
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	assertDraftBlockingResult(t, repository, runID, 1)
}

func draftResultRepository(t *testing.T, bin, vendor, result string) (string, []string) {
	t.Helper()
	document := draftSchedulingScore()
	interview := document["movements"].([]any)[1].(map[string]any)
	interview["may_propose"] = true
	compiled, diagnostics := score.CompileValue(document)
	if len(diagnostics) != 0 {
		t.Fatalf("compile draft result fixture: %v", diagnostics)
	}
	baseHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	writeValidateInputs(t, repository, document, draftSchedulingCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	environmentValues := map[string]string{runVendorDraftResultEnvironment: result}
	if result == "proposal" {
		environmentValues[runVendorProposalBaseHashEnvironment] = baseHash
	}
	environment := replaceEnvironment(draftSchedulingEnvironment(bin, vendor), environmentValues)
	return repository, environment
}

func assertDraftNoBlockingFailure(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	journal := readDraftSchedulingJournal(t, repository, runID)
	completed := -1
	failed := -1
	for index, event := range journal {
		switch event.Type {
		case runstate.EventPerformerCompleted:
			completed = index
		case runstate.EventAttemptFailed:
			failed = index
			if !strings.Contains(string(event.Payload), `"kind":"task_failed"`) ||
				!strings.Contains(string(event.Payload), `"reason":"draft_no_blocking_output"`) {
				t.Fatalf("draft attempt.failed payload = %s", event.Payload)
			}
		}
	}
	if completed < 0 || failed != completed+1 {
		t.Fatalf("draft empty journal sequence = %v", eventKinds(journal))
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	attempts := input.Projection.State.Attempts
	for _, attempt := range attempts {
		if attempt.MovementID == "interview" {
			found = true
			if attempt.Failure == nil || attempt.Failure.Reason != "draft_no_blocking_output" {
				t.Fatalf("draft attempt projection = %+v", attempt)
			}
		}
	}
	if !found {
		t.Fatalf("draft attempt projection is absent: %+v", attempts)
	}
}

func assertDraftBlockingResult(t *testing.T, repository string, runID runstate.RunID, wantPending int) {
	t.Helper()
	journal := readDraftSchedulingJournal(t, repository, runID)
	type indexedPayload struct {
		index   int
		payload map[string]any
	}
	blocked := -1
	raised := make(map[string]map[string]any)
	raisedOrder := make([]string, 0, wantPending)
	requested := make(map[string][]indexedPayload)
	requestOrder := make([]string, 0, wantPending)
	routed := make(map[string]indexedPayload)
	for index, event := range journal {
		switch event.Type {
		case runstate.EventAttemptBlocked:
			blocked = index
			var payload struct {
				Raised []map[string]any `json:"raised"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			for _, decision := range payload.Raised {
				decisionID, _ := decision["decision_id"].(string)
				raised[decisionID] = decision
				raisedOrder = append(raisedOrder, decisionID)
			}
		case runstate.EventAmendmentRoutedHuman:
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			decisionID, _ := payload["decision_id"].(string)
			routed[decisionID] = indexedPayload{index: index, payload: payload}
		case runstate.EventDecisionRequested:
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			decisionID, _ := payload["decision_id"].(string)
			requested[decisionID] = append(requested[decisionID], indexedPayload{index: index, payload: payload})
			requestOrder = append(requestOrder, decisionID)
			if blocked < 0 || index <= blocked {
				t.Fatalf("decision.requested at %d precedes attempt.blocked at %d", index, blocked)
			}
		case runstate.EventAttemptFailed:
			t.Fatalf("blocking draft result failed: %s", event.Payload)
		case runstate.EventAcceptanceStarted:
			t.Fatalf("blocking draft result started acceptance: %s", event.EventID)
		}
	}
	if blocked < 0 {
		t.Fatalf("blocking draft result journal = %v", eventKinds(journal))
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	state := input.Projection.State
	if state.Run != runstate.RunWaitingHuman || len(state.PendingDecisions) != wantPending {
		t.Fatalf("blocking projection run=%s pending=%d, want WAITING_HUMAN pending=%d", state.Run, len(state.PendingDecisions), wantPending)
	}
	for decisionID := range state.PendingDecisions {
		requests := requested[decisionID]
		if len(requests) != 1 {
			t.Fatalf("pending decision %q has %d decision.requested events, want 1", decisionID, len(requests))
		}
		source := raised[decisionID]
		request := requests[0]
		switch state.PendingDecisions[decisionID].Type {
		case "question":
			if request.payload["decision_type"] != "question" ||
				request.payload["question"] != source["question"] ||
				request.payload["emitted_id"] != source["emitted_id"] {
				t.Fatalf("question request=%#v, want blocked source=%#v", request.payload, source)
			}
		case "amendment":
			route, ok := routed[decisionID]
			if !ok || route.index <= blocked || request.index <= route.index {
				t.Fatalf("amendment order blocked=%d routed=%d requested=%d", blocked, route.index, request.index)
			}
			for requestField, routedField := range map[string]string{
				"decision_id": "decision_id", "decision_type": "decision_type", "proposal_id": "proposal_id",
				"routed_reason": "reason", "blocking": "blocking", "emitted_id": "emitted_id",
			} {
				if request.payload[requestField] != route.payload[routedField] {
					t.Fatalf("request %s=%#v, want routed %s=%#v", requestField, request.payload[requestField], routedField, route.payload[routedField])
				}
			}
		default:
			t.Fatalf("unexpected pending decision type %q", state.PendingDecisions[decisionID].Type)
		}
	}
	if len(requested) != wantPending {
		t.Fatalf("decision.requested count=%d, want %d", len(requested), wantPending)
	}
	if !slices.Equal(requestOrder, raisedOrder) {
		t.Fatalf("decision.requested order=%v, want raised order=%v", requestOrder, raisedOrder)
	}
}

func assertDraftAcceptanceAbsent(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	for _, event := range readDraftSchedulingJournal(t, repository, runID) {
		if event.Type == runstate.EventAcceptanceStarted {
			t.Fatalf("draft result recovery started acceptance: %s", event.EventID)
		}
	}
}

func assertDraftResultRecoveryFixedPoint(
	t *testing.T,
	binary, repository string,
	environment []string,
	runID string,
	expected expectedFailure,
) {
	t.Helper()
	code, stdout, stderr := runCommandBinaryWithin(t, 10*time.Second, binary, repository, environment, "resume", runID)
	if code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal := filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl")
	first, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	assertExpectedFailure(t, first, expected)
	code, stdout, stderr = runCommandBinaryWithin(t, 10*time.Second, binary, repository, environment, "resume", runID)
	if code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("fixed-point replay exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	second, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("fixed-point replay appended duplicate durable events")
	}
}

func assertDraftRecoveryFailureCausation(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	var completedID string
	for _, event := range readDraftSchedulingJournal(t, repository, runID) {
		if event.Type == runstate.EventPerformerCompleted {
			completedID = event.EventID
		}
		if event.Type == runstate.EventAttemptFailed && strings.Contains(string(event.Payload), `"reason":"draft_no_blocking_output"`) {
			if completedID == "" || event.CausationID != completedID {
				t.Fatalf("draft recovery failure causation=%q, want performer.completed %q", event.CausationID, completedID)
			}
			return
		}
	}
	t.Fatal("draft recovery failure is absent")
}
