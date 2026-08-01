package criterionexec_test

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

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

const vendorEnvironment = "PARTITUR_CRITERIONEXEC_VENDOR"

func TestMain(m *testing.M) {
	if os.Getenv(vendorEnvironment) == "1" {
		runVendor()
		return
	}
	os.Exit(m.Run())
}

func TestHardRunCriterionEndToEnd(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildBinary(t, root, bin, "partitur")
	buildBinary(t, root, bin, "partitur-adapter-codex")
	buildBinary(t, root, bin, "partitur-trampoline")
	writeCriterionHelper(t, bin)
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	writeJSON(t, filepath.Join(repository, "partitur.yaml"), scoreFixture())
	writeJSON(t, filepath.Join(repository, ".partitur", "cast.yaml"), castFixture())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")

	var stdout, stderr bytes.Buffer
	command := exec.Command(partitur, "run")
	command.Dir = repository
	command.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN="+vendor,
		vendorEnvironment+"=1",
	)
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("partitur run: %v stderr=%s", err, stderr.String())
	}
	runID := runstate.RunID(strings.TrimSpace(stdout.String()))
	if runID == "" || stderr.Len() != 0 {
		t.Fatalf("run output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	started, completed := false, false
	for _, event := range journal.Events {
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["criterion_id"] != "command-passes" {
			continue
		}
		switch event.Type {
		case runstate.EventCriterionStarted:
			if _, ok := payload["criterion_process"].(map[string]any); !ok || payload["spawn_failed"] != nil {
				t.Fatalf("run criterion start payload = %#v", payload)
			}
			started = true
		case runstate.EventCriterionCompleted:
			if payload["outcome"] != "PASS" || payload["output_ref"] == "" {
				t.Fatalf("run criterion completion payload = %#v", payload)
			}
			completed = true
		}
	}
	if !started || !completed {
		t.Fatalf("hard.run lifecycle started=%t completed=%t", started, completed)
	}
}

func TestCriterionLaunchKillCuts(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildBinary(t, root, bin, "partitur")
	buildBinary(t, root, bin, "partitur-adapter-codex")
	buildBinary(t, root, bin, "partitur-trampoline")
	writeCriterionHelper(t, bin)
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, edge := range []struct {
		name   string
		before faultpoint.PointID
		after  faultpoint.PointID
		assert func(*testing.T, string, runstate.RunID, []runstate.Event, bool)
	}{
		{
			name:   "launch.criterion.marker_held_to_identity_published",
			before: faultpoint.PointLaunchCriterionMarkerHeld,
			after:  faultpoint.PointLaunchCriterionIdentityPublished,
			assert: assertCriterionMarkerToIdentityCut,
		},
		{
			name:   "launch.criterion.identity_published_to_recorded",
			before: faultpoint.PointLaunchCriterionIdentityPublished,
			after:  faultpoint.PointLaunchCriterionIdentityRecorded,
			assert: assertCriterionIdentityToRecordedCut,
		},
		{
			name:   "launch.criterion.recorded_to_gate",
			before: faultpoint.PointLaunchCriterionIdentityRecorded,
			after:  faultpoint.PointLaunchCriterionGateReleased,
			assert: assertCriterionRecordedToGateCut,
		},
	} {
		edge := edge
		t.Run(edge.name, func(t *testing.T) {
			t.Parallel()
			for _, side := range []struct {
				name  string
				point faultpoint.PointID
			}{
				{name: "before", point: edge.before},
				{name: "after", point: edge.after},
			} {
				side := side
				t.Run(side.name, func(t *testing.T) {
					t.Parallel()
					repository, environment := criterionRepository(t, bin, vendor)
					runID := killCriterionAtPoint(t, partitur, repository, environment, side.point)
					journal := readCriterionJournal(t, repository, runID)
					edge.assert(t, repository, runID, journal, side.name == "before")
					assertCriterionRecoveryFixedPoint(t, partitur, repository, environment, runID)
				})
			}
		})
	}
}

func TestCriterionRecoveryPreservesCompletedLaunchEvidence(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildBinary(t, root, bin, "partitur")
	buildBinary(t, root, bin, "partitur-adapter-codex")
	buildBinary(t, root, bin, "partitur-trampoline")
	writeCriterionHelper(t, bin)
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment := twoCriterionRepository(t, bin, vendor)
	runID := killCriterionAtOccurrence(t, partitur, repository, environment, faultpoint.PointLaunchCriterionIdentityPublished, 2)

	firstLaunch := criterionLaunchPath(repository, runID, "criterion-first-completes")
	secondLaunch := criterionLaunchPath(repository, runID, "criterion-second-spawn-fails")
	if _, err := os.Stat(filepath.Join(firstLaunch, "identity.json")); err != nil {
		t.Fatalf("completed first criterion launch evidence before recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(secondLaunch, "identity.json")); err != nil {
		t.Fatalf("unjournaled second criterion launch evidence before recovery: %v", err)
	}
	journal := readCriterionJournal(t, repository, runID)
	if criterionEventCount(t, journal, runstate.EventCriterionCompleted, "first-completes") != 1 ||
		criterionEventCount(t, journal, runstate.EventCriterionStarted, "second-spawn-fails") != 0 {
		t.Fatalf("cut lifecycle does not leave first completed and second unjournaled")
	}
	killCriterionResumeAtPoint(t, partitur, repository, environment, runID, faultpoint.PointLaunchCriterionMarkerHeld)
	if _, err := os.Stat(filepath.Join(firstLaunch, "identity.json")); err != nil {
		t.Fatalf("recovery removed completed first criterion evidence: %v", err)
	}
	if _, err := os.Stat(filepath.Join(secondLaunch, "identity.json")); !os.IsNotExist(err) {
		t.Fatalf("recovery did not remove the unjournaled second criterion identity before relaunch: %v", err)
	}
	if err := os.Remove(filepath.Join(bin, "criterion-helper")); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCriterionCommand(t, partitur, repository, environment, "resume", string(runID))
	if code != 0 && code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	first, err := os.ReadFile(filepath.Join(repository, ".partitur", "runs", string(runID), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr = runCriterionCommand(t, partitur, repository, environment, "resume", string(runID))
	if code != 0 && code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("fixed-point replay exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	second, err := os.ReadFile(filepath.Join(repository, ".partitur", "runs", string(runID), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("fixed-point replay appended duplicate durable events")
	}
}

func runVendor() {
	for _, argument := range os.Args[1:] {
		if argument == "--version" {
			_, _ = os.Stdout.WriteString("codex 1.0.0\n")
			return
		}
	}
	prompt, err := os.ReadFile("/dev/stdin")
	if err != nil {
		os.Exit(91)
	}
	outputDirectory, artifactID := "", ""
	for _, line := range strings.Split(string(prompt), "\n") {
		if strings.HasPrefix(line, "- Writable artifact directory: ") {
			outputDirectory = strings.TrimSpace(strings.TrimPrefix(line, "- Writable artifact directory: "))
		}
		if strings.HasPrefix(line, "- artifact_id=\"") {
			artifactID, _, _ = strings.Cut(strings.TrimPrefix(line, "- artifact_id=\""), "\"")
		}
	}
	if outputDirectory == "" || artifactID == "" {
		os.Exit(92)
	}
	if err := os.WriteFile(filepath.Join(outputDirectory, "report.txt"), []byte("report\n"), 0o600); err != nil {
		os.Exit(93)
	}
	result, err := json.Marshal(map[string]any{
		"version":   1,
		"artifacts": []any{map[string]any{"artifact_id": artifactID, "path": "report.txt"}},
		"questions": []any{}, "proposal": nil, "summary": "completed",
	})
	if err != nil {
		os.Exit(94)
	}
	if err := os.WriteFile(filepath.Join(outputDirectory, "partitur-result.json"), result, 0o600); err != nil {
		os.Exit(95)
	}
}

func buildBinary(t *testing.T, root, bin, name string) string {
	t.Helper()
	output := filepath.Join(bin, name)
	command := exec.Command("go", "build", "-tags=faultprobe", "-o", output, "./cmd/"+name)
	command.Dir = root
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, data)
	}
	return output
}

func criterionRepository(t *testing.T, bin, vendor string) (string, []string) {
	t.Helper()
	repository := t.TempDir()
	writeJSON(t, filepath.Join(repository, "partitur.yaml"), scoreFixture())
	writeJSON(t, filepath.Join(repository, ".partitur", "cast.yaml"), castFixture())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, append(os.Environ(),
		"HOME="+t.TempDir(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN="+vendor,
		vendorEnvironment+"=1",
	)
}

func twoCriterionRepository(t *testing.T, bin, vendor string) (string, []string) {
	t.Helper()
	repository := t.TempDir()
	fixture := scoreFixture()
	movement := fixture["movements"].([]any)[0].(map[string]any)
	acceptance := movement["acceptance"].(map[string]any)
	acceptance["hard"] = []any{
		map[string]any{"id": "first-completes", "run": []any{"criterion-helper"}},
		map[string]any{"id": "second-spawn-fails", "run": []any{"criterion-helper"}},
	}
	writeJSON(t, filepath.Join(repository, "partitur.yaml"), fixture)
	writeJSON(t, filepath.Join(repository, ".partitur", "cast.yaml"), castFixture())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, append(os.Environ(),
		"HOME="+t.TempDir(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN="+vendor,
		vendorEnvironment+"=1",
	)
}

func writeCriterionHelper(t *testing.T, bin string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, "criterion-helper"), []byte("#!/bin/sh\nprintf x > \"$TMPDIR/criterion-ran\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func killCriterionAtPoint(t *testing.T, binary, repository string, environment []string, target faultpoint.PointID) runstate.RunID {
	t.Helper()
	return killCriterionAtOccurrence(t, binary, repository, environment, target, 1)
}

func killCriterionAtOccurrence(t *testing.T, binary, repository string, environment []string, target faultpoint.PointID, occurrence int) runstate.RunID {
	t.Helper()
	return runstate.RunID(killCriterionCommandAtOccurrence(t, binary, repository, environment, []string{"run"}, target, occurrence))
}

func killCriterionResumeAtPoint(t *testing.T, binary, repository string, environment []string, runID runstate.RunID, target faultpoint.PointID) {
	t.Helper()
	_ = killCriterionCommandAtOccurrence(t, binary, repository, environment, []string{"resume", string(runID)}, target, 1)
}

func killCriterionCommandAtOccurrence(t *testing.T, binary, repository string, environment []string, arguments []string, target faultpoint.PointID, occurrence int) string {
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
	command := exec.Command(binary, arguments...)
	command.Dir, command.ExtraFiles, command.Stdout, command.Stderr = repository, files, &stdout, &stderr
	command.Env = append(environment,
		"PARTITUR_FAULTPOINT_HARNESS=1",
		"PARTITUR_FAULTPOINT_NOTIFY_FD=9",
		"PARTITUR_FAULTPOINT_RELEASE_FD=10",
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = notifyWrite.Close()
	_ = releaseRead.Close()
	scanner := bufio.NewScanner(notifyRead)
	reached := 0
	for {
		point, pid, err := nextCriterionPoint(scanner)
		if err != nil {
			waitErr := command.Wait()
			t.Fatalf("wait for faultpoint %s: %v; run=%v stdout=%q stderr=%q", target, err, waitErr, stdout.String(), stderr.String())
		}
		if point != target {
			if _, err := releaseWrite.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			continue
		}
		reached++
		if reached != occurrence {
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
	output := strings.TrimSpace(stdout.String())
	if len(arguments) == 1 && arguments[0] == "run" && output == "" {
		t.Fatalf("run at %s published no run id; stderr=%s", target, stderr.String())
	}
	if len(arguments) != 1 || arguments[0] != "run" {
		if output != "" || stderr.Len() != 0 {
			t.Fatalf("resume at %s output stdout=%q stderr=%q", target, stdout.String(), stderr.String())
		}
	}
	return output
}

func nextCriterionPoint(scanner *bufio.Scanner) (faultpoint.PointID, int, error) {
	type reached struct {
		point faultpoint.PointID
		pid   int
		err   error
	}
	ready := make(chan reached, 1)
	go func() {
		if !scanner.Scan() {
			ready <- reached{err: fmt.Errorf("faultpoint notification closed: %w", scanner.Err())}
			return
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			ready <- reached{err: fmt.Errorf("malformed probe notification %q", scanner.Text())}
			return
		}
		pid, err := strconv.Atoi(fields[1])
		ready <- reached{point: faultpoint.PointID(fields[0]), pid: pid, err: err}
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

func readCriterionJournal(t *testing.T, repository string, runID runstate.RunID) []runstate.Event {
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

func assertCriterionMarkerToIdentityCut(t *testing.T, repository string, runID runstate.RunID, _ []runstate.Event, before bool) {
	t.Helper()
	identity := criterionIdentityPath(repository, runID)
	_, err := os.Stat(identity)
	if before && !os.IsNotExist(err) {
		t.Fatalf("marker-held crash unexpectedly published identity: %v", err)
	}
	if !before && err != nil {
		t.Fatalf("identity-published crash has no identity: %v", err)
	}
}

func assertCriterionIdentityToRecordedCut(t *testing.T, repository string, runID runstate.RunID, events []runstate.Event, before bool) {
	t.Helper()
	if _, err := os.Stat(criterionIdentityPath(repository, runID)); err != nil {
		t.Fatalf("identity-published crash has no identity: %v", err)
	}
	assertCriterionStartedEndpoint(t, events, before)
}

func assertCriterionRecordedToGateCut(t *testing.T, repository string, runID runstate.RunID, events []runstate.Event, before bool) {
	t.Helper()
	assertCriterionStartedEndpoint(t, events, false)
	if criterionCompleted(t, events) {
		t.Fatal("launch handoff crash unexpectedly completed the gated criterion")
	}
	if _, err := os.Stat(criterionRanPath(repository, runID)); !os.IsNotExist(err) {
		t.Fatalf("%s crash unexpectedly released the criterion helper: %v", map[bool]string{true: "identity-recorded", false: "gate-released"}[before], err)
	}
}

func criterionIdentityPath(repository string, runID runstate.RunID) string {
	return filepath.Join(criterionAttemptPath(repository, runID), "criterion-command-passes", "identity.json")
}

func criterionRanPath(repository string, runID runstate.RunID) string {
	return filepath.Join(criterionAttemptPath(repository, runID), "tmp", "criterion-ran")
}

func criterionAttemptPath(repository string, runID runstate.RunID) string {
	attempts, err := os.ReadDir(filepath.Join(repository, ".partitur", "work", string(runID)))
	if err != nil {
		return ""
	}
	for _, attempt := range attempts {
		if attempt.IsDir() {
			return filepath.Join(repository, ".partitur", "work", string(runID), attempt.Name())
		}
	}
	return ""
}

func criterionLaunchPath(repository string, runID runstate.RunID, launchID string) string {
	return filepath.Join(criterionAttemptPath(repository, runID), launchID)
}

func assertCriterionStartedEndpoint(t *testing.T, events []runstate.Event, before bool) {
	t.Helper()
	count := 0
	for _, event := range events {
		if event.Type == runstate.EventCriterionStarted && criterionEventID(t, event) == "command-passes" {
			count++
		}
	}
	if before && count != 0 {
		t.Fatalf("before crash unexpectedly recorded criterion.started: %d", count)
	}
	if !before && count != 1 {
		t.Fatalf("after crash criterion.started events = %d, want one", count)
	}
}

func criterionEventID(t *testing.T, event runstate.Event) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	id, _ := payload["criterion_id"].(string)
	return id
}

func criterionEventCount(t *testing.T, events []runstate.Event, eventType runstate.EventType, criterionID string) int {
	t.Helper()
	count := 0
	for _, event := range events {
		if event.Type == eventType && criterionEventID(t, event) == criterionID {
			count++
		}
	}
	return count
}

func criterionCompleted(t *testing.T, events []runstate.Event) bool {
	t.Helper()
	for _, event := range events {
		if event.Type == runstate.EventCriterionCompleted && criterionEventID(t, event) == "command-passes" {
			return true
		}
	}
	return false
}

func assertCriterionRecoveryFixedPoint(t *testing.T, binary, repository string, environment []string, runID runstate.RunID) {
	t.Helper()
	code, stdout, stderr := runCriterionCommand(t, binary, repository, environment, "resume", string(runID))
	if code != 0 && code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	first, err := os.ReadFile(filepath.Join(repository, ".partitur", "runs", string(runID), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if !input.Projection.State.Run.Terminal() {
		t.Fatalf("recovery projection run = %q, want terminal", input.Projection.State.Run)
	}
	for key, launch := range input.Projection.State.CriterionLaunches {
		spawned, ok := launch.(runstate.SpawnedCriterionLaunch)
		if !ok {
			continue
		}
		empty, err := adapter.SessionEmpty(spawned.Process)
		if err != nil || !empty {
			t.Fatalf("recorded criterion %q session after recovery: empty=%t err=%v", key.CriterionID, empty, err)
		}
	}
	code, stdout, stderr = runCriterionCommand(t, binary, repository, environment, "resume", string(runID))
	if code != 0 && code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("fixed-point replay exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	second, err := os.ReadFile(filepath.Join(repository, ".partitur", "runs", string(runID), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("fixed-point replay appended duplicate durable events")
	}
}

func runCriterionCommand(t *testing.T, binary, repository string, environment []string, arguments ...string) (int, string, string) {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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

func scoreFixture() map[string]any {
	return map[string]any{
		"score": "0.2", "name": "criterion-exec", "revision": 1, "status": "finalized", "goal": "run a declared criterion",
		"verification": map[string]any{
			"expectation":    map[string]any{"intent": "pass-existing-tests", "apply_gate": map[string]any{"require": []any{"verified"}}},
			"final_movement": "inspect",
		},
		"parts": map[string]any{"reader": map[string]any{"capabilities": []any{"repo_read", "shell", "network"}, "read_only": true}},
		"movements": []any{map[string]any{
			"id": "inspect", "part": "reader", "grants": []any{"repo_read", "shell", "network"}, "instruction": "Write the declared report.",
			"outputs": []any{map[string]any{"id": "report", "kind": "artifact"}},
			"acceptance": map[string]any{"hard": []any{
				map[string]any{"id": "report-present", "artifact": "report"},
				map[string]any{"id": "command-passes", "run": []any{"criterion-helper"}},
			}},
		}},
		"policy": map[string]any{"allowed_paths": []any{"**"}, "budget": map[string]any{"active_wall_clock_min": 10}},
	}
}

func castFixture() map[string]any {
	return map[string]any{
		"cast": "0.1", "performers": map[string]any{"worker": map[string]any{"adapter": "codex", "model": "fixture"}},
		"bindings": map[string]any{"reader": map[string]any{"performer": "worker"}},
	}
}
