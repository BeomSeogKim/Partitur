package main

import (
	"bytes"
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

	for _, result := range []string{"question", "proposal"} {
		t.Run("blocking "+result+" remains blocked", func(t *testing.T) {
			repository, environment := draftResultRepository(t, bin, vendor, result)
			code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
			runID := strings.TrimSpace(stdout)
			if code != 0 || runID == "" || stdout != runID+"\n" || stderr != "" {
				t.Fatalf("run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			assertDraftBlockingResult(t, repository, runstate.RunID(runID))
		})
	}
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

func assertDraftBlockingResult(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	journal := readDraftSchedulingJournal(t, repository, runID)
	for _, event := range journal {
		switch event.Type {
		case runstate.EventAttemptBlocked:
			return
		case runstate.EventAttemptFailed:
			t.Fatalf("blocking draft result failed: %s", event.Payload)
		case runstate.EventAcceptanceStarted:
			t.Fatalf("blocking draft result started acceptance: %s", event.EventID)
		}
	}
	t.Fatalf("blocking draft result journal = %v", eventKinds(journal))
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
