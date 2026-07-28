package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

type killEdge struct {
	id     faultpoint.EdgeID
	before faultpoint.PointID
	after  faultpoint.PointID
}

type expectedFailure struct {
	kind   string
	reason string
}

func TestSubprocessKillHarness(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, edge := range killHarnessEdges() {
		edge := edge
		t.Run(string(edge.id), func(t *testing.T) {
			for _, side := range []struct {
				name  string
				point faultpoint.PointID
			}{
				{name: "before", point: edge.before},
				{name: "after", point: edge.after},
			} {
				side := side
				t.Run(side.name, func(t *testing.T) {
					repository, environment := killHarnessRepository(t, bin, vendor)
					runID := killAtPoint(t, partitur, repository, environment, side.point)
					assertRecoveryFixedPoint(t, partitur, repository, environment, runID, expectedFailureFor(side.point))
				})
			}
		})
	}
}

func TestRetryDispositionCanFollowExecuteCut(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	score := runScore()
	score["policy"].(map[string]any)["budget"].(map[string]any)["retries_per_movement"] = float64(1)
	for _, point := range retryCoveragePoints() {
		repository, environment := killHarnessRepositoryWithInputs(t, bin, vendor, score, runCast())
		runID := killAtPoint(t, partitur, repository, environment, point)
		code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "resume", runID)
		journal, err := os.ReadFile(filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(journal, []byte(`"charged":"quality_retry"`)) {
			t.Fatalf("retry disposition was not recorded at %s: resume exit=%d stdout=%q stderr=%q journal=%s", point, code, stdout, stderr, journal)
		}
		if started := bytes.Count(journal, []byte(`"type":"attempt.started"`)); started == 0 {
			t.Fatalf("recovery-selected retry never reached attempt.started at %s: journal=%s", point, journal)
		}
		if unstarted := bytes.Count(journal, []byte(`"reason":"attempt_never_started"`)); unstarted != 1 {
			t.Fatalf("attempt_never_started failures=%d at %s, want one crashed attempt before the retry: journal=%s", unstarted, point, journal)
		}
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("retry recovery at %s: exit=%d stdout=%q stderr=%q", point, code, stdout, stderr)
		}
		assertFixedPointReplay(t, partitur, repository, environment, runID, journal)
		t.Logf("recovery-selected retry at %s reached attempt.started", point)
	}
}

func killHarnessEdges() []killEdge {
	return []killEdge{
		{faultpoint.EdgeAuthorityGrantedToLeaseCreated, faultpoint.PointAuthorityGranted, faultpoint.PointAuthorityLeaseCreated},
		{faultpoint.EdgeLaunchAdapterMarkerHeldToIdentity, faultpoint.PointLaunchAdapterMarkerHeld, faultpoint.PointLaunchAdapterIdentityPublished},
		{faultpoint.EdgeLaunchAdapterIdentityPublishedToRecorded, faultpoint.PointLaunchAdapterIdentityPublished, faultpoint.PointLaunchAdapterIdentityRecorded},
		{faultpoint.EdgeLaunchAdapterRecordedToGate, faultpoint.PointLaunchAdapterIdentityRecorded, faultpoint.PointLaunchAdapterGateReleased},
		{faultpoint.EdgeExecuteAdapterSweptToIntervalStopped, faultpoint.PointExecuteAdapterSwept, faultpoint.PointExecuteIntervalStopped},
		{faultpoint.EdgeExecuteIntervalStoppedToOutcome, faultpoint.PointExecuteIntervalStopped, faultpoint.PointExecuteOutcomeRecorded},
		{faultpoint.EdgeLifecycleAttemptCompletedToMovementSucceeded, faultpoint.PointLifecycleAttemptCompleted, faultpoint.PointLifecycleMovementSucceeded},
	}
}

func retryCoveragePoints() []faultpoint.PointID {
	return []faultpoint.PointID{
		faultpoint.PointLaunchAdapterMarkerHeld,
		faultpoint.PointLaunchAdapterIdentityPublished,
	}
}

func TestKillHarnessCatalogCrossCheck(t *testing.T) {
	design := edgeIDsFromAppendixE(t)
	dispositions := gateCutDispositions(t)
	if len(design) == 0 || len(dispositions) == 0 {
		t.Fatal("catalog extraction must not be empty")
	}
	if len(design) != len(dispositions) {
		t.Fatalf("catalog count mismatch: DESIGN=%d HARNESS=%d", len(design), len(dispositions))
	}

	reachable := make(map[string]bool)
	for _, edge := range killHarnessEdges() {
		id := string(edge.id)
		if reachable[id] {
			t.Fatalf("kill harness declares duplicate reachable edge %q", id)
		}
		reachable[id] = true
	}
	if len(reachable) != 7 {
		t.Fatalf("reachable edge count=%d, want seven", len(reachable))
	}
	if len(retryCoveragePoints()) != 2 || retryCoveragePoints()[0] == retryCoveragePoints()[1] {
		t.Fatalf("retry coverage must name two distinct cut sides: %v", retryCoveragePoints())
	}

	for edge := range design {
		disposition, present := dispositions[edge]
		if !present {
			t.Fatalf("HARNESS has no disposition for DESIGN edge %q", edge)
		}
		if disposition.reason == "" || disposition.clause == "" {
			t.Fatalf("HARNESS disposition for %q lacks reason or owning clause", edge)
		}
		switch disposition.kind {
		case "reachable":
			if !reachable[edge] {
				t.Fatalf("HARNESS calls %q reachable but this gate has no two-sided cut", edge)
			}
		case "not reached by this gate's cuts":
			if reachable[edge] {
				t.Fatalf("HARNESS marks executed edge %q as not reached", edge)
			}
		default:
			t.Fatalf("HARNESS disposition for %q = %q", edge, disposition.kind)
		}
	}
	for edge := range dispositions {
		if !design[edge] {
			t.Fatalf("HARNESS names edge %q absent from DESIGN E.2", edge)
		}
	}
}

type gateCutDisposition struct {
	kind   string
	clause string
	reason string
}

func edgeIDsFromAppendixE(t *testing.T) map[string]bool {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	return edgeIDsFromTable(t, string(contents), "## E.2 The catalog", "## E.3 ", 1, 1)
}

func gateCutDispositions(t *testing.T) map[string]gateCutDisposition {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "HARNESS.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")
	start, end := tableBounds(t, lines, "## Gate-cut dispositions", "## Execution model — deterministic interleaving, not a self-racing process")
	rows := make(map[string]gateCutDisposition)
	for _, line := range lines[start:end] {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| Edge |") || strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) != 6 {
			t.Fatalf("unparseable HARNESS disposition row %q", line)
		}
		id := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if id == "" || rows[id].kind != "" {
			t.Fatalf("empty or duplicate HARNESS disposition edge %q", id)
		}
		rows[id] = gateCutDisposition{
			kind: strings.TrimSpace(cells[2]), clause: strings.TrimSpace(cells[3]), reason: strings.TrimSpace(cells[4]),
		}
	}
	if len(rows) == 0 {
		t.Fatal("HARNESS disposition extraction produced no rows")
	}
	return rows
}

func edgeIDsFromTable(t *testing.T, contents, heading, nextHeading string, wantHeading, wantNext int) map[string]bool {
	t.Helper()
	lines := strings.Split(contents, "\n")
	start, end := tableBoundsCounted(t, lines, heading, nextHeading, wantHeading, wantNext)
	pattern := regexp.MustCompile("^\\| `([a-z][a-z0-9_.]*)` \\|")
	edges := make(map[string]bool)
	for _, line := range lines[start:end] {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| Edge |") || strings.HasPrefix(line, "|---") {
			continue
		}
		match := pattern.FindStringSubmatch(line)
		if match == nil || edges[match[1]] {
			t.Fatalf("unparseable or duplicate %s row %q", heading, line)
		}
		edges[match[1]] = true
	}
	if len(edges) == 0 {
		t.Fatalf("%s extraction produced no edge IDs", heading)
	}
	return edges
}

func tableBounds(t *testing.T, lines []string, heading, nextHeading string) (int, int) {
	t.Helper()
	return tableBoundsCounted(t, lines, heading, nextHeading, 1, 1)
}

func tableBoundsCounted(t *testing.T, lines []string, heading, nextHeading string, wantHeading, wantNext int) (int, int) {
	t.Helper()
	start, end := -1, -1
	headingCount, nextCount := 0, 0
	for index, line := range lines {
		if line == heading {
			start = index + 1
			headingCount++
		}
		if strings.HasPrefix(line, nextHeading) {
			end = index
			nextCount++
		}
	}
	if headingCount != wantHeading || nextCount != wantNext || end <= start {
		t.Fatalf("table bounds %q -> %q: headings=%d/%d next=%d/%d start=%d end=%d", heading, nextHeading, headingCount, wantHeading, nextCount, wantNext, start, end)
	}
	return start, end
}

func killHarnessRepository(t *testing.T, bin, vendor string) (string, []string) {
	return killHarnessRepositoryWithInputs(t, bin, vendor, runScore(), runCast())
}

func killHarnessRepositoryWithInputs(
	t *testing.T,
	bin, vendor string,
	scoreDocument, castDocument map[string]any,
) (string, []string) {
	t.Helper()
	repository := t.TempDir()
	writeValidateInputs(t, repository, scoreDocument, castDocument)
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, replaceEnvironment(os.Environ(), map[string]string{
		"HOME":               t.TempDir(),
		"PATH":               bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN": vendor,
		runVendorEnvironment: "1",
	})
}

func killAtPoint(
	t *testing.T,
	binary, repository string,
	environment []string,
	target faultpoint.PointID,
) string {
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
	command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  "9",
		"PARTITUR_FAULTPOINT_RELEASE_FD": "10",
	})
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
		point, pid := nextKillPoint(t, scanner)
		if point != target {
			if _, err := releaseWrite.Write([]byte{1}); err != nil {
				t.Fatalf("release %q: %v", point, err)
			}
			continue
		}
		if err := command.Process.Kill(); err != nil {
			t.Fatalf("kill run process at %q: %v", target, err)
		}
		if pid != command.Process.Pid {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = releaseWrite.Close()
		if err := command.Wait(); err == nil {
			t.Fatalf("run at %q exited successfully\nstdout:\n%s\nstderr:\n%s", target, &stdout, &stderr)
		}
		break
	}

	runID := strings.TrimSpace(stdout.String())
	if runID == "" {
		t.Fatalf("run at %q did not publish a run id\nstderr:\n%s", target, &stderr)
	}
	return runID
}

func nextKillPoint(t *testing.T, scanner *bufio.Scanner) (faultpoint.PointID, int) {
	t.Helper()
	type reached struct {
		point faultpoint.PointID
		pid   int
		err   error
	}
	ready := make(chan reached, 1)
	go func() {
		if !scanner.Scan() {
			ready <- reached{err: scanner.Err()}
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
			t.Fatalf("probe notification = %#v", result)
		}
		return result.point, result.pid
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for faultpoint probe")
		return "", 0
	}
}

func expectedFailureFor(point faultpoint.PointID) *expectedFailure {
	switch point {
	case faultpoint.PointAuthorityGranted, faultpoint.PointAuthorityLeaseCreated:
		return nil
	case faultpoint.PointLaunchAdapterMarkerHeld, faultpoint.PointLaunchAdapterIdentityPublished:
		return &expectedFailure{kind: "task_failed", reason: "attempt_never_started"}
	case faultpoint.PointLaunchAdapterIdentityRecorded, faultpoint.PointLaunchAdapterGateReleased:
		return &expectedFailure{kind: "adapter_unavailable", reason: "probe_terminated_incomplete"}
	case faultpoint.PointExecuteAdapterSwept, faultpoint.PointExecuteIntervalStopped:
		return &expectedFailure{kind: "task_failed", reason: "attempt_terminated_incomplete"}
	default:
		return nil
	}
}

func assertRecoveryFixedPoint(t *testing.T, binary, repository string, environment []string, runID string, expected *expectedFailure) {
	t.Helper()
	code, stdout, stderr := runCommandBinary(t, binary, repository, environment, "resume", runID)
	if code == 5 {
		if !strings.Contains(stderr, "recovery halted: run_id=") || !strings.Contains(stderr, "reason=") || stdout != "" {
			t.Fatalf("halt exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		return
	}
	if code != 0 && code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal := filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl")
	first, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if code == 4 {
		if expected == nil {
			t.Fatalf("unexpected failed fixed point: journal=%s", first)
		}
		assertExpectedFailure(t, first, *expected)
	} else if expected != nil {
		t.Fatalf("resume exit=%d, want failed fixed point %s/%s", code, expected.kind, expected.reason)
	}
	code, stdout, stderr = runCommandBinary(t, binary, repository, environment, "resume", runID)
	assertFixedPointReplayResult(t, code, stdout, stderr, binary, repository, environment, runID, first)
}

func assertFixedPointReplay(
	t *testing.T,
	binary, repository string,
	environment []string,
	runID string,
	first []byte,
) {
	t.Helper()
	code, stdout, stderr := runCommandBinary(t, binary, repository, environment, "resume", runID)
	assertFixedPointReplayResult(t, code, stdout, stderr, binary, repository, environment, runID, first)
}

func assertFixedPointReplayResult(
	t *testing.T,
	code int,
	stdout, stderr, binary, repository string,
	environment []string,
	runID string,
	first []byte,
) {
	t.Helper()
	if code != 0 && code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("fixed-point replay exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal := filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl")
	second, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("fixed-point replay appended duplicate durable events")
	}
	if _, err := os.Stat(filepath.Join(repository, ".partitur", "runs", runID, "driver.lease")); !os.IsNotExist(err) {
		t.Fatalf("driver lease after fixed-point recovery = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repository, ".partitur", "work", runID)); !os.IsNotExist(err) {
		t.Fatalf("attempt worktree after fixed-point recovery = %v", err)
	}
	checkRef := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/partitur/runs/"+runID+"/base")
	checkRef.Dir = repository
	if err := checkRef.Run(); err != nil {
		t.Fatalf("base ref is inconsistent after recovery: %v", err)
	}
}

func assertExpectedFailure(t *testing.T, journal []byte, expected expectedFailure) {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	var last *runstate.Event
	for scanner.Scan() {
		var event runstate.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == runstate.EventAttemptFailed || event.Type == runstate.EventAcceptanceFailed {
			copy := event
			last = &copy
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if last == nil {
		t.Fatalf("failed fixed point has no recorded attempt or acceptance failure: journal=%s", journal)
	}
	var payload map[string]any
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["kind"] != expected.kind || payload["reason"] != expected.reason {
		t.Fatalf("recorded failure = kind=%q reason=%q, want %q/%q", payload["kind"], payload["reason"], expected.kind, expected.reason)
	}
}
