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
	}, incidentalTestDeadline, 50*time.Millisecond)
	cancel := make(chan struct{})
	baseWrite := client.write
	client.write = func(writer io.Writer, data []byte) error {
		if strings.Contains(string(data), `"method":"execute"`) {
			close(cancel)
		}
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
		successfulRecorder(&order),
		&order,
	)
	plan.Cancel = cancel
	report, err := client.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result == nil || report.Result.Outcome != protocol.OutcomeCancelled {
		t.Fatalf("report = %#v", report)
	}
	want := []string{
		"attempt.started",
		"adapter.probed",
		"session.verified_empty",
		"execution.stopped",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("journal order = %v, want %v", order, want)
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
		if strings.Contains(string(data), `"method":"execute"`) {
			close(cancel)
		}
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
		successfulRecorder(&order),
		&order,
	)
	plan.Cancel = cancel
	report, err := client.Execute(context.Background(), plan)
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

func TestExecuteRejectsDuplicateCancelAcknowledgement(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_cancelled_duplicate_ack",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 50*time.Millisecond)
	cancel := make(chan struct{})
	baseWrite := client.write
	client.write = func(writer io.Writer, data []byte) error {
		if strings.Contains(string(data), `"method":"execute"`) {
			close(cancel)
		}
		return baseWrite(writer, data)
	}
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
	plan.Cancel = cancel
	report, err := client.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != nil || outcome.EventType != "attempt.failed" ||
		outcome.Result.Failure == nil || outcome.Result.Failure.Kind != protocol.FailureProtocolError ||
		outcome.FailureReason != "strict_decode_failed" {
		t.Fatalf("report = %#v, outcome = %#v", report, outcome)
	}
	// This fixture's second acknowledgement arrives after the execute response, so the
	// drain loop refuses it — not the in-flight duplicate guard. Both produce
	// strict_decode_failed, so the detail is what keeps the two apart.
	if outcome.Result.Failure.Detail != "frame followed execute response" {
		t.Fatalf("detail = %q", outcome.Result.Failure.Detail)
	}
}

// The in-flight guards below need acknowledgements that arrive *before* the execute
// response. The post-response fixtures cannot reach them: every frame after the response
// is handled by the drain loop instead.

func TestExecuteRejectsEarlyDuplicateCancelAcknowledgement(t *testing.T) {
	report, outcome := runCancelAckFixture(t, "execute_early_duplicate_cancel_ack", true)
	if report.Result != nil || outcome.EventType != "attempt.failed" ||
		outcome.Result.Failure == nil || outcome.Result.Failure.Kind != protocol.FailureProtocolError ||
		outcome.FailureReason != "strict_decode_failed" ||
		outcome.Result.Failure.Detail != "duplicate cancel acknowledgement" {
		t.Fatalf("report = %#v, outcome = %#v", report, outcome)
	}
}

// runCancelAckFixture drives one fake-adapter mode and returns what the core recorded.
// requestCancel issues the protocol cancel as the execute request goes out, so the
// acknowledgement the fixture writes afterwards is a solicited one.
func runCancelAckFixture(t *testing.T, mode string, requestCancel bool) (ExecuteReport, OutcomeObservation) {
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
	cancel := make(chan struct{})
	if requestCancel {
		baseWrite := client.write
		client.write = func(writer io.Writer, data []byte) error {
			if strings.Contains(string(data), `"method":"execute"`) {
				close(cancel)
			}
			return baseWrite(writer, data)
		}
	}
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
	if requestCancel {
		plan.Cancel = cancel
	}
	report, err := client.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	return report, outcome
}

func TestExecuteRejectsMissingCancelAcknowledgement(t *testing.T) {
	directory := t.TempDir()
	installFake(t, directory, "fake")
	var order []string
	client := newClient([]string{
		fakeModeEnv + "=execute_cancelled_without_ack",
		fakeMarkerEnv + "=" + filepath.Join(t.TempDir(), "response"),
	}, incidentalTestDeadline, 50*time.Millisecond)
	cancel := make(chan struct{})
	baseWrite := client.write
	client.write = func(writer io.Writer, data []byte) error {
		if strings.Contains(string(data), `"method":"execute"`) {
			close(cancel)
		}
		return baseWrite(writer, data)
	}
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
	plan.Cancel = cancel
	report, err := client.Execute(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if report.Result != nil || outcome.EventType != "attempt.failed" ||
		outcome.Result.Failure == nil || outcome.Result.Failure.Kind != protocol.FailureProtocolError ||
		outcome.FailureReason != "strict_decode_failed" {
		t.Fatalf("report = %#v, outcome = %#v", report, outcome)
	}
}

func TestExecuteRejectsUnsolicitedCancelAcknowledgement(t *testing.T) {
	report, outcome := runCancelAckFixture(t, "execute_early_unsolicited_cancel_ack", false)
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
			if err := newClient(nil, incidentalTestDeadline, time.Second).recordResult(&state, false); err != nil {
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

func (controller *recordingSessionController) verifyEmpty(sid int, leaderStart string) (bool, error) {
	return controller.base.verifyEmpty(sid, leaderStart)
}

func (controller *recordingSessionController) terminate(sid int, leaderStart string, grace time.Duration) error {
	controller.terminateFn()
	return controller.base.terminate(sid, leaderStart, grace)
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
