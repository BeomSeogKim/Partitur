package main

import (
	"os"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

func TestDraftSchedulingSelectsOnlyInterviewInRunAndResume(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("live run", func(t *testing.T) {
		repository, environment := draftSchedulingRepository(t, bin, vendor)
		child := pauseRunAtReceipt(t, partitur, repository, environment, "movement.movement.started")
		assertOnlyInterviewScheduled(t, repository, "interview")
		killPausedRun(t, child)
	})

	t.Run("interrupted resume", func(t *testing.T) {
		repository, environment := draftSchedulingRepository(t, bin, vendor)
		child := pauseRunAtReceipt(t, partitur, repository, environment, "authority.driver_lease")
		killPausedRun(t, child)
		runID := routedProposalRunID(t, repository)

		resumed := pauseCommandAtReceipt(t, partitur, repository, environment, "recovery.movement.started", "resume", string(runID))
		assertOnlyInterviewScheduled(t, repository, "interview")
		killPausedRun(t, resumed)
	})
}

func TestFinalizedDraftMovementIsInapplicableAndRunTerminates(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment := finalizedDraftSchedulingRepository(t, bin, vendor)

	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
	runID := strings.TrimSpace(stdout)
	if code != 0 || runID == "" || stdout != runID+"\n" || stderr != "" {
		t.Fatalf("run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Run != runstate.RunSucceeded || input.Projection.State.Movements["interview"] != runstate.MovementInapplicable {
		t.Fatalf("terminal draft projection = %+v", input.Projection.State)
	}
	for _, attempt := range input.Projection.State.Attempts {
		if attempt.MovementID == "interview" {
			t.Fatalf("inapplicable interview has attempt %+v", attempt)
		}
	}
	for _, event := range readDraftSchedulingJournal(t, repository, runstate.RunID(runID)) {
		if event.MovementID == "interview" {
			t.Fatalf("inapplicable interview event = %+v", event)
		}
	}
}

func draftSchedulingRepository(t *testing.T, bin, vendor string) (string, []string) {
	t.Helper()
	repository := t.TempDir()
	writeValidateInputs(t, repository, draftSchedulingScore(), draftSchedulingCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, draftSchedulingEnvironment(bin, vendor)
}

func finalizedDraftSchedulingRepository(t *testing.T, bin, vendor string) (string, []string) {
	t.Helper()
	repository := t.TempDir()
	writeValidateInputs(t, repository, finalizedDraftSchedulingScore(), draftSchedulingCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, draftSchedulingEnvironment(bin, vendor)
}

func draftSchedulingEnvironment(bin, vendor string) []string {
	return replaceEnvironment(os.Environ(), map[string]string{
		"HOME":               os.TempDir(),
		"PATH":               bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN": vendor,
		runVendorEnvironment: "1",
	})
}

func draftSchedulingScore() map[string]any {
	return map[string]any{
		"score": "0.2", "name": "draft-scheduling", "revision": float64(1), "status": "draft", "goal": "Interview first.",
		"draft": map[string]any{"interview_movement": "interview"},
		"parts": map[string]any{
			"ordinary":    map[string]any{"capabilities": []any{"repo_read", "shell", "network"}, "read_only": true},
			"interviewer": map[string]any{"capabilities": []any{"repo_read", "shell", "network"}, "read_only": true},
		},
		"movements": []any{
			map[string]any{"id": "ordinary", "part": "ordinary", "grants": []any{"repo_read", "shell", "network"}, "instruction": "Must not run while draft."},
			map[string]any{"id": "interview", "phase": "draft", "part": "interviewer", "grants": []any{"repo_read", "shell", "network"}, "instruction": "Run the interview."},
		},
		"policy": map[string]any{"allowed_paths": []any{"**"}, "budget": map[string]any{"active_wall_clock_min": float64(10)}},
	}
}

func finalizedDraftSchedulingScore() map[string]any {
	score := runScore()
	score["draft"] = map[string]any{"interview_movement": "interview"}
	score["movements"] = append(score["movements"].([]any), map[string]any{
		"id": "interview", "phase": "draft", "part": "reader", "grants": []any{"repo_read", "shell", "network"}, "instruction": "Retained only for a draft run.",
	})
	return score
}

func draftSchedulingCast() map[string]any {
	return map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"ordinary-performer":  map[string]any{"adapter": "codex", "model": "gpt-5.6-sol"},
			"interview-performer": map[string]any{"adapter": "codex", "model": "gpt-5.6-sol"},
			"worker":              map[string]any{"adapter": "codex", "model": "gpt-5.6-sol"},
		},
		"bindings": map[string]any{
			"ordinary":    map[string]any{"performer": "ordinary-performer"},
			"interviewer": map[string]any{"performer": "interview-performer"},
			"reader":      map[string]any{"performer": "worker"},
		},
	}
}

func assertOnlyInterviewScheduled(t *testing.T, repository, interview string) {
	t.Helper()
	runID := routedProposalRunID(t, repository)
	journal := readDraftSchedulingJournal(t, repository, runID)
	var scheduled []runstate.Event
	for _, event := range journal {
		if event.Type == runstate.EventMovementReady || event.Type == runstate.EventMovementStarted {
			scheduled = append(scheduled, event)
		}
	}
	if len(scheduled) != 2 || scheduled[0].Type != runstate.EventMovementReady || scheduled[1].Type != runstate.EventMovementStarted ||
		scheduled[0].MovementID != runstate.MovementID(interview) || scheduled[1].MovementID != runstate.MovementID(interview) {
		t.Fatalf("draft scheduled events = %+v", scheduled)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Movements["ordinary"] != runstate.MovementPending || input.Projection.State.Movements[runstate.MovementID(interview)] != runstate.MovementRunning {
		t.Fatalf("draft scheduling state = %+v", input.Projection.State.Movements)
	}
}

func readDraftSchedulingJournal(t *testing.T, repository string, runID runstate.RunID) []runstate.Event {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	return journal.Events
}
