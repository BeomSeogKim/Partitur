package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

const helperEnv = "PARTITUR_CODEX_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) != "" {
		os.Exit(runHelper())
	}
	os.Exit(m.Run())
}

func TestBuildCommandGrantCombinations(t *testing.T) {
	t.Parallel()

	for _, shell := range []bool{false, true} {
		for _, network := range []bool{false, true} {
			name := fmt.Sprintf("shell_%t_network_%t", shell, network)
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				request := testRequest("/workspace", "/artifacts")
				request.Grants = protocol.Grants{
					PathsRW: []string{"src/**", "/external/shared/**"},
					PathsRO: []string{"/reference/**"},
					Shell:   shell,
					Network: network,
				}

				command, err := buildCommand(request, true)
				if err != nil {
					t.Fatal(err)
				}
				if command.dir != "/workspace" {
					t.Fatalf("dir = %q", command.dir)
				}
				if len(command.args) == 0 || command.args[0] != "exec" || command.args[len(command.args)-1] != "-" {
					t.Fatalf("unexpected command framing: %#v", command.args)
				}
				assertFlagValue(t, command.args, "--model", "gpt-5.6-terra")
				assertFlagValue(t, command.args, "--sandbox", "workspace-write")
				assertFlagValue(t, command.args, "-C", "/workspace")
				for _, flag := range []string{"--json", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules"} {
					if !slices.Contains(command.args, flag) {
						t.Errorf("missing flag %q in %#v", flag, command.args)
					}
				}

				configs := allFlagValues(command.args, "-c")
				for _, config := range []string{
					"sandbox_workspace_write.exclude_tmpdir_env_var=true",
					"sandbox_workspace_write.exclude_slash_tmp=true",
				} {
					if !slices.Contains(configs, config) {
						t.Errorf("missing config %q in %#v", config, configs)
					}
				}
				assertConfig(t, configs, "features.shell_tool=false", !shell)
				if network {
					assertConfig(t, configs, "sandbox_workspace_write.network_access=true", true)
					assertConfig(t, configs, `web_search="live"`, true)
				} else {
					assertConfig(t, configs, "sandbox_workspace_write.network_access=false", true)
					assertConfig(t, configs, `web_search="disabled"`, true)
				}

				addDirs := allFlagValues(command.args, "--add-dir")
				wantDirs := []string{"/artifacts", "/external/shared"}
				if !slices.Equal(addDirs, wantDirs) {
					t.Errorf("--add-dir values = %#v, want %#v", addDirs, wantDirs)
				}
			})
		}
	}
}

func TestBuildCommandReadOnlyMovement(t *testing.T) {
	t.Parallel()

	request := testRequest("/workspace", "/artifacts")
	request.Grants.PathsRO = []string{"/reference/**"}
	command, err := buildCommand(request, true)
	if err != nil {
		t.Fatal(err)
	}
	if command.dir != "/artifacts" {
		t.Fatalf("dir = %q", command.dir)
	}
	assertFlagValue(t, command.args, "-C", "/artifacts")
	if slices.Contains(command.args, "--add-dir") {
		t.Fatalf("read-only movement contains --add-dir: %#v", command.args)
	}
	configs := allFlagValues(command.args, "-c")
	for _, want := range []string{
		"sandbox_workspace_write.exclude_tmpdir_env_var=true",
		"sandbox_workspace_write.exclude_slash_tmp=true",
		"sandbox_workspace_write.network_access=false",
		`web_search="disabled"`,
		"features.shell_tool=false",
	} {
		assertConfig(t, configs, want, true)
	}
}

func TestBuildCommandResumeEffortAndUnknownExtension(t *testing.T) {
	t.Parallel()

	request := testRequest("/workspace", "/artifacts")
	request.SessionHint = json.RawMessage(`{"session_id":"session-123","ignored":true}`)
	request.Extensions = map[string]json.RawMessage{
		"codex": json.RawMessage(`{"effort":"high","future_field":42}`),
	}

	command, err := buildCommand(request, true)
	if err != nil {
		t.Fatal(err)
	}
	configs := allFlagValues(command.args, "-c")
	assertConfig(t, configs, `model_reasoning_effort="high"`, true)
	resumeIndex := slices.Index(command.args, "resume")
	if resumeIndex < 0 || !slices.Equal(command.args[resumeIndex:], []string{"resume", "session-123", "-"}) {
		t.Fatalf("resume suffix = %#v", command.args)
	}
	for _, flag := range []string{"--json", "--model", "--sandbox", "-C", "--ignore-user-config", "--ignore-rules"} {
		if slices.Index(command.args, flag) > resumeIndex {
			t.Errorf("flag %q appears after resume in %#v", flag, command.args)
		}
	}

	fresh, err := buildCommand(request, false)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fresh.args, "resume") {
		t.Fatalf("fresh retry contains resume: %#v", fresh.args)
	}
}

func TestBuildCommandEscapesEffort(t *testing.T) {
	t.Parallel()

	request := testRequest("/workspace", "/artifacts")
	request.Extensions = map[string]json.RawMessage{
		"codex": json.RawMessage(`{"effort":"high\"value"}`),
	}
	command, err := buildCommand(request, true)
	if err != nil {
		t.Fatal(err)
	}
	assertConfig(t, allFlagValues(command.args, "-c"), `model_reasoning_effort="high\"value"`, true)
}

func TestBuildCommandRejectsMalformedKnownFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		hint   json.RawMessage
		extend json.RawMessage
	}{
		{name: "session hint", hint: json.RawMessage(`{"session_id":7}`)},
		{name: "effort", extend: json.RawMessage(`{"effort":7}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := testRequest("/workspace", "/artifacts")
			request.SessionHint = test.hint
			if test.extend != nil {
				request.Extensions = map[string]json.RawMessage{"codex": test.extend}
			}
			if _, err := buildCommand(request, true); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestStreamParsing(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	state := streamState{sink: sink}
	lines := []string{
		`{"type":"thread.started","thread_id":"secret-session"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"item-1","type":"command_execution","command":"env SECRET=value && rm -rf /private"}}`,
		`{"type":"item.completed","item":{"id":"item-2","type":"agent_message","text":"Working on it secret-session\nfull command should not be emitted"}}`,
		`not-json`,
		`{"type":"future.event","payload":"ignored"}`,
		`{"type":"turn.completed","usage":{"input_tokens":10}}`,
	}
	for _, line := range lines {
		if err := state.consume([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	if state.sessionID != "secret-session" || !state.sawResult || state.resultIsError {
		t.Fatalf("unexpected state: %#v", state)
	}
	if got := sink.progress; !slices.Equal(got, []string{"tool: command_execution", "assistant: Working on it [REDACTED]"}) {
		t.Fatalf("progress = %#v", got)
	}
	if len(sink.logs) != 2 {
		t.Fatalf("logs = %#v", sink.logs)
	}
	joined := strings.Join(sink.progress, "\n")
	if strings.Contains(joined, "SECRET") || strings.Contains(joined, "rm -rf") {
		t.Fatalf("command leaked into progress: %s", joined)
	}
}

func TestStreamFailureDetail(t *testing.T) {
	t.Parallel()

	sink := &recordingSink{}
	state := streamState{sink: sink}
	if err := state.consume([]byte(`{"type":"turn.failed","error":{"message":"request timed out"}}`)); err != nil {
		t.Fatal(err)
	}
	if !state.resultIsError || state.detail != "request timed out" {
		t.Fatalf("unexpected state: %#v", state)
	}
}

func TestFailureClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		evidence string
		want     protocol.FailureKind
	}{
		{name: "grant", evidence: "denied by sandbox policy", want: protocol.FailureGrantDenied},
		{name: "auth", evidence: "HTTP 401 unauthorized", want: protocol.FailureAuthentication},
		{name: "rate", evidence: "usage limit reached", want: protocol.FailureRateLimited},
		{name: "timeout", evidence: "request timed out", want: protocol.FailureProviderTimeout},
		{name: "model", evidence: "unknown model", want: protocol.FailureModelUnavailable},
		{name: "unknown", evidence: "something broke", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyFailure(strings.ToLower(test.evidence)); got != test.want {
				t.Fatalf("classifyFailure(%q) = %q, want %q", test.evidence, got, test.want)
			}
		})
	}
	if got := classifyFailure("denied by sandbox; unauthorized"); got != protocol.FailureGrantDenied {
		t.Fatalf("precedence = %q, want grant_denied", got)
	}
}

func TestProbePresentAndMissingBinary(t *testing.T) {
	t.Setenv(helperEnv, "1")
	t.Setenv("PARTITUR_CODEX_TEST_MODE", "version")
	t.Setenv(binaryEnv, helperBinary(t))

	result, err := New(io.Discard).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Protocol != protocol.ProtocolVersion || result.Adapter.ID != adapterID || result.Adapter.Version != "0.144.4" {
		t.Fatalf("unexpected probe result: %#v", result)
	}
	if result.Enforcement.PathGrants || !result.Enforcement.ReadOnly || !result.Enforcement.NetworkGrants ||
		result.Enforcement.ShellGrants || result.Enforcement.ReadGrants {
		t.Fatalf("unexpected enforcement: %#v", result.Enforcement)
	}
	if len(result.Capabilities.Models) != 2 {
		t.Fatalf("models = %#v", result.Capabilities.Models)
	}

	t.Setenv(binaryEnv, filepath.Join(t.TempDir(), "missing-codex"))
	if _, err := New(io.Discard).Probe(context.Background()); err == nil {
		t.Fatal("missing binary probe succeeded")
	}
}

func TestDiagnosticWriterBuffersSanitizesAndBounds(t *testing.T) {
	var target bytes.Buffer
	writer := newDiagnosticWriter(&target)
	input := []byte("before late-session\n" + strings.Repeat("x", maxDiagnostic*2))

	written, err := writer.Write(input)
	if err != nil {
		t.Fatal(err)
	}
	if written != len(input) {
		t.Fatalf("written = %d, want %d", written, len(input))
	}
	if target.Len() != 0 {
		t.Fatalf("diagnostics persisted before sanitization: %q", target.String())
	}
	if err := writer.Flush("late-session"); err != nil {
		t.Fatal(err)
	}
	if target.Len() > maxDiagnostic {
		t.Fatalf("persisted diagnostic size = %d", target.Len())
	}
	if strings.Contains(target.String(), "late-session") || !strings.Contains(target.String(), "[REDACTED]") {
		t.Fatalf("diagnostic was not sanitized: %q", target.String())
	}
}

func TestExecuteAgainstHelperCLI(t *testing.T) {
	workdir := t.TempDir()
	outputDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args.json")
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	configureHelper(t, "success", outputDir, argsFile, promptFile)

	request := testRequest(workdir, outputDir)
	request.Grants.PathsRW = []string{"src/**"}
	request.Grants.Shell = true
	request.Grants.Network = true
	sink := &recordingSink{}
	result, err := New(io.Discard).Execute(context.Background(), request, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeCompleted || result.Detail != "helper complete" {
		t.Fatalf("execute result = %#v", result)
	}
	if string(result.SessionHint) != `{"session_id":"helper-session"}` {
		t.Fatalf("session hint = %s", result.SessionHint)
	}
	if len(sink.progress) != 2 {
		t.Fatalf("progress = %#v", sink.progress)
	}

	var args []string
	readJSONFile(t, argsFile, &args)
	assertFlagValue(t, args, "--model", request.Model)
	if args[len(args)-1] != "-" {
		t.Fatalf("prompt stdin marker missing: %#v", args)
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(prompt, []byte(request.Brief.Goal)) || !bytes.Contains(prompt, []byte(adapterkit.ResultFilename)) {
		t.Fatalf("rendered prompt missing contract: %s", prompt)
	}
}

func TestServeExecuteRoundTrip(t *testing.T) {
	workdir := t.TempDir()
	outputDir := t.TempDir()
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	configureHelper(t, "success", outputDir, "", promptFile)

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- adapterkit.Serve(New(io.Discard), stdinReader, stdoutWriter, io.Discard)
	}()

	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "execute-1",
		"method":  "execute",
		"params":  testRequest(workdir, outputDir),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(frame, []byte(`"budget":{"remaining_ms":330001}`)) ||
		bytes.Contains(frame, []byte("active_wall_clock_min")) {
		t.Fatalf("wire budget = %s", frame)
	}
	if _, err := stdinWriter.Write(append(frame, '\n')); err != nil {
		t.Fatal(err)
	}

	type wireResult struct {
		events int
		result protocol.ExecuteResult
		err    error
	}
	wireDone := make(chan wireResult, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutReader)
		var observed wireResult
		for scanner.Scan() {
			var message struct {
				ID     json.RawMessage         `json:"id"`
				Method string                  `json:"method"`
				Result *protocol.ExecuteResult `json:"result"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				observed.err = err
				break
			}
			if message.Method == "event" {
				observed.events++
			}
			if string(message.ID) == `"execute-1"` && message.Result != nil {
				observed.result = *message.Result
				wireDone <- observed
				return
			}
		}
		if observed.err == nil {
			observed.err = scanner.Err()
		}
		wireDone <- observed
	}()

	var observed wireResult
	select {
	case observed = <-wireDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for execute response")
	}
	if observed.err != nil {
		t.Fatal(observed.err)
	}
	if observed.events != 2 || observed.result.Outcome != protocol.OutcomeCompleted {
		t.Fatalf("wire result = %#v", observed)
	}
	if err := stdinWriter.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for adapter shutdown")
	}
	prompt, err := os.ReadFile(promptFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(prompt, []byte("330001 milliseconds")) {
		t.Fatalf("prompt budget = %s", prompt)
	}
}

func TestExecuteMissingEnvelopeIsTaskFailed(t *testing.T) {
	workdir := t.TempDir()
	outputDir := t.TempDir()
	configureHelper(t, "no-envelope", outputDir, "", "")

	result, err := New(io.Discard).Execute(context.Background(), testRequest(workdir, outputDir), &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeFailed || result.Failure == nil ||
		result.Failure.Kind != protocol.FailureTaskFailed ||
		result.Failure.Detail != "result envelope missing" {
		t.Fatalf("execute result = %#v", result)
	}
}

func TestExecuteFailureFixtures(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		evidence string
		want     protocol.FailureKind
	}{
		{name: "grant denied", mode: "failure", evidence: "denied by sandbox policy", want: protocol.FailureGrantDenied},
		{name: "authentication", mode: "failure", evidence: "HTTP 401 unauthorized", want: protocol.FailureAuthentication},
		{name: "rate limited", mode: "failure", evidence: "usage limit reached", want: protocol.FailureRateLimited},
		{name: "provider timeout", mode: "failure", evidence: "request timed out", want: protocol.FailureProviderTimeout},
		{name: "model unavailable", mode: "failure", evidence: "unknown model", want: protocol.FailureModelUnavailable},
		{name: "unknown failure", mode: "failure", evidence: "vendor stopped", want: protocol.FailureTaskFailed},
		{name: "incompatible stream", mode: "incompatible", evidence: "", want: protocol.FailureProtocolError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workdir := t.TempDir()
			outputDir := t.TempDir()
			configureHelper(t, test.mode, outputDir, "", "")
			t.Setenv("PARTITUR_CODEX_TEST_FAILURE", test.evidence)

			result, err := New(io.Discard).Execute(context.Background(), testRequest(workdir, outputDir), &recordingSink{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != protocol.OutcomeFailed || result.Failure == nil || result.Failure.Kind != test.want {
				t.Fatalf("execute result = %#v, want %q", result, test.want)
			}
		})
	}
}

func TestExecuteMissingBinary(t *testing.T) {
	t.Setenv(binaryEnv, filepath.Join(t.TempDir(), "missing-codex"))
	result, err := New(io.Discard).Execute(context.Background(), testRequest(t.TempDir(), t.TempDir()), &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeFailed || result.Failure == nil ||
		result.Failure.Kind != protocol.FailureAdapterUnavailable {
		t.Fatalf("execute result = %#v", result)
	}
}

func TestExecuteCancelledDuringInvocation(t *testing.T) {
	workdir := t.TempDir()
	outputDir := t.TempDir()
	configureHelper(t, "wait", outputDir, "", "")
	sink := &recordingSink{progressCh: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan *protocol.ExecuteResult, 1)
	errorCh := make(chan error, 1)
	go func() {
		result, err := New(io.Discard).Execute(ctx, testRequest(workdir, outputDir), sink)
		if err != nil {
			errorCh <- err
			return
		}
		resultCh <- result
	}()

	select {
	case <-sink.progressCh:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for helper process")
	}
	select {
	case err := <-errorCh:
		t.Fatal(err)
	case result := <-resultCh:
		if result.Outcome != protocol.OutcomeCancelled {
			t.Fatalf("execute result = %#v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for cancellation")
	}
}

func TestExecuteStaleResumeRetriesFresh(t *testing.T) {
	workdir := t.TempDir()
	outputDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "args.jsonl")
	configureHelper(t, "stale-then-success", outputDir, argsFile, "")

	request := testRequest(workdir, outputDir)
	request.SessionHint = json.RawMessage(`{"session_id":"stale-session"}`)
	sink := &recordingSink{}
	result, err := New(io.Discard).Execute(context.Background(), request, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeCompleted {
		t.Fatalf("execute result = %#v", result)
	}
	lines := readLines(t, argsFile)
	if len(lines) != 2 {
		t.Fatalf("invocations = %d, want 2: %#v", len(lines), lines)
	}
	var resumed, fresh []string
	if err := json.Unmarshal([]byte(lines[0]), &resumed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &fresh); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(resumed, "resume") || !slices.Contains(resumed, "stale-session") {
		t.Fatalf("resumed argv = %#v", resumed)
	}
	if slices.Contains(fresh, "resume") {
		t.Fatalf("fresh retry contains resume: %#v", fresh)
	}
	if !slices.Contains(sink.logs, "warn: Codex session hint was stale; retrying without it") {
		t.Fatalf("logs = %#v", sink.logs)
	}
}

func TestRetryDiagnosticsAreAttemptBoundedAndSanitizedOnce(t *testing.T) {
	workdir := t.TempDir()
	outputDir := t.TempDir()
	configureHelper(t, "stale-sensitive-then-success", outputDir, "", "")

	request := testRequest(workdir, outputDir)
	request.SessionHint = json.RawMessage(`{"session_id":"stale-session"}`)
	var diagnostics bytes.Buffer
	result, err := New(&diagnostics).Execute(context.Background(), request, &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeCompleted {
		t.Fatalf("execute result = %#v", result)
	}
	if diagnostics.Len() > maxDiagnostic {
		t.Fatalf("attempt diagnostics size = %d", diagnostics.Len())
	}
	if strings.Contains(diagnostics.String(), "future-session") || !strings.Contains(diagnostics.String(), "[REDACTED]") {
		t.Fatalf("retry diagnostics were not sanitized with final session values: %q", diagnostics.String())
	}
}

func TestCapturedSessionIsRedacted(t *testing.T) {
	workdir := t.TempDir()
	outputDir := t.TempDir()
	configureHelper(t, "sensitive", outputDir, "", "")

	sink := &recordingSink{}
	var diagnostics bytes.Buffer
	result, err := New(&diagnostics).Execute(context.Background(), testRequest(workdir, outputDir), sink)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(struct {
		Progress []string          `json:"progress"`
		Logs     []string          `json:"logs"`
		Failure  *protocol.Failure `json:"failure"`
		Detail   string            `json:"detail"`
	}{sink.progress, sink.logs, result.Failure, result.Detail})
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "sensitive-session") {
		t.Fatalf("session leaked outside session_hint: %s", text)
	}
	if !strings.Contains(string(result.SessionHint), "sensitive-session") {
		t.Fatalf("session hint missing captured ID: %s", result.SessionHint)
	}
	if strings.Contains(diagnostics.String(), "sensitive-session") || !strings.Contains(diagnostics.String(), "[REDACTED]") {
		t.Fatalf("session leaked through diagnostics: %q", diagnostics.String())
	}
}

func testRequest(workdir, outputDir string) *protocol.ExecuteRequest {
	return &protocol.ExecuteRequest{
		RunID:         "run-1",
		MovementID:    "movement-1",
		AttemptID:     "attempt-1",
		ScoreRevision: 1,
		Model:         "gpt-5.6-terra",
		Brief: protocol.Brief{
			Goal:        "Produce the requested output",
			Instruction: "Follow the brief",
			Outputs:     []protocol.OutputSpec{},
		},
		Workdir:   workdir,
		OutputDir: outputDir,
		Budget:    protocol.Budget{RemainingMS: 330_001},
	}
}

type recordingSink struct {
	mu         sync.Mutex
	logs       []string
	progress   []string
	progressCh chan struct{}
}

func (s *recordingSink) Log(level, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, level+": "+message)
	return nil
}

func (s *recordingSink) Progress(message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = append(s.progress, message)
	if s.progressCh != nil {
		select {
		case s.progressCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (s *recordingSink) Artifact(string, string) error {
	return nil
}

func (s *recordingSink) Proposal(string, json.RawMessage, bool) error {
	return nil
}

func (s *recordingSink) Question(string, string) error {
	return nil
}

func assertConfig(t *testing.T, configs []string, config string, want bool) {
	t.Helper()
	if got := slices.Contains(configs, config); got != want {
		t.Errorf("config %q present = %t, want %t in %#v", config, got, want, configs)
	}
}

func assertFlagValue(t *testing.T, args []string, flag, want string) {
	t.Helper()
	if got := flagValue(t, args, flag); got != want {
		t.Errorf("%s = %q, want %q", flag, got, want)
	}
}

func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for index := 0; index < len(args); index++ {
		if args[index] != flag {
			continue
		}
		if index+1 >= len(args) {
			t.Fatalf("%s has no value in %#v", flag, args)
		}
		return args[index+1]
	}
	t.Fatalf("missing %s in %#v", flag, args)
	return ""
}

func allFlagValues(args []string, flag string) []string {
	var values []string
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			values = append(values, args[index+1])
			index++
		}
	}
	return values
}

func helperBinary(t *testing.T) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

func configureHelper(t *testing.T, mode, outputDir, argsFile, promptFile string) {
	t.Helper()
	t.Setenv(helperEnv, "1")
	t.Setenv("PARTITUR_CODEX_TEST_MODE", mode)
	t.Setenv("PARTITUR_CODEX_TEST_OUTPUT", outputDir)
	t.Setenv("PARTITUR_CODEX_TEST_ARGS", argsFile)
	t.Setenv("PARTITUR_CODEX_TEST_PROMPT", promptFile)
	t.Setenv(binaryEnv, helperBinary(t))
}

func runHelper() int {
	mode := os.Getenv("PARTITUR_CODEX_TEST_MODE")
	if mode == "version" || slices.Contains(os.Args[1:], "--version") {
		fmt.Println("codex-cli 0.144.4")
		return 0
	}

	if path := os.Getenv("PARTITUR_CODEX_TEST_ARGS"); path != "" {
		encoded, _ := json.Marshal(os.Args[1:])
		if mode == "stale-then-success" {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				return 90
			}
			_, _ = file.Write(append(encoded, '\n'))
			_ = file.Close()
		} else if err := os.WriteFile(path, encoded, 0o600); err != nil {
			return 91
		}
	}
	prompt, _ := io.ReadAll(os.Stdin)
	if path := os.Getenv("PARTITUR_CODEX_TEST_PROMPT"); path != "" {
		if err := os.WriteFile(path, prompt, 0o600); err != nil {
			return 92
		}
	}

	if (mode == "stale-then-success" || mode == "stale-sensitive-then-success") && slices.Contains(os.Args[1:], "resume") {
		message := "Thread not found"
		if mode == "stale-sensitive-then-success" {
			message += " future-session " + strings.Repeat("x", maxDiagnostic)
		}
		fmt.Fprintln(os.Stderr, message)
		return 1
	}
	if mode == "failure" {
		fmt.Fprintln(os.Stderr, os.Getenv("PARTITUR_CODEX_TEST_FAILURE"))
		return 1
	}
	if mode == "incompatible" {
		fmt.Println("not-json")
		return 1
	}

	sessionID := "helper-session"
	if mode == "sensitive" {
		sessionID = "sensitive-session"
		fmt.Fprintln(os.Stderr, "diagnostic before session discovery: "+sessionID)
	}
	if mode == "stale-sensitive-then-success" {
		sessionID = "future-session"
		fmt.Fprintln(os.Stderr, strings.Repeat("y", maxDiagnostic))
	}
	fmt.Printf("{\"type\":\"thread.started\",\"thread_id\":%q}\n", sessionID)
	fmt.Println(`{"type":"turn.started"}`)
	fmt.Println(`{"type":"item.started","item":{"id":"item-1","type":"command_execution","command":"env SECRET=value"}}`)
	if mode == "wait" {
		time.Sleep(30 * time.Second)
	}
	fmt.Printf("{\"type\":\"item.completed\",\"item\":{\"id\":\"item-2\",\"type\":\"agent_message\",\"text\":%q}}\n", "working with "+sessionID)
	fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":5}}`)

	if mode != "no-envelope" {
		summary := "helper complete"
		if mode == "sensitive" {
			summary = "summary sensitive-session"
		}
		envelope, _ := json.Marshal(map[string]any{
			"version":   1,
			"artifacts": []any{},
			"questions": []any{},
			"proposal":  nil,
			"summary":   summary,
		})
		if err := os.WriteFile(filepath.Join(os.Getenv("PARTITUR_CODEX_TEST_OUTPUT"), adapterkit.ResultFilename), envelope, 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 93
		}
	}
	return 0
}

func readJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}
