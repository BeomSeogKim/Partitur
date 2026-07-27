//go:build linux || darwin

package launch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestMarkerIsHeldBeforeIdentityPublication(t *testing.T) {
	for _, test := range []struct {
		kind  Kind
		point faultpoint.PointID
	}{
		{kind: Adapter, point: faultpoint.PointLaunchAdapterMarkerHeld},
		{kind: Criterion, point: faultpoint.PointLaunchCriterionMarkerHeld},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			launchDir := filepath.Join(t.TempDir(), "attempt", "launch")
			if err := os.MkdirAll(launchDir, 0o700); err != nil {
				t.Fatal(err)
			}
			gateRead, gateWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			readyRead, readyWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			notifyRead, notifyWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			resumeRead, resumeWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			configuration := trampolineConfiguration{
				Kind:       test.kind,
				LaunchDir:  launchDir,
				Nonce:      "nonce",
				Executable: os.Args[0],
				Arguments:  []string{"-test.run=^TestAdapterHelper$", "--", "exit"},
			}
			arguments, err := encodeTrampolineArguments(configuration)
			if err != nil {
				t.Fatal(err)
			}
			helperArguments := []string{
				"-test.run=^TestTrampolineHelper$",
				"--",
				"test-probe-fds=5,6",
			}
			helperArguments = append(helperArguments, arguments...)
			command := exec.Command(os.Args[0], helperArguments...)
			command.Env = slices.Clone(os.Environ())
			command.ExtraFiles = []*os.File{
				gateRead,
				readyWrite,
				notifyWrite,
				resumeRead,
			}
			command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			_ = gateRead.Close()
			_ = readyWrite.Close()
			_ = notifyWrite.Close()
			_ = resumeRead.Close()

			line := readLineWithGuard(t, notifyRead)
			if line != string(test.point) {
				t.Fatalf("faultpoint = %q, want %q", line, test.point)
			}
			markerPath := filepath.Join(launchDir, markerName)
			if contents, err := os.ReadFile(markerPath); err != nil {
				t.Fatal(err)
			} else if string(contents) != "nonce" {
				t.Fatalf("marker = %q", contents)
			}
			assertLockHeld(t, markerPath)
			if _, err := os.Lstat(filepath.Join(launchDir, identityName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("identity before marker-held boundary release: %v", err)
			}

			// This is the exact Appendix E pre-publication injection: the
			// trampoline is terminated while blocked at marker-held, before
			// identity publication. A recovery test can begin as soon as the
			// kill is sent; no polling or inferred phase is needed.
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = gateWrite.Close()
			_ = resumeWrite.Close()
			if err := command.Wait(); err == nil {
				t.Fatal("killed trampoline exited successfully")
			}
			assertLockFree(t, markerPath)
			if _, err := os.Lstat(filepath.Join(launchDir, identityName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("identity after pre-publication kill: %v", err)
			}
			_ = readyRead.Close()
			_ = notifyRead.Close()
		})
	}
}

func TestIdentityIsRecordedBeforeProgramRuns(t *testing.T) {
	for _, kind := range []Kind{Adapter, Criterion} {
		t.Run(string(kind), func(t *testing.T) {
			root := t.TempDir()
			sentinel := filepath.Join(root, "adapter-ran")
			request := validRequest(t, kind, root, "launch")
			request.Arguments = []string{
				"-test.run=^TestAdapterHelper$",
				"--",
				"touch",
				sentinel,
			}
			request.RecordIdentity = func(
				identity runstate.ProcessIdentity,
			) (faultpoint.DurabilityReceipt, error) {
				if identity.PID <= 0 || identity.PID != identity.SessionID {
					t.Fatalf("identity = %+v", identity)
				}
				if _, err := os.Stat(filepath.Join(
					request.AttemptRoot,
					request.LaunchID,
					identityName,
				)); err != nil {
					t.Fatalf("identity not published at record boundary: %v", err)
				}
				assertLockHeld(t, filepath.Join(
					request.AttemptRoot,
					request.LaunchID,
					markerName,
				))
				if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("program ran before durable record: %v", err)
				}
				return validReceipt(kind), nil
			}
			process, err := launch(context.Background(), request, testDependencies())
			if err != nil {
				t.Fatal(err)
			}
			if err := process.Wait(); err != nil {
				t.Fatal(err)
			}
			if contents, err := os.ReadFile(sentinel); err != nil {
				t.Fatal(err)
			} else if string(contents) != "ran" {
				t.Fatalf("sentinel = %q", contents)
			}
			assertLockFree(t, filepath.Join(process.LaunchDir, markerName))
		})
	}
}

func TestGateEOFRunsNoProgramCode(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "adapter-ran")
	request := validRequest(t, Adapter, root, "launch")
	request.Arguments = []string{
		"-test.run=^TestAdapterHelper$",
		"--",
		"touch",
		sentinel,
	}
	recordErr := errors.New("journal append failed")
	request.RecordIdentity = func(
		runstate.ProcessIdentity,
	) (faultpoint.DurabilityReceipt, error) {
		return faultpoint.DurabilityReceipt{}, recordErr
	}
	if _, err := launch(context.Background(), request, testDependencies()); !errors.Is(err, recordErr) {
		t.Fatalf("Launch error = %v, want record failure", err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("program ran after gate EOF: %v", err)
	}
	assertLockFree(t, filepath.Join(
		request.AttemptRoot,
		request.LaunchID,
		markerName,
	))
}

func TestCancelledLaunchContextNeverReleasesProgram(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "adapter-ran")
	request := validRequest(t, Adapter, root, "launch")
	request.Arguments = []string{
		"-test.run=^TestAdapterHelper$",
		"--",
		"touch",
		sentinel,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LaunchContext(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("LaunchContext error = %v, want context cancellation", err)
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("program ran after cancelled launch: %v", err)
	}
}

func TestMarkerLockSurvivesExecForProgramLifetime(t *testing.T) {
	inputRead, inputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest(t, Adapter, t.TempDir(), "launch")
	request.Stdin = inputRead
	request.Stdout = outputWrite
	request.Arguments = []string{
		"-test.run=^TestAdapterHelper$",
		"--",
		"ready-and-wait",
	}
	process, err := launch(context.Background(), request, testDependencies())
	if err != nil {
		t.Fatal(err)
	}
	_ = inputRead.Close()
	_ = outputWrite.Close()
	ready := readBytesWithGuard(t, outputRead, 1)
	if string(ready) != "R" {
		t.Fatalf("program readiness = %q", ready)
	}
	markerPath := filepath.Join(process.LaunchDir, markerName)
	assertLockHeld(t, markerPath)
	if err := inputWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if remainder := readAllWithGuard(t, outputRead); len(remainder) != 0 {
		t.Fatalf("unexpected program output %q", remainder)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	assertLockFree(t, markerPath)
}

func TestLaunchControlDoesNotReachProgramEnvironmentOrArguments(t *testing.T) {
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	environment := append(
		slices.Clone(os.Environ()),
		"PARTITUR_OPERATOR_SENTINEL=unchanged",
	)
	request := validRequest(t, Adapter, t.TempDir(), "launch")
	request.Environment = environment
	request.Stdout = outputWrite
	request.Arguments = []string{
		"-test.run=^TestAdapterHelper$",
		"--",
		"report",
	}
	process, err := launch(context.Background(), request, testDependencies())
	if err != nil {
		t.Fatal(err)
	}
	_ = outputWrite.Close()
	contents := readAllWithGuard(t, outputRead)
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	var report adapterReport
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatalf("decode report %q: %v", contents, err)
	}
	wantArguments := append([]string{request.Executable}, request.Arguments...)
	if !slices.Equal(report.Arguments, wantArguments) {
		t.Fatalf("adapter argv = %#v, want %#v", report.Arguments, wantArguments)
	}
	slices.Sort(report.Environment)
	slices.Sort(environment)
	if !slices.Equal(report.Environment, environment) {
		t.Fatalf("adapter environment changed for keys %q", changedEnvironmentKeys(
			report.Environment,
			environment,
		))
	}
	reportDirectory, err := os.Stat(report.Directory)
	if err != nil {
		t.Fatal(err)
	}
	requestDirectory, err := os.Stat(request.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(reportDirectory, requestDirectory) {
		t.Fatalf("adapter directory = %q, want %q", report.Directory, request.Directory)
	}
	for _, entry := range report.Environment {
		if strings.HasPrefix(entry, "PARTITUR_LAUNCH_") {
			t.Fatalf("launch-control environment leaked: %q", entry)
		}
	}
}

func TestEachLaunchHasItsOwnHandoffDirectory(t *testing.T) {
	root := t.TempDir()
	var directories []string
	for _, launchID := range []string{"adapter-launch", "criterion-launch"} {
		request := validRequest(t, Adapter, root, launchID)
		request.Arguments = []string{
			"-test.run=^TestAdapterHelper$",
			"--",
			"exit",
		}
		process, err := launch(context.Background(), request, testDependencies())
		if err != nil {
			t.Fatal(err)
		}
		if err := process.Wait(); err != nil {
			t.Fatal(err)
		}
		directories = append(directories, process.LaunchDir)
	}
	if directories[0] == directories[1] {
		t.Fatalf("successive launches reused %q", directories[0])
	}
	for _, directory := range directories {
		if filepath.Dir(directory) != filepath.Join(root, "attempt") {
			t.Fatalf("launch directory = %q", directory)
		}
		if _, err := os.Stat(filepath.Join(directory, identityName)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExistingLaunchDirectoryIsRejected(t *testing.T) {
	request := validRequest(t, Adapter, t.TempDir(), "launch")
	if err := os.Mkdir(filepath.Join(
		request.AttemptRoot,
		request.LaunchID,
	), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := launch(context.Background(), request, testDependencies()); !errors.Is(err, ErrLaunchCollision) {
		t.Fatalf("Launch error = %v, want collision", err)
	}
}

func TestInvalidRequestsAreRejectedIndependently(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{"kind", func(request *Request) { request.Kind = "other" }},
		{"trampoline_path", func(request *Request) { request.TrampolinePath = "" }},
		{"trampoline_path_relative", func(request *Request) { request.TrampolinePath = "trampoline" }},
		{"attempt_root", func(request *Request) { request.AttemptRoot = "" }},
		{"launch_id_absent", func(request *Request) { request.LaunchID = "" }},
		{"launch_id_parent", func(request *Request) { request.LaunchID = "../other" }},
		{"launch_id_nested", func(request *Request) { request.LaunchID = "one/two" }},
		{"executable", func(request *Request) { request.Executable = "" }},
		{"executable_relative", func(request *Request) { request.Executable = "adapter" }},
		{"arguments", func(request *Request) { request.Arguments = nil }},
		{"environment", func(request *Request) { request.Environment = nil }},
		{"record", func(request *Request) { request.RecordIdentity = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest(t, Adapter, t.TempDir(), "launch")
			test.mutate(&request)
			if _, err := launch(context.Background(), request, testDependencies()); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Launch error = %v, want invalid request", err)
			}
		})
	}
}

func TestReceiptGuardsKeepGateClosedIndependently(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*faultpoint.DurabilityReceipt)
	}{
		{"kind", func(receipt *faultpoint.DurabilityReceipt) {
			receipt.Mutation.Kind = faultpoint.FilePublication
		}},
		{"event_type", func(receipt *faultpoint.DurabilityReceipt) {
			receipt.Mutation.EventType = "performer.completed"
		}},
		{"event_id", func(receipt *faultpoint.DurabilityReceipt) {
			receipt.Mutation.EventID = ""
		}},
		{"sequence", func(receipt *faultpoint.DurabilityReceipt) {
			receipt.Mutation.Sequence = 0
		}},
		{"timestamp", func(receipt *faultpoint.DurabilityReceipt) {
			receipt.Mutation.Timestamp = ""
		}},
		{"path", func(receipt *faultpoint.DurabilityReceipt) {
			receipt.Mutation.Path = ""
		}},
	}
	for _, kind := range []Kind{Adapter, Criterion} {
		for _, test := range tests {
			t.Run(string(kind)+"/"+test.name, func(t *testing.T) {
				root := t.TempDir()
				sentinel := filepath.Join(root, "program-ran")
				request := validRequest(t, kind, root, "launch")
				request.Arguments = []string{
					"-test.run=^TestAdapterHelper$",
					"--",
					"touch",
					sentinel,
				}
				request.RecordIdentity = func(
					runstate.ProcessIdentity,
				) (faultpoint.DurabilityReceipt, error) {
					receipt := validReceipt(kind)
					test.mutate(&receipt)
					return receipt, nil
				}
				if _, err := launch(context.Background(), request, testDependencies()); !errors.Is(err, ErrInvalidReceipt) {
					t.Fatalf("Launch error = %v, want invalid receipt", err)
				}
				if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("program ran with invalid receipt: %v", err)
				}
			})
		}
	}
}

func TestCoreRejectsIdentityForAnotherPID(t *testing.T) {
	request := validRequest(t, Adapter, t.TempDir(), "launch")
	dependencies := maliciousDependencies("other-pid")
	process, err := launch(context.Background(), request, dependencies)
	if err == nil {
		_ = process.Wait()
	}
	if !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("Launch error = %v, want invalid handoff", err)
	}
}

func TestCoreRejectsMismatchedProcessStartIdentity(t *testing.T) {
	request := validRequest(t, Adapter, t.TempDir(), "launch")
	dependencies := maliciousDependencies("other-start")
	process, err := launch(context.Background(), request, dependencies)
	if err == nil {
		_ = process.Wait()
	}
	if !errors.Is(err, ErrInvalidHandoff) {
		t.Fatalf("Launch error = %v, want invalid handoff", err)
	}
}

func TestIdentityPublicationSyncsRenamedFileDirectory(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, identityName)
	syncCalls := 0
	err := publishIdentity(path, []byte(`{"identity":true}`), func(parent string) error {
		syncCalls++
		if parent != directory {
			t.Fatalf("sync parent = %q, want %q", parent, directory)
		}
		if contents, err := os.ReadFile(path); err != nil {
			t.Fatalf("published identity absent at directory sync: %v", err)
		} else if string(contents) != `{"identity":true}` {
			t.Fatalf("published identity = %q", contents)
		}
		if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary survives rename: %v", err)
		}
		return syncDirectory(parent)
	})
	if err != nil {
		t.Fatal(err)
	}
	if syncCalls != 1 {
		t.Fatalf("directory sync calls = %d, want 1", syncCalls)
	}
}

func TestTrampolineRejectsUnexpectedArguments(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"--partitur-launch"},
		{"--other", `{}`},
		{"--partitur-launch", `{}`},
		{"--partitur-launch", `{"kind":"adapter","launch_dir":"/tmp","nonce":"n","executable":"x","arguments":[],"extra":true}`},
	} {
		if _, err := decodeTrampolineArguments(arguments); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("decode %#v error = %v", arguments, err)
		}
	}
}

func validRequest(
	t *testing.T,
	kind Kind,
	root string,
	launchID string,
) Request {
	t.Helper()
	attemptRoot := filepath.Join(root, "attempt")
	if err := os.MkdirAll(attemptRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		Kind:           kind,
		TrampolinePath: executable,
		AttemptRoot:    attemptRoot,
		LaunchID:       launchID,
		Executable:     executable,
		Arguments:      []string{},
		Environment:    slices.Clone(os.Environ()),
		Directory:      root,
		RecordIdentity: func(runstate.ProcessIdentity) (faultpoint.DurabilityReceipt, error) {
			return validReceipt(kind), nil
		},
	}
}

func validReceipt(kind Kind) faultpoint.DurabilityReceipt {
	eventType := string(runstate.EventAttemptStarted)
	if kind == Criterion {
		eventType = string(runstate.EventCriterionStarted)
	}
	return faultpoint.DurabilityReceipt{
		Address: "test.launch.record",
		Mutation: faultpoint.Mutation{
			Kind:      faultpoint.JournalAppend,
			EventID:   "event-1",
			EventType: eventType,
			Sequence:  1,
			Timestamp: "2026-07-27T00:00:00.000Z",
			Path:      ".partitur/runs/run/journal.jsonl",
		},
	}
}

func testDependencies() launchDependencies {
	return launchDependencies{
		newNonce: func() (string, error) { return "test-nonce", nil },
		newCommand: func(_ string, arguments ...string) *exec.Cmd {
			helperArguments := []string{
				"-test.run=^TestTrampolineHelper$",
				"--",
			}
			helperArguments = append(helperArguments, arguments...)
			return exec.Command(os.Args[0], helperArguments...)
		},
	}
}

func maliciousDependencies(mode string) launchDependencies {
	return launchDependencies{
		newNonce: func() (string, error) { return "test-nonce", nil },
		newCommand: func(_ string, arguments ...string) *exec.Cmd {
			helperArguments := []string{
				"-test.run=^TestTrampolineHelper$",
				"--",
				"test-malicious=" + mode,
			}
			helperArguments = append(helperArguments, arguments...)
			return exec.Command(os.Args[0], helperArguments...)
		},
	}
}

func assertLockHeld(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		t.Fatal("launch marker lock is free, want held")
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatalf("probe launch marker lock: %v", err)
	}
}

func assertLockFree(t *testing.T, path string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(
		int(file.Fd()),
		syscall.LOCK_EX|syscall.LOCK_NB,
	); err != nil {
		t.Fatalf("launch marker lock remains held: %v", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
}

func readLineWithGuard(t *testing.T, file *os.File) string {
	t.Helper()
	contents := readWithGuard(t, func() ([]byte, error) {
		line, err := bufio.NewReader(file).ReadString('\n')
		return []byte(strings.TrimSuffix(line, "\n")), err
	})
	return string(contents)
}

func readBytesWithGuard(t *testing.T, file *os.File, count int) []byte {
	t.Helper()
	return readWithGuard(t, func() ([]byte, error) {
		contents := make([]byte, count)
		_, err := io.ReadFull(file, contents)
		return contents, err
	})
}

func readAllWithGuard(t *testing.T, file *os.File) []byte {
	t.Helper()
	return readWithGuard(t, func() ([]byte, error) {
		return io.ReadAll(file)
	})
}

type readResult struct {
	contents []byte
	err      error
}

func readWithGuard(
	t *testing.T,
	read func() ([]byte, error),
) []byte {
	t.Helper()
	result := make(chan readResult, 1)
	go func() {
		contents, err := read()
		result <- readResult{contents: contents, err: err}
	}()
	select {
	case result := <-result:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result.contents
	case <-time.After(10 * time.Second):
		t.Fatal("guard timeout waiting for process pipe")
		return nil
	}
}

type blockingProbe struct {
	notify *os.File
	resume *os.File
}

func (probe blockingProbe) Reached(point faultpoint.PointID) {
	if _, err := fmt.Fprintf(probe.notify, "%s\n", point); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	var release [1]byte
	if _, err := io.ReadFull(probe.resume, release[:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func TestTrampolineHelper(t *testing.T) {
	arguments := argumentsAfterDoubleDash()
	if arguments == nil {
		return
	}
	if len(arguments) > 0 && strings.HasPrefix(arguments[0], "test-malicious=") {
		if err := runMaliciousTrampoline(
			strings.TrimPrefix(arguments[0], "test-malicious="),
			arguments[1:],
		); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	var probe faultpoint.Probe = faultpoint.Nop{}
	if len(arguments) > 0 && strings.HasPrefix(arguments[0], "test-probe-fds=") {
		var notifyFD, resumeFD int
		if _, err := fmt.Sscanf(
			arguments[0],
			"test-probe-fds=%d,%d",
			&notifyFD,
			&resumeFD,
		); err != nil {
			t.Fatal(err)
		}
		probe = blockingProbe{
			notify: os.NewFile(uintptr(notifyFD), "test-faultpoint-notify"),
			resume: os.NewFile(uintptr(resumeFD), "test-faultpoint-resume"),
		}
		arguments = arguments[1:]
	}
	if err := RunTrampoline(arguments, probe); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runMaliciousTrampoline(mode string, arguments []string) error {
	configuration, err := decodeTrampolineArguments(arguments)
	if err != nil {
		return err
	}
	marker, err := acquireMarker(configuration.LaunchDir, configuration.Nonce)
	if err != nil {
		return err
	}
	defer marker.Close()
	pid := os.Getpid()
	sessionID, err := currentSessionID()
	if err != nil {
		return err
	}
	start, err := procid.Read(pid)
	if err != nil {
		return err
	}
	var impostor *exec.Cmd
	var stopImpostor *os.File
	switch mode {
	case "other-pid":
		stopRead, stopWrite, err := os.Pipe()
		if err != nil {
			return err
		}
		impostor = exec.Command(
			os.Args[0],
			"-test.run=^TestImpostorHelper$",
			"--",
		)
		impostor.Env = slices.Clone(os.Environ())
		impostor.Stdin = stopRead
		impostor.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := impostor.Start(); err != nil {
			return err
		}
		_ = stopRead.Close()
		stopImpostor = stopWrite
		pid = impostor.Process.Pid
		sessionID = pid
		start, err = procid.Read(pid)
		if err != nil {
			return err
		}
	case "other-start":
		switch identity := start.(type) {
		case runstate.LinuxStartIdentity:
			identity.StartTicks += "0"
			start = identity
		case runstate.DarwinStartIdentity:
			identity.StartTVSec++
			start = identity
		default:
			return fmt.Errorf("unexpected start identity %T", start)
		}
	default:
		return fmt.Errorf("unknown malicious mode %q", mode)
	}
	contents, err := encodeIdentity(configuration.Nonce, runstate.ProcessIdentity{
		PID:       pid,
		SessionID: sessionID,
		Start:     start,
	})
	if err != nil {
		return err
	}
	if err := publishIdentity(
		filepath.Join(configuration.LaunchDir, identityName),
		contents,
		syncDirectory,
	); err != nil {
		return err
	}
	ready := os.NewFile(readyFD, "malicious-ready")
	if _, err := ready.Write([]byte{1}); err != nil {
		return err
	}
	if err := ready.Close(); err != nil {
		return err
	}
	gate := os.NewFile(gateFD, "malicious-gate")
	defer gate.Close()
	_, gateErr := io.Copy(io.Discard, gate)
	if stopImpostor != nil {
		_ = stopImpostor.Close()
		if waitErr := impostor.Wait(); gateErr == nil {
			gateErr = waitErr
		}
	}
	return gateErr
}

type adapterReport struct {
	Arguments   []string `json:"arguments"`
	Environment []string `json:"environment"`
	Directory   string   `json:"directory"`
}

func TestAdapterHelper(t *testing.T) {
	arguments := argumentsAfterDoubleDash()
	if arguments == nil {
		return
	}
	if len(arguments) == 0 {
		t.Fatal("adapter helper mode absent")
	}
	switch arguments[0] {
	case "exit":
	case "touch":
		if len(arguments) != 2 {
			t.Fatal("touch path absent")
		}
		if err := os.WriteFile(arguments[1], []byte("ran"), 0o600); err != nil {
			t.Fatal(err)
		}
	case "ready-and-wait":
		if _, err := os.Stdout.Write([]byte("R")); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			t.Fatal(err)
		}
	case "report":
		directory, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(adapterReport{
			Arguments:   slices.Clone(os.Args),
			Environment: slices.Clone(os.Environ()),
			Directory:   directory,
		}); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown adapter helper mode %q", arguments[0])
	}
	os.Exit(0)
}

func TestImpostorHelper(t *testing.T) {
	if argumentsAfterDoubleDash() == nil {
		return
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func argumentsAfterDoubleDash() []string {
	for index, argument := range os.Args {
		if argument == "--" {
			return os.Args[index+1:]
		}
	}
	return nil
}

func changedEnvironmentKeys(got, want []string) []string {
	values := func(environment []string) map[string][]string {
		result := make(map[string][]string)
		for _, entry := range environment {
			name, _, _ := strings.Cut(entry, "=")
			result[name] = append(result[name], entry)
		}
		for name := range result {
			slices.Sort(result[name])
		}
		return result
	}
	gotValues := values(got)
	wantValues := values(want)
	union := make(map[string]struct{}, len(gotValues)+len(wantValues))
	for name := range gotValues {
		union[name] = struct{}{}
	}
	for name := range wantValues {
		union[name] = struct{}{}
	}
	var changed []string
	for name := range union {
		if !slices.Equal(gotValues[name], wantValues[name]) {
			changed = append(changed, name)
		}
	}
	slices.Sort(changed)
	return changed
}
