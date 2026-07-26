package adapter

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

const (
	fakeModeEnv   = "PARTITUR_ADAPTER_TEST_MODE"
	fakeMarkerEnv = "PARTITUR_ADAPTER_TEST_MARKER"
)

func TestMain(m *testing.M) {
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
	report := newClient(environment, 5*time.Second, 200*time.Millisecond).ProbeAll([]string{"codex", "claude"})
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
	report := newClient(environment, time.Second, 100*time.Millisecond).ProbeAll([]string{"fake"})
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
			report := newClient(test.env, time.Second, time.Millisecond).ProbeAll([]string{test.id})
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
		report := newClient([]string{"PATH=" + directory}, time.Second, time.Millisecond).ProbeAll([]string{"bad"})
		assertDiagnosticKinds(t, report, DiagnosticSpawnFailed)
	})

	t.Run("request", func(t *testing.T) {
		client := fakeClient(t, "hang_no_response", 100*time.Millisecond, 10*time.Millisecond)
		client.write = func(io.Writer, []byte) error { return errors.New("injected write failure") }
		report := client.ProbeAll([]string{"fake"})
		assertDiagnosticKinds(t, report, DiagnosticRequestIO)
	})

	t.Run("response", func(t *testing.T) {
		client := fakeClient(t, "hang_no_response", 100*time.Millisecond, 10*time.Millisecond)
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
			report := fakeClient(t, test.mode, time.Second, 20*time.Millisecond).ProbeAll([]string{"fake"})
			assertDiagnosticKinds(t, report, test.kind)
		})
	}
}

func TestBlankLinesAndPostResponseWait(t *testing.T) {
	t.Run("blank lines", func(t *testing.T) {
		report := fakeClient(t, "blank_success", time.Second, 20*time.Millisecond).ProbeAll([]string{"fake"})
		if len(report.Diagnostics) != 0 || len(report.Probes) != 1 {
			t.Fatalf("report = %#v", report)
		}
	})
	t.Run("waits for clean exit", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "exited")
		client := fakeClientWithMarker(t, "delayed_exit", marker, time.Second, 20*time.Millisecond)
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
	client := fakeClient(t, "immediate_clean_exit", time.Second, 20*time.Millisecond)
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
			report := fakeClient(t, mode, 40*time.Millisecond, 30*time.Millisecond).ProbeAll([]string{"fake"})
			assertDiagnosticKinds(t, report, DiagnosticDeadline)
		})
	}
}

func TestTimeoutSweepsEveryProcessGroupInSession(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pids")
	client := fakeClientWithMarker(t, "session_tree_hang", marker, 50*time.Millisecond, 40*time.Millisecond)
	report := client.ProbeAll([]string{"fake"})
	assertDiagnosticKinds(t, report, DiagnosticDeadline)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("session member %d still observable: %v", pid, err)
		}
	}
}

func TestSessionLeadership(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "identity")
	report := fakeClientWithMarker(t, "session_identity", marker, time.Second, 20*time.Millisecond).ProbeAll([]string{"fake"})
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
	client := fakeClient(t, "premature_eof", time.Second, time.Millisecond)
	client.sessions = failingSessionController{}
	report := client.ProbeAll([]string{"fake"})
	assertDiagnosticKinds(t, report, DiagnosticCleanupUnverifiable, DiagnosticPrematureEOF)
}

func TestStderrIsBoundedAndSanitized(t *testing.T) {
	report := fakeClient(t, "stderr_failure", time.Second, 20*time.Millisecond).ProbeAll([]string{"fake"})
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
	report := newClient(environment, time.Second, 20*time.Millisecond).ProbeAll(
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

func (failingSessionController) verifyEmpty(int, string) (bool, error) {
	return false, errors.New("injected enumeration failure")
}

func (failingSessionController) terminate(int, string, time.Duration) error {
	return errors.New("injected enumeration failure")
}

func fakeClient(t *testing.T, mode string, deadline, grace time.Duration) *Client {
	t.Helper()
	return fakeClientWithMarker(t, mode, "", deadline, grace)
}

func fakeClientWithMarker(t *testing.T, mode, marker string, deadline, grace time.Duration) *Client {
	t.Helper()
	directory := t.TempDir()
	installFake(t, directory, "fake")
	environment := []string{"PATH=" + directory, fakeModeEnv + "=" + mode}
	if marker != "" {
		environment = append(environment, fakeMarkerEnv+"="+marker)
	}
	return newClient(environment, deadline, grace)
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

func runFakeAdapter(mode string) {
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
		child := exec.Command(os.Args[0])
		child.Env = replaceEnvironment(os.Environ(), map[string]string{fakeModeEnv: "child_hang"})
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			os.Exit(8)
		}
		_ = os.WriteFile(marker, []byte(fmt.Sprintf("%d %d", os.Getpid(), child.Process.Pid)), 0o600)
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
