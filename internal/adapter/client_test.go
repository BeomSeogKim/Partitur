package adapter

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

const (
	fakeModeEnv               = "PARTITUR_ADAPTER_TEST_MODE"
	fakeMarkerEnv             = "PARTITUR_ADAPTER_TEST_MARKER"
	fakeFixtureDirectoryEnv   = "PARTITUR_ADAPTER_TEST_FIXTURE_DIRECTORY"
	fakeFixtureOwnerPIDEnv    = "PARTITUR_ADAPTER_TEST_FIXTURE_OWNER_PID"
	fakeFixtureOwnerStartEnv  = "PARTITUR_ADAPTER_TEST_FIXTURE_OWNER_START"
	fakeBrokenSweepEnv        = "PARTITUR_ADAPTER_TEST_BREAK_FIXTURE_SWEEP"
	fakeInterruptMarkerEnv    = "PARTITUR_ADAPTER_TEST_INTERRUPT_MARKER"
	fakeReaperRegistrationEnv = "PARTITUR_ADAPTER_TEST_REAPER_REGISTRATION"
	vendorModeEnv             = "PARTITUR_EXECUTE_VENDOR_MODE"
	vendorOutEnv              = "PARTITUR_EXECUTE_VENDOR_OUTPUT"
	incidentalTestDeadline    = 10 * time.Second
	// The fixture observes its owner at this cadence. The outer SIGKILL test
	// reserves half of the ordinary test deadline for scheduling and process
	// inspection under -race; it is a liveness bound, not the expected latency.
	fakeFixtureOwnerWatchPoll = 20 * time.Millisecond
	fixtureOwnerReapTimeout   = incidentalTestDeadline / 2
	fakeFixtureRecordPrefix   = ".partitur-adapter-fixture-"
	// One suffix per fixture step, never rewritten: a single stage file would
	// have to be truncated and replaced, and a sweep landing inside that
	// rewrite leaves a file whose contents say nothing.
	sweepStageSpawning = ".spawning"
	sweepStageSpawned  = ".spawned"
	sweepStageWritten  = ".written"
)

const fakeParentInterruptMarkerEnv = "PARTITUR_ADAPTER_TEST_PARENT_INTERRUPT_MARKER"

// A child that watched its spawning fixture instead of the test binary would
// observe that fixture's SIGKILL within one poll and exit, so two intervals
// give it a second opportunity. This bound holds for the untagged suite only,
// and lengthening it buys nothing: under -race the child can reach
// startFakeFixture after its parent has already died, read a reparented and
// therefore live ppid, and never exit no matter how long it is watched.
// Measured against that exact evasion: red 8 of 8 untagged, green 5 of 5 under
// -race. The untagged gate is what locks this care; -race cannot.
const fixtureChildParentDeathObservation = 2 * fakeFixtureOwnerWatchPoll

var fakeFixtureSurvivalAllowedAfterUnverifiableSweep = map[string]struct{}{
	// This is the complete inventory of tests that deliberately make session
	// enumeration unverifiable. TestFixtureReaperReportsSurvivingSessionTree
	// instead requires the ordinary reaper to report its surviving fixture.
	"TestCleanupUnverifiableIsAggregated":                 {},
	"TestSweepUnverifiableLeavesIntervalAndOutcomeAbsent": {},
}

func TestMain(m *testing.M) {
	if mode := os.Getenv(sigtermVendorModeEnv); mode != "" {
		os.Exit(runSIGTERMVendorHelper(mode))
	}
	if mode := os.Getenv(vendorModeEnv); mode != "" {
		os.Exit(runExecuteVendorHelper(mode))
	}
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("partitur-test 9.8.7")
		os.Exit(0)
	}
	if mode := os.Getenv(fakeModeEnv); mode != "" {
		runFakeAdapter(mode)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRealFirstPartyAdaptersProbeAndExitCleanly(t *testing.T) {
	directory := t.TempDir()
	buildAdapter(t, directory, "claude")
	buildAdapter(t, directory, "codex")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	environment := replaceEnvironment(os.Environ(), map[string]string{
		"PATH":                directory,
		"PARTITUR_CLAUDE_BIN": executable,
		"PARTITUR_CODEX_BIN":  executable,
	})
	report := newClient(environment, incidentalTestDeadline, 200*time.Millisecond).ProbeAll([]string{"codex", "claude"})
	if len(report.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", report.Diagnostics)
	}
	if got := probeIDs(report.Probes); !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Fatalf("probe ids = %v", got)
	}
	for _, probe := range report.Probes {
		if probe.Result.Protocol != 2 || probe.Result.Adapter.ID != probe.AdapterID ||
			probe.Result.Adapter.Version != "9.8.7" || probe.Result.Features != nil {
			t.Fatalf("%s result = %#v", probe.AdapterID, probe.Result)
		}
		if !probe.Result.Capabilities.RepoRead || !probe.Result.Capabilities.RepoWrite ||
			!probe.Result.Capabilities.Shell || !probe.Result.Capabilities.Network ||
			!probe.Result.Capabilities.ResumableSessions {
			t.Fatalf("%s capabilities = %#v", probe.AdapterID, probe.Result.Capabilities)
		}
		switch probe.AdapterID {
		case "claude":
			if probe.Result.Enforcement != (protocol.Enforcement{}) ||
				len(probe.Result.Capabilities.Models) != 4 {
				t.Fatalf("claude result = %#v", probe.Result)
			}
		case "codex":
			if probe.Result.Enforcement != (protocol.Enforcement{ReadOnly: true, NetworkGrants: true}) ||
				len(probe.Result.Capabilities.Models) != 2 {
				t.Fatalf("codex result = %#v", probe.Result)
			}
		}
	}
}

func TestNewClientSnapshotsInheritedEnvironmentUnchanged(t *testing.T) {
	want := os.Environ()
	client := NewClient()
	if len(client.environment) != len(want) {
		t.Fatalf("environment snapshot length = %d, want %d", len(client.environment), len(want))
	}
	for index := range want {
		if client.environment[index] != want[index] {
			t.Fatalf(
				"environment snapshot entry %d changed: got name %q, want name %q",
				index,
				environmentEntryName(client.environment[index]),
				environmentEntryName(want[index]),
			)
		}
	}
}

func TestPATHResolutionUsesExactNameFirstEntryAndSnapshot(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	installFake(t, first, "fake")
	installFake(t, second, "fake")
	installFake(t, first, "fake-lookalike")
	environment := []string{
		"PATH=" + strings.Join([]string{first, second}, string(os.PathListSeparator)),
		fakeModeEnv + "=environment",
		fakeMarkerEnv + "=" + marker,
		"VENDOR_CREDENTIAL=keep-me",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=operator-helper",
	}
	report := newClient(environment, incidentalTestDeadline, 100*time.Millisecond).ProbeAll([]string{"fake"})
	if len(report.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", report.Diagnostics)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Argv0       string
		Environment []string
	}
	if err := json.Unmarshal(data, &observed); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(observed.Argv0) != first {
		t.Fatalf("argv[0] = %q, want first PATH directory %q", observed.Argv0, first)
	}
	if !reflect.DeepEqual(observed.Environment, environment) {
		t.Fatalf("child environment changed\n got: %#v\nwant: %#v", observed.Environment, environment)
	}
}

func TestDiscoveryFailuresDoNotSearchLoosely(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake-extra")
	tests := []struct {
		name string
		env  []string
		id   string
	}{
		{"exact name absent", []string{"PATH=" + directory}, "fake"},
		{"PATH absent", []string{"OTHER=value"}, "fake"},
		{"slash cannot become direct path", []string{"PATH=" + directory}, "../fake"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := newClient(test.env, incidentalTestDeadline, time.Millisecond).ProbeAll([]string{test.id})
			assertDiagnosticKinds(t, report, DiagnosticExecutableAbsent)
		})
	}
}

func TestSpawnAndInjectedIOFailures(t *testing.T) {
	t.Run("spawn", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "partitur-adapter-bad")
		if err := os.WriteFile(path, []byte("not an executable format"), 0o755); err != nil {
			t.Fatal(err)
		}
		report := newClient([]string{"PATH=" + directory}, incidentalTestDeadline, time.Millisecond).ProbeAll([]string{"bad"})
		assertDiagnosticKinds(t, report, DiagnosticSpawnFailed)
	})

	t.Run("request", func(t *testing.T) {
		client := fakeClient(t, "hang_no_response", 10*time.Millisecond)
		client.write = func(io.Writer, []byte) error { return errors.New("injected write failure") }
		report := client.ProbeAll([]string{"fake"})
		assertDiagnosticKinds(t, report, DiagnosticRequestIO)
	})

	t.Run("response", func(t *testing.T) {
		client := fakeClient(t, "hang_no_response", 10*time.Millisecond)
		client.read = func(_ io.Reader, events chan<- frameEvent) {
			events <- frameEvent{err: errors.New("injected response read failure")}
		}
		report := client.ProbeAll([]string{"fake"})
		assertDiagnosticKinds(t, report, DiagnosticResponseIO)
	})
}

func TestFramingFailuresAreNotSkipped(t *testing.T) {
	tests := []struct {
		mode string
		kind DiagnosticKind
	}{
		{"premature_eof", DiagnosticPrematureEOF},
		{"partial_eof", DiagnosticPrematureEOF},
		{"malformed_then_valid", DiagnosticMalformedResponse},
		{"duplicate_then_valid", DiagnosticDuplicateKey},
		{"invalid_utf8_then_valid", DiagnosticInvalidUTF8},
		{"oversized_then_valid", DiagnosticOversizedResponse},
		{"error_response", DiagnosticErrorResponse},
		{"wrong_adapter", DiagnosticWrongAdapter},
		{"unsupported_protocol", DiagnosticUnsupportedProtocol},
		{"extra_response", DiagnosticMalformedResponse},
		{"partial_after_response", DiagnosticPrematureEOF},
		{"nonzero_after_response", DiagnosticNonzeroExit},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			report := fakeClient(t, test.mode, 20*time.Millisecond).ProbeAll([]string{"fake"})
			assertDiagnosticKinds(t, report, test.kind)
		})
	}
}

func TestBlankLinesAndPostResponseWait(t *testing.T) {
	t.Run("blank lines", func(t *testing.T) {
		report := fakeClient(t, "blank_success", 20*time.Millisecond).ProbeAll([]string{"fake"})
		if len(report.Diagnostics) != 0 || len(report.Probes) != 1 {
			t.Fatalf("report = %#v", report)
		}
	})
	t.Run("waits for clean exit", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "exited")
		client := fakeClientWithMarker(t, "delayed_exit", marker, 20*time.Millisecond)
		started := time.Now()
		report := client.ProbeAll([]string{"fake"})
		if len(report.Diagnostics) != 0 {
			t.Fatalf("diagnostics = %#v", report.Diagnostics)
		}
		if time.Since(started) < 60*time.Millisecond {
			t.Fatal("probe returned before the delayed adapter exit")
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("exit marker: %v", err)
		}
	})
}

func TestWaitCannotCloseOutputBeforeReadersFinish(t *testing.T) {
	client := fakeClient(t, "immediate_clean_exit", 20*time.Millisecond)
	waitCompleted := make(chan struct{})
	stderrCopied := make(chan string, 1)
	client.wait = func(command *exec.Cmd) error {
		err := command.Wait()
		close(waitCompleted)
		return err
	}
	client.copyStderr = func(writer io.Writer, reader io.Reader) (int64, error) {
		<-waitCompleted
		written, err := io.Copy(writer, reader)
		stderrCopied <- writer.(*limitedBuffer).String()
		return written, err
	}
	client.read = func(reader io.Reader, events chan<- frameEvent) {
		frames := protocol.NewReader(reader)
		frame, err := frames.ReadFrame()
		events <- frameEvent{frame: frame, err: err}
		if err != nil {
			return
		}
		<-waitCompleted
		frame, err = frames.ReadFrame()
		events <- frameEvent{frame: frame, err: err}
	}

	report := client.ProbeAll([]string{"fake"})
	if len(report.Diagnostics) != 0 || len(report.Probes) != 1 {
		t.Fatalf("report = %#v", report)
	}
	if got := <-stderrCopied; got != "immediate-clean-exit-stderr" {
		t.Fatalf("stderr = %q, want buffered immediate-exit diagnostic", got)
	}
}

func TestDeadlineCoversResponseAndCleanExit(t *testing.T) {
	for _, mode := range []string{"hang_no_response", "hang_after_response"} {
		t.Run(mode, func(t *testing.T) {
			report := deadlineSubjectFakeClient(
				t,
				mode,
				"",
				40*time.Millisecond,
				30*time.Millisecond,
			).ProbeAll([]string{"fake"})
			assertDiagnosticKinds(t, report, DiagnosticDeadline)
		})
	}
}

func TestTimeoutSweepsEveryProcessGroupInSession(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pids")
	client := deadlineSubjectFakeClient(
		t,
		"session_tree_hang",
		marker,
		50*time.Millisecond,
		40*time.Millisecond,
	)
	armed := time.Now()
	report := client.ProbeAll([]string{"fake"})
	elapsed := time.Since(armed)
	// This fixture owns only its recorded pids; host-wide enumeration may add
	// cleanup_unverifiable for an unrelated process. The helper documents the
	// sibling tests that reject a sweep reporting failure after succeeding.
	assertDeadlineWithOnlyCleanupUnverifiable(t, report)
	if len(report.Probes) != 0 {
		t.Fatalf("failed report unexpectedly contains probes: %#v", report.Probes)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("%v\n%s", err, sweepFixtureDiagnosis(marker, report, armed, elapsed))
	}
	// A marker whose write failed part way leaves a readable but short file,
	// and the loop below would then iterate nothing and pass. The session has
	// exactly two members, so anything else is the fixture failing to record
	// what this test exists to observe.
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		t.Fatalf("marker = %q, want the fake's pid and its child's\n%s",
			data, sweepFixtureDiagnosis(marker, report, armed, elapsed))
	}
	for _, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("session member %d still observable: %v", pid, err)
		}
	}
}

func TestFixtureChildInheritsTestOwnerIdentity(t *testing.T) {
	if marker := os.Getenv(fakeParentInterruptMarkerEnv); marker != "" {
		// This test binary is the live owner for both fixtures. The parent starts
		// the child directly, without Client cleanup, so the outer test can kill
		// only that parent and observe the child's owner behavior.
		directory := filepath.Dir(marker)
		installFake(t, directory, "fake")
		owner, err := processByPID(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		parent := exec.Command(filepath.Join(directory, "partitur-adapter-fake"))
		parent.Env = replaceEnvironment(os.Environ(), map[string]string{
			fakeModeEnv:              "session_tree_hang",
			fakeMarkerEnv:            marker,
			fakeFixtureDirectoryEnv:  directory,
			fakeFixtureOwnerPIDEnv:   strconv.Itoa(owner.PID),
			fakeFixtureOwnerStartEnv: owner.Start,
		})
		if err := parent.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { _ = parent.Wait() }()
		for {
			time.Sleep(time.Hour)
		}
	}

	marker := filepath.Join(t.TempDir(), "pids")
	command := exec.Command(os.Args[0], "-test.run=^TestFixtureChildInheritsTestOwnerIdentity$")
	command.Env = replaceEnvironment(os.Environ(), map[string]string{fakeParentInterruptMarkerEnv: marker})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	fixtures := waitForFixturePIDs(t, marker)
	parentPID, err := strconv.Atoi(fixtures[0])
	if err != nil {
		t.Fatal(err)
	}
	parent, err := processByPID(parentPID)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(fixtures[1])
	if err != nil {
		t.Fatal(err)
	}
	child, err := processByPID(childPID)
	if err != nil {
		t.Fatal(err)
	}
	testOwner, err := processByPID(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
		if process, err := processByPID(child.PID); err == nil && !process.IsZombie && process.Start == child.Start {
			_ = syscall.Kill(child.PID, syscall.SIGKILL)
		}
	})
	if err := syscall.Kill(parentPID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	parentReapDeadline := time.Now().Add(fixtureOwnerReapTimeout)
	for {
		_, err := processByPID(parent.PID)
		if isProcessGone(err) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !time.Now().Before(parentReapDeadline) {
			t.Fatalf("spawning parent %d remained observable after SIGKILL", parent.PID)
		}
		time.Sleep(fakeFixtureOwnerWatchPoll)
	}

	deadline := time.Now().Add(fixtureChildParentDeathObservation)
	for {
		owner, err := processByPID(testOwner.PID)
		if err != nil || owner.IsZombie || owner.Start != testOwner.Start {
			t.Fatalf("test-binary owner did not remain live: process = %#v, err = %v", owner, err)
		}
		process, err := processByPID(child.PID)
		if err != nil || process.IsZombie || process.Start != child.Start {
			t.Fatalf("child fixture exited after its spawning parent was SIGKILLed while test owner remained live: process = %#v, err = %v", process, err)
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(fakeFixtureOwnerWatchPoll)
	}
	record := readFakeFixtureRecord(t, filepath.Dir(marker), childPID)
	if got, want := record.owner, (fakeFixtureOwner{pid: testOwner.PID, start: testOwner.Start}); got != want {
		t.Fatalf("child fixture owner = %#v, want test binary %#v", got, want)
	}
}

func TestFixtureReaperReportsSurvivingSessionTree(t *testing.T) {
	if os.Getenv(fakeBrokenSweepEnv) != "" {
		marker := filepath.Join(t.TempDir(), "pids")
		client := deadlineSubjectFakeClient(
			t,
			"session_tree_hang",
			marker,
			50*time.Millisecond,
			40*time.Millisecond,
		)
		client.sessions = parentOnlySessionController{}
		report := client.ProbeAll([]string{"fake"})
		assertDiagnosticKinds(t, report, DiagnosticDeadline)
		data, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		if fields := strings.Fields(string(data)); len(fields) != 2 {
			t.Fatalf("marker = %q, want the fake's pid and its child's", data)
		}
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestFixtureReaperReportsSurvivingSessionTree$")
	command.Env = replaceEnvironment(os.Environ(), map[string]string{fakeBrokenSweepEnv: "1"})
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("broken session sweep passed:\n%s", output)
	}
	if got := strings.Count(string(output), "fixture survived assertions"); got != 1 {
		t.Fatalf("broken session sweep fixture failures = %d, want 1 child fixture:\n%s", got, output)
	}
}

func TestFixtureReaperIsRegisteredByInstallFake(t *testing.T) {
	if os.Getenv(fakeReaperRegistrationEnv) != "" {
		// This matches execute_test.go's direct setup: installFake is the only
		// registration point, and the client supplies no fixture owner identity.
		// hang_no_response ignores SIGTERM and never exits on its own, while the
		// injected session controller cannot sweep it. That makes it certain the
		// cleanup reaper sees a survivor; premature_eof could exit before cleanup.
		directory := t.TempDir()
		installFake(t, directory, "fake")
		client := newClient([]string{
			"PATH=" + directory,
			fakeModeEnv + "=hang_no_response",
			fakeFixtureDirectoryEnv + "=" + directory,
		}, 200*time.Millisecond, 20*time.Millisecond)
		client.sessions = failingSessionController{}
		report := client.ProbeAll([]string{"fake"})
		assertDiagnosticKinds(t, report, DiagnosticCleanupUnverifiable, DiagnosticDeadline)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestFixtureReaperIsRegisteredByInstallFake$")
	command.Env = replaceEnvironment(os.Environ(), map[string]string{fakeReaperRegistrationEnv: "1"})
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("installFake did not register the fixture reaper:\n%s", output)
	}
	if got := strings.Count(string(output), "fixture survived assertions"); got != 1 {
		t.Fatalf("installFake fixture reaper failures = %d, want 1:\n%s", got, output)
	}
}

func TestFixtureOwnerWatchReapsAfterTestBinarySIGKILL(t *testing.T) {
	if marker := os.Getenv(fakeInterruptMarkerEnv); marker != "" {
		// The hand-built client intentionally omits the owner environment, so
		// startFakeFixture must select this test binary through its PPID fallback.
		directory := t.TempDir()
		installFake(t, directory, "fake")
		newClient([]string{
			"PATH=" + directory,
			fakeModeEnv + "=session_tree_hang",
			fakeMarkerEnv + "=" + marker,
			fakeFixtureDirectoryEnv + "=" + directory,
		}, 10*time.Second, 40*time.Millisecond).ProbeAll([]string{"fake"})
		t.Fatal("fixture owner survived its intended SIGKILL")
	}

	marker := filepath.Join(t.TempDir(), "pids")
	command := exec.Command(os.Args[0], "-test.run=^TestFixtureOwnerWatchReapsAfterTestBinarySIGKILL$")
	command.Env = replaceEnvironment(os.Environ(), map[string]string{fakeInterruptMarkerEnv: marker})
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	fields := waitForFixturePIDs(t, marker)
	fixtures := make([]processRecord, len(fields))
	for index, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		process, err := processByPID(pid)
		if err != nil {
			t.Fatal(err)
		}
		fixtures[index] = process
	}
	t.Cleanup(func() {
		for _, fixture := range fixtures {
			if process, err := processByPID(fixture.PID); err == nil && !process.IsZombie && process.Start == fixture.Start {
				t.Errorf("fixture %d survived test-owner SIGKILL; reaping", fixture.PID)
				_ = syscall.Kill(fixture.PID, syscall.SIGKILL)
			}
		}
	})
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("fixture owner survived SIGKILL")
	}
	for _, fixture := range fixtures {
		deadline := time.Now().Add(fixtureOwnerReapTimeout)
		for {
			process, err := processByPID(fixture.PID)
			if isProcessGone(err) || err == nil && (process.IsZombie || process.Start != fixture.Start) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("fixture %d survived test-owner SIGKILL", fixture.PID)
			}
			time.Sleep(fakeFixtureOwnerWatchPoll)
		}
	}
}

func waitForFixturePIDs(t *testing.T, marker string) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 2 {
				return fields
			}
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("fixture marker %q was not written with two pids", marker)
		}
		time.Sleep(fakeFixtureOwnerWatchPoll)
	}
}

func TestSessionLeadership(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "identity")
	report := fakeClientWithMarker(t, "session_identity", marker, 20*time.Millisecond).ProbeAll([]string{"fake"})
	if len(report.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", report.Diagnostics)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 || fields[0] != fields[1] {
		t.Fatalf("pid/sid = %q, want equal", data)
	}
}

func TestCleanupUnverifiableIsAggregated(t *testing.T) {
	client := fakeClient(t, "premature_eof", time.Millisecond)
	client.sessions = failingSessionController{}
	report := client.ProbeAll([]string{"fake"})
	assertDiagnosticKinds(t, report, DiagnosticCleanupUnverifiable, DiagnosticPrematureEOF)
}

func TestStderrIsBoundedAndSanitized(t *testing.T) {
	report := fakeClient(t, "stderr_failure", 20*time.Millisecond).ProbeAll([]string{"fake"})
	assertDiagnosticKinds(t, report, DiagnosticPrematureEOF)
	stderr := report.Diagnostics[0].Stderr
	if !strings.Contains(stderr, "harmless") || !strings.Contains(stderr, "[REDACTED]") {
		t.Fatalf("sanitized stderr = %q", stderr[:min(len(stderr), 200)])
	}
	if strings.Contains(stderr, "supersecret") || strings.Contains(stderr, "BEYOND-CAP") {
		t.Fatal("stderr retained a secret or bytes beyond the raw cap")
	}
	if len(stderr) > MaxProbeStderrBytes {
		t.Fatalf("stderr length = %d", len(stderr))
	}

	var buffer limitedBuffer
	buffer.limit = 4
	_, _ = buffer.Write([]byte("abcdef"))
	if buffer.Len() != 4 || buffer.String() != "abcd" {
		t.Fatalf("bounded buffer = %q (%d)", buffer.String(), buffer.Len())
	}
}

func TestProbeAllDeduplicatesAggregatesAndOrders(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(t.TempDir(), "attempts")
	for _, id := range []string{"zeta", "bad", "alpha"} {
		installFake(t, directory, id)
	}
	environment := []string{
		"PATH=" + directory,
		fakeModeEnv + "=aggregate",
		fakeMarkerEnv + "=" + marker,
	}
	report := newClient(environment, incidentalTestDeadline, 20*time.Millisecond).ProbeAll(
		[]string{"zeta", "bad", "alpha", "zeta", "bad"},
	)
	if got := probeIDs(report.Probes); !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("probe ids = %v", got)
	}
	if len(report.Diagnostics) != 1 || report.Diagnostics[0].Kind != DiagnosticPrematureEOF ||
		report.Diagnostics[0].AdapterID != "bad" {
		t.Fatalf("diagnostics = %#v", report.Diagnostics)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	attempts := strings.Fields(string(data))
	sort.Strings(attempts)
	if !reflect.DeepEqual(attempts, []string{"alpha", "bad", "zeta"}) {
		t.Fatalf("attempts = %v", attempts)
	}
}

func TestPublishedLimits(t *testing.T) {
	if ProbeCompletionDeadline != 15_000*time.Millisecond {
		t.Fatalf("completion deadline = %v", ProbeCompletionDeadline)
	}
	if OuterTerminationGrace != 30_000*time.Millisecond {
		t.Fatalf("outer grace = %v", OuterTerminationGrace)
	}
	if MaxProbeStderrBytes != 65_536 {
		t.Fatalf("stderr cap = %d", MaxProbeStderrBytes)
	}
}

func TestSortReportDeterministic(t *testing.T) {
	report := Report{
		Probes: []Probe{{AdapterID: "zeta"}, {AdapterID: "alpha"}},
		Diagnostics: []Diagnostic{
			{AdapterID: "zeta", Kind: DiagnosticWrongAdapter, Detail: "b"},
			{AdapterID: "alpha", Kind: DiagnosticWrongAdapter, Detail: "z"},
			{AdapterID: "zeta", Kind: DiagnosticMalformedResponse, Detail: "a"},
			{AdapterID: "zeta", Kind: DiagnosticWrongAdapter, Detail: "a"},
		},
	}
	sortReport(&report)
	if got := probeIDs(report.Probes); !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("probes = %v", got)
	}
	got := make([]string, len(report.Diagnostics))
	for index, diagnostic := range report.Diagnostics {
		got[index] = fmt.Sprintf("%s/%s/%s", diagnostic.AdapterID, diagnostic.Kind, diagnostic.Detail)
	}
	want := []string{
		"alpha/wrong_adapter_id/z",
		"zeta/malformed_response/a",
		"zeta/wrong_adapter_id/a",
		"zeta/wrong_adapter_id/b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostics = %v, want %v", got, want)
	}
}

type failingSessionController struct{}

type parentOnlySessionController struct{}

func (failingSessionController) verifyEmpty(int, string) (bool, error) {
	return false, errors.New("injected enumeration failure")
}

func (failingSessionController) terminate(int, string, time.Duration) error {
	return errors.New("injected enumeration failure")
}

func (parentOnlySessionController) verifyEmpty(int, string) (bool, error) {
	return false, nil
}

func (parentOnlySessionController) terminate(sid int, _ string, _ time.Duration) error {
	return syscall.Kill(sid, syscall.SIGKILL)
}

func fakeClient(t *testing.T, mode string, grace time.Duration) *Client {
	t.Helper()
	return newFakeClient(t, mode, "", incidentalTestDeadline, grace)
}

func fakeClientWithMarker(t *testing.T, mode, marker string, grace time.Duration) *Client {
	t.Helper()
	return newFakeClient(t, mode, marker, incidentalTestDeadline, grace)
}

func deadlineSubjectFakeClient(
	t *testing.T,
	mode string,
	marker string,
	deadline time.Duration,
	grace time.Duration,
) *Client {
	t.Helper()
	return newFakeClient(t, mode, marker, deadline, grace)
}

func newFakeClient(t *testing.T, mode, marker string, deadline, grace time.Duration) *Client {
	client, _ := newFakeClientWithFixtureDirectory(t, mode, marker, deadline, grace)
	return client
}

func newFakeClientWithFixtureDirectory(
	t *testing.T,
	mode, marker string,
	deadline, grace time.Duration,
) (*Client, string) {
	t.Helper()
	directory := t.TempDir()
	installFake(t, directory, "fake")
	owner, err := processByPID(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	environment := []string{
		"PATH=" + directory,
		fakeModeEnv + "=" + mode,
		fakeFixtureDirectoryEnv + "=" + directory,
		fakeFixtureOwnerPIDEnv + "=" + strconv.Itoa(owner.PID),
		fakeFixtureOwnerStartEnv + "=" + owner.Start,
	}
	if marker != "" {
		environment = append(environment, fakeMarkerEnv+"="+marker)
	}
	return newClient(environment, deadline, grace), directory
}

func installFake(t *testing.T, directory, adapterID string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "partitur-adapter-"+adapterID)
	if err := os.Symlink(executable, path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, allowed := fakeFixtureSurvivalAllowedAfterUnverifiableSweep[t.Name()]; allowed {
			reapFakeFixturesSilently(directory)
			return
		}
		reapFakeFixtures(t, directory)
	})
}

func buildAdapter(t *testing.T, directory, adapterID string) {
	t.Helper()
	output := filepath.Join(directory, "partitur-adapter-"+adapterID)
	command := exec.Command("go", "build", "-o", output, "../../cmd/partitur-adapter-"+adapterID)
	command.Env = os.Environ()
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s adapter: %v\n%s", adapterID, err, data)
	}
}

func buildTrampoline(t *testing.T, directory string) string {
	t.Helper()
	output := filepath.Join(directory, "partitur-trampoline")
	command := exec.Command("go", "build", "-o", output, "../../cmd/partitur-trampoline")
	command.Env = os.Environ()
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build trampoline: %v\n%s", err, data)
	}
	return output
}

func probeIDs(probes []Probe) []string {
	ids := make([]string, len(probes))
	for index, probe := range probes {
		ids[index] = probe.AdapterID
	}
	return ids
}

func assertDiagnosticKinds(t *testing.T, report Report, want ...DiagnosticKind) {
	t.Helper()
	got := make([]DiagnosticKind, len(report.Diagnostics))
	for index, diagnostic := range report.Diagnostics {
		got[index] = diagnostic.Kind
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic kinds = %v, want %v; report = %#v", got, want, report)
	}
	if len(report.Probes) != 0 {
		t.Fatalf("failed report unexpectedly contains probes: %#v", report.Probes)
	}
}

// assertDeadlineWithOnlyCleanupUnverifiable keeps this tree-kill test scoped
// to its fixture while rejecting unrelated diagnostics. It intentionally
// permits at most one cleanup_unverifiable from a foreign process after both
// fixture pids are gone.
// TestDeadlineCoversResponseAndCleanExit asserts the exact diagnostic list;
// the following Execute tests reject a post-sweep failure through their error
// assertions: TestExecuteCancelAfterEOFStillArmsTheGrace,
// TestExecuteCancellationBoundsBlockedRequestWrites,
// TestExecuteCancelGraceTimeoutForcesVerifiedEmptySweep,
// TestExecuteCancelResponseWithoutAcknowledgementTimesOutAndSweeps, and
// TestExecuteCancelGraceSurvivesOutputAndProcessDrain.
func assertDeadlineWithOnlyCleanupUnverifiable(t *testing.T, report Report) {
	t.Helper()
	deadlineCount := 0
	cleanupUnverifiableCount := 0
	for _, diagnostic := range report.Diagnostics {
		switch diagnostic.Kind {
		case DiagnosticDeadline:
			deadlineCount++
		case DiagnosticCleanupUnverifiable:
			cleanupUnverifiableCount++
		default:
			t.Fatalf("diagnostic kinds = %#v, want deadline_exceeded and only cleanup_unverifiable extras", report.Diagnostics)
		}
	}
	if deadlineCount != 1 {
		t.Fatalf("deadline_exceeded diagnostics = %d, want 1; report = %#v", deadlineCount, report)
	}
	if cleanupUnverifiableCount > 1 {
		t.Fatalf("cleanup_unverifiable diagnostics = %d, want at most 1; report = %#v", cleanupUnverifiableCount, report)
	}
}

// sweepFixtureDiagnosis reports what the session-sweep fixture had done by the
// time its marker was read. An absent marker on its own cannot say whether the
// fake was killed before it could write one — a fixture race — or whether the
// sweep acted on a session it should not have, which is a production defect;
// the two repairs point in opposite directions.
//
// The fixture stamps one file per step with the wall clock, and this converts
// each to time elapsed since this test called ProbeAll. That crossing of the
// process boundary is the point: the timer starts before the fake exists,
// so a duration the fake measures for itself cannot say how much budget was
// left, and a fake killed at "request+1ms" may have been started 49ms into a
// 50ms deadline.
//
// The offsets are measured from this test's call, not from the core's timer:
// the core resolves the executable and encodes the request before arming it, so
// an offset is an *upper* bound on elapsed deadline and therefore a *lower*
// bound on the budget that remained. A step at probe+10ms of a 50ms deadline
// had at least 40ms left. That is the direction the conclusion needs — "the
// fixture still had room and produced no marker" survives the imprecision —
// but it is diagnostic evidence, not a timer oracle: the stamps cross processes
// as wall clock and lose Go's monotonic reading, so a clock adjustment
// mid-probe can skew them either way.
//
// Known limitation, predating this instrumentation: the assertion checks the
// swept pids with Kill(pid, 0) and has no start-identity check, so a pid reused
// between the sweep and the check reads as a surviving session member.
//
// A step is reported as absent only when the file does not exist; any other
// read error is reported as such, so a filesystem fault is not read as "never
// got there". Step-write failures additionally go to stderr, which the core
// collects independently of that directory.
func sweepFixtureDiagnosis(marker string, report Report, armed time.Time, elapsed time.Duration) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "probe elapsed=%s\n", elapsed)
	for _, suffix := range []string{sweepStageSpawning, sweepStageSpawned, sweepStageWritten} {
		stamp, err := os.ReadFile(marker + suffix)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			fmt.Fprintf(&builder, "step %s: absent\n", suffix)
			continue
		case err != nil:
			fmt.Fprintf(&builder, "step %s: unreadable (%v)\n", suffix, err)
			continue
		}
		at, parseErr := time.Parse(time.RFC3339Nano, string(stamp))
		if parseErr != nil {
			fmt.Fprintf(&builder, "step %s: unparsable stamp %q (%v)\n", suffix, stamp, parseErr)
			continue
		}
		fmt.Fprintf(&builder, "step %s: probe+%s\n", suffix, at.Sub(armed))
	}
	entries, err := os.ReadDir(filepath.Dir(marker))
	if err != nil {
		fmt.Fprintf(&builder, "marker directory unreadable: %v\n", err)
	} else {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		fmt.Fprintf(&builder, "marker directory=%v\n", names)
	}
	for _, diagnostic := range report.Diagnostics {
		fmt.Fprintf(&builder, "diagnostic kind=%v detail=%q stderr=%q\n",
			diagnostic.Kind, diagnostic.Detail, diagnostic.Stderr)
	}
	return builder.String()
}

func replaceEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	seen := make(map[string]bool)
	for _, entry := range environment {
		name := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			name = entry[:index]
		}
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

func environmentEntryName(entry string) string {
	if index := strings.IndexByte(entry, '='); index >= 0 {
		return entry[:index]
	}
	return entry
}

type fakeFixtureOwner struct {
	pid   int
	start string
}

type fakeFixture struct {
	directory string
	owner     fakeFixtureOwner
}

type fakeFixtureRecord struct {
	process processRecord
	owner   fakeFixtureOwner
}

func startFakeFixture() fakeFixture {
	directory := os.Getenv(fakeFixtureDirectoryEnv)
	if directory == "" {
		directory = filepath.Dir(os.Args[0])
	}
	fixture := fakeFixture{directory: directory, owner: fakeFixtureOwnerFromEnvironment()}
	if fixture.owner.pid <= 0 {
		fixture.owner.pid = os.Getppid()
		if owner, err := processByPID(fixture.owner.pid); err == nil {
			fixture.owner.start = owner.Start
		}
	}
	if process, err := processByPID(os.Getpid()); err == nil {
		recordFakeFixture(directory, process, fixture.owner)
	}
	go fixture.watchOwner()
	return fixture
}

func fakeFixtureOwnerFromEnvironment() fakeFixtureOwner {
	pid, err := strconv.Atoi(os.Getenv(fakeFixtureOwnerPIDEnv))
	if err != nil || pid <= 0 {
		return fakeFixtureOwner{}
	}
	return fakeFixtureOwner{pid: pid, start: os.Getenv(fakeFixtureOwnerStartEnv)}
}

func (fixture fakeFixture) watchOwner() {
	if fixture.owner.pid <= 0 {
		return
	}
	for {
		time.Sleep(fakeFixtureOwnerWatchPoll)
		if fixture.owner.start == "" {
			if err := syscall.Kill(fixture.owner.pid, 0); errors.Is(err, syscall.ESRCH) {
				os.Exit(0)
			}
			continue
		}
		owner, err := processByPID(fixture.owner.pid)
		if isProcessGone(err) || err == nil && (owner.IsZombie || owner.Start != fixture.owner.start) {
			os.Exit(0)
		}
	}
}

func recordFakeFixture(directory string, process processRecord, owner fakeFixtureOwner) {
	path := filepath.Join(directory, fakeFixtureRecordPrefix+strconv.Itoa(process.PID))
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%d\t%s\t%d\t%s\n", process.PID, process.Start, owner.pid, owner.start)), 0o600)
}

func readFakeFixtureRecord(t *testing.T, directory string, pid int) fakeFixtureRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, fakeFixtureRecordPrefix+strconv.Itoa(pid)))
	if err != nil {
		t.Fatal(err)
	}
	record, err := parseFakeFixtureRecord(data)
	if err != nil {
		t.Fatalf("fake fixture record for %d: %v", pid, err)
	}
	return record
}

func reapFakeFixtures(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Errorf("read fake fixture directory %q: %v", directory, err)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), fakeFixtureRecordPrefix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Errorf("read fake fixture record %q: %v", entry.Name(), err)
			continue
		}
		record, err := parseFakeFixtureRecord(data)
		if err != nil {
			t.Errorf("fake fixture record %q: %v", entry.Name(), err)
			continue
		}
		process, err := processByPID(record.process.PID)
		if isProcessGone(err) || err == nil && (process.IsZombie || process.Start != record.process.Start) {
			continue
		}
		if err != nil {
			t.Errorf("inspect fake fixture %d: %v", record.process.PID, err)
			continue
		}
		t.Errorf("partitur-adapter fixture survived assertions: pid=%d start=%s; reaping", record.process.PID, process.Start)
		if err := syscall.Kill(record.process.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("reap fake fixture %d: %v", record.process.PID, err)
		}
	}
}

func reapFakeFixturesSilently(directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), fakeFixtureRecordPrefix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			continue
		}
		record, err := parseFakeFixtureRecord(data)
		if err != nil {
			continue
		}
		process, err := processByPID(record.process.PID)
		if err == nil && !process.IsZombie && process.Start == record.process.Start {
			_ = syscall.Kill(record.process.PID, syscall.SIGKILL)
		}
	}
}

func parseFakeFixtureRecord(data []byte) (fakeFixtureRecord, error) {
	fields := strings.Split(strings.TrimSuffix(string(data), "\n"), "\t")
	if len(fields) != 4 {
		return fakeFixtureRecord{}, fmt.Errorf("%q, want process and owner identities", data)
	}
	processPID, err := strconv.Atoi(fields[0])
	if err != nil || processPID <= 0 {
		return fakeFixtureRecord{}, fmt.Errorf("process pid = %q", fields[0])
	}
	ownerPID, err := strconv.Atoi(fields[2])
	if err != nil || ownerPID <= 0 {
		return fakeFixtureRecord{}, fmt.Errorf("owner pid = %q", fields[2])
	}
	return fakeFixtureRecord{
		process: processRecord{PID: processPID, Start: fields[1]},
		owner:   fakeFixtureOwner{pid: ownerPID, start: fields[3]},
	}, nil
}

func runFakeAdapter(mode string) {
	fixture := startFakeFixture()
	adapterID := strings.TrimPrefix(filepath.Base(os.Args[0]), "partitur-adapter-")
	marker := os.Getenv(fakeMarkerEnv)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	writeValid := func(id string) {
		_, _ = os.Stdout.Write(append(validProbeFrame(id, 2, "", `{}`), '\n'))
	}
	waitEOF := func() { _, _ = io.Copy(io.Discard, os.Stdin) }
	closeOutputAndWaitEOF := func() {
		// Deliver read-side EOF without exiting first: the core must finish
		// the request-side close before Wait can close its stdin pipe.
		_ = os.Stdout.Close()
		waitEOF()
	}

	switch mode {
	case "execute_completed", "execute_nonzero", "execute_extra_after_response":
		writeValid(adapterID)
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","method":"event","params":{"type":"log","level":"info","message":"working"}}` + "\n")
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","method":"event","params":{"type":"progress","message":"done"}}` + "\n")
		_ = os.WriteFile(marker, []byte("response"), 0o600)
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"execute","result":{"outcome":"completed"}}` + "\n")
		if mode == "execute_extra_after_response" {
			_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","method":"event","params":{"type":"progress","message":"late"}}` + "\n")
		}
		waitEOF()
		if mode == "execute_nonzero" {
			_, _ = os.Stderr.WriteString("token=supersecret\n")
			_, _ = os.Stderr.WriteString(strings.Repeat("x", MaxProbeStderrBytes))
			_, _ = os.Stderr.WriteString("BEYOND-CAP")
			os.Exit(7)
		}
	case "execute_waiting_human_without_blocking":
		writeValid(adapterID)
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"execute","result":{"outcome":"waiting_human"}}` + "\n")
		waitEOF()
	case "execute_cancelled", "execute_cancelled_duplicate_ack", "execute_cancelled_extra_after_response", "execute_completed_after_cancel", "execute_cancelled_nonzero", "execute_cancel_timeout", "execute_cancelled_without_ack_hang", "execute_cancelled_after_response_hang", "execute_cancelled_eof_stderr_hang", "execute_cancelled_eof_process_hang":
		writeValid(adapterID)
		reader := bufio.NewReader(os.Stdin)
		_, _ = reader.ReadString('\n')
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","method":"event","params":{"type":"log","level":"info","message":"ready to cancel"}}` + "\n")
		_, _ = reader.ReadString('\n')
		if mode == "execute_cancel_timeout" {
			_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"cancel","result":{}}` + "\n")
			ignoreTermAndHang()
		}
		_ = os.WriteFile(marker, []byte("response"), 0o600)
		outcome := "cancelled"
		if mode == "execute_completed_after_cancel" {
			outcome = "completed"
		}
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"execute","result":{"outcome":"` + outcome + `"}}` + "\n")
		if mode == "execute_cancelled_without_ack_hang" {
			ignoreTermAndHang()
		}
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"cancel","result":{}}` + "\n")
		if mode == "execute_cancelled_after_response_hang" {
			ignoreTermAndHang()
		}
		if mode == "execute_cancelled_eof_stderr_hang" {
			_ = os.Stdout.Close()
			ignoreTermAndHang()
		}
		if mode == "execute_cancelled_eof_process_hang" {
			_ = os.Stdout.Close()
			_ = os.Stderr.Close()
			ignoreTermAndHang()
		}
		if mode == "execute_cancelled_duplicate_ack" {
			_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"cancel","result":{}}` + "\n")
		}
		if mode == "execute_cancelled_extra_after_response" {
			_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","method":"event","params":{"type":"progress","message":"late"}}` + "\n")
		}
		waitEOF()
		if mode == "execute_cancelled_nonzero" {
			os.Exit(7)
		}
	case "execute_early_duplicate_cancel_ack":
		writeValid(adapterID)
		reader := bufio.NewReader(os.Stdin)
		// Two reads: the execute request, then the cancel request itself. Waiting for the
		// cancel to arrive is what makes both acknowledgements solicited, so the duplicate
		// reaches the in-flight guard rather than the unsolicited one.
		_, _ = reader.ReadString('\n')
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","method":"event","params":{"type":"log","level":"info","message":"ready to cancel"}}` + "\n")
		_, _ = reader.ReadString('\n')
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"cancel","result":{}}` + "\n")
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"cancel","result":{}}` + "\n")
		_ = os.WriteFile(marker, []byte("response"), 0o600)
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"execute","result":{"outcome":"cancelled"}}` + "\n")
		waitEOF()
	case "execute_early_unsolicited_cancel_ack":
		writeValid(adapterID)
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		// No cancel was requested, so this acknowledgement is unsolicited. The execute
		// response that follows is what makes the guard's absence a named failure
		// rather than a hang.
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"cancel","result":{}}` + "\n")
		_ = os.WriteFile(marker, []byte("response"), 0o600)
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"execute","result":{"outcome":"completed"}}` + "\n")
		waitEOF()
	case "execute_post_eof_stderr_hang", "execute_post_eof_process_hang":
		// Respond, acknowledge nothing (no cancel was requested yet), then close stdout so
		// the core leaves its frame loop. The marker tells the test the post-EOF window is
		// open; only then does it cancel.
		writeValid(adapterID)
		reader := bufio.NewReader(os.Stdin)
		_, _ = reader.ReadString('\n')
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"execute","result":{"outcome":"completed"}}` + "\n")
		_ = os.Stdout.Close()
		if mode == "execute_post_eof_process_hang" {
			_ = os.Stderr.Close()
		}
		_ = os.WriteFile(marker+".eof", []byte("eof"), 0o600)
		ignoreTermAndHang()
	case "execute_never_reads_stdin":
		writeValid(adapterID)
		ignoreTermAndHang()
	case "execute_event_then_never_reads_stdin":
		writeValid(adapterID)
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","method":"event","params":{"type":"log","level":"info","message":"ready to cancel"}}` + "\n")
		ignoreTermAndHang()
	case "execute_cancelled_without_ack":
		writeValid(adapterID)
		reader := bufio.NewReader(os.Stdin)
		_, _ = reader.ReadString('\n')
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","method":"event","params":{"type":"log","level":"info","message":"ready to cancel"}}` + "\n")
		_, _ = reader.ReadString('\n')
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"execute","result":{"outcome":"cancelled"}}` + "\n")
		waitEOF()
	case "environment":
		data, _ := json.Marshal(struct {
			Argv0       string
			Environment []string
		}{os.Args[0], os.Environ()})
		_ = os.WriteFile(marker, data, 0o600)
		writeValid(adapterID)
		waitEOF()
	case "blank_success":
		_, _ = os.Stdout.WriteString("\n \n")
		writeValid(adapterID)
		waitEOF()
	case "delayed_exit":
		writeValid(adapterID)
		waitEOF()
		time.Sleep(80 * time.Millisecond)
		_ = os.WriteFile(marker, []byte("exited"), 0o600)
	case "immediate_clean_exit":
		writeValid(adapterID)
		waitEOF()
		_, _ = os.Stderr.WriteString("immediate-clean-exit-stderr")
	case "session_identity":
		process, err := processByPID(os.Getpid())
		if err != nil {
			os.Exit(8)
		}
		sid := process.SID
		_ = os.WriteFile(marker, []byte(fmt.Sprintf("%d %d", os.Getpid(), sid)), 0o600)
		writeValid(adapterID)
		waitEOF()
	case "premature_eof":
		closeOutputAndWaitEOF()
	case "partial_eof":
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0"`)
		closeOutputAndWaitEOF()
	case "malformed_then_valid":
		_, _ = os.Stdout.WriteString("{bad}\n")
		writeValid(adapterID)
		waitEOF()
	case "duplicate_then_valid":
		_, _ = os.Stdout.WriteString(strings.Replace(string(validProbeFrame(adapterID, 2, "", `{}`)), `"protocol":2`, `"protocol":2,"protocol":"bad"`, 1) + "\n")
		writeValid(adapterID)
		waitEOF()
	case "invalid_utf8_then_valid":
		_, _ = os.Stdout.Write([]byte{'{', 0xff, '}', '\n'})
		writeValid(adapterID)
		waitEOF()
	case "oversized_then_valid":
		_, _ = os.Stdout.WriteString(strings.Repeat("x", protocol.MaxFrameBytes+1) + "\n")
		writeValid(adapterID)
		waitEOF()
	case "error_response":
		_, _ = os.Stdout.WriteString(`{"jsonrpc":"2.0","id":"probe","error":{"code":-32603,"message":"failed"}}` + "\n")
		waitEOF()
	case "wrong_adapter":
		writeValid("other")
		waitEOF()
	case "unsupported_protocol":
		_, _ = os.Stdout.Write(append(validProbeFrame(adapterID, 3, "", `{}`), '\n'))
		waitEOF()
	case "extra_response":
		writeValid(adapterID)
		writeValid(adapterID)
		waitEOF()
	case "partial_after_response":
		writeValid(adapterID)
		_, _ = os.Stdout.WriteString(`{"jsonrpc"`)
		closeOutputAndWaitEOF()
	case "nonzero_after_response":
		writeValid(adapterID)
		waitEOF()
		os.Exit(7)
	case "hang_no_response":
		ignoreTermAndHang()
	case "hang_after_response":
		writeValid(adapterID)
		ignoreTermAndHang()
	case "session_tree_hang":
		// Each step drops its own file stamped with the wall clock. The test
		// runs on the same machine and knows when it called ProbeAll, so it can
		// bound these against the deadline — which this process cannot do on
		// its own: the core's timer starts before the fake exists, so nothing
		// measured from inside it bounds the remaining budget.
		stamp := func(suffix string) {
			if err := os.WriteFile(marker+suffix, []byte(time.Now().Format(time.RFC3339Nano)), 0o600); err != nil {
				// These share a directory with the marker, so a filesystem
				// fault takes them too. stderr does not depend on that
				// directory and the core collects it on the deadline path.
				_, _ = fmt.Fprintf(os.Stderr, "stage %s write failed: %v\n", suffix, err)
			}
		}
		stamp(sweepStageSpawning)
		child := exec.Command(os.Args[0])
		child.Env = replaceEnvironment(os.Environ(), map[string]string{
			fakeModeEnv:              "child_hang",
			fakeFixtureDirectoryEnv:  fixture.directory,
			fakeFixtureOwnerPIDEnv:   strconv.Itoa(fixture.owner.pid),
			fakeFixtureOwnerStartEnv: fixture.owner.start,
		})
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "spawn failed: %v\n", err)
			os.Exit(8)
		}
		stamp(sweepStageSpawned)
		if err := os.WriteFile(marker, []byte(fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)), 0o600); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "marker write failed: %v\n", err)
		} else {
			stamp(sweepStageWritten)
		}
		ignoreTermAndHang()
	case "child_hang":
		ignoreTermAndHang()
	case "stderr_failure":
		_, _ = os.Stderr.WriteString("harmless token=supersecret\n")
		_, _ = os.Stderr.WriteString(strings.Repeat("x", MaxProbeStderrBytes))
		_, _ = os.Stderr.WriteString("BEYOND-CAP")
	case "aggregate":
		file, _ := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		_, _ = fmt.Fprintln(file, adapterID)
		_ = file.Close()
		if adapterID == "bad" {
			return
		}
		writeValid(adapterID)
		waitEOF()
	default:
		os.Exit(9)
	}
}

func runExecuteVendorHelper(mode string) int {
	if slices.Contains(os.Args[1:], "--version") {
		fmt.Println("partitur-test 9.8.7")
		return 0
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	outputDir := os.Getenv(vendorOutEnv)
	if outputDir == "" {
		return 90
	}
	if mode == "completed" {
		if err := os.WriteFile(filepath.Join(outputDir, "report.txt"), []byte("report"), 0o600); err != nil {
			return 91
		}
		envelope := `{"version":1,"artifacts":[{"artifact_id":"report","path":"report.txt"}],"questions":[],"proposal":null,"summary":"complete"}`
		if err := os.WriteFile(filepath.Join(outputDir, "partitur-result.json"), []byte(envelope), 0o600); err != nil {
			return 92
		}
		return 0
	}
	return 93
}

func ignoreTermAndHang() {
	signalIgnore(syscall.SIGTERM)
	for {
		time.Sleep(time.Hour)
	}
}

var signalIgnore = func(value syscall.Signal) {
	// Isolated so the helper path is the only test code that changes signal
	// disposition.
	signal.Ignore(value)
}
