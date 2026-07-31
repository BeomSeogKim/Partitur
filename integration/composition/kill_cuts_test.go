package composition_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestCompositionKillCuts(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildBinary(t, root, bin, "partitur")
	buildBinary(t, root, bin, "partitur-adapter-codex")
	buildBinary(t, root, bin, "partitur-trampoline")
	vendor := buildVendor(t, root, bin)

	for _, edge := range []struct {
		name         string
		before       faultpoint.PointID
		after        faultpoint.PointID
		score        map[string]any
		noVerdictGit bool
		assert       func(*testing.T, string, string, []byte, bool)
	}{
		{
			name: "change_set.captured_to_recorded", before: faultpoint.PointChangeSetCaptured, after: faultpoint.PointChangeSetRecorded,
			score: writerScore(), assert: assertChangeSetCut,
		},
		{
			name: "composition.movement_evidence_to_terminal/conflicted", before: faultpoint.PointCompositionMovementEvidence, after: faultpoint.PointCompositionMovementTerminal,
			score: movementConflictScore(), assert: assertMovementCompositionCut,
		},
		{
			name: "composition.movement_evidence_to_terminal/failed", before: faultpoint.PointCompositionMovementEvidence, after: faultpoint.PointCompositionMovementTerminal,
			score: movementFailureScore(), noVerdictGit: true, assert: assertMovementCompositionFailureCut,
		},
		{
			name: "composition.candidate_evidence_to_terminal/conflicted", before: faultpoint.PointCompositionCandidateEvidence, after: faultpoint.PointCompositionCandidateTerminal,
			score: candidateConflictScore(), assert: assertCandidateCompositionCut,
		},
		{
			name: "composition.candidate_evidence_to_terminal/failed", before: faultpoint.PointCompositionCandidateEvidence, after: faultpoint.PointCompositionCandidateTerminal,
			score: candidateFailureScore(), noVerdictGit: true, assert: assertCandidateCompositionFailureCut,
		},
	} {
		edge := edge
		t.Run(edge.name, func(t *testing.T) {
			for _, side := range []struct {
				name  string
				point faultpoint.PointID
			}{
				{name: "before", point: edge.before},
				{name: "after", point: edge.after},
			} {
				side := side
				t.Run(side.name, func(t *testing.T) {
					repository, environment := compositionRepository(t, bin, vendor, edge.score, edge.noVerdictGit)
					runID := killAtPoint(t, partitur, repository, environment, side.point)
					journal := readJournal(t, repository, runID)
					edge.assert(t, repository, runID, journal, side.name == "before")
					assertRecoveryFixedPoint(t, partitur, repository, environment, runID)
				})
			}
		})
	}
}

func compositionRepository(t *testing.T, bin, vendor string, score map[string]any, noVerdictGit bool) (string, []string) {
	t.Helper()
	repository := t.TempDir()
	writeJSON(t, filepath.Join(repository, "partitur.yaml"), score)
	if err := os.Mkdir(filepath.Join(repository, ".partitur"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(repository, ".partitur", "cast.yaml"), map[string]any{
		"cast": "0.1", "performers": map[string]any{"worker": map[string]any{"adapter": "codex", "model": "fixture"}},
		"bindings": map[string]any{"worker": map[string]any{"performer": "worker"}},
	})
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")
	if noVerdictGit {
		path = noVerdictGitDirectory(t) + string(os.PathListSeparator) + path
	}
	return repository, replaceEnvironment(os.Environ(), map[string]string{
		"HOME": t.TempDir(), "PATH": path,
		"PARTITUR_CODEX_BIN": vendor,
	})
}

func writerScore() map[string]any {
	return scoreDocument([]any{writerMovement("writer-one")}, true, "")
}

func movementConflictScore() map[string]any {
	return scoreDocument([]any{
		writerMovement("writer-one"), writerMovement("writer-two"),
		map[string]any{"id": "target", "part": "worker", "needs": []any{"writer-one", "writer-two"}, "grants": []any{"repo_read"}, "instruction": "target", "outputs": []any{map[string]any{"id": "target-report", "kind": "artifact"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "target-report-present", "artifact": "target-report"}}}},
		map[string]any{"id": "verify", "part": "worker", "needs": []any{"target"}, "grants": []any{"repo_read"}, "instruction": "verify", "outputs": []any{map[string]any{"id": "verify-report", "kind": "artifact"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "verify-report-present", "artifact": "verify-report"}}}},
	}, false, "verify")
}

func movementFailureScore() map[string]any {
	return scoreDocument([]any{
		writerMovementWithInstruction("writer-one", "failure-one"), writerMovementWithInstruction("writer-two", "failure-two"),
		map[string]any{"id": "target", "part": "worker", "needs": []any{"writer-one", "writer-two"}, "grants": []any{"repo_read"}, "instruction": "target", "outputs": []any{map[string]any{"id": "target-report", "kind": "artifact"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "target-report-present", "artifact": "target-report"}}}},
		map[string]any{"id": "verify", "part": "worker", "needs": []any{"target"}, "grants": []any{"repo_read"}, "instruction": "verify", "outputs": []any{map[string]any{"id": "verify-report", "kind": "artifact"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "verify-report-present", "artifact": "verify-report"}}}},
	}, false, "verify")
}

func candidateConflictScore() map[string]any {
	return scoreDocument([]any{writerMovement("writer-one"), writerMovement("writer-two")}, true, "")
}

func candidateFailureScore() map[string]any {
	return scoreDocument([]any{writerMovementWithInstruction("writer-one", "failure-one"), writerMovementWithInstruction("writer-two", "failure-two")}, true, "")
}

func scoreDocument(movements []any, waived bool, finalMovement string) map[string]any {
	verification := map[string]any{"expectation": map[string]any{"intent": "pass-existing-tests", "apply_gate": map[string]any{"require": []any{"verified"}}}}
	if waived {
		verification["expectation"].(map[string]any)["apply_gate"] = map[string]any{"waived": true, "reason": "composition kill-cut fixture"}
	} else {
		verification["final_movement"] = finalMovement
	}
	return map[string]any{
		"score": "0.2", "name": "composition-kill-cut", "revision": 1, "status": "finalized", "goal": "exercise durable composition cuts",
		"verification": verification,
		"parts":        map[string]any{"worker": map[string]any{"capabilities": []any{"repo_read", "repo_write", "shell", "network"}}},
		"movements":    movements,
		"policy":       map[string]any{"allowed_paths": []any{"**"}, "budget": map[string]any{"active_wall_clock_min": 10}},
	}
}

func writerMovement(id string) map[string]any {
	return writerMovementWithInstruction(id, id)
}

func writerMovementWithInstruction(id, instruction string) map[string]any {
	return map[string]any{
		"id": id, "part": "worker", "grants": []any{"repo_read", "repo_write", "shell", "network"}, "instruction": instruction,
		"outputs": []any{
			map[string]any{"id": id + "-change-set", "kind": "change_set"},
			map[string]any{"id": id + "-report", "kind": "artifact"},
		},
		"acceptance": map[string]any{"hard": []any{map[string]any{"id": id + "-report-present", "artifact": id + "-report"}}},
	}
}

func killAtPoint(t *testing.T, binary, repository string, environment []string, target faultpoint.PointID) string {
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
		defer file.Close()
		files = append(files, file)
	}
	files = append(files, notifyWrite, releaseRead)
	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, "run")
	command.Dir, command.ExtraFiles, command.Stdout, command.Stderr = repository, files, &stdout, &stderr
	command.Env = replaceEnvironment(environment, map[string]string{"PARTITUR_FAULTPOINT_NOTIFY_FD": "9", "PARTITUR_FAULTPOINT_RELEASE_FD": "10"})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = notifyWrite.Close()
	_ = releaseRead.Close()
	scanner := bufio.NewScanner(notifyRead)
	for {
		point, pid, err := nextPoint(scanner)
		if err != nil {
			waitErr := command.Wait()
			runID := strings.TrimSpace(stdout.String())
			journal := ""
			if runID != "" {
				if contents, readErr := os.ReadFile(filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl")); readErr == nil {
					journal = string(contents)
				}
			}
			t.Fatalf("wait for faultpoint %s: %v; command=%v stdout=%q stderr=%q journal=%s", target, err, waitErr, stdout.String(), stderr.String(), journal)
		}
		if point != target {
			if _, err := releaseWrite.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := command.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		if pid != command.Process.Pid {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = releaseWrite.Close()
		if err := command.Wait(); err == nil {
			t.Fatalf("run at %s exited successfully; stderr=%s", target, stderr.String())
		}
		break
	}
	runID := strings.TrimSpace(stdout.String())
	if runID == "" {
		t.Fatalf("run at %s published no run id; stderr=%s", target, stderr.String())
	}
	return runID
}

func nextPoint(scanner *bufio.Scanner) (faultpoint.PointID, int, error) {
	ready := make(chan struct {
		point faultpoint.PointID
		pid   int
		err   error
	}, 1)
	go func() {
		if !scanner.Scan() {
			ready <- struct {
				point faultpoint.PointID
				pid   int
				err   error
			}{err: fmt.Errorf("faultpoint notification closed: %w", scanner.Err())}
			return
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			ready <- struct {
				point faultpoint.PointID
				pid   int
				err   error
			}{err: fmt.Errorf("malformed probe notification %q", scanner.Text())}
			return
		}
		value, err := strconv.Atoi(fields[1])
		ready <- struct {
			point faultpoint.PointID
			pid   int
			err   error
		}{point: faultpoint.PointID(fields[0]), pid: value, err: err}
	}()
	select {
	case result := <-ready:
		if result.err != nil || result.point == "" || result.pid <= 0 {
			return "", 0, fmt.Errorf("probe notification = %#v", result)
		}
		return result.point, result.pid, nil
	case <-time.After(15 * time.Second):
		return "", 0, fmt.Errorf("timed out waiting for faultpoint probe")
	}
}

func assertRecoveryFixedPoint(t *testing.T, binary, repository string, environment []string, runID string) {
	t.Helper()
	code, stdout, stderr := runCommand(t, binary, repository, environment, "resume", runID)
	if (code != 0 && code != 4) || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	first := readJournal(t, repository, runID)
	if code == 4 && !hasEvent(first, runstate.EventRunFailed) {
		t.Fatalf("failed recovery has no run.failed: %s", first)
	}
	code, stdout, stderr = runCommand(t, binary, repository, environment, "resume", runID)
	if (code != 0 && code != 4) || stdout != "" || stderr != "" {
		t.Fatalf("fixed-point replay exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if second := readJournal(t, repository, runID); !bytes.Equal(first, second) {
		t.Fatal("fixed-point replay appended duplicate durable events")
	}
}

func readJournal(t *testing.T, repository, runID string) []byte {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func hasEvent(journal []byte, want runstate.EventType) bool {
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	for scanner.Scan() {
		var event runstate.Event
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == want {
			return true
		}
	}
	return false
}

func assertChangeSetCut(t *testing.T, repository, runID string, journal []byte, before bool) {
	t.Helper()
	completed := scopedEvents(t, journal, func(event runstate.Event, payload map[string]any) bool {
		return event.RunID == runstate.RunID(runID) && event.MovementID == "writer-one" && event.Type == runstate.EventPerformerCompleted
	})
	if len(completed) != 1 || completed[0].AttemptID == "" {
		t.Fatalf("writer performer.completed events = %#v, want one scoped checkpoint predecessor", completed)
	}
	attemptID := completed[0].AttemptID
	ref := "refs/partitur/runs/" + string(runID) + "/attempts/" + string(attemptID) + "/changeset"
	if !gitRefExists(repository, ref) {
		t.Fatalf("captured change-set ref %q is absent", ref)
	}
	recorded := scopedEvents(t, journal, func(event runstate.Event, payload map[string]any) bool {
		return event.RunID == runstate.RunID(runID) && event.MovementID == "writer-one" && event.AttemptID == attemptID && event.Type == runstate.EventChangeSetRecorded
	})
	assertCutEndpoint(t, "change_set.recorded", recorded, before)
}

func assertMovementCompositionCut(t *testing.T, _ string, runID string, journal []byte, before bool) {
	t.Helper()
	evidence := scopedEvents(t, journal, func(event runstate.Event, payload map[string]any) bool {
		return event.RunID == runstate.RunID(runID) && event.MovementID == "target" && event.AttemptID == "" &&
			event.Type == runstate.EventCompositionConflicted && payload["scope"] == "movement" && payload["target_id"] == "target"
	})
	if len(evidence) != 1 {
		t.Fatalf("movement composition evidence = %#v, want one scoped composition.conflicted", evidence)
	}
	terminal := scopedEvents(t, journal, func(event runstate.Event, payload map[string]any) bool {
		return event.RunID == runstate.RunID(runID) && event.MovementID == "target" && event.AttemptID == "" &&
			event.Type == runstate.EventMovementFailed && payload["reason"] == "composition_unresolvable"
	})
	assertCutEndpoint(t, "movement.failed", terminal, before)
}

func assertMovementCompositionFailureCut(t *testing.T, _ string, runID string, journal []byte, before bool) {
	t.Helper()
	evidence := scopedEvents(t, journal, func(event runstate.Event, payload map[string]any) bool {
		return event.RunID == runstate.RunID(runID) && event.MovementID == "target" && event.AttemptID == "" &&
			event.Type == runstate.EventCompositionFailed && payload["scope"] == "movement" && payload["target_id"] == "target" &&
			payload["cause"] == "git_exit" && payload["git_exit_code"] == float64(2)
	})
	if len(evidence) != 1 {
		t.Fatalf("movement composition evidence = %#v, want one scoped no-verdict composition.failed", evidence)
	}
	terminal := scopedEvents(t, journal, func(event runstate.Event, payload map[string]any) bool {
		return event.RunID == runstate.RunID(runID) && event.MovementID == "target" && event.AttemptID == "" &&
			event.Type == runstate.EventMovementFailed && payload["reason"] == "composition_failed"
	})
	assertCutEndpoint(t, "movement.failed", terminal, before)
}

func assertCandidateCompositionCut(t *testing.T, _ string, runID string, journal []byte, before bool) {
	t.Helper()
	evidence := scopedEvents(t, journal, func(event runstate.Event, payload map[string]any) bool {
		return event.RunID == runstate.RunID(runID) && event.MovementID == "" && event.AttemptID == "" &&
			event.Type == runstate.EventCompositionConflicted && payload["scope"] == "candidate" && payload["target_id"] == runID
	})
	if len(evidence) != 1 {
		t.Fatalf("candidate composition evidence = %#v, want one scoped composition.conflicted", evidence)
	}
	terminal := scopedEvents(t, journal, func(event runstate.Event, payload map[string]any) bool {
		return event.RunID == runstate.RunID(runID) && event.MovementID == "" && event.AttemptID == "" &&
			event.Type == runstate.EventRunFailed && payload["reason"] == "composition_unresolvable"
	})
	assertCutEndpoint(t, "run.failed", terminal, before)
}

func assertCandidateCompositionFailureCut(t *testing.T, _ string, runID string, journal []byte, before bool) {
	t.Helper()
	evidence := scopedEvents(t, journal, func(event runstate.Event, payload map[string]any) bool {
		return event.RunID == runstate.RunID(runID) && event.MovementID == "" && event.AttemptID == "" &&
			event.Type == runstate.EventCompositionFailed && payload["scope"] == "candidate" && payload["target_id"] == runID &&
			payload["cause"] == "git_exit" && payload["git_exit_code"] == float64(2)
	})
	if len(evidence) != 1 {
		t.Fatalf("candidate composition evidence = %#v, want one scoped no-verdict composition.failed", evidence)
	}
	terminal := scopedEvents(t, journal, func(event runstate.Event, payload map[string]any) bool {
		return event.RunID == runstate.RunID(runID) && event.MovementID == "" && event.AttemptID == "" &&
			event.Type == runstate.EventRunFailed && payload["reason"] == "composition_failed"
	})
	assertCutEndpoint(t, "run.failed", terminal, before)
}

func assertCutEndpoint(t *testing.T, name string, events []runstate.Event, before bool) {
	t.Helper()
	if before && len(events) != 0 {
		t.Fatalf("before crash unexpectedly recorded %s: %#v", name, events)
	}
	if !before && len(events) != 1 {
		t.Fatalf("after crash %s events = %#v, want one scoped endpoint", name, events)
	}
}

func scopedEvents(t *testing.T, journal []byte, match func(runstate.Event, map[string]any) bool) []runstate.Event {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	var matched []runstate.Event
	for scanner.Scan() {
		var event runstate.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode crashed journal event: %v", err)
		}
		payload := make(map[string]any)
		if len(event.Payload) != 0 {
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatalf("decode crashed journal payload for %s: %v", event.Type, err)
			}
		}
		if match(event, payload) {
			matched = append(matched, event)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan crashed journal: %v", err)
	}
	return matched
}

func gitRefExists(repository, ref string) bool {
	command := exec.Command("git", "show-ref", "--verify", "--quiet", ref)
	command.Dir = repository
	return command.Run() == nil
}

func noVerdictGitDirectory(t *testing.T) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	script := "#!/bin/sh\nfor argument in \"$@\"; do\n" +
		"  if [ \"$argument\" = merge-tree ]; then exit 2; fi\n" +
		"done\nexec " + strconv.Quote(realGit) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(directory, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func buildBinary(t *testing.T, root, bin, name string) string {
	t.Helper()
	output := filepath.Join(bin, name)
	command := exec.Command("go", "build", "-o", output, "./cmd/"+name)
	command.Dir = root
	command.Env = os.Environ()
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, data)
	}
	return output
}

func buildVendor(t *testing.T, root, bin string) string {
	t.Helper()
	output := filepath.Join(bin, "codex")
	command := exec.Command("go", "build", "-o", output, "./integration/composition/vendor")
	command.Dir = root
	command.Env = os.Environ()
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build composition vendor: %v\n%s", err, data)
	}
	return output
}

func runCommand(t *testing.T, binary, repository string, environment []string, arguments ...string) (int, string, string) {
	t.Helper()
	command := exec.Command(binary, arguments...)
	command.Dir, command.Env = repository, environment
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), stdout.String(), stderr.String()
	}
	t.Fatal(err)
	return 0, "", ""
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, data)
	}
}

func replaceEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	seen := make(map[string]bool, len(replacements))
	for _, entry := range environment {
		name := strings.SplitN(entry, "=", 2)[0]
		if value, ok := replacements[name]; ok {
			if !seen[name] {
				result = append(result, name+"="+value)
				seen[name] = true
			}
			continue
		}
		result = append(result, entry)
	}
	for name, value := range replacements {
		if !seen[name] {
			result = append(result, name+"="+value)
		}
	}
	return result
}
