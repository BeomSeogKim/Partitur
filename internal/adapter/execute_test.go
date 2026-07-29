package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestRealFirstPartyAdaptersExecuteThroughGatedPeer(t *testing.T) {
	binaries := t.TempDir()
	buildAdapter(t, binaries, "claude")
	buildAdapter(t, binaries, "codex")
	trampoline := buildTrampoline(t, binaries)
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, adapterID := range []string{"claude", "codex"} {
		t.Run(adapterID, func(t *testing.T) {
			workdir := t.TempDir()
			outputDir := t.TempDir()
			attemptRoot := t.TempDir()
			environment := replaceEnvironment(os.Environ(), map[string]string{
				vendorModeEnv:         "completed",
				vendorOutEnv:          outputDir,
				"PARTITUR_CLAUDE_BIN": vendor,
				"PARTITUR_CODEX_BIN":  vendor,
			})
			client := newClient(environment, incidentalTestDeadline, 200*time.Millisecond)
			var order []string
			recorder := successfulRecorder(&order)
			recorder.RecordArtifact = func(observation ArtifactObservation) (faultpoint.DurabilityReceipt, error) {
				order = append(order, "artifact.recorded")
				info, infoErr := os.Stat(observation.Path)
				wantInfo, wantErr := os.Stat(filepath.Join(outputDir, "report.txt"))
				if observation.ArtifactID != "report" || observation.Kind != "document" ||
					observation.SourcePath != "report.txt" || infoErr != nil || wantErr != nil ||
					!os.SameFile(info, wantInfo) {
					t.Fatalf("artifact = %#v", observation)
				}
				return receipt("artifact.recorded"), nil
			}
			report, err := client.Execute(context.Background(), executePlan(
				adapterID,
				filepath.Join(binaries, "partitur-adapter-"+adapterID),
				trampoline,
				attemptRoot,
				workdir,
				outputDir,
				recorder,
				&order,
			))
			if err != nil {
				t.Fatal(err)
			}
			if report.Probe.Adapter.ID != adapterID || report.Result == nil ||
				report.Result.Outcome != protocol.OutcomeCompleted ||
				len(report.Artifacts) != 1 {
				t.Fatalf("report = %#v; order = %v", report, order)
			}
			want := []string{
				"attempt.started",
				"adapter.probed",
				"artifact.recorded",
				"execution.stopped",
				"performer.completed",
			}
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("durable order = %v, want %v", order, want)
			}
		})
	}
}

func TestExecuteCompletionOrderObservesResponseBeforeSweep(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	trampoline := buildTrampoline(t, directory)
	marker := filepath.Join(t.TempDir(), "response")
	outputDir := t.TempDir()
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_completed",
		fakeMarkerEnv + "=" + marker,
	}, incidentalTestDeadline, 20*time.Millisecond)
	probeRecorded := false
	baseWrite := client.write
	client.write = func(writer io.Writer, data []byte) error {
		if strings.Contains(string(data), `"id":"execute"`) && !probeRecorded {
			t.Fatal("execute was sent before adapter.probed became durable")
		}
		return baseWrite(writer, data)
	}
	client.sessions = &observingSessionController{
		verify: func() (bool, error) {
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "response" {
				t.Fatalf("sweep ran before complete response: data=%q err=%v", data, err)
			}
			order = append(order, "session.verified_empty")
			return true, nil
		},
	}
	recorder := successfulRecorder(&order)
	baseProbe := recorder.RecordProbe
	recorder.RecordProbe = func(result protocol.ProbeResult) (faultpoint.DurabilityReceipt, error) {
		value, err := baseProbe(result)
		probeRecorded = true
		return value, err
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		trampoline,
		t.TempDir(),
		t.TempDir(),
		outputDir,
		recorder,
		&order,
	)
	report, err := client.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result == nil || report.Result.Outcome != protocol.OutcomeCompleted {
		t.Fatalf("report = %#v; order = %v", report, order)
	}
	want := []string{
		"attempt.started",
		"adapter.probed",
		"session.verified_empty",
		"execution.stopped",
		"performer.completed",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestSweepUnverifiableLeavesIntervalAndOutcomeAbsent(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	trampoline := buildTrampoline(t, directory)
	marker := filepath.Join(t.TempDir(), "response")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_completed",
		fakeMarkerEnv + "=" + marker,
	}, incidentalTestDeadline, 20*time.Millisecond)
	client.sessions = &observingSessionController{
		verify: func() (bool, error) {
			order = append(order, "session.unverifiable")
			return false, errors.New("injected enumeration failure")
		},
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		trampoline,
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		successfulRecorder(&order),
		&order,
	)
	_, err := client.Execute(context.Background(), plan)
	if !errors.Is(err, ErrSweepUnverifiable) {
		t.Fatalf("error = %v, want sweep halt", err)
	}
	want := []string{"attempt.started", "adapter.probed", "session.unverifiable"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestNonzeroExitDiscardsResponseThenSweepsClosesAndFails(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	trampoline := buildTrampoline(t, directory)
	marker := filepath.Join(t.TempDir(), "response")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_nonzero",
		fakeMarkerEnv + "=" + marker,
	}, incidentalTestDeadline, 20*time.Millisecond)
	client.sessions = &observingSessionController{
		terminateFn: func() error {
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("sweep ran before response: %v", err)
			}
			order = append(order, "session.swept")
			return nil
		},
	}
	recorder := successfulRecorder(&order)
	var outcome OutcomeObservation
	recorder.RecordOutcome = func(observation OutcomeObservation) (faultpoint.DurabilityReceipt, error) {
		outcome = observation
		order = append(order, observation.EventType)
		return receipt(observation.EventType), nil
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		trampoline,
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		recorder,
		&order,
	)
	report, err := client.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != nil {
		t.Fatalf("provisional result was retained: %#v", report.Result)
	}
	if outcome.EventType != "attempt.failed" || outcome.Result.Failure == nil ||
		outcome.Result.Failure.Kind != protocol.FailureAdapterUnavailable ||
		outcome.FailureReason != "" {
		t.Fatalf("outcome = %#v", outcome)
	}
	if strings.Contains(report.Stderr, "supersecret") ||
		strings.Contains(report.Stderr, "BEYOND-CAP") ||
		!strings.Contains(report.Stderr, "[REDACTED]") ||
		len(report.Stderr) > MaxProbeStderrBytes {
		t.Fatalf("bounded sanitized stderr = %q", report.Stderr[:min(len(report.Stderr), 200)])
	}
	want := []string{
		"attempt.started",
		"adapter.probed",
		"session.swept",
		"execution.stopped",
		"attempt.failed",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestFrameAfterExecuteResponseIsProtocolFailure(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_extra_after_response",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 20*time.Millisecond)
	client.sessions = &observingSessionController{}
	recorder := successfulRecorder(&order)
	var outcome OutcomeObservation
	recorder.RecordOutcome = func(observation OutcomeObservation) (faultpoint.DurabilityReceipt, error) {
		outcome = observation
		order = append(order, observation.EventType)
		return receipt(observation.EventType), nil
	}
	report, err := client.Execute(context.Background(), executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		recorder,
		&order,
	))
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != nil || outcome.Result.Failure == nil ||
		outcome.Result.Failure.Kind != protocol.FailureProtocolError ||
		outcome.FailureReason != "strict_decode_failed" {
		t.Fatalf("report = %#v, outcome = %#v", report, outcome)
	}
}

func TestExecuteCancelAwaitsResponseAndRecordsNoOutcome(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	marker := filepath.Join(t.TempDir(), "response")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_cancelled",
		fakeMarkerEnv + "=" + marker,
	}, incidentalTestDeadline, 10*time.Second)
	cancel := make(chan struct{})
	baseWrite := client.write
	client.write = func(writer io.Writer, data []byte) error {
		if strings.Contains(string(data), `"method":"cancel"`) {
			assertCancelRequest(t, data)
		}
		return baseWrite(writer, data)
	}
	client.sessions = &observingSessionController{
		verify: func() (bool, error) {
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "response" {
				t.Fatalf("sweep ran before execute response: data=%q err=%v", data, err)
			}
			order = append(order, "session.verified_empty")
			return true, nil
		},
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		cancellingRecorder(&order, cancel),
		&order,
	)
	plan.Cancel = cancel
	report, err := client.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	// The response was awaited — §4 makes it the completeness marker — but it is not
	// authoritative once the cancel is on the wire, so it survives neither the journal nor
	// the report. `Result == nil` is the uniform signal across every suppressed exit.
	if report.Result != nil {
		t.Fatalf("a suppressed response survived in the report: %#v", report.Result)
	}
	want := []string{
		"attempt.started",
		"adapter.probed",
		"session.verified_empty",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("journal order = %v, want %v", order, want)
	}
}

func TestExecuteContextInterruptionWinsOverReadyCancelSignal(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_cancelled",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 10*time.Second)
	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()
	cancel := make(chan struct{})
	baseWrite := client.write
	client.write = func(writer io.Writer, data []byte) error {
		if strings.Contains(string(data), `"method":"cancel"`) {
			t.Fatal("protocol cancel was written after context interruption")
		}
		return baseWrite(writer, data)
	}
	recorder := cancellingRecorder(&order, cancel)
	recorder.ObserveLog = func(protocol.LogEvent) { cancelContext() }
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		recorder,
		&order,
	)
	plan.Cancel = cancel
	report, err := client.Execute(ctx, plan)
	if !errors.Is(err, context.Canceled) || report.Result != nil {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
	want := []string{"attempt.started", "adapter.probed"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("journal order = %v, want %v", order, want)
	}
}

// A cancel the core cannot deliver is still a cancellation. §6 leaves the interval to the
// oracle's (c) and B orders `cancelled` ahead of `adapter_unavailable`, so this route must
// record neither `execution.stopped` nor `attempt.failed`.
func TestExecuteCancelWriteFailureRecordsNoIntervalOrOutcome(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_cancelled",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 20*time.Millisecond)
	cancel := make(chan struct{})
	baseWrite := client.write
	client.write = func(writer io.Writer, data []byte) error {
		if strings.Contains(string(data), `"method":"cancel"`) {
			return errors.New("injected cancel write failure")
		}
		return baseWrite(writer, data)
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		cancellingRecorder(&order, cancel),
		&order,
	)
	plan.Cancel = cancel
	report, err := executeWithin(t, client, plan, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "injected cancel write failure") {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
	if report.Result != nil {
		t.Fatalf("report retained a result: %#v", report.Result)
	}
	want := []string{"attempt.started", "adapter.probed"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("journal order = %v, want %v", order, want)
	}
}

// The cancellation can land after the execute response has been consumed and before the
// ordinary recording runs. Nothing in the drain path selects on the signal, so the guard has
// to be the recording entry points reading it themselves — a flag set by some earlier select
// would still be false here.
func TestExecuteCancelObservedAfterResponseStillSuppressesRecording(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	marker := filepath.Join(t.TempDir(), "response")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_completed",
		fakeMarkerEnv + "=" + marker,
	}, incidentalTestDeadline, 10*time.Second)
	cancel := make(chan struct{})
	client.sessions = &observingSessionController{
		verify: func() (bool, error) {
			// The verified-empty sweep is the last thing before recordStop, so closing here
			// puts the cancellation exactly in the window the drain never watches.
			close(cancel)
			order = append(order, "session.verified_empty")
			return true, nil
		},
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		successfulRecorder(&order),
		&order,
	)
	plan.Cancel = cancel
	report, err := executeWithin(t, client, plan, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != nil {
		t.Fatalf("a suppressed response survived in the report: %#v", report.Result)
	}
	want := []string{"attempt.started", "adapter.probed", "session.verified_empty"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("journal order = %v, want %v", order, want)
	}
}

// Two windows open after stdout EOF — waiting for the stderr drain, then waiting for the
// process — and neither is inside the loop that watches the signal. The test-only hook closes
// cancellation at each window entry, establishing the happens-before edge the old marker did not.
func TestExecuteCancelAfterEOFStillArmsTheGrace(t *testing.T) {
	for _, test := range []struct {
		name   string
		mode   string
		window executeWindow
	}{
		{
			name:   "stderr drain",
			mode:   "execute_post_eof_stderr_hang",
			window: executeWindowStderrDrain,
		},
		{
			name:   "process wait",
			mode:   "execute_post_eof_process_hang",
			window: executeWindowProcessWait,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			installFake(t, directory, "fake")
			var order []string
			client := newClient([]string{
				fakeModeEnv + "=" + test.mode,
			}, incidentalTestDeadline, 20*time.Millisecond)
			cancel := make(chan struct{})
			var observedWindow executeWindow
			client.observeExecuteWindow = func(window executeWindow) {
				if window == test.window {
					observedWindow = window
					close(cancel)
				}
			}
			terminated := 0
			client.sessions = &recordingSessionController{
				base:        systemSessionController{},
				terminateFn: func() { terminated++ },
			}
			plan := executePlan(
				"fake",
				filepath.Join(directory, "partitur-adapter-fake"),
				buildTrampoline(t, directory),
				t.TempDir(),
				t.TempDir(),
				t.TempDir(),
				successfulRecorder(&order),
				&order,
			)
			plan.Cancel = cancel
			report, err := executeWithin(t, client, plan, 2*time.Second)
			if err == nil || !strings.Contains(err.Error(), "cancel completion grace expired") {
				t.Fatalf("report = %#v, error = %v", report, err)
			}
			if report.Result != nil || terminated != 1 {
				t.Fatalf("report = %#v, forced session sweeps = %d", report, terminated)
			}
			if observedWindow != test.window {
				t.Fatalf("observed window = %s, want %s", observedWindow, test.window)
			}
			want := []string{"attempt.started", "adapter.probed"}
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("journal order = %v, want %v", order, want)
			}
		})
	}
}

// The execute and cancel writes are cancellation windows. The fake consumes only the run
// probe, so the injected oversized request blocks in the real stdin pipe until the sweep closes it.
// §6's watch runs for the driver's whole tenure, and §4 puts the run-probe request write
// inside the completion deadline. The adapter process is already running by then, so a
// cancellation arriving before `adapter.probed` has to sweep it — with no protocol `cancel`,
// since no `execute` has been authorized.
// The gated handoff is the first window of the call and precedes every other observation
// point, so a cancellation arriving there has to end it rather than wait for a process that
// is still starting.
func TestExecuteCancellationDuringLaunchEndsTheCall(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{fakeModeEnv + "=hang_no_response"}, incidentalTestDeadline, 20*time.Millisecond)
	cancel := make(chan struct{})
	client.launch = func(ctx context.Context, _ launch.Request) (*launch.Process, error) {
		close(cancel)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		successfulRecorder(&order),
		&order,
	)
	plan.Cancel = cancel
	// Bounded well below the client's own 10s deadline: without the launch watcher that
	// deadline is what ends the call, and the test would pass for the wrong reason.
	report, err := executeWithin(t, client, plan, 2*time.Second)
	if err == nil {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
	// The launch never happened, so nothing was recorded at all.
	if len(order) != 0 {
		t.Fatalf("journal order = %v, want nothing recorded", order)
	}
}

func TestExecuteCancellationIsObservedBeforeTheProbeCompletes(t *testing.T) {
	for _, test := range []struct {
		name       string
		blockWrite bool
		wantError  string
	}{
		{name: "probe request write", blockWrite: true, wantError: "cancel observed while writing run probe request"},
		{name: "probe response wait", blockWrite: false, wantError: "cancel observed while awaiting run probe response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			installFake(t, directory, "fake")
			var order []string
			// The fake never reads stdin and never responds, so neither window can close
			// on its own — only the cancellation can end this call.
			client := newClient([]string{fakeModeEnv + "=hang_no_response"}, incidentalTestDeadline, 20*time.Millisecond)
			cancel := make(chan struct{})
			if test.blockWrite {
				baseWrite := client.write
				client.write = func(writer io.Writer, data []byte) error {
					if !strings.Contains(string(data), `"method":"probe"`) {
						return baseWrite(writer, data)
					}
					// Oversized so the pipe fills against a fake that never reads.
					go close(cancel)
					return baseWrite(writer, bytes.Repeat(data, 1<<16))
				}
			} else {
				// Entry to the response window is the happens-before edge; closing from the
				// write hook would leave the two selects racing for the same signal.
				client.observeExecuteWindow = func(window executeWindow) {
					if window == executeWindowProbeResponse {
						close(cancel)
					}
				}
			}
			terminated := 0
			client.sessions = &recordingSessionController{
				base:        systemSessionController{},
				terminateFn: func() { terminated++ },
			}
			plan := executePlan(
				"fake",
				filepath.Join(directory, "partitur-adapter-fake"),
				buildTrampoline(t, directory),
				t.TempDir(),
				t.TempDir(),
				t.TempDir(),
				successfulRecorder(&order),
				&order,
			)
			plan.Cancel = cancel
			report, err := executeWithin(t, client, plan, 30*time.Second)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("report = %#v, error = %v", report, err)
			}
			if report.Result != nil || terminated != 1 {
				t.Fatalf("report = %#v, forced session sweeps = %d", report, terminated)
			}
			// Nothing durable is owed: the probe never completed, so not even
			// adapter.probed was recorded.
			want := []string{"attempt.started"}
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("journal order = %v, want %v", order, want)
			}
		})
	}
}

func TestExecuteCancellationBoundsBlockedRequestWrites(t *testing.T) {
	for _, test := range []struct {
		name      string
		mode      string
		blockedID string
		wantError string
	}{
		{name: "execute request", mode: "execute_never_reads_stdin", blockedID: "execute", wantError: "cancel observed while writing execute request"},
		{name: "cancel request", mode: "execute_event_then_never_reads_stdin", blockedID: "cancel", wantError: "cancel completion grace expired"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			installFake(t, directory, "fake")
			var order []string
			client := newClient([]string{fakeModeEnv + "=" + test.mode}, incidentalTestDeadline, 20*time.Millisecond)
			cancel := make(chan struct{})
			writeStarted := make(chan struct{}, 1)
			baseWrite := client.write
			client.write = func(writer io.Writer, data []byte) error {
				request := string(data)
				if strings.Contains(request, `"method":"execute"`) {
					if test.blockedID == "execute" {
						writeStarted <- struct{}{}
						return baseWrite(writer, bytes.Repeat(data, 1<<16))
					}
					return baseWrite(writer, data)
				}
				if strings.Contains(request, `"method":"cancel"`) && test.blockedID == "cancel" {
					writeStarted <- struct{}{}
					return baseWrite(writer, bytes.Repeat(data, 1<<16))
				}
				return baseWrite(writer, data)
			}
			if test.blockedID == "execute" {
				go func() {
					<-writeStarted
					close(cancel)
				}()
			}
			terminated := 0
			client.sessions = &recordingSessionController{
				base:        systemSessionController{},
				terminateFn: func() { terminated++ },
			}
			plan := executePlan(
				"fake",
				filepath.Join(directory, "partitur-adapter-fake"),
				buildTrampoline(t, directory),
				t.TempDir(),
				t.TempDir(),
				t.TempDir(),
				cancellingRecorder(&order, cancel),
				&order,
			)
			plan.Cancel = cancel
			report, err := executeWithin(t, client, plan, 2*time.Second)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("report = %#v, error = %v", report, err)
			}
			if report.Result != nil || terminated != 1 {
				t.Fatalf("report = %#v, forced session sweeps = %d", report, terminated)
			}
			want := []string{"attempt.started", "adapter.probed"}
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("journal order = %v, want %v", order, want)
			}
		})
	}
}

func TestExecuteCancelGraceTimeoutForcesVerifiedEmptySweep(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_cancel_timeout",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 20*time.Millisecond)
	cancel := make(chan struct{})
	baseWrite := client.write
	client.write = func(writer io.Writer, data []byte) error {
		if strings.Contains(string(data), `"method":"cancel"`) {
			assertCancelRequest(t, data)
		}
		return baseWrite(writer, data)
	}
	terminated := 0
	client.sessions = &recordingSessionController{
		base: systemSessionController{},
		terminateFn: func() {
			terminated++
		},
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		cancellingRecorder(&order, cancel),
		&order,
	)
	plan.Cancel = cancel
	report, err := executeWithin(t, client, plan, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "cancel completion grace expired") {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
	if terminated != 1 {
		t.Fatalf("forced session sweeps = %d, want 1", terminated)
	}
	want := []string{"attempt.started", "adapter.probed"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("journal order = %v, want %v", order, want)
	}
}

func TestExecuteCancelResponseWithoutAcknowledgementTimesOutAndSweeps(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_cancelled_without_ack_hang",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 20*time.Millisecond)
	cancel := make(chan struct{})
	terminated := 0
	client.sessions = &recordingSessionController{
		base: systemSessionController{},
		terminateFn: func() {
			terminated++
		},
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		cancellingRecorder(&order, cancel),
		&order,
	)
	plan.Cancel = cancel
	report, err := executeWithin(t, client, plan, 30*time.Second)
	if err == nil || !strings.Contains(err.Error(), "cancel completion grace expired") {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
	if report.Result != nil || terminated != 1 {
		t.Fatalf("report = %#v, forced session sweeps = %d", report, terminated)
	}
	want := []string{"attempt.started", "adapter.probed"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("journal order = %v, want %v", order, want)
	}
}

// executeWithin bounds Execute so that losing the cancellation deadline fails this test
// by name instead of hanging the package. The guards under test are deadlines: deleting
// one makes the call wait forever, and a package-level timeout hides which test broke.
func executeWithin(t *testing.T, client *Client, plan ExecutePlan, limit time.Duration) (ExecuteReport, error) {
	t.Helper()
	type outcome struct {
		report ExecuteReport
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		report, err := client.Execute(context.Background(), plan)
		done <- outcome{report: report, err: err}
	}()
	select {
	case result := <-done:
		return result.report, result.err
	case <-time.After(limit):
		t.Fatalf("Execute did not return within %s: the cancellation deadline is not bounding this window", limit)
		return ExecuteReport{}, nil
	}
}

func TestExecuteCancelGraceSurvivesOutputAndProcessDrain(t *testing.T) {
	for _, mode := range []string{
		"execute_cancelled_eof_stderr_hang",
		"execute_cancelled_eof_process_hang",
	} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			installFake(t, directory, "fake")
			var order []string
			client := newClient([]string{
				fakeModeEnv + "=" + mode,
				fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
			}, incidentalTestDeadline, 20*time.Millisecond)
			cancel := make(chan struct{})
			terminated := 0
			client.sessions = &recordingSessionController{
				base: systemSessionController{},
				terminateFn: func() {
					terminated++
				},
			}
			plan := executePlan(
				"fake",
				filepath.Join(directory, "partitur-adapter-fake"),
				buildTrampoline(t, directory),
				t.TempDir(),
				t.TempDir(),
				t.TempDir(),
				cancellingRecorder(&order, cancel),
				&order,
			)
			plan.Cancel = cancel
			report, err := executeWithin(t, client, plan, 30*time.Second)
			if err == nil || !strings.Contains(err.Error(), "cancel completion grace expired") {
				t.Fatalf("report = %#v, error = %v", report, err)
			}
			if report.Result != nil || terminated != 1 {
				t.Fatalf("report = %#v, forced session sweeps = %d", report, terminated)
			}
			want := []string{"attempt.started", "adapter.probed"}
			if !reflect.DeepEqual(order, want) {
				t.Fatalf("journal order = %v, want %v", order, want)
			}
		})
	}
}

func TestExecuteCancelExpirySweepFailureHaltsWithoutTerminalJournalEvent(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_cancelled_after_response_hang",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 20*time.Millisecond)
	cancel := make(chan struct{})
	client.sessions = sweepingFailureSessionController{}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		cancellingRecorder(&order, cancel),
		&order,
	)
	plan.Cancel = cancel
	report, err := executeWithin(t, client, plan, 30*time.Second)
	if !errors.Is(err, ErrSweepUnverifiable) || report.Result != nil {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
	want := []string{"attempt.started", "adapter.probed"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("journal order = %v, want %v", order, want)
	}
}

func TestExecuteRejectsDuplicateCancelAcknowledgement(t *testing.T) {
	report, order, err := runSolicitedCancelFixture(t, "execute_cancelled_duplicate_ack")
	if err == nil {
		t.Fatal("duplicate acknowledgement succeeded")
	}
	if !strings.Contains(err.Error(), "frame followed execute response") {
		t.Fatalf("error = %v", err)
	}
	if report.Result != nil {
		t.Fatalf("report = %#v", report)
	}
	assertCancelJournalStopsBeforeTerminal(t, order)
}

// The in-flight guards below need acknowledgements that arrive *before* the execute
// response. The post-response fixtures cannot reach them: every frame after the response
// is handled by the drain loop instead.

func TestExecuteRejectsEarlyDuplicateCancelAcknowledgement(t *testing.T) {
	report, order, err := runSolicitedCancelFixture(t, "execute_early_duplicate_cancel_ack")
	if err == nil {
		t.Fatal("duplicate acknowledgement succeeded")
	}
	if !strings.Contains(err.Error(), "duplicate cancel acknowledgement") {
		t.Fatalf("error = %v", err)
	}
	if report.Result != nil {
		t.Fatalf("report = %#v", report)
	}
	assertCancelJournalStopsBeforeTerminal(t, order)
}

func TestExecuteRejectsFrameAfterSolicitedCancel(t *testing.T) {
	report, order, err := runSolicitedCancelFixture(t, "execute_cancelled_extra_after_response")
	if err == nil {
		t.Fatal("extra frame succeeded")
	}
	if !strings.Contains(err.Error(), "frame followed execute response") {
		t.Fatalf("error = %v", err)
	}
	if report.Result != nil {
		t.Fatalf("report = %#v", report)
	}
	assertCancelJournalStopsBeforeTerminal(t, order)
}

func TestExecuteSuppressesWaitFailureAfterSolicitedCancel(t *testing.T) {
	report, order, err := runSolicitedCancelFixture(t, "execute_cancelled_nonzero")
	if err == nil {
		t.Fatal("nonzero exit succeeded")
	}
	if !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("error = %v", err)
	}
	if report.Result != nil {
		t.Fatalf("report = %#v", report)
	}
	assertCancelJournalStopsBeforeTerminal(t, order)
}

// An adapter may legitimately finish while the cancel is in flight (§4 blesses that race).
// The journal ignores the response, and so must the report: driver.go reads
// report.Result.Outcome directly and treats `completed` as licence to run acceptance, so a
// result left in place here would restart the pipeline on a run that is terminating.
func TestExecuteSuppressesCompletedResultAfterSolicitedCancel(t *testing.T) {
	report, order, err := runSolicitedCancelFixture(t, "execute_completed_after_cancel")
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != nil {
		t.Fatalf("a suppressed response survived in the report: %#v", report.Result)
	}
	assertCancelJournalStopsBeforeTerminal(t, order)
}

// runCancelAckFixture drives an unsolicited-ack fake-adapter mode and returns what the core recorded.
func runCancelAckFixture(t *testing.T, mode string) (ExecuteReport, OutcomeObservation) {
	t.Helper()
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=" + mode,
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
		// A grace long enough that the fixture's extra stdin round trip cannot expire it.
		// The guards under test are about frame handling, not about timing, so the timer
		// must not be able to decide the outcome.
	}, incidentalTestDeadline, 10*time.Second)
	var outcome OutcomeObservation
	recorder := successfulRecorder(&order)
	recorder.RecordOutcome = func(observation OutcomeObservation) (faultpoint.DurabilityReceipt, error) {
		outcome = observation
		order = append(order, observation.EventType)
		return receipt(observation.EventType), nil
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		recorder,
		&order,
	)
	report, err := client.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	return report, outcome
}

func runSolicitedCancelFixture(t *testing.T, mode string) (ExecuteReport, []string, error) {
	t.Helper()
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=" + mode,
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 10*time.Second)
	cancel := make(chan struct{})
	baseWrite := client.write
	client.write = func(writer io.Writer, data []byte) error {
		if strings.Contains(string(data), `"method":"cancel"`) {
			assertCancelRequest(t, data)
		}
		return baseWrite(writer, data)
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		cancellingRecorder(&order, cancel),
		&order,
	)
	plan.Cancel = cancel
	report, err := executeWithin(t, client, plan, 30*time.Second)
	return report, order, err
}

func assertCancelJournalStopsBeforeTerminal(t *testing.T, order []string) {
	t.Helper()
	want := []string{"attempt.started", "adapter.probed"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("journal order = %v, want %v", order, want)
	}
}

func TestExecuteMissingCancelAcknowledgementLeavesIntervalAndOutcomeAbsent(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_cancelled_without_ack",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 10*time.Second)
	cancel := make(chan struct{})
	client.sessions = &observingSessionController{
		terminateFn: func() error {
			order = append(order, "session.swept")
			return nil
		},
	}
	plan := executePlan(
		"fake",
		filepath.Join(directory, "partitur-adapter-fake"),
		buildTrampoline(t, directory),
		t.TempDir(),
		t.TempDir(),
		t.TempDir(),
		cancellingRecorder(&order, cancel),
		&order,
	)
	plan.Cancel = cancel
	report, err := client.Execute(context.Background(), plan)
	if err == nil {
		t.Fatal("missing acknowledgement succeeded")
	}
	if errors.Is(err, ErrSweepUnverifiable) {
		t.Fatal("missing acknowledgement became a sweep halt")
	}
	if !strings.Contains(err.Error(), "cancel acknowledgement absent") {
		t.Fatalf("error = %v", err)
	}
	if report.Result != nil {
		t.Fatalf("report retained missing-ack result: %#v", report)
	}
	want := []string{"attempt.started", "adapter.probed", "session.swept"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("journal order = %v, want %v", order, want)
	}
}

func TestExecuteRejectsUnsolicitedCancelAcknowledgement(t *testing.T) {
	report, outcome := runCancelAckFixture(t, "execute_early_unsolicited_cancel_ack")
	if report.Result != nil || outcome.EventType != "attempt.failed" ||
		outcome.Result.Failure == nil || outcome.Result.Failure.Kind != protocol.FailureProtocolError ||
		outcome.FailureReason != "strict_decode_failed" ||
		outcome.Result.Failure.Detail != "unsolicited cancel acknowledgement" {
		t.Fatalf("report = %#v, outcome = %#v", report, outcome)
	}
}

func TestEventGuardsRejectBeforeArtifactAppend(t *testing.T) {
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "ok.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(outputDir, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(outputDir, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		outputs []protocol.OutputSpec
		events  []any
		reason  string
	}{
		{
			name:    "duplicate artifact notification",
			outputs: []protocol.OutputSpec{{ArtifactID: "report", Kind: "document"}},
			events: []any{
				&protocol.ArtifactEvent{Type: protocol.EventArtifact, ArtifactID: "report", Path: "ok.txt"},
				&protocol.ArtifactEvent{Type: protocol.EventArtifact, ArtifactID: "report", Path: "ok.txt"},
			},
			reason: "duplicate_artifact_instance",
		},
		{
			name: "undeclared artifact",
			events: []any{
				&protocol.ArtifactEvent{Type: protocol.EventArtifact, ArtifactID: "report", Path: "ok.txt"},
			},
			reason: "undeclared_artifact",
		},
		{
			name:    "change set artifact",
			outputs: []protocol.OutputSpec{{ArtifactID: "changes", Kind: "change_set"}},
			events: []any{
				&protocol.ArtifactEvent{Type: protocol.EventArtifact, ArtifactID: "changes", Path: "ok.txt"},
			},
			reason: "change_set_emitted_as_artifact",
		},
		{
			name:    "path escape",
			outputs: []protocol.OutputSpec{{ArtifactID: "report", Kind: "document"}},
			events: []any{
				&protocol.ArtifactEvent{Type: protocol.EventArtifact, ArtifactID: "report", Path: outside},
			},
			reason: "artifact_path_escape",
		},
		{
			name:    "symlink escape",
			outputs: []protocol.OutputSpec{{ArtifactID: "report", Kind: "document"}},
			events: []any{
				&protocol.ArtifactEvent{Type: protocol.EventArtifact, ArtifactID: "report", Path: "outside-link"},
			},
			reason: "artifact_path_escape",
		},
		{
			name:    "non-regular artifact",
			outputs: []protocol.OutputSpec{{ArtifactID: "report", Kind: "document"}},
			events: []any{
				&protocol.ArtifactEvent{Type: protocol.EventArtifact, ArtifactID: "report", Path: "directory"},
			},
			reason: "artifact_path_escape",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			appends := 0
			state := executeState{
				plan: ExecutePlan{
					Request: protocol.ExecuteRequest{OutputDir: outputDir},
					Recorder: ExecuteRecorder{
						RecordArtifact: func(ArtifactObservation) (faultpoint.DurabilityReceipt, error) {
							appends++
							return receipt("artifact.recorded"), nil
						},
					},
				},
				outputs:      declaredOutputs(test.outputs),
				artifactSeen: map[string]bool{},
				emittedIDs:   map[string]bool{},
				blockingIDs:  map[string]bool{},
			}
			var eventErr error
			for _, event := range test.events {
				eventErr = state.consumeEvent(event)
				if eventErr != nil {
					break
				}
			}
			failure, ok := asProtocolFailure(eventErr)
			if !ok || failure.reason != test.reason {
				t.Fatalf("failure = %#v, want %q", eventErr, test.reason)
			}
			wantAppends := 0
			if test.name == "duplicate artifact notification" {
				wantAppends = 1
			}
			if appends != wantAppends {
				t.Fatalf("artifact appends = %d, want %d", appends, wantAppends)
			}
		})
	}
}

func TestArtifactPersistenceFailureIsNotMisclassifiedAsProtocolError(t *testing.T) {
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "report.txt"), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	persistenceErr := errors.New("injected immutable publication failure")
	state := executeState{
		plan: ExecutePlan{
			Request: protocol.ExecuteRequest{OutputDir: outputDir},
			Recorder: ExecuteRecorder{
				RecordArtifact: func(ArtifactObservation) (faultpoint.DurabilityReceipt, error) {
					return faultpoint.DurabilityReceipt{}, persistenceErr
				},
			},
		},
		outputs:      map[string]string{"report": "document"},
		artifactSeen: map[string]bool{},
		emittedIDs:   map[string]bool{},
		blockingIDs:  map[string]bool{},
	}
	err := state.consumeEvent(&protocol.ArtifactEvent{
		Type: protocol.EventArtifact, ArtifactID: "report", Path: "report.txt",
	})
	if !errors.Is(err, persistenceErr) {
		t.Fatalf("error = %v, want persistence failure", err)
	}
	if _, protocolErr := asProtocolFailure(err); protocolErr {
		t.Fatalf("persistence failure was classified as protocol error: %v", err)
	}
}

func TestDecisionAndEventLimitGuards(t *testing.T) {
	tests := []struct {
		name       string
		mayPropose bool
		draft      bool
		eventCount int
		events     []any
		reason     string
	}{
		{
			name: "proposal authority",
			events: []any{&protocol.ProposalEvent{
				Type: protocol.EventProposal, ID: "p1",
				Amendment: json.RawMessage(`{}`),
			}},
			reason: "proposal_without_authority",
		},
		{
			name:       "draft proposal blocks",
			mayPropose: true,
			draft:      true,
			events: []any{&protocol.ProposalEvent{
				Type: protocol.EventProposal, ID: "p1",
				Amendment: json.RawMessage(`{}`),
			}},
			reason: "draft_non_blocking_proposal",
		},
		{
			name:       "duplicate emitted id",
			mayPropose: true,
			events: []any{
				&protocol.QuestionEvent{Type: protocol.EventQuestion, ID: "same", Question: "why?"},
				&protocol.ProposalEvent{Type: protocol.EventProposal, ID: "same", Amendment: json.RawMessage(`{}`)},
			},
			reason: "duplicate_emitted_id",
		},
		{
			name:       "event limit",
			eventCount: adapterkit.MaxEventsPerAttempt,
			events:     []any{&protocol.ProgressEvent{Type: protocol.EventProgress, Message: "more"}},
			reason:     "event_limit_exceeded",
		},
		{
			name:       "proposal amendment object",
			mayPropose: true,
			events: []any{&protocol.ProposalEvent{
				Type: protocol.EventProposal, ID: "p1",
				Amendment: json.RawMessage(`[]`),
			}},
			reason: "strict_decode_failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := executeState{
				plan: ExecutePlan{
					MayPropose: test.mayPropose,
					Draft:      test.draft,
					Recorder: ExecuteRecorder{
						ObserveProgress: func(protocol.ProgressEvent) {},
					},
				},
				outputs:      map[string]string{},
				artifactSeen: map[string]bool{},
				emittedIDs:   map[string]bool{},
				blockingIDs:  map[string]bool{},
				eventCount:   test.eventCount,
			}
			var eventErr error
			for _, event := range test.events {
				eventErr = state.consumeEvent(event)
				if eventErr != nil {
					break
				}
			}
			failure, ok := asProtocolFailure(eventErr)
			if !ok || failure.reason != test.reason {
				t.Fatalf("failure = %#v, want %q", eventErr, test.reason)
			}
		})
	}
}

func TestBlockingSetMustExactlyMatchRaisedDecisions(t *testing.T) {
	state := executeState{blockingIDs: map[string]bool{"q1": true}}
	tests := []protocol.ExecuteResult{
		{Outcome: protocol.OutcomeCompleted},
		{Outcome: protocol.OutcomeWaitingHuman, PendingDecisionIDs: []string{"other"}},
		{Outcome: protocol.OutcomeWaitingHuman, PendingDecisionIDs: []string{"q1", "q1"}},
	}
	for _, result := range tests {
		if failure := state.validateBlocking(result); failure == nil ||
			failure.reason != "blocking_set_mismatch" {
			t.Fatalf("result %#v failure = %#v", result, failure)
		}
	}
	if failure := state.validateBlocking(protocol.ExecuteResult{
		Outcome: protocol.OutcomeWaitingHuman, PendingDecisionIDs: []string{"q1"},
	}); failure != nil {
		t.Fatalf("matching set failed: %v", failure)
	}
}

func TestExecuteOutcomeMappingIsOneToOne(t *testing.T) {
	tests := []struct {
		name      string
		result    protocol.ExecuteResult
		eventType string
		failure   protocol.FailureKind
		reason    string
		raised    int
	}{
		{
			name:      "completed",
			result:    protocol.ExecuteResult{Outcome: protocol.OutcomeCompleted},
			eventType: "performer.completed",
		},
		{
			name: "failed",
			result: protocol.ExecuteResult{
				Outcome: protocol.OutcomeFailed,
				Failure: &protocol.Failure{Kind: protocol.FailureRateLimited},
			},
			eventType: "attempt.failed",
			failure:   protocol.FailureRateLimited,
		},
		{
			name: "waiting human",
			result: protocol.ExecuteResult{
				Outcome:            protocol.OutcomeWaitingHuman,
				PendingDecisionIDs: []string{"q1", "p1"},
			},
			eventType: "attempt.blocked",
			raised:    2,
		},
		{
			name:      "unsolicited cancellation",
			result:    protocol.ExecuteResult{Outcome: protocol.OutcomeCancelled},
			eventType: "attempt.failed",
			failure:   protocol.FailureTaskFailed,
			reason:    "unsolicited_cancel",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var observation OutcomeObservation
			state := executeState{
				plan: ExecutePlan{Recorder: ExecuteRecorder{
					RecordOutcome: func(value OutcomeObservation) (faultpoint.DurabilityReceipt, error) {
						observation = value
						return receipt(value.EventType), nil
					},
				}},
				report: ExecuteReport{Result: &test.result},
			}
			if test.raised != 0 {
				state.report.Questions = []protocol.QuestionEvent{{
					Type: protocol.EventQuestion, ID: "q1", Question: "question?",
				}}
				state.report.Proposals = []protocol.ProposalEvent{{
					Type: protocol.EventProposal, ID: "p1",
					Amendment: json.RawMessage(`{}`), RequiresDecision: true,
				}}
			}
			if err := newClient(nil, incidentalTestDeadline, time.Second).recordResult(&state); err != nil {
				t.Fatal(err)
			}
			if observation.EventType != test.eventType ||
				observation.FailureReason != test.reason ||
				len(observation.Raised) != test.raised {
				t.Fatalf("observation = %#v", observation)
			}
			if test.failure != "" {
				if observation.Result.Failure == nil ||
					observation.Result.Failure.Kind != test.failure {
					t.Fatalf("failure = %#v, want %q", observation.Result.Failure, test.failure)
				}
			}
		})
	}
}

func TestCancellationInFlightSuppressesEveryTerminalRecordingEntryPoint(t *testing.T) {
	var recorded []string
	state := executeState{
		plan: ExecutePlan{Recorder: ExecuteRecorder{
			RecordExecutionStopped: func(ExecutionStop) (faultpoint.DurabilityReceipt, error) {
				recorded = append(recorded, "execution.stopped")
				return receipt("execution.stopped"), nil
			},
			RecordOutcome: func(observation OutcomeObservation) (faultpoint.DurabilityReceipt, error) {
				recorded = append(recorded, observation.EventType)
				return receipt(observation.EventType), nil
			},
		}},
		report:               ExecuteReport{Result: &protocol.ExecuteResult{Outcome: protocol.OutcomeCompleted}},
		cancellationInFlight: true,
	}
	client := newClient(nil, incidentalTestDeadline, time.Second)
	if err := client.recordStop(&state); err != nil {
		t.Fatal(err)
	}
	if err := client.recordResult(&state); err != nil {
		t.Fatal(err)
	}
	_, err := client.recordFailure(&state, protocol.FailureAdapterUnavailable, "", "wait error", errors.New("wait error"))
	if err == nil {
		t.Fatal("suppressed failure lost its cause")
	}
	if !strings.Contains(err.Error(), "wait error") {
		t.Fatalf("error = %v", err)
	}
	if err := state.recordOutcome("performer.completed", protocol.ExecuteResult{Outcome: protocol.OutcomeCompleted}, ""); err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 0 {
		t.Fatalf("terminal recordings = %v", recorded)
	}
}

func TestExecuteReceiptsBindEveryDurableBoundary(t *testing.T) {
	for _, eventType := range []string{
		"adapter.probed",
		"artifact.recorded",
		"execution.stopped",
		"performer.completed",
		"attempt.failed",
		"attempt.blocked",
	} {
		t.Run(eventType, func(t *testing.T) {
			if err := validateExecuteReceipt(receipt(eventType), eventType); err != nil {
				t.Fatal(err)
			}
			mutated := receipt(eventType)
			mutated.Mutation.EventType = "wrong"
			if err := validateExecuteReceipt(mutated, eventType); err == nil {
				t.Fatal("wrong event receipt accepted")
			}
		})
	}
}

func TestExecuteWireRejectsNonStrictAndPostResponseShapes(t *testing.T) {
	tests := []string{
		`{"jsonrpc":"2.0","id":"execute","id":"other","result":{"outcome":"completed"}}`,
		`{"jsonrpc":"2.0","id":"execute","result":{"outcome":"completed","extra":true}}`,
		`{"jsonrpc":"2.0","method":"other","params":{"type":"progress","message":"x"}}`,
		`{"jsonrpc":"2.0","method":"event","params":{"type":"unknown"}}`,
		`{"jsonrpc":"2.0","id":"execute","result":{"outcome":"failed"}}`,
	}
	for _, frame := range tests {
		if _, err := decodeExecuteFrame([]byte(frame)); err == nil {
			t.Fatalf("accepted frame %s", frame)
		}
	}
}

func TestExecuteWireAcceptsOnlyStrictCancelAcknowledgement(t *testing.T) {
	ack, err := decodeExecuteFrame([]byte(`{"jsonrpc":"2.0","id":"cancel","result":{}}`))
	if err != nil || ack.kind != executeFrameCancelAck {
		t.Fatalf("ack = %#v, error = %v", ack, err)
	}
	for _, frame := range []string{
		`{"jsonrpc":"2.0","id":"other","result":{}}`,
		`{"jsonrpc":"2.0","id":"execute","result":{}}`,
		`{"jsonrpc":"2.0","id":"cancel","params":{},"result":{}}`,
		`{"jsonrpc":"2.0","id":"cancel","error":{"code":-32603,"message":"no"}}`,
		`{"jsonrpc":"2.0","id":"cancel","result":null}`,
		`{"jsonrpc":"2.0","id":"cancel","result":[]}`,
		`{"jsonrpc":"2.0","id":"cancel","result":{"unexpected":true}}`,
	} {
		if _, err := decodeExecuteFrame([]byte(frame)); err == nil {
			t.Fatalf("accepted invalid cancel acknowledgement %s", frame)
		}
	}
}

func assertCancelRequest(t *testing.T, frame []byte) {
	t.Helper()
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := protocol.DecodeStrict(bytes.TrimSpace(frame), &envelope); err != nil {
		t.Fatalf("cancel frame is not strict: %v", err)
	}
	if envelope.JSONRPC != "2.0" || !bytes.Equal(envelope.ID, cancelRequestID) || envelope.Method != "cancel" {
		t.Fatalf("cancel envelope = %#v", envelope)
	}
	var request protocol.CancelRequest
	if err := protocol.DecodeStrict(envelope.Params, &request); err != nil {
		t.Fatalf("cancel params = %s: %v", envelope.Params, err)
	}
	if request.AttemptID != "attempt-1" {
		t.Fatalf("cancel request = %#v", request)
	}
}

type observingSessionController struct {
	mu          sync.Mutex
	verify      func() (bool, error)
	terminateFn func() error
}

type recordingSessionController struct {
	base        sessionController
	terminateFn func()
}

type sweepingFailureSessionController struct{}

func (controller *recordingSessionController) verifyEmpty(sid int, leaderStart string) (bool, error) {
	return controller.base.verifyEmpty(sid, leaderStart)
}

func (controller *recordingSessionController) terminate(sid int, leaderStart string, grace time.Duration) error {
	controller.terminateFn()
	return controller.base.terminate(sid, leaderStart, grace)
}

func (sweepingFailureSessionController) verifyEmpty(sid int, leaderStart string) (bool, error) {
	return systemSessionController{}.verifyEmpty(sid, leaderStart)
}

func (sweepingFailureSessionController) terminate(sid int, leaderStart string, grace time.Duration) error {
	_ = systemSessionController{}.terminate(sid, leaderStart, grace)
	return errors.New("injected enumeration failure")
}

func (controller *observingSessionController) verifyEmpty(int, string) (bool, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.verify == nil {
		return true, nil
	}
	return controller.verify()
}

func (controller *observingSessionController) terminate(int, string, time.Duration) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.terminateFn == nil {
		return nil
	}
	return controller.terminateFn()
}

func executePlan(
	adapterID, adapterPath, trampoline, attemptRoot, workdir, outputDir string,
	recorder ExecuteRecorder,
	order *[]string,
) ExecutePlan {
	return ExecutePlan{
		AdapterID:      adapterID,
		AdapterPath:    adapterPath,
		TrampolinePath: trampoline,
		AttemptRoot:    attemptRoot,
		LaunchID:       "adapter-1",
		Directory:      workdir,
		Request: protocol.ExecuteRequest{
			RunID:         "run-1",
			MovementID:    "movement-1",
			AttemptID:     "attempt-1",
			ScoreRevision: 1,
			Model:         "test-model",
			Brief: protocol.Brief{
				Goal:        "produce report",
				Instruction: "write report",
				Outputs: []protocol.OutputSpec{{
					ArtifactID: "report",
					Kind:       "document",
				}},
			},
			Workdir:   workdir,
			OutputDir: outputDir,
			Grants:    protocol.Grants{},
			Budget:    protocol.Budget{RemainingMS: 60_000},
		},
		IntervalID:     "interval-1",
		IntervalOpened: time.Now().Add(-time.Millisecond),
		RecordIdentity: func(runstate.ProcessIdentity) (faultpoint.DurabilityReceipt, error) {
			*order = append(*order, "attempt.started")
			return receipt("attempt.started"), nil
		},
		Recorder: recorder,
	}
}

func successfulRecorder(order *[]string) ExecuteRecorder {
	return ExecuteRecorder{
		RecordProbe: func(protocol.ProbeResult) (faultpoint.DurabilityReceipt, error) {
			*order = append(*order, "adapter.probed")
			return receipt("adapter.probed"), nil
		},
		RecordArtifact: func(ArtifactObservation) (faultpoint.DurabilityReceipt, error) {
			*order = append(*order, "artifact.recorded")
			return receipt("artifact.recorded"), nil
		},
		RecordExecutionStopped: func(stop ExecutionStop) (faultpoint.DurabilityReceipt, error) {
			if stop.Reason != "normal" || stop.Charging != "measured" ||
				stop.ChargedDurationMS < 0 || stop.ObservedAt.IsZero() {
				return faultpoint.DurabilityReceipt{}, errors.New("invalid stop")
			}
			*order = append(*order, "execution.stopped")
			return receipt("execution.stopped"), nil
		},
		RecordOutcome: func(observation OutcomeObservation) (faultpoint.DurabilityReceipt, error) {
			*order = append(*order, observation.EventType)
			return receipt(observation.EventType), nil
		},
		ObserveLog:      func(protocol.LogEvent) {},
		ObserveProgress: func(protocol.ProgressEvent) {},
	}
}

func cancellingRecorder(order *[]string, cancel chan struct{}) ExecuteRecorder {
	recorder := successfulRecorder(order)
	recorder.ObserveLog = func(protocol.LogEvent) { close(cancel) }
	return recorder
}

func receipt(eventType string) faultpoint.DurabilityReceipt {
	return faultpoint.DurabilityReceipt{Mutation: faultpoint.Mutation{
		Kind:      faultpoint.JournalAppend,
		EventID:   "event-" + strings.ReplaceAll(eventType, ".", "-"),
		EventType: eventType,
		Sequence:  1,
		Timestamp: "2026-07-27T00:00:00Z",
		Path:      "/journal",
	}}
}
