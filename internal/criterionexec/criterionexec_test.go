package criterionexec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const (
	criterionTestRunID     = runstate.RunID("01a05f59-8be8-7abc-9123-123456789abc")
	criterionTestAttemptID = runstate.AttemptID("01a05f5d-04b4-7def-8456-abcdef123456")
)

func TestCriterionHelperProcess(t *testing.T) {
	if len(os.Args) < 2 {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "pass":
		fmt.Fprintln(os.Stdout, "criterion-pass")
	case "report-environment":
		if err := json.NewEncoder(os.Stdout).Encode(os.Environ()); err != nil {
			t.Fatal(err)
		}
	case "mutate":
		if err := os.WriteFile("mutated", []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	case "descendant":
		child := exec.Command(os.Args[0], "-test.run=^TestCriterionHelperProcess$", "--", "sleep")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(os.Stdout, child.Process.Pid)
	case "sleep":
		select {}
	case "hold-stderr":
		select {}
	case "bind-unix-socket":
		fmt.Fprintln(os.Stdout, os.Getenv("TMPDIR"))
		directory := filepath.Join(os.Getenv("TMPDIR"), "tsx-501")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		socket := filepath.Join(directory, "53072.pipe")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatalf("listen unix socket %q (%d bytes): %v", socket, len([]byte(socket)), err)
		}
		defer listener.Close()
	case "hold-unix-socket":
		ready := os.Args[len(os.Args)-3]
		release := os.Args[len(os.Args)-2]
		directory := filepath.Join(os.Getenv("TMPDIR"), "shared-name")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		socket := filepath.Join(directory, "criterion.pipe")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatalf("listen unix socket %q: %v", socket, err)
		}
		defer listener.Close()
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(release); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("release marker was not published")
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprintln(os.Stdout, os.Getenv("TMPDIR"))
	}
}

func TestRunProvidesSocketSafeCriterionTemporaryDirectory(t *testing.T) {
	root, worktree, trampoline := criterionFixture(t)
	config := criterionConfig(root, worktree, trampoline)
	config.RunID = "01a05f59-8be8-7abc-9123-123456789abc"
	config.AttemptID = "01a05f5d-04b4-7def-8456-abcdef123456"
	config.AttemptRoot = filepath.Join(root, ".partitur", "work", string(config.RunID), string(config.AttemptID))
	result := Run(config, criterionRequest(t, "bind-unix-socket"))
	stdout, _ := os.ReadFile(filepath.Join(root, ".partitur", "runs", string(config.RunID), "attempts", string(config.AttemptID), "criteria", "criterion", "stdout"))
	temporary, _, _ := strings.Cut(string(stdout), "\n")
	if strings.HasPrefix(temporary, "/tmp/p") {
		t.Cleanup(func() {
			if err := os.RemoveAll(filepath.Dir(temporary)); err != nil {
				t.Fatal(err)
			}
		})
	}
	if result.Outcome != "PASS" {
		stderr, _ := os.ReadFile(filepath.Join(root, ".partitur", "runs", string(config.RunID), "attempts", string(config.AttemptID), "criteria", "criterion", "stderr"))
		t.Fatalf("criterion result = %#v\nstdout=%s\nstderr=%s", result, stdout, stderr)
	}
	if !strings.HasPrefix(temporary, "/tmp/p") {
		t.Fatalf("criterion TMPDIR = %q, want the short criterion namespace", temporary)
	}
	t.Logf("bound unix socket under %d-byte TMPDIR on %s", len([]byte(temporary)), runtime.GOOS)
}

func TestConcurrentAttemptsUseDistinctCriterionTemporaryDirectories(t *testing.T) {
	root, worktree, trampoline := criterionFixture(t)
	runID := runstate.RunID("01a05f59-8be8-7abc-9123-123456789abc")
	t.Cleanup(func() {
		if err := CleanupRunTemporary(runID); err != nil {
			t.Fatal(err)
		}
	})
	barrier := t.TempDir()
	release := filepath.Join(barrier, "release")
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0o600) })
	type attemptResult struct {
		config Config
		result acceptance.RunCriterionResult
	}
	results := make(chan attemptResult, 2)
	configs := []Config{
		criterionConfig(root, worktree, trampoline),
		criterionConfig(root, worktree, trampoline),
	}
	configs[0].RunID = runID
	configs[0].AttemptID = "01a05f5d-04b4-7def-8456-abcdef123456"
	configs[1].RunID = runID
	configs[1].AttemptID = "01a05f5e-04b4-7def-8456-abcdef123456"
	for index := range configs {
		config := configs[index]
		config.AttemptRoot = filepath.Join(root, ".partitur", "work", string(config.RunID), string(config.AttemptID))
		ready := filepath.Join(barrier, fmt.Sprintf("ready-%d", index))
		request := criterionRequest(t, "hold-unix-socket")
		request.Argv = append(request.Argv[:len(request.Argv)-1], ready, release, "hold-unix-socket")
		go func() { results <- attemptResult{config: config, result: Run(config, request)} }()
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		ready := 0
		for index := range configs {
			if _, err := os.Stat(filepath.Join(barrier, fmt.Sprintf("ready-%d", index))); err == nil {
				ready++
			}
		}
		if ready == len(configs) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent criteria did not both bind their socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	temporary := make(map[string]struct{}, len(configs))
	for range configs {
		got := <-results
		if got.result.Outcome != "PASS" {
			t.Fatalf("attempt %s result = %#v", got.config.AttemptID, got.result)
		}
		path, err := AttemptTemporaryDirectory(got.config.RunID, got.config.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		temporary[path] = struct{}{}
	}
	if len(temporary) != len(configs) {
		t.Fatalf("concurrent attempts shared temporary directories: %v", temporary)
	}
}

func TestRunRejectsUnsafeCriterionTemporaryNamespace(t *testing.T) {
	for _, test := range []struct {
		name  string
		plant func(*testing.T, string)
	}{
		{
			name: "symlink run root",
			plant: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "permissive run root",
			plant: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(root, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, worktree, trampoline := criterionFixture(t)
			config := criterionConfig(root, worktree, trampoline)
			runTemporary, err := RunTemporaryDirectory(config.RunID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(runTemporary) })
			test.plant(t, runTemporary)
			result := Run(config, criterionRequest(t, "pass"))
			if result.Err == nil {
				t.Fatalf("criterion result = %#v, want unsafe temporary namespace rejection", result)
			}
		})
	}
}

func TestRunRejectsNonUUIDTemporaryIdentity(t *testing.T) {
	root, worktree, trampoline := criterionFixture(t)
	config := criterionConfig(root, worktree, trampoline)
	config.RunID = "run"
	config.AttemptID = "attempt"
	result := Run(config, criterionRequest(t, "pass"))
	if result.Err == nil {
		t.Fatalf("criterion result = %#v, want non-UUID temporary identity rejection", result)
	}
}

func TestRunGivesCriterionAllowlistAndTrampolineHarnessEnvironment(t *testing.T) {
	root, worktree, trampoline := criterionFixture(t)
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
	command := exec.Command(os.Args[0], "-test.run=^TestCriterionEnvironmentHelperProcess$")
	command.Env = append(os.Environ(),
		"PARTITUR_CRITERION_ENV_HELPER=1",
		"PARTITUR_CRITERION_ROOT="+root,
		"PARTITUR_CRITERION_WORKTREE="+worktree,
		"PARTITUR_CRITERION_TRAMPOLINE="+trampoline,
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
	defer func() { _ = command.Process.Kill() }()

	scanner := bufio.NewScanner(notifyRead)
	for _, want := range []faultpoint.PointID{
		faultpoint.PointLaunchCriterionMarkerHeld,
		faultpoint.PointLaunchCriterionIdentityPublished,
		faultpoint.PointLaunchCriterionGateReleased,
	} {
		if got := readCriterionTrampolinePoint(t, scanner); got != want {
			t.Fatalf("criterion trampoline faultpoint = %q, want %q", got, want)
		}
		if _, err := releaseWrite.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		if err != nil {
			t.Fatalf("criterion environment helper: %v\nstdout=%q\nstderr=%q", err, stdout.String(), stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("criterion environment helper did not complete after releasing trampoline probes")
	}
}

func TestCriterionEnvironmentHelperProcess(t *testing.T) {
	if os.Getenv("PARTITUR_CRITERION_ENV_HELPER") != "1" {
		return
	}
	root := os.Getenv("PARTITUR_CRITERION_ROOT")
	worktree := os.Getenv("PARTITUR_CRITERION_WORKTREE")
	trampoline := os.Getenv("PARTITUR_CRITERION_TRAMPOLINE")
	config := criterionConfig(root, worktree, trampoline)
	result := Run(config, criterionRequest(t, "report-environment"))
	if result.Outcome != "PASS" {
		t.Fatalf("criterion result = %#v", result)
	}
	contents, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", string(criterionTestRunID), "attempts", string(criterionTestAttemptID), "criteria", "criterion", "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	environmentJSON, _, _ := bytes.Cut(contents, []byte("\n"))
	if err := json.Unmarshal(environmentJSON, &got); err != nil {
		t.Fatalf("decode criterion environment %q: %v", contents, err)
	}
	temporary, err := AttemptTemporaryDirectory(config.RunID, config.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := CleanupRunTemporary(config.RunID); err != nil {
			t.Fatal(err)
		}
	})
	want := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"PWD=" + worktree,
		"TMPDIR=" + temporary,
		"TMP=" + temporary,
		"TEMP=" + temporary,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("criterion command environment has %d entries, want the exact six-entry allowlist", len(got))
	}
}

func readCriterionTrampolinePoint(t *testing.T, scanner *bufio.Scanner) faultpoint.PointID {
	t.Helper()
	type scannedPoint struct {
		point string
		ok    bool
	}
	result := make(chan scannedPoint, 1)
	go func() {
		if !scanner.Scan() {
			result <- scannedPoint{}
			return
		}
		result <- scannedPoint{point: strings.Fields(scanner.Text())[0], ok: true}
	}()
	select {
	case got := <-result:
		if !got.ok {
			t.Fatal("criterion trampoline did not reach its harness probe")
		}
		return faultpoint.PointID(got.point)
	case <-time.After(10 * time.Second):
		t.Fatal("criterion trampoline did not reach its harness probe")
		return ""
	}
}

func TestRunCapturesRealCriterionAndSweepsDescendant(t *testing.T) {
	root, worktree, trampoline := criterionFixture(t)
	result := Run(criterionConfig(root, worktree, trampoline), criterionRequest(t, "descendant"))
	if result.Outcome != "PASS" || result.OutputRef == "" {
		t.Fatalf("result = %#v", result)
	}
	contents, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", string(criterionTestRunID), result.OutputRef, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.SplitN(string(contents), "\n", 2)[0])
	if err != nil {
		t.Fatalf("descendant pid output %q: %v", contents, err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("descendant %d remains after criterion completion", pid)
	}
}

func TestRunCapturesTrampolineStderrWhenIdentityPublicationFails(t *testing.T) {
	root, worktree, _ := criterionFixture(t)
	trampoline := filepath.Join(t.TempDir(), "failing-trampoline")
	if err := os.WriteFile(trampoline, []byte("#!/bin/sh\nprintf 'race report token=supersecret\n' >&2\nexit 66\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	result := Run(criterionConfig(root, worktree, trampoline), criterionRequest(t, "pass"))
	if !result.SpawnFailed || result.OutputRef == "" {
		t.Fatalf("result = %#v", result)
	}
	contents, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", string(criterionTestRunID), result.OutputRef, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "race report token=supersecret\n" {
		t.Fatalf("trampoline stderr = %q", contents)
	}
}

func TestRunPostReleaseFailureDoesNotWaitForStderrDescendant(t *testing.T) {
	originalLaunch := launchCriterion
	t.Cleanup(func() { launchCriterion = originalLaunch })
	var holder *exec.Cmd
	started := make(chan struct{})
	launchCriterion = func(_ context.Context, request launch.Request) (*launch.Process, error) {
		holder = exec.Command(os.Args[0], "-test.run=^TestCriterionHelperProcess$", "--", "hold-stderr")
		holder.Stderr = request.Stderr
		if err := holder.Start(); err != nil {
			return nil, err
		}
		close(started)
		return nil, fmt.Errorf("%w: injected gate close failure", launch.ErrHandoffReleased)
	}
	root := t.TempDir()
	config := Config{
		RunID:          criterionTestRunID,
		AttemptID:      criterionTestAttemptID,
		AttemptRoot:    filepath.Join(root, "attempt"),
		Worktree:       t.TempDir(),
		RepositoryRoot: root,
		SubjectTree:    "subject",
		TrampolinePath: "/bin/false",
		RemainingMS:    10_000,
	}
	request := acceptance.RunCriterionRequest{
		ID:   "criterion",
		Argv: []string{os.Args[0]},
		RecordStarted: func(runstate.ProcessIdentity) (faultpoint.DurabilityReceipt, error) {
			return faultpoint.DurabilityReceipt{}, nil
		},
	}
	result := make(chan acceptance.RunCriterionResult, 1)
	go func() { result <- Run(config, request) }()
	<-started
	t.Cleanup(func() {
		if holder != nil && holder.Process != nil {
			_ = holder.Process.Kill()
			_ = holder.Wait()
		}
	})
	select {
	case got := <-result:
		if !got.SpawnFailed || got.OutputRef == "" {
			t.Fatalf("criterion result = %#v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("criterion launch failure waited for descendant stderr")
	}
}

func TestRunCancelsRecordedCriterionSession(t *testing.T) {
	root, worktree, trampoline := criterionFixture(t)
	config := criterionConfig(root, worktree, trampoline)
	cancel := make(chan struct{})
	config.Cancel = cancel
	request := criterionRequest(t, "sleep")
	recorded := make(chan runstate.ProcessIdentity, 1)
	request.RecordStarted = func(identity runstate.ProcessIdentity) (faultpoint.DurabilityReceipt, error) {
		recorded <- identity
		return faultpoint.DurabilityReceipt{Address: "test", Mutation: faultpoint.Mutation{Kind: faultpoint.JournalAppend, EventType: string(runstate.EventCriterionStarted), EventID: "id", Sequence: 1, Timestamp: "time", Path: "journal"}}, nil
	}
	result := make(chan acceptance.RunCriterionResult, 1)
	go func() { result <- Run(config, request) }()
	identity := <-recorded
	close(cancel)
	select {
	case got := <-result:
		if !got.Cancelled {
			t.Fatalf("criterion result = %#v, want cancelled", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("criterion did not stop after control cancellation")
	}
	empty, err := adapter.SessionEmpty(identity)
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		t.Fatal("cancelled criterion session remains live")
	}
}

func TestRunCancelsDuringLaunchHandoff(t *testing.T) {
	root, worktree, trampoline := criterionFixture(t)
	config := criterionConfig(root, worktree, trampoline)
	cancel := make(chan struct{})
	config.Cancel = cancel
	request := criterionRequest(t, "sleep")
	request.RecordStarted = func(runstate.ProcessIdentity) (faultpoint.DurabilityReceipt, error) {
		close(cancel)
		return faultpoint.DurabilityReceipt{Address: "test", Mutation: faultpoint.Mutation{Kind: faultpoint.JournalAppend, EventType: string(runstate.EventCriterionStarted), EventID: "id", Sequence: 1, Timestamp: "time", Path: "journal"}}, nil
	}
	result := Run(config, request)
	if !result.Cancelled || result.OutputRef != "" {
		t.Fatalf("handoff cancellation result = %#v, want no released criterion", result)
	}
}

func TestRunFailsWhenCriterionMutatesSubject(t *testing.T) {
	root, worktree, trampoline := criterionFixture(t)
	result := Run(criterionConfig(root, worktree, trampoline), criterionRequest(t, "mutate"))
	if result.Outcome != "FAIL" || result.Reason != "acceptance_mutated_workspace" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunTimeoutStillFailsWhenCriterionMutatesSubject(t *testing.T) {
	root, worktree, trampoline := criterionFixture(t)
	script := filepath.Join(worktree, "mutate-and-sleep")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf x > mutated\nsleep 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := criterionConfig(root, worktree, trampoline)
	config.RemainingMS = 250
	request := criterionRequest(t, "pass")
	request.Argv = []string{script}
	result := Run(config, request)
	if result.Outcome != "FAIL" || result.Reason != "acceptance_mutated_workspace" {
		t.Fatalf("timeout result = %#v, want mutated-workspace failure after sweep", result)
	}
	if result.DurationMS < config.RemainingMS {
		t.Fatalf("criterion returned before its timeout: duration=%dms timeout=%dms", result.DurationMS, config.RemainingMS)
	}
}

func TestTimeoutDetailDistinguishesCriterionAndBudget(t *testing.T) {
	if got := timeoutDetail(false); got != "criterion_timeout" {
		t.Fatalf("criterion timeout detail = %q", got)
	}
	if got := timeoutDetail(true); got != "acceptance_budget_exhausted" {
		t.Fatalf("budget exhaustion detail = %q", got)
	}
}

func TestEffectiveTimeoutTieClassifiesAsCriterionTimeout(t *testing.T) {
	timeout, budget, tied := effectiveTimeout(60_000, 1)
	if timeout != time.Minute || budget || !tied {
		t.Fatalf("equal deadlines = (%s, budget=%t, tied=%t), want (1m0s, budget=false, tied=true)", timeout, budget, tied)
	}
}

func criterionFixture(t *testing.T) (string, string, string) {
	t.Helper()
	t.Cleanup(func() {
		if err := CleanupRunTemporary(criterionTestRunID); err != nil {
			t.Errorf("clean default criterion temporary test fixture: %v", err)
		}
	})
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.name", "Partitur Test")
	runGit(t, root, "config", "user.email", "partitur@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked"), []byte("tracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked")
	runGit(t, root, "commit", "-m", "fixture")
	worktree := filepath.Join(t.TempDir(), "worktree")
	runGit(t, root, "worktree", "add", "--detach", worktree, "HEAD")
	trampoline := filepath.Join(t.TempDir(), "partitur-trampoline")
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-tags=faultprobe", "-o", trampoline, "./cmd/partitur-trampoline")
	build.Dir = repository
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build trampoline: %v\n%s", err, output)
	}
	return root, worktree, trampoline
}

func criterionConfig(root, worktree, trampoline string) Config {
	return Config{RunID: criterionTestRunID, AttemptID: criterionTestAttemptID, AttemptRoot: filepath.Join(root, ".partitur", "work", string(criterionTestRunID), string(criterionTestAttemptID)), Worktree: worktree, RepositoryRoot: root, SubjectTree: gitText(root, "rev-parse", "HEAD^{tree}"), TrampolinePath: trampoline, RemainingMS: 10_000, Probe: faultpoint.Nop{}}
}

func criterionRequest(t *testing.T, mode string) acceptance.RunCriterionRequest {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return acceptance.RunCriterionRequest{ID: "criterion", Argv: []string{path, "-test.run=^TestCriterionHelperProcess$", "--", mode}, RecordStarted: func(runstate.ProcessIdentity) (faultpoint.DurabilityReceipt, error) {
		return faultpoint.DurabilityReceipt{Address: "test", Mutation: faultpoint.Mutation{Kind: faultpoint.JournalAppend, EventType: string(runstate.EventCriterionStarted), EventID: "id", Sequence: 1, Timestamp: "time", Path: "journal"}}, nil
	}}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}
func gitText(directory string, arguments ...string) string {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, _ := command.Output()
	return strings.TrimSpace(string(output))
}
