package scheduler_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

const vendorEnvironment = "PARTITUR_SCHEDULER_VENDOR"

func TestMain(m *testing.M) {
	if os.Getenv(vendorEnvironment) == "1" {
		runVendor()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestSequentialDeclarationOrderDAGScheduling(t *testing.T) {
	events := runScore(t, nonWaivedScore(), "")
	for _, movement := range []string{"c", "a", "b"} {
		assertMovementSequence(t, events, movement, false)
	}
	assertMovementSequence(t, events, "final", true)
	if containsEvent(events, runstate.EventRunSucceeded) {
		t.Fatal("non-waived path appended run.succeeded")
	}
	if firstMovement(events) != "c" {
		t.Fatalf("first eligible movement = %q, want declaration-first c", firstMovement(events))
	}
	if movementSequence(events, "b")[0].Seq < movementSequence(events, "a")[len(movementSequence(events, "a"))-1].Seq {
		t.Fatal("dependency movement b became ready before a succeeded")
	}
}

func TestWaivedRunCarriesItsCandidateWithoutMovementTerminalTransition(t *testing.T) {
	events := runScore(t, waivedScore(), "")
	for _, movement := range []string{"first", "second"} {
		assertMovementSequence(t, events, movement, false)
	}
	if got := eventTypes(events)[len(events)-1]; got != runstate.EventRunSucceeded {
		t.Fatalf("last event = %s, want run.succeeded", got)
	}
	for _, event := range events {
		if event.Type != runstate.EventMovementSucceeded {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["run_succeeded"] != false {
			t.Fatalf("waived movement.succeeded payload = %v", payload)
		}
	}
	var terminal map[string]any
	if err := json.Unmarshal(events[len(events)-1].Payload, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal["waiver"].(map[string]any)["reason"] != "scheduler integration fixture" {
		t.Fatalf("waiver payload = %v", terminal)
	}
	candidate := terminal["candidate"].(map[string]any)
	if candidate["candidate_id"] == "" {
		t.Fatalf("candidate payload = %v", candidate)
	}
	if candidate["base_tree"] != candidate["result_tree"] {
		t.Fatalf("candidate payload = %v", candidate)
	}
}

func TestFailedMovementStopsTheLiveScheduler(t *testing.T) {
	events := runScore(t, nonWaivedScore(), "c")
	if !containsEvent(events, runstate.EventMovementFailed) {
		t.Fatalf("journal lacks movement.failed: %v", eventTypes(events))
	}
	if !containsEvent(events, runstate.EventRunFailed) {
		t.Fatalf("journal lacks run.failed: %v", eventTypes(events))
	}
	failed := false
	for _, event := range events {
		if event.Type == runstate.EventMovementFailed {
			failed = true
			continue
		}
		if !failed {
			continue
		}
		switch event.Type {
		case runstate.EventMovementReady, runstate.EventMovementStarted, runstate.EventPerformerSelected:
			t.Fatalf("event %s followed movement.failed", event.Type)
		}
	}
}

func TestMovementFailureCutPointsSurviveSubprocessKill(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := build(t, root, bin, "partitur")
	build(t, root, bin, "partitur-adapter-codex")
	build(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, cut := range []struct {
		name         string
		point        faultpoint.PointID
		wantTerminal bool
	}{
		{name: "movement_failed", point: faultpoint.PointLifecycleMovementFailed},
		{name: "run_failed", point: faultpoint.PointLifecycleRunFailed, wantTerminal: true},
	} {
		cut := cut
		t.Run(cut.name, func(t *testing.T) {
			repository, environment := schedulerFailureRepository(t, bin, vendor)
			runID := killSchedulerAtPoint(t, partitur, repository, environment, cut.point)
			store, err := runstore.New(repository, faultpoint.Nop{})
			if err != nil {
				t.Fatal(err)
			}
			journal, err := store.ReadJournal(runID)
			if err != nil {
				t.Fatal(err)
			}
			input, err := store.LoadRunInput(runID)
			if err != nil {
				t.Fatal(err)
			}
			if !containsEvent(journal.Events, runstate.EventMovementFailed) {
				t.Fatalf("journal at %s lacks movement.failed: %v", cut.point, eventTypes(journal.Events))
			}
			if containsEvent(journal.Events, runstate.EventRunFailed) != cut.wantTerminal {
				t.Fatalf("journal at %s run.failed=%t, want %t: %v", cut.point, containsEvent(journal.Events, runstate.EventRunFailed), cut.wantTerminal, eventTypes(journal.Events))
			}
			if input.Projection.State.Run.Terminal() != cut.wantTerminal {
				t.Fatalf("state at %s terminal=%t, want %t", cut.point, input.Projection.State.Run.Terminal(), cut.wantTerminal)
			}
		})
	}
}

func runScore(t *testing.T, score map[string]any, failedMovement string) []runstate.Event {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := build(t, root, bin, "partitur")
	build(t, root, bin, "partitur-adapter-codex")
	build(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	writeInputs(t, repository, score)
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	environment := append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN="+vendor,
		vendorEnvironment+"=1",
		"PARTITUR_SCHEDULER_FAIL_MOVEMENT="+failedMovement,
	)
	var stdout, stderr bytes.Buffer
	command := exec.Command(partitur, "run")
	command.Dir = repository
	command.Env = environment
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if failedMovement == "" {
			t.Fatalf("run: %v stderr=%s", err, stderr.String())
		}
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runstate.RunID(strings.TrimSpace(stdout.String())))
	if err != nil {
		t.Fatal(err)
	}
	return journal.Events
}

func schedulerFailureRepository(t *testing.T, bin, vendor string) (string, []string) {
	t.Helper()
	repository := t.TempDir()
	writeInputs(t, repository, nonWaivedScore())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN="+vendor,
		vendorEnvironment+"=1",
		"PARTITUR_SCHEDULER_FAIL_MOVEMENT=c",
	)
}

func killSchedulerAtPoint(
	t *testing.T,
	binary, repository string,
	environment []string,
	target faultpoint.PointID,
) runstate.RunID {
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

	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, "run")
	command.Dir = repository
	command.Env = append(environment,
		"PARTITUR_FAULTPOINT_NOTIFY_FD=9",
		"PARTITUR_FAULTPOINT_RELEASE_FD=10",
	)
	command.ExtraFiles = files
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = notifyWrite.Close()
	_ = releaseRead.Close()

	scanner := bufio.NewScanner(notifyRead)
	for {
		point, pid := nextSchedulerFaultPoint(t, scanner)
		if point != target {
			if _, err := releaseWrite.Write([]byte{1}); err != nil {
				t.Fatalf("release %q: %v", point, err)
			}
			continue
		}
		if pid != command.Process.Pid {
			t.Fatalf("faultpoint %q pid=%d, want command pid=%d", point, pid, command.Process.Pid)
		}
		if err := command.Process.Kill(); err != nil {
			t.Fatalf("kill at %q: %v", point, err)
		}
		_ = releaseWrite.Close()
		if err := command.Wait(); err == nil {
			t.Fatalf("run at %q exited successfully\nstdout:\n%s\nstderr:\n%s", target, &stdout, &stderr)
		}
		runID := runstate.RunID(strings.TrimSpace(stdout.String()))
		if runID == "" {
			t.Fatalf("run at %q did not publish a run id\nstderr:\n%s", target, &stderr)
		}
		return runID
	}
}

func nextSchedulerFaultPoint(t *testing.T, scanner *bufio.Scanner) (faultpoint.PointID, int) {
	t.Helper()
	type reached struct {
		point faultpoint.PointID
		pid   int
		err   error
	}
	result := make(chan reached, 1)
	go func() {
		if !scanner.Scan() {
			result <- reached{err: scanner.Err()}
			return
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			result <- reached{err: fmt.Errorf("malformed probe notification %q", scanner.Text())}
			return
		}
		pid, err := strconv.Atoi(fields[1])
		result <- reached{point: faultpoint.PointID(fields[0]), pid: pid, err: err}
	}()
	select {
	case reached := <-result:
		if reached.err != nil || reached.point == "" || reached.pid <= 0 {
			t.Fatalf("probe notification = %#v", reached)
		}
		return reached.point, reached.pid
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for movement-failure faultpoint")
		return "", 0
	}
}

func build(t *testing.T, root, bin, name string) string {
	t.Helper()
	output := filepath.Join(bin, name)
	command := exec.Command("go", "build", "-o", output, "./cmd/"+name)
	command.Dir = root
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, data)
	}
	return output
}

func writeInputs(t *testing.T, repository string, score map[string]any) {
	t.Helper()
	writeJSON(t, filepath.Join(repository, "partitur.yaml"), score)
	if err := os.MkdirAll(filepath.Join(repository, ".partitur"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(repository, ".partitur", "cast.yaml"), map[string]any{
		"cast": "0.1", "performers": map[string]any{"worker": map[string]any{"adapter": "codex", "model": "scheduler"}},
		"bindings": map[string]any{"reader": map[string]any{"performer": "worker"}},
	})
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

func nonWaivedScore() map[string]any {
	return scoreDocument(false, []any{
		movement("b", []any{"a"}, false), movement("c", []any{}, false),
		movement("a", []any{}, false), movement("final", []any{"a", "b", "c"}, true),
	})
}

func waivedScore() map[string]any {
	return scoreDocument(true, []any{movement("first", []any{}, false), movement("second", []any{"first"}, false)})
}

func scoreDocument(waived bool, movements []any) map[string]any {
	gate := map[string]any{"require": []any{"verified"}}
	verification := map[string]any{"expectation": map[string]any{"intent": "pass-existing-tests", "apply_gate": gate}}
	if waived {
		gate = map[string]any{"waived": true, "reason": "scheduler integration fixture"}
		verification = map[string]any{"expectation": map[string]any{"intent": "pass-existing-tests", "apply_gate": gate}}
	} else {
		verification["final_movement"] = "final"
	}
	return map[string]any{
		"score": "0.2", "name": "scheduler", "revision": 1, "status": "finalized", "goal": "exercise scheduling",
		"verification": verification,
		"parts":        map[string]any{"reader": map[string]any{"capabilities": []any{"repo_read", "shell", "network"}, "read_only": true}},
		"movements":    movements,
		"policy":       map[string]any{"allowed_paths": []any{"**"}, "budget": map[string]any{"active_wall_clock_min": 10}},
	}
}

func movement(id string, needs []any, final bool) map[string]any {
	value := map[string]any{"id": id, "part": "reader", "needs": needs, "grants": []any{"repo_read", "shell", "network"}, "instruction": id}
	artifactID := "report-" + id
	value["outputs"] = []any{map[string]any{"id": artifactID, "kind": "artifact"}}
	value["acceptance"] = map[string]any{"hard": []any{map[string]any{"id": "report-present", "artifact": artifactID}}}
	return value
}

func assertMovementSequence(t *testing.T, events []runstate.Event, movement string, runSucceeded bool) {
	t.Helper()
	sequence := movementSequence(events, movement)
	want := []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted, runstate.EventPerformerSelected, runstate.EventExecutionStarted, runstate.EventAttemptStarted, runstate.EventAdapterProbed, runstate.EventProgress, runstate.EventArtifactRecorded, runstate.EventExecutionStopped, runstate.EventPerformerCompleted, runstate.EventVerificationPassed, runstate.EventExecutionStarted, runstate.EventAcceptanceStarted, runstate.EventCriterionStarted, runstate.EventCriterionCompleted, runstate.EventAcceptanceEvaluationCompleted, runstate.EventExecutionStopped, runstate.EventAttemptCompleted, runstate.EventMovementSucceeded}
	if !slices.Equal(eventTypes(sequence), want) {
		t.Fatalf("movement %s events = %v, want %v", movement, eventTypes(sequence), want)
	}
	var payload map[string]any
	if err := json.Unmarshal(sequence[len(sequence)-1].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["run_succeeded"] != runSucceeded {
		t.Fatalf("movement %s run_succeeded = %v, want %t", movement, payload["run_succeeded"], runSucceeded)
	}
}

func movementSequence(events []runstate.Event, movement string) []runstate.Event {
	var result []runstate.Event
	for _, event := range events {
		if string(event.MovementID) == movement {
			result = append(result, event)
		}
	}
	return result
}

func firstMovement(events []runstate.Event) string {
	for _, event := range events {
		if event.Type == runstate.EventMovementReady {
			return string(event.MovementID)
		}
	}
	return ""
}

func eventTypes(events []runstate.Event) []runstate.EventType {
	result := make([]runstate.EventType, len(events))
	for index, event := range events {
		result[index] = event.Type
	}
	return result
}

func containsEvent(events []runstate.Event, want runstate.EventType) bool {
	return slices.Contains(eventTypes(events), want)
}

func runVendor() {
	for _, argument := range os.Args[1:] {
		if argument == "--version" {
			fmt.Println("scheduler 1.0.0")
			return
		}
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(91)
	}
	outputDir := ""
	for _, line := range strings.Split(string(prompt), "\n") {
		if strings.HasPrefix(line, "- Writable artifact directory: ") {
			outputDir = strings.TrimSpace(strings.TrimPrefix(line, "- Writable artifact directory: "))
		}
	}
	if outputDir == "" {
		os.Exit(92)
	}
	failMovement := os.Getenv("PARTITUR_SCHEDULER_FAIL_MOVEMENT")
	artifactID := declaredArtifactID(string(prompt))
	artifacts := []any{}
	if artifactID != "" {
		if strings.Contains(string(prompt), "## Instruction\n\n"+failMovement+"\n") {
			artifactID = ""
		}
	}
	if artifactID != "" {
		if err := os.WriteFile(filepath.Join(outputDir, "report.txt"), []byte("report\n"), 0o600); err != nil {
			os.Exit(93)
		}
		artifacts = append(artifacts, map[string]any{"artifact_id": artifactID, "path": "report.txt"})
	}
	data, err := json.Marshal(map[string]any{"version": 1, "artifacts": artifacts, "questions": []any{}, "proposal": nil, "summary": "completed"})
	if err != nil {
		os.Exit(94)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "partitur-result.json"), data, 0o600); err != nil {
		os.Exit(95)
	}
	fmt.Println(`{"type":"item.started","item":{"type":"command_execution","name":"fixture"}}`)
}

func declaredArtifactID(prompt string) string {
	marker := `- artifact_id="`
	start := strings.Index(prompt, marker)
	if start < 0 {
		return ""
	}
	value := prompt[start+len(marker):]
	end := strings.Index(value, `"`)
	if end < 0 {
		return ""
	}
	return value[:end]
}
