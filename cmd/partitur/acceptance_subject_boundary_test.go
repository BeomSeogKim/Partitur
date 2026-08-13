package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

func TestAcceptanceSubjectPinnedToStartedKillCuts(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	acceptanceFailure := expectedFailure{event: runstate.EventAcceptanceFailed, reason: "criterion_failed", terminalReason: "retries_exhausted"}
	worktreeLostFailure := expectedFailure{event: runstate.EventAttemptFailed, kind: "task_failed", reason: "worktree_lost", terminalReason: "retries_exhausted"}

	t.Run("subject_pinned", func(t *testing.T) {
		repository, environment := acceptanceSubjectRepository(t, bin, vendor)
		runID := killAtPoint(t, partitur, repository, environment, faultpoint.PointAcceptanceSubjectPinned)
		attemptID := writerAttemptID(t, repository, runID)
		if err := validateAcceptanceSubjectEndpoint(repository, runID, attemptID, false, false); err != nil {
			t.Fatal(err)
		}
		assertRecoveryFixedPoint(t, partitur, repository, environment, runID, &acceptanceFailure)
		if err := validateAcceptanceSubjectEndpoint(repository, runID, attemptID, true, false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("subject_pinned_worktree_lost", func(t *testing.T) {
		repository, environment := acceptanceSubjectRepository(t, bin, vendor)
		runID := killAtPoint(t, partitur, repository, environment, faultpoint.PointAcceptanceSubjectPinned)
		attemptID := writerAttemptID(t, repository, runID)
		worktree := filepath.Join(repository, ".partitur", "work", runID, string(attemptID), "worktree")
		if err := os.Rename(worktree, worktree+".lost"); err != nil {
			t.Fatal(err)
		}
		assertRecoveryFixedPoint(t, partitur, repository, environment, runID, &worktreeLostFailure)
		if err := validateAcceptanceSubjectEndpoint(repository, runID, attemptID, false, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("subject_pinned_ref_lost", func(t *testing.T) {
		repository, environment := acceptanceSubjectRepository(t, bin, vendor)
		runID := killAtPoint(t, partitur, repository, environment, faultpoint.PointAcceptanceSubjectPinned)
		attemptID := writerAttemptID(t, repository, runID)
		ref := "refs/partitur/runs/" + runID + "/attempts/" + string(attemptID) + "/subject"
		command := exec.Command("git", "-C", repository, "update-ref", "-d", ref)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("remove subject ref: %v: %s", err, output)
		}
		assertRecoveryFixedPoint(t, partitur, repository, environment, runID, &acceptanceFailure)
		if err := validateAcceptanceSubjectEndpoint(repository, runID, attemptID, true, false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("acceptance_started", func(t *testing.T) {
		repository, environment := acceptanceSubjectRepository(t, bin, vendor)
		child := pauseRunAtReceipt(t, partitur, repository, environment, "acceptance.acceptance.started")
		runID := string(routedProposalRunID(t, repository))
		attemptID := writerAttemptID(t, repository, runID)
		killPausedRun(t, child)
		if err := validateAcceptanceSubjectEndpoint(repository, runID, attemptID, true, false); err != nil {
			t.Fatal(err)
		}
		assertRecoveryFixedPoint(t, partitur, repository, environment, runID, &acceptanceFailure)
		if err := validateAcceptanceSubjectEndpoint(repository, runID, attemptID, true, false); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAcceptanceSubjectProbeOracle(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment := acceptanceSubjectRepository(t, bin, vendor)
	observed := runAcceptanceSubjectProbeOracle(t, partitur, repository, environment)
	if err := validateAcceptanceSubjectProbeTrace(observed); err != nil {
		t.Fatal(err)
	}
}

func runAcceptanceSubjectProbeOracle(t *testing.T, binary, repository string, environment []string) []string {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer notifyRead.Close()
	defer notifyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()

	files := make([]*os.File, 0, 8)
	for range 6 {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		defer file.Close()
	}
	files = append(files, notifyWrite, releaseRead)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, binary, "run")
	command.Dir = repository
	command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_FAULTPOINT_HARNESS":    "1",
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  "9",
		"PARTITUR_FAULTPOINT_RELEASE_FD": "10",
		"PARTITUR_RECEIPT_NOTIFY_FD":     "9",
		"PARTITUR_RECEIPT_RELEASE_FD":    "10",
	})
	command.ExtraFiles = files
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = notifyWrite.Close()
	_ = releaseRead.Close()

	var observed []string
	scanner := bufio.NewScanner(notifyRead)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			t.Fatalf("malformed probe-or-receipt notification %q", scanner.Text())
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			t.Fatalf("notification %q has invalid pid %q: %v", fields[0], fields[1], err)
		}
		observed = append(observed, fields[0])
		if fields[0] == string(faultpoint.PointAcceptanceSubjectPinned) {
			runID := string(routedProposalRunID(t, repository))
			attemptID := writerAttemptID(t, repository, runID)
			ref := "refs/partitur/runs/" + runID + "/attempts/" + string(attemptID) + "/subject"
			if _, err := validateAcceptanceSubjectRef(repository, ref); err != nil {
				t.Fatalf("acceptance.subject_pinned emitted before its subject ref was durable: %v", err)
			}
		}
		if _, err := releaseWrite.Write([]byte{1}); err != nil {
			t.Fatalf("release notification %q: %v", fields[0], err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read probe-or-receipt notifications: %v", err)
	}
	err = command.Wait()
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 4 {
		t.Fatalf("run did not reach its expected completed failure (exit 4): %v\nstdout:\n%s\nstderr:\n%s", err, &stdout, &stderr)
	}
	runID := strings.TrimSpace(stdout.String())
	assertRunFailedReason(t, readHarnessJournal(t, repository, runID), "movement_failed")
	return observed
}

func validateAcceptanceSubjectProbeTrace(observed []string) error {
	probeIndex, startedIndex := -1, -1
	probeCount, startedCount := 0, 0
	for index, notification := range observed {
		switch notification {
		case string(faultpoint.PointAcceptanceSubjectPinned):
			probeCount++
			probeIndex = index
		case "acceptance.acceptance.started":
			startedCount++
			startedIndex = index
		}
	}
	if probeCount != 1 {
		return fmt.Errorf("acceptance.subject_pinned emitted %d times, want exactly once", probeCount)
	}
	if startedCount != 1 {
		return fmt.Errorf("acceptance.acceptance.started observed %d times, want exactly once (positive control)", startedCount)
	}
	if probeIndex >= startedIndex {
		return fmt.Errorf("acceptance.subject_pinned notification index=%d, want before acceptance.acceptance.started index=%d", probeIndex, startedIndex)
	}
	return nil
}

func TestAcceptanceSubjectProbeTraceValidator(t *testing.T) {
	for _, test := range []struct {
		name     string
		observed []string
		wantErr  bool
	}{
		{name: "positive_control", observed: []string{"acceptance.subject_pinned", "acceptance.acceptance.started"}},
		{name: "missing_probe", observed: []string{"acceptance.acceptance.started"}, wantErr: true},
		{name: "probe_after_started", observed: []string{"acceptance.acceptance.started", "acceptance.subject_pinned"}, wantErr: true},
		{name: "missing_started_positive_control", observed: []string{"acceptance.subject_pinned"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateAcceptanceSubjectProbeTrace(test.observed)
			if (err != nil) != test.wantErr {
				t.Fatalf("validator error=%v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func acceptanceSubjectRepository(t *testing.T, bin, vendor string) (string, []string) {
	t.Helper()
	score := strictRealAdapterScore()
	writer := score["movements"].([]any)[0].(map[string]any)
	score["movements"] = []any{writer}
	delete(score["parts"].(map[string]any), "verify")
	writer["outputs"] = append(outputValues(writer), map[string]any{"id": "report", "kind": "artifact"})
	writer["acceptance"].(map[string]any)["hard"] = []any{map[string]any{"id": "reject", "run": []any{"false"}}}
	cast := map[string]any{
		"cast":       "0.1",
		"performers": map[string]any{"worker": map[string]any{"adapter": "codex", "model": "gpt-5.6-sol"}},
		"bindings": map[string]any{
			"implement": map[string]any{"performer": "worker"},
		},
	}
	repository, environment := killHarnessRepositoryWithInputs(t, bin, vendor, score, cast)
	return repository, fixtureOutcomeEnvironment(environment, "read_only_violation")
}

func outputValues(movement map[string]any) []any {
	values, _ := movement["outputs"].([]any)
	return values
}

func writerAttemptID(t *testing.T, repository, runID string) runstate.AttemptID {
	t.Helper()
	for _, event := range readHarnessEvents(t, repository, runID) {
		if event.MovementID == "implement" && event.AttemptID != "" {
			return event.AttemptID
		}
	}
	t.Fatal("writer attempt is absent")
	return ""
}

func validateAcceptanceSubjectEndpoint(repository, runID string, attemptID runstate.AttemptID, wantStarted, wantLost bool) error {
	ref := "refs/partitur/runs/" + runID + "/attempts/" + string(attemptID) + "/subject"
	subjectTree, err := validateAcceptanceSubjectRef(repository, ref)
	if err != nil {
		return err
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		return err
	}
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		return err
	}
	return validateAcceptanceSubjectEvents(journal.Events, attemptID, subjectTree, wantStarted, wantLost)
}

func validateAcceptanceSubjectRef(repository, ref string) (string, error) {
	output, err := exec.Command("git", "-C", repository, "rev-parse", "--verify", ref+"^{tree}").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("subject ref %q is not recoverable: %w: %s", ref, err, strings.TrimSpace(string(output)))
	}
	return "git-sha1:" + strings.TrimSpace(string(output)), nil
}

func validateAcceptanceSubjectEvents(events []runstate.Event, attemptID runstate.AttemptID, subjectTree string, wantStarted, wantLost bool) error {
	started := 0
	failedLost := 0
	for _, event := range events {
		switch event.Type {
		case runstate.EventAcceptanceStarted:
			// This edge is attempt-scoped. Events for another attempt neither
			// satisfy nor violate either endpoint.
			if event.AttemptID != attemptID {
				continue
			}
			started++
			var payload struct {
				SubjectTree string `json:"subject_tree"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return fmt.Errorf("decode acceptance.started: %w", err)
			}
			if payload.SubjectTree != subjectTree {
				return fmt.Errorf("acceptance.started subject_tree=%q, want recoverable ref tree %q", payload.SubjectTree, subjectTree)
			}
		case runstate.EventAttemptFailed:
			if event.AttemptID != attemptID {
				continue
			}
			var payload struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return fmt.Errorf("decode attempt.failed: %w", err)
			}
			if payload.Reason == "worktree_lost" {
				failedLost++
			}
		}
	}
	wantStartedCount := 0
	if wantStarted {
		wantStartedCount = 1
	}
	if started != wantStartedCount {
		return fmt.Errorf("acceptance.started count=%d, want %d", started, wantStartedCount)
	}
	wantLostCount := 0
	if wantLost {
		wantLostCount = 1
	}
	if failedLost != wantLostCount {
		return fmt.Errorf("attempt.failed worktree_lost count=%d, want %d", failedLost, wantLostCount)
	}
	return nil
}

func TestAcceptanceSubjectEndpointValidator(t *testing.T) {
	attemptID := runstate.AttemptID("attempt-1")
	otherAttemptID := runstate.AttemptID("attempt-2")
	started := runstate.Event{AttemptID: attemptID, Type: runstate.EventAcceptanceStarted, Payload: json.RawMessage(`{"subject_tree":"git-sha1:subject"}`)}
	lost := runstate.Event{AttemptID: attemptID, Type: runstate.EventAttemptFailed, Payload: json.RawMessage(`{"reason":"worktree_lost"}`)}
	otherStarted := runstate.Event{AttemptID: otherAttemptID, Type: runstate.EventAcceptanceStarted, Payload: json.RawMessage(`{"subject_tree":"git-sha1:other"}`)}
	otherFailure := runstate.Event{AttemptID: attemptID, Type: runstate.EventAttemptFailed, Payload: json.RawMessage(`{"reason":"other"}`)}
	for _, test := range []struct {
		name                  string
		events                []runstate.Event
		subject               string
		wantStarted, wantLost bool
		wantError             bool
	}{
		{name: "positive_left", subject: "git-sha1:subject"},
		{name: "positive_started", events: []runstate.Event{started}, subject: "git-sha1:subject", wantStarted: true},
		{name: "positive_lost", events: []runstate.Event{lost}, subject: "git-sha1:subject", wantLost: true},
		{name: "ignore_other_attempt_on_left", events: []runstate.Event{otherStarted}, subject: "git-sha1:subject"},
		{name: "reject_started_on_left", events: []runstate.Event{started}, subject: "git-sha1:subject", wantError: true},
		{name: "reject_duplicate_started", events: []runstate.Event{started, started}, subject: "git-sha1:subject", wantStarted: true, wantError: true},
		{name: "reject_missing_started", subject: "git-sha1:subject", wantStarted: true, wantError: true},
		{name: "reject_wrong_subject_tree", events: []runstate.Event{started}, subject: "git-sha1:other", wantStarted: true, wantError: true},
		{name: "reject_missing_worktree_lost", subject: "git-sha1:subject", wantLost: true, wantError: true},
		{name: "reject_duplicate_worktree_lost", events: []runstate.Event{lost, lost}, subject: "git-sha1:subject", wantLost: true, wantError: true},
		{name: "reject_other_failure_reason", events: []runstate.Event{otherFailure}, subject: "git-sha1:subject", wantLost: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateAcceptanceSubjectEvents(test.events, attemptID, test.subject, test.wantStarted, test.wantLost)
			if (err != nil) != test.wantError {
				t.Fatalf("validator error=%v, wantError=%v", err, test.wantError)
			}
		})
	}
}

func TestAcceptanceSubjectRefValidatorRejectsMissingRef(t *testing.T) {
	repository := t.TempDir()
	for _, arguments := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "Partitur Test"},
		{"config", "user.email", "partitur@example.invalid"},
		{"commit", "--allow-empty", "-qm", "base"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", repository}, arguments...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	if _, err := validateAcceptanceSubjectRef(repository, "refs/partitur/missing-subject"); err == nil {
		t.Fatal("missing subject ref was accepted")
	}
}
