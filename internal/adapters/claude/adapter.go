package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

const (
	adapterID        = "claude"
	binaryEnv        = "PARTITUR_CLAUDE_BIN"
	probeTimeout     = 10 * time.Second
	maxDiagnostic    = adapterkit.MaxDiagnosticBytes
	maxFailureDetail = 512
)

var versionPattern = regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?\b`)

// Adapter invokes Claude Code as a Partitur adapter.
type Adapter struct {
	stderr io.Writer
}

// New constructs a Claude adapter. Claude inherits the parent environment
// because its OAuth, keychain, HOME, and provider configuration are external to
// the adapter; this credential inheritance is an unavoidable trust boundary.
func New(stderr io.Writer) *Adapter {
	if stderr == nil {
		stderr = io.Discard
	}
	return &Adapter{stderr: stderr}
}

func (a *Adapter) Probe(ctx context.Context) (*protocol.ProbeResult, error) {
	binary, err := resolveBinary()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	var output bytes.Buffer
	diagnostic := newDiagnosticWriter(a.stderr)
	_, err = adapterkit.RunProcess(ctx, adapterkit.ProcessSpec{
		Path:   binary,
		Args:   []string{"--version"},
		Env:    os.Environ(),
		Stderr: diagnostic,
	}, func(line []byte) error {
		if output.Len() < maxDiagnostic {
			remaining := maxDiagnostic - output.Len()
			if len(line) > remaining {
				line = line[:remaining]
			}
			output.Write(line)
			output.WriteByte('\n')
		}
		return nil
	})
	if flushErr := diagnostic.Flush(); err == nil {
		err = flushErr
	}
	if err != nil {
		return nil, fmt.Errorf("probe Claude CLI: %w", err)
	}

	version := versionPattern.FindString(output.String())
	if version == "" {
		version = versionPattern.FindString(diagnostic.String())
	}
	if version == "" {
		return nil, errors.New("probe Claude CLI: version token not found")
	}

	return &protocol.ProbeResult{
		Protocol: protocol.ProtocolVersion,
		Adapter: protocol.AdapterIdentity{
			ID:      adapterID,
			Version: version,
		},
		Capabilities: protocol.Capabilities{
			RepoRead:          true,
			RepoWrite:         true,
			Shell:             true,
			Network:           true,
			ResumableSessions: true,
			// These are IDs this adapter version is known to speak, not a
			// claim that the current account can access every model.
			Models: []protocol.Model{
				{ID: "claude-fable-5", Aliases: []string{"fable"}},
				{ID: "claude-opus-5", Aliases: []string{"opus"}},
				{ID: "claude-sonnet-5", Aliases: []string{"sonnet"}},
				{ID: "claude-haiku-4-5", Aliases: []string{"haiku"}},
			},
		},
		// Claude's own permission engine is advisory here: reads are
		// unconfined, shell access defeats path confinement, and some ambient
		// integrations may remain available despite best-effort isolation.
		Enforcement: protocol.Enforcement{
			PathGrants:    false,
			ReadOnly:      false,
			NetworkGrants: false,
			ShellGrants:   false,
			ReadGrants:    false,
		},
	}, nil
}

func (a *Adapter) Execute(ctx context.Context, request *protocol.ExecuteRequest, sink adapterkit.EventSink) (result *protocol.ExecuteResult, err error) {
	if ctx.Err() != nil {
		return cancelled(), nil
	}
	binary, err := resolveBinary()
	if err != nil {
		return failed(protocol.FailureAdapterUnavailable, "Claude CLI is unavailable"), nil
	}

	command, err := buildCommand(request, true)
	if err != nil {
		return failed(protocol.FailureProtocolError, err.Error()), nil
	}

	diagnostic := newDiagnosticWriter(a.stderr)
	var sensitive []string
	defer func() {
		if flushErr := diagnostic.Flush(sensitive...); err == nil {
			err = flushErr
		}
	}()

	invocation := a.invoke(ctx, binary, command, sink, diagnostic)
	sensitive = unique(append(sensitive, invocation.stream.sensitive...))
	if ctx.Err() != nil {
		return withSession(cancelled(), invocation.stream.sessionID), nil
	}

	if command.resumeID != "" && invocation.staleSession() {
		if err := sink.Log("warn", "Claude session hint was stale; retrying without it"); err != nil {
			return failed(protocol.FailureProtocolError, "emit retry event"), nil
		}
		command, err = buildCommand(request, false)
		if err != nil {
			return failed(protocol.FailureProtocolError, err.Error()), nil
		}
		invocation = a.invoke(ctx, binary, command, sink, diagnostic)
		sensitive = unique(append(sensitive, invocation.stream.sensitive...))
		if ctx.Err() != nil {
			return withSession(cancelled(), invocation.stream.sessionID), nil
		}
	}

	result = invocation.result()
	if result == nil {
		result = adapterkit.CollectResult(request.OutputDir, sessionRedactingSink{
			EventSink: sink,
			sensitive: invocation.stream.sensitive,
		})
	}
	return withSession(result, invocation.stream.sessionID, invocation.stream.sensitive...), nil
}

func (a *Adapter) invoke(ctx context.Context, binary string, command commandSpec, sink adapterkit.EventSink, diagnostic *diagnosticWriter) invocationResult {
	state := streamState{sink: sink}
	if command.resumeID != "" {
		state.sensitive = []string{command.resumeID}
	}
	diagnosticStart := len(diagnostic.data)
	process, err := adapterkit.RunProcess(ctx, adapterkit.ProcessSpec{
		Path:   binary,
		Args:   command.args,
		Dir:    command.dir,
		Env:    os.Environ(),
		Stdin:  strings.NewReader(command.prompt),
		Stderr: diagnostic,
	}, state.consume)
	return invocationResult{
		process: process,
		err:     err,
		stderr:  string(diagnostic.data[diagnosticStart:]),
		stream:  state,
	}
}

func resolveBinary() (string, error) {
	binary := os.Getenv(binaryEnv)
	if binary == "" {
		binary = "claude"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("resolve Claude CLI %q: %w", binary, err)
	}
	return resolved, nil
}

func failed(kind protocol.FailureKind, detail string) *protocol.ExecuteResult {
	return &protocol.ExecuteResult{
		Outcome: protocol.OutcomeFailed,
		Failure: &protocol.Failure{
			Kind:   kind,
			Detail: adapterkit.SanitizeMessage(detail),
		},
	}
}

func cancelled() *protocol.ExecuteResult {
	return &protocol.ExecuteResult{Outcome: protocol.OutcomeCancelled}
}

func withSession(result *protocol.ExecuteResult, sessionID string, sensitive ...string) *protocol.ExecuteResult {
	if result == nil {
		return result
	}
	result.Detail = adapterkit.SanitizeMessage(result.Detail, sensitive...)
	if result.Failure != nil {
		result.Failure.Detail = adapterkit.SanitizeMessage(result.Failure.Detail, sensitive...)
	}
	for index, id := range result.PendingDecisionIDs {
		result.PendingDecisionIDs[index] = redactSensitive(id, sensitive)
	}
	if sessionID != "" {
		hint, err := json.Marshal(struct {
			SessionID string `json:"session_id"`
		}{SessionID: sessionID})
		if err == nil {
			result.SessionHint = hint
		}
	}
	return result
}

type diagnosticWriter struct {
	target io.Writer
	data   []byte
}

func newDiagnosticWriter(target io.Writer) *diagnosticWriter {
	if target == nil {
		target = io.Discard
	}
	return &diagnosticWriter{target: target}
}

func (w *diagnosticWriter) Write(p []byte) (int, error) {
	if remaining := maxDiagnostic - len(w.data); remaining > 0 {
		if len(p) > remaining {
			w.data = append(w.data, p[:remaining]...)
		} else {
			w.data = append(w.data, p...)
		}
	}
	return len(p), nil
}

func (w *diagnosticWriter) Flush(sensitive ...string) error {
	sanitized := adapterkit.SanitizeDiagnostic(string(w.data), sensitive...)
	written, err := io.WriteString(w.target, sanitized)
	if err == nil && written != len(sanitized) {
		return io.ErrShortWrite
	}
	return err
}

func (w *diagnosticWriter) String() string {
	return string(w.data)
}

type commandSpec struct {
	args     []string
	dir      string
	prompt   string
	resumeID string
}

type claudeSettings struct {
	Permissions claudePermissions `json:"permissions"`
}

type claudePermissions struct {
	Allow []string `json:"allow"`
}

func buildCommand(request *protocol.ExecuteRequest, includeResume bool) (commandSpec, error) {
	if request == nil {
		return commandSpec{}, errors.New("execute request is required")
	}
	if strings.TrimSpace(request.Workdir) == "" || strings.TrimSpace(request.OutputDir) == "" {
		return commandSpec{}, errors.New("workdir and output_dir are required")
	}

	settings, err := buildSettings(request)
	if err != nil {
		return commandSpec{}, err
	}
	effort, err := parseEffort(request.Extensions[adapterID])
	if err != nil {
		return commandSpec{}, err
	}
	resumeID, err := parseSessionHint(request.SessionHint)
	if err != nil {
		return commandSpec{}, err
	}
	if !includeResume {
		resumeID = ""
	}

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--model", request.Model,
		"--permission-mode", "dontAsk",
		"--setting-sources", "",
		"--safe-mode",
		"--strict-mcp-config",
		"--no-chrome",
		"--disable-slash-commands",
		"--settings", string(settings),
	}
	// Empty setting sources and safe mode suppress user/project settings,
	// hooks, plugins, MCP servers, and related ambient customizations where
	// Claude permits it. Managed policy and inherited credentials may remain.
	for _, directory := range addDirectories(request) {
		args = append(args, "--add-dir", directory)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}

	return commandSpec{
		args:     args,
		dir:      request.Workdir,
		prompt:   adapterkit.RenderPrompt(request),
		resumeID: resumeID,
	}, nil
}

func buildSettings(request *protocol.ExecuteRequest) ([]byte, error) {
	allow := []string{"Read", "Glob", "Grep"}
	output, err := absolutePattern(request.Workdir, filepath.Join(request.OutputDir, "**"))
	if err != nil {
		return nil, fmt.Errorf("resolve output grant: %w", err)
	}
	allow = append(allow, "Edit("+output+")", "Write("+output+")")
	for _, pattern := range request.Grants.PathsRW {
		resolved, err := absolutePattern(request.Workdir, pattern)
		if err != nil {
			return nil, fmt.Errorf("resolve write grant %q: %w", pattern, err)
		}
		allow = append(allow, "Edit("+resolved+")", "Write("+resolved+")")
	}
	if request.Grants.Shell {
		allow = append(allow, "Bash")
	}
	if request.Grants.Network {
		// Network grants govern data-plane tools only. Claude's provider API
		// traffic remains available so the invocation can function.
		allow = append(allow, "WebFetch", "WebSearch")
	}
	return json.Marshal(claudeSettings{Permissions: claudePermissions{Allow: unique(allow)}})
}

func absolutePattern(workdir, pattern string) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", errors.New("empty path pattern")
	}
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(workdir, pattern)
	}
	return filepath.Clean(pattern), nil
}

func addDirectories(request *protocol.ExecuteRequest) []string {
	directories := []string{filepath.Clean(request.OutputDir)}
	grants := append(append([]string(nil), request.Grants.PathsRW...), request.Grants.PathsRO...)
	for _, pattern := range grants {
		resolved, err := absolutePattern(request.Workdir, pattern)
		if err != nil {
			continue
		}
		directory := directoryFromPattern(resolved)
		if directory != "" && outside(request.Workdir, directory) {
			directories = append(directories, directory)
		}
	}
	return unique(directories)
}

func directoryFromPattern(pattern string) string {
	index := strings.IndexAny(pattern, "*?[")
	if index < 0 {
		if info, err := os.Stat(pattern); err == nil && info.IsDir() {
			return filepath.Clean(pattern)
		}
		return filepath.Dir(pattern)
	}
	prefix := pattern[:index]
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix = filepath.Dir(prefix)
	}
	prefix = strings.TrimRight(prefix, string(filepath.Separator))
	if prefix == "" {
		return string(filepath.Separator)
	}
	return filepath.Clean(prefix)
}

func outside(workdir, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(workdir), filepath.Clean(path))
	return err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func parseEffort(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", nil
	}
	var extension map[string]json.RawMessage
	if err := json.Unmarshal(raw, &extension); err != nil {
		return "", errors.New("extensions.claude must be an object")
	}
	// Extension fields are forward-compatible: this adapter consumes effort
	// and intentionally ignores fields it does not understand.
	effortRaw, exists := extension["effort"]
	if !exists {
		return "", nil
	}
	var effort string
	if err := json.Unmarshal(effortRaw, &effort); err != nil {
		return "", errors.New("extensions.claude.effort must be a string")
	}
	return effort, nil
}

func parseSessionHint(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var hint struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &hint); err != nil {
		return "", errors.New("session_hint must contain a string session_id")
	}
	if strings.TrimSpace(hint.SessionID) == "" {
		return "", errors.New("session_hint.session_id must not be empty")
	}
	return hint.SessionID, nil
}
