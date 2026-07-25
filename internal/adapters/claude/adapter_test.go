package claude

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

const helperEnv = "PARTITUR_CLAUDE_TEST_HELPER"

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
				assertFlagValue(t, command.args, "--model", "claude-sonnet-5")
				assertFlagValue(t, command.args, "--permission-mode", "dontAsk")
				assertFlagValue(t, command.args, "--setting-sources", "")
				for _, flag := range []string{"-p", "--verbose", "--safe-mode", "--strict-mcp-config", "--no-chrome", "--disable-slash-commands"} {
					if !slices.Contains(command.args, flag) {
						t.Errorf("missing flag %q in %#v", flag, command.args)
					}
				}

				settings := decodeSettings(t, flagValue(t, command.args, "--settings"))
				wantBase := []string{
					"Read",
					"Glob",
					"Grep",
					"Edit(/artifacts/**)",
					"Write(/artifacts/**)",
					"Edit(/workspace/src/**)",
					"Write(/workspace/src/**)",
					"Edit(/external/shared/**)",
					"Write(/external/shared/**)",
				}
				for _, permission := range wantBase {
					if !slices.Contains(settings.Permissions.Allow, permission) {
						t.Errorf("missing permission %q in %#v", permission, settings.Permissions.Allow)
					}
				}
				assertPermission(t, settings.Permissions.Allow, "Bash", shell)
				assertPermission(t, settings.Permissions.Allow, "WebFetch", network)
				assertPermission(t, settings.Permissions.Allow, "WebSearch", network)

				addDirs := allFlagValues(command.args, "--add-dir")
				wantDirs := []string{"/artifacts", "/external/shared", "/reference"}
				if !slices.Equal(addDirs, wantDirs) {
					t.Errorf("--add-dir values = %#v, want %#v", addDirs, wantDirs)
				}
			})
		}
	}
}

func TestBuildCommandResumeEffortAndUnknownExtension(t *testing.T) {
	t.Parallel()

	request := testRequest("/workspace", "/artifacts")
	request.SessionHint = json.RawMessage(`{"session_id":"session-123","ignored":true}`)
	request.Extensions = map[string]json.RawMessage{
		"claude": json.RawMessage(`{"effort":"high","future_field":42}`),
	}

	command, err := buildCommand(request, true)
	if err != nil {
		t.Fatal(err)
	}
	assertFlagValue(t, command.args, "--effort", "high")
	assertFlagValue(t, command.args, "--resume", "session-123")

	fresh, err := buildCommand(request, false)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(fresh.args, "--resume") {
		t.Fatalf("fresh retry contains --resume: %#v", fresh.args)
	}
}

func TestBuildCommandReadOnlyMovement(t *testing.T) {
	t.Parallel()

	request := testRequest("/workspace", "/artifacts")
	request.Grants = protocol.Grants{}
	command, err := buildCommand(request, true)
	if err != nil {
		t.Fatal(err)
	}
	settings := decodeSettings(t, flagValue(t, command.args, "--settings"))
	want := []string{
		"Read",
		"Glob",
		"Grep",
		"Edit(/artifacts/**)",
		"Write(/artifacts/**)",
	}
	if !slices.Equal(settings.Permissions.Allow, want) {
		t.Fatalf("permissions = %#v, want %#v", settings.Permissions.Allow, want)
	}
	if got := allFlagValues(command.args, "--add-dir"); !slices.Equal(got, []string{"/artifacts"}) {
		t.Fatalf("--add-dir values = %#v", got)
	}
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
				request.Extensions = map[string]json.RawMessage{"claude": test.extend}
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
		`{"type":"system","subtype":"init","session_id":"secret-session"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Working on it\nfull command should not be emitted"},{"type":"tool_use","name":"Read","input":{"file_path":"/private"}}]}}`,
		`not-json`,
		`{"type":"future","payload":"ignored"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done secret-session"}`,
	}
	for _, line := range lines {
		if err := state.consume([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	if state.sessionID != "secret-session" || !state.sawResult || state.resultIsError {
		t.Fatalf("unexpected state: %#v", state)
	}
	if state.detail != "done [REDACTED]" {
		t.Fatalf("detail = %q", state.detail)
	}
	if got := sink.progress; !slices.Equal(got, []string{"assistant: Working on it", "tool: Read"}) {
		t.Fatalf("progress = %#v", got)
	}
	if len(sink.logs) != 2 {
		t.Fatalf("logs = %#v", sink.logs)
	}
}

func TestFailureClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		evidence string
		want     protocol.FailureKind
	}{
		{name: "grant", evidence: "Permission denied by policy", want: protocol.FailureGrantDenied},
		{name: "auth", evidence: "Authentication failed", want: protocol.FailureAuthentication},
		{name: "rate", evidence: "HTTP 429 too many requests", want: protocol.FailureRateLimited},
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

	if got := classifyFailure("permission denied; authentication failed"); got != protocol.FailureGrantDenied {
		t.Fatalf("precedence = %q, want grant_denied", got)
	}
}

func TestProbePresentAndMissingBinary(t *testing.T) {
	t.Setenv(helperEnv, "1")
	t.Setenv("PARTITUR_CLAUDE_TEST_MODE", "version")
	t.Setenv(binaryEnv, helperBinary(t))

	result, err := New(io.Discard).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Protocol != protocol.ProtocolVersion || result.Adapter.ID != adapterID || result.Adapter.Version != "2.1.219" {
		t.Fatalf("unexpected probe result: %#v", result)
	}
	if result.Enforcement.PathGrants || result.Enforcement.ReadOnly || result.Enforcement.NetworkGrants ||
		result.Enforcement.ShellGrants || result.Enforcement.ReadGrants {
		t.Fatalf("dishonest enforcement: %#v", result.Enforcement)
	}
	if len(result.Capabilities.Models) != 4 {
		t.Fatalf("models = %#v", result.Capabilities.Models)
	}

	t.Setenv(binaryEnv, filepath.Join(t.TempDir(), "missing-claude"))
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
	configureHelper(t, "success", outputDir, "", "")

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
		{name: "grant denied", mode: "failure", evidence: "permission denied by policy", want: protocol.FailureGrantDenied},
		{name: "authentication", mode: "failure", evidence: "authentication failed", want: protocol.FailureAuthentication},
		{name: "rate limited", mode: "failure", evidence: "HTTP 429 too many requests", want: protocol.FailureRateLimited},
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
			t.Setenv("PARTITUR_CLAUDE_TEST_FAILURE", test.evidence)

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
	t.Setenv(binaryEnv, filepath.Join(t.TempDir(), "missing-claude"))
	result, err := New(io.Discard).Execute(context.Background(), testRequest(t.TempDir(), t.TempDir()), &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeFailed || result.Failure == nil ||
		result.Failure.Kind != protocol.FailureAdapterUnavailable {
		t.Fatalf("execute result = %#v", result)
	}
}

func TestExecuteCancelled(t *testing.T) {
	workdir := t.TempDir()
	outputDir := t.TempDir()
	configureHelper(t, "wait", outputDir, "", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := New(io.Discard).Execute(ctx, testRequest(workdir, outputDir), &recordingSink{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != protocol.OutcomeCancelled {
		t.Fatalf("execute result = %#v", result)
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
	assertFlagValue(t, resumed, "--resume", "stale-session")
	if slices.Contains(fresh, "--resume") {
		t.Fatalf("fresh retry contains --resume: %#v", fresh)
	}
	if !slices.Contains(sink.logs, "warn: Claude session hint was stale; retrying without it") {
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
		Model:         "claude-sonnet-5",
		Brief: protocol.Brief{
			Goal:        "Produce the requested output",
			Instruction: "Follow the brief",
			Outputs:     []protocol.OutputSpec{},
		},
		Workdir:   workdir,
		OutputDir: outputDir,
		Budget:    protocol.Budget{ActiveWallClockMin: 5.5},
	}
}

type recordingSink struct {
	mu       sync.Mutex
	logs     []string
	progress []string
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

func decodeSettings(t *testing.T, raw string) claudeSettings {
	t.Helper()
	var settings claudeSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatal(err)
	}
	return settings
}

func assertPermission(t *testing.T, permissions []string, permission string, want bool) {
	t.Helper()
	if got := slices.Contains(permissions, permission); got != want {
		t.Errorf("permission %q present = %t, want %t in %#v", permission, got, want, permissions)
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
	t.Setenv("PARTITUR_CLAUDE_TEST_MODE", mode)
	t.Setenv("PARTITUR_CLAUDE_TEST_OUTPUT", outputDir)
	t.Setenv("PARTITUR_CLAUDE_TEST_ARGS", argsFile)
	t.Setenv("PARTITUR_CLAUDE_TEST_PROMPT", promptFile)
	t.Setenv(binaryEnv, helperBinary(t))
}

func runHelper() int {
	mode := os.Getenv("PARTITUR_CLAUDE_TEST_MODE")
	if mode == "version" || slices.Contains(os.Args[1:], "--version") {
		fmt.Println("2.1.219 (Claude Code)")
		return 0
	}

	if path := os.Getenv("PARTITUR_CLAUDE_TEST_ARGS"); path != "" {
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
	if path := os.Getenv("PARTITUR_CLAUDE_TEST_PROMPT"); path != "" {
		if err := os.WriteFile(path, prompt, 0o600); err != nil {
			return 92
		}
	}

	if (mode == "stale-then-success" || mode == "stale-sensitive-then-success") && slices.Contains(os.Args[1:], "--resume") {
		message := "No conversation found for session"
		if mode == "stale-sensitive-then-success" {
			message += " future-session " + strings.Repeat("x", maxDiagnostic)
		}
		fmt.Fprintln(os.Stderr, message)
		return 1
	}
	if mode == "failure" {
		fmt.Fprintln(os.Stderr, os.Getenv("PARTITUR_CLAUDE_TEST_FAILURE"))
		return 1
	}
	if mode == "incompatible" {
		fmt.Println("not-json")
		return 1
	}
	if mode == "wait" {
		time.Sleep(30 * time.Second)
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
	fmt.Printf("{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":%q}\n", sessionID)
	fmt.Printf("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":%q},{\"type\":\"tool_use\",\"name\":\"Write\",\"input\":{\"command\":\"secret\"}}]}}\n", "working with "+sessionID)
	fmt.Printf("{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":%q}\n", "finished "+sessionID)

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
		if err := os.WriteFile(filepath.Join(os.Getenv("PARTITUR_CLAUDE_TEST_OUTPUT"), adapterkit.ResultFilename), []byte(envelope), 0o600); err != nil {
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
