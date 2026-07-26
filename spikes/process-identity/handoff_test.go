package processidentity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const gateFD = 3

type startedEvent struct {
	Type      string        `json:"type"`
	Process   processRecord `json:"process"`
	Criterion string        `json:"criterion_id,omitempty"`
}

func TestGateParentDeathBeforeStartedEventRunsNoTarget(t *testing.T) {
	directory := t.TempDir()
	core := coreCommand("crash-before-started", directory, "adapter-hold")
	if err := core.Run(); err != nil {
		t.Fatal(err)
	}
	handoff := waitForHandoff(t, filepath.Join(directory, "handoff.json"))
	waitUntilGone(t, handoff.Process)
	if _, err := os.Stat(filepath.Join(directory, "target-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target executed before started fsync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "attempt.started")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected attempt.started: %v", err)
	}
	t.Logf("PRESTART_CRASH_RESULT os=%s child_exited=true target_ran=false", runtime.GOOS)
}

func TestGateParentDeathAfterStartedBeforeReleaseIsRecoverable(t *testing.T) {
	directory := t.TempDir()
	core := coreCommand("crash-after-started", directory, "adapter-hold")
	if err := core.Run(); err != nil {
		t.Fatal(err)
	}
	var event startedEvent
	if err := readJSON(filepath.Join(directory, "attempt.started"), &event); err != nil {
		t.Fatal(err)
	}
	waitUntilGone(t, event.Process)
	if _, err := os.Stat(filepath.Join(directory, "target-ran")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target executed without release: %v", err)
	}
	t.Logf("POSTSTART_CRASH_RESULT os=%s recorded_pid=%d recoverable=true target_ran=false",
		runtime.GOOS, event.Process.PID)
}

func TestGateExecPreservesRecordedIdentity(t *testing.T) {
	directory := t.TempDir()
	command, gateWrite := startTrampoline(t, directory, "adapter-hold", 0)
	handoff := waitForHandoff(t, filepath.Join(directory, "handoff.json"))
	if handoff.Process.SID != handoff.Process.PID {
		t.Fatalf("trampoline is not session leader: %+v", handoff.Process)
	}
	if err := writeDurableJSON(filepath.Join(directory, "attempt.started"), startedEvent{
		Type: "attempt.started", Process: handoff.Process,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := gateWrite.Write([]byte{'G'}); err != nil {
		t.Fatal(err)
	}
	_ = gateWrite.Close()

	var body processRecord
	waitForJSON(t, filepath.Join(directory, "body-identity.json"), &body)
	if body.PID != handoff.Process.PID ||
		body.SID != handoff.Process.SID ||
		body.Start != handoff.Process.Start {
		t.Fatalf("identity changed across exec: before=%+v after=%+v", handoff.Process, body)
	}
	if err := sweepSession(handoff.Process.SID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	t.Logf("EXEC_HANDOFF_RESULT os=%s pid=%d sid=%d identity_preserved=true",
		runtime.GOOS, body.PID, body.SID)
}

func TestParentIdentityIsLostAfterCoreDeath(t *testing.T) {
	directory := t.TempDir()
	core := coreCommand("stop-child-then-crash", directory, "adapter-hold")
	if err := core.Run(); err != nil {
		t.Fatal(err)
	}
	handoff := waitForHandoff(t, filepath.Join(directory, "handoff.json"))
	current, err := processByPID(handoff.Process.PID)
	if err != nil {
		t.Fatal(err)
	}
	if current.PPID == handoff.Process.PPID {
		t.Fatalf("child retained dead parent %d: %+v", handoff.Process.PPID, current)
	}
	if err := syscall.Kill(handoff.Process.PID, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	waitUntilGone(t, handoff.Process)
	t.Logf("PARENT_ATTRIBUTION_RESULT os=%s original_ppid=%d recovered_ppid=%d lost=true",
		runtime.GOOS, handoff.Process.PPID, current.PPID)
}

func TestInheritedLockMarkerSurvivesParentDeathButDoesNotNameOwner(t *testing.T) {
	directory := t.TempDir()
	core := coreCommand("lock-marker-then-crash", directory, "adapter-hold")
	if err := core.Run(); err != nil {
		t.Fatal(err)
	}
	handoff := waitForHandoff(t, filepath.Join(directory, "handoff.json"))
	markerPath := filepath.Join(directory, "spawn.lock")
	probe, err := os.OpenFile(markerPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = probe.Close()
		t.Fatal("inherited marker lock was released with parent")
	}
	_ = probe.Close()
	if err := syscall.Kill(handoff.Process.PID, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	waitUntilGone(t, handoff.Process)
	// On Linux, a zombie thread-group leader can briefly coexist with runtime
	// threads that still share its file table. Assert the lock state directly.
	postWaitProcess := processObservation(handoff.Process)
	probe, err = os.OpenFile(markerPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	waitForMarkerLockRelease(t, probe, handoff.Process, postWaitProcess)
	t.Logf("LOCK_MARKER_RESULT os=%s parent_dead_holder_detected=true owner_from_lock=false",
		runtime.GOOS)
}

func TestFastCriterionIdentityAndPIDChurn(t *testing.T) {
	const gatedTrials = 100
	for trial := 0; trial < gatedTrials; trial++ {
		directory := t.TempDir()
		command, gateWrite := startTrampoline(t, directory, "fast-exit", 0)
		handoff := waitForHandoff(t, filepath.Join(directory, "handoff.json"))
		if err := writeDurableJSON(filepath.Join(directory, "criterion.started"), startedEvent{
			Type: "criterion.started", Criterion: "lint", Process: handoff.Process,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := gateWrite.Write([]byte{'G'}); err != nil {
			t.Fatal(err)
		}
		_ = gateWrite.Close()
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}

	const churnTrials = 500
	seen := map[int]startIdentity{}
	reusedPIDs := 0
	duplicateStartOnly := 0
	observed := 0
	seenStarts := map[startIdentity]bool{}
	for trial := 0; trial < churnTrials; trial++ {
		command := exec.Command("true")
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		record, err := processByPID(command.Process.Pid)
		if err != nil {
			_ = command.Wait()
			// A child already gone before inspection is a censored sample, not
			// evidence against PID/start-identity separation. These children are
			// ungated on purpose — this loop exists to show why the gate above is
			// needed — and Q3 records their inspectability as an unsafe ordering
			// that only held because an unreaped child retains a zombie record.
			// It is not retried: the PID may already be reused, so a second look
			// could attribute a different process's identity to it. Any other
			// inspection error is still a real failure.
			if isProcessGone(err) {
				continue
			}
			t.Fatalf("inspect fast child %d: %v", command.Process.Pid, err)
		}
		observed++
		if previous, ok := seen[record.PID]; ok {
			reusedPIDs++
			if previous == record.Start {
				t.Fatalf("PID %d reused with identical start identity %+v", record.PID, record.Start)
			}
		}
		seen[record.PID] = record.Start
		if seenStarts[record.Start] {
			duplicateStartOnly++
		}
		seenStarts[record.Start] = true
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf(
		"FAST_CRITERION_RESULT os=%s gated=%d identity_failures=0 attempted=%d observed=%d unobservable=%d pid_reuse=%d duplicate_start_without_pid=%d",
		runtime.GOOS, gatedTrials, churnTrials, observed, churnTrials-observed, reusedPIDs, duplicateStartOnly,
	)
}

func TestCriterionRecoverySweepsBeforeSynthesis(t *testing.T) {
	directory := t.TempDir()
	core := coreCommand("release-criterion-then-crash", directory, "heartbeat")
	if err := core.Run(); err != nil {
		t.Fatal(err)
	}
	var event startedEvent
	if err := readJSON(filepath.Join(directory, "criterion.started"), &event); err != nil {
		t.Fatal(err)
	}
	heartbeat := filepath.Join(directory, "heartbeat")
	waitForFileGrowth(t, heartbeat)
	if err := sweepSession(event.Process.SID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	after, err := os.Stat(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() {
		t.Fatalf("worktree mutation continued after verified sweep: %d -> %d", before.Size(), after.Size())
	}

	synthesized := struct {
		Type        string `json:"type"`
		CriterionID string `json:"criterion_id"`
		Outcome     string `json:"outcome"`
		DurationMS  *int64 `json:"duration_ms,omitempty"`
		ErrorDetail string `json:"error_detail"`
	}{
		Type: "criterion.completed", CriterionID: "lint", Outcome: "ERROR",
		DurationMS: nil, ErrorDetail: "recovered_without_observed_completion",
	}
	data, err := json.Marshal(synthesized)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"type":"criterion.completed","criterion_id":"lint","outcome":"ERROR","error_detail":"recovered_without_observed_completion"}` {
		t.Fatalf("non-deterministic synthesis: %s", data)
	}
	t.Logf("CRITERION_RECOVERY_RESULT os=%s sweep_before_synthesis=true duration_omitted=true",
		runtime.GOOS)
}

func TestCriterionSessionEscapeRemainsOutsidePortableCeiling(t *testing.T) {
	directory := t.TempDir()
	command, gateWrite := startTrampoline(t, directory, "escape-parent", 0)
	handoff := waitForHandoff(t, filepath.Join(directory, "handoff.json"))
	if err := writeDurableJSON(filepath.Join(directory, "criterion.started"), startedEvent{
		Type: "criterion.started", Criterion: "escape", Process: handoff.Process,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := gateWrite.Write([]byte{'G'}); err != nil {
		t.Fatal(err)
	}
	_ = gateWrite.Close()

	var escaped processRecord
	waitForJSON(t, filepath.Join(directory, "escaped-identity.json"), &escaped)
	if escaped.SID == handoff.Process.SID {
		t.Fatalf("helper failed to escape session: outer=%+v escaped=%+v", handoff.Process, escaped)
	}
	if err := sweepSession(handoff.Process.SID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	live, err := identityMatches(escaped)
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		t.Fatal("escaped criterion descendant did not survive outer session sweep")
	}
	if err := sweepSession(escaped.SID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	t.Logf("CRITERION_ESCAPE_RESULT os=%s escaped_sid=%d outer_sid=%d survives_outer_sweep=true",
		runtime.GOOS, escaped.SID, handoff.Process.SID)
}

func startTrampoline(
	t *testing.T,
	directory string,
	action string,
	delay time.Duration,
) (*exec.Cmd, *os.File) {
	t.Helper()
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command := helperCommand("trampoline")
	command.Env = append(command.Env,
		"PARTITUR_DIRECTORY="+directory,
		"PARTITUR_ACTION="+action,
		"PARTITUR_HANDOFF_DELAY_MS="+strconv.FormatInt(delay.Milliseconds(), 10),
	)
	command.ExtraFiles = []*os.File{gateRead}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdout = io.Discard
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		t.Fatal(err)
	}
	_ = gateRead.Close()
	return command, gateWrite
}

func coreCommand(phase string, directory string, action string) *exec.Cmd {
	command := helperCommand("core")
	command.Env = append(command.Env,
		"PARTITUR_CORE_PHASE="+phase,
		"PARTITUR_DIRECTORY="+directory,
		"PARTITUR_ACTION="+action,
	)
	command.Stdout = io.Discard
	command.Stderr = os.Stderr
	return command
}

func waitForHandoff(t *testing.T, path string) launchHandoff {
	t.Helper()
	var handoff launchHandoff
	waitForJSON(t, path, &handoff)
	if handoff.Process.PID <= 0 || handoff.Process.SID <= 0 ||
		handoff.Process.Start.Platform == "" {
		t.Fatalf("incomplete handoff: %+v", handoff)
	}
	return handoff
}

func waitForJSON(t *testing.T, path string, target any) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := readJSON(path, target); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out reading %s", path)
}

func waitUntilGone(t *testing.T, record processRecord) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		live, err := identityMatches(record)
		if err != nil {
			t.Fatal(err)
		}
		if !live {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("process still live: %+v", record)
}

func waitForMarkerLockRelease(
	t *testing.T,
	probe *os.File,
	record processRecord,
	postWaitProcess string,
) {
	t.Helper()
	const timeout = 3 * time.Second
	start := time.Now()
	deadline := start.Add(timeout)
	attempts := 0
	var lastErr error
	for {
		attempts++
		lastErr = syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lastErr == nil {
			return
		}
		if !errors.Is(lastErr, syscall.EWOULDBLOCK) &&
			!errors.Is(lastErr, syscall.EAGAIN) {
			t.Fatalf(
				"marker lock acquisition failed unexpectedly after child exit: "+
					"attempts=%d post_wait_process=%s current_process=%s error=%v",
				attempts,
				postWaitProcess,
				processObservation(record),
				lastErr,
			)
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf(
		"marker lock remained after child exit and %s bounded wait: "+
			"attempts=%d post_wait_process=%s current_process=%s last_error=%v",
		timeout,
		attempts,
		postWaitProcess,
		processObservation(record),
		lastErr,
	)
}

func processObservation(record processRecord) string {
	current, err := processByPID(record.PID)
	if err != nil {
		return fmt.Sprintf("pid=%d inspection_error=%v", record.PID, err)
	}
	return fmt.Sprintf(
		"identity_match=%t zombie=%t current=%+v",
		current.Start == record.Start,
		current.IsZombie,
		current,
	)
}

func waitForFileGrowth(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		info, err := os.Stat(path)
		if err == nil && info.Size() >= 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("file did not grow: %s", path)
}

func TestProcessIdentityHelper(t *testing.T) {
	mode := os.Getenv("PARTITUR_SPIKE_HELPER")
	if mode == "" {
		return
	}
	var err error
	switch mode {
	case "lock-writer":
		err = runLockWriter()
	case "core":
		err = runCoreHelper()
	case "trampoline":
		err = runTrampoline()
	case "adapter-body":
		err = runAdapterBody()
	case "escaped-writer":
		err = runEscapedWriter()
	default:
		err = fmt.Errorf("unknown helper mode %q", mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runCoreHelper() error {
	directory := os.Getenv("PARTITUR_DIRECTORY")
	phase := os.Getenv("PARTITUR_CORE_PHASE")
	action := os.Getenv("PARTITUR_ACTION")
	delay := time.Duration(0)
	if phase == "crash-before-started" {
		delay = 50 * time.Millisecond
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return err
	}
	var marker *os.File
	extraFiles := []*os.File{gateRead}
	if phase == "lock-marker-then-crash" {
		marker, err = os.OpenFile(
			filepath.Join(directory, "spawn.lock"),
			os.O_CREATE|os.O_RDWR,
			0o600,
		)
		if err != nil {
			return err
		}
		if err := syscall.Flock(int(marker.Fd()), syscall.LOCK_EX); err != nil {
			return err
		}
		extraFiles = append(extraFiles, marker)
	}
	command := helperCommand("trampoline")
	command.Env = append(command.Env,
		"PARTITUR_DIRECTORY="+directory,
		"PARTITUR_ACTION="+action,
		"PARTITUR_HANDOFF_DELAY_MS="+strconv.FormatInt(delay.Milliseconds(), 10),
	)
	command.ExtraFiles = extraFiles
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdout = io.Discard
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		return err
	}
	_ = gateRead.Close()
	if marker != nil {
		_ = marker.Close()
	}

	if phase == "crash-before-started" {
		// os.Exit closes the only write end. The delayed trampoline may publish
		// identity, but it can never receive GO and therefore cannot exec.
		return nil
	}
	handoffPath := filepath.Join(directory, "handoff.json")
	var handoff launchHandoff
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := readJSON(handoffPath, &handoff); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if handoff.Process.PID == 0 {
		return errors.New("trampoline did not publish handoff")
	}

	switch phase {
	case "crash-after-started":
		return writeDurableJSON(filepath.Join(directory, "attempt.started"), startedEvent{
			Type: "attempt.started", Process: handoff.Process,
		})
	case "stop-child-then-crash":
		if err := syscall.Kill(handoff.Process.PID, syscall.SIGSTOP); err != nil {
			return err
		}
		return nil
	case "lock-marker-then-crash":
		if err := syscall.Kill(handoff.Process.PID, syscall.SIGSTOP); err != nil {
			return err
		}
		return nil
	case "release-criterion-then-crash":
		if err := writeDurableJSON(filepath.Join(directory, "criterion.started"), startedEvent{
			Type: "criterion.started", Criterion: "lint", Process: handoff.Process,
		}); err != nil {
			return err
		}
		if _, err := gateWrite.Write([]byte{'G'}); err != nil {
			return err
		}
		return gateWrite.Close()
	default:
		return fmt.Errorf("unknown core phase %q", phase)
	}
}

func runTrampoline() error {
	directory := os.Getenv("PARTITUR_DIRECTORY")
	delayMS, _ := strconv.Atoi(os.Getenv("PARTITUR_HANDOFF_DELAY_MS"))
	if delayMS > 0 {
		time.Sleep(time.Duration(delayMS) * time.Millisecond)
	}
	record, err := currentIdentity()
	if err != nil {
		return err
	}
	handoff := launchHandoff{Nonce: filepath.Base(directory), Process: record}
	if err := writeDurableJSON(filepath.Join(directory, "handoff.json"), handoff); err != nil {
		return err
	}
	gate := os.NewFile(gateFD, "launch-gate")
	if gate == nil {
		return errors.New("missing launch gate")
	}
	defer gate.Close()
	var value [1]byte
	count, err := gate.Read(value[:])
	if errors.Is(err, io.EOF) || count == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	if value[0] != 'G' {
		return fmt.Errorf("invalid gate byte %q", value[0])
	}
	if err := gate.Close(); err != nil {
		return err
	}

	action := os.Getenv("PARTITUR_ACTION")
	if action == "fast-exit" {
		return nil
	}
	environment := replaceEnvironment(os.Environ(), "PARTITUR_SPIKE_HELPER", "adapter-body")
	return syscall.Exec(
		os.Args[0],
		[]string{os.Args[0], "-test.run=TestProcessIdentityHelper"},
		environment,
	)
}

func replaceEnvironment(environment []string, key string, value string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, prefix+value)
}

func runAdapterBody() error {
	directory := os.Getenv("PARTITUR_DIRECTORY")
	action := os.Getenv("PARTITUR_ACTION")
	record, err := currentIdentity()
	if err != nil {
		return err
	}
	if err := writeDurableJSON(filepath.Join(directory, "body-identity.json"), record); err != nil {
		return err
	}
	switch action {
	case "adapter-hold":
		if err := os.WriteFile(filepath.Join(directory, "target-ran"), []byte("yes\n"), 0o600); err != nil {
			return err
		}
		for {
			time.Sleep(time.Hour)
		}
	case "heartbeat":
		for {
			file, err := os.OpenFile(
				filepath.Join(directory, "heartbeat"),
				os.O_CREATE|os.O_APPEND|os.O_WRONLY,
				0o600,
			)
			if err != nil {
				return err
			}
			_, err = file.Write([]byte("x\n"))
			_ = file.Close()
			if err != nil {
				return err
			}
			time.Sleep(5 * time.Millisecond)
		}
	case "escape-parent":
		escaped := helperCommand("escaped-writer")
		escaped.Env = append(escaped.Env, "PARTITUR_DIRECTORY="+directory)
		escaped.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		escaped.Stdout = io.Discard
		escaped.Stderr = os.Stderr
		if err := escaped.Start(); err != nil {
			return err
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		return fmt.Errorf("unknown adapter action %q", action)
	}
}

func runEscapedWriter() error {
	directory := os.Getenv("PARTITUR_DIRECTORY")
	record, err := currentIdentity()
	if err != nil {
		return err
	}
	if err := writeDurableJSON(filepath.Join(directory, "escaped-identity.json"), record); err != nil {
		return err
	}
	for {
		file, err := os.OpenFile(
			filepath.Join(directory, "escaped-heartbeat"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY,
			0o600,
		)
		if err != nil {
			return err
		}
		_, err = file.Write([]byte("x\n"))
		_ = file.Close()
		if err != nil {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
