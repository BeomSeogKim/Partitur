package codex

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
	adapterID        = "codex"
	binaryEnv        = "PARTITUR_CODEX_BIN"
	probeTimeout     = 10 * time.Second
	maxDiagnostic    = 64 << 10
	maxFailureDetail = 512
)

var versionPattern = regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?\b`)

// Adapter invokes Codex CLI as a Partitur adapter.
type Adapter struct {
	stderr io.Writer
}

// New constructs a Codex adapter. Codex inherits the parent environment
// because CODEX_HOME and provider credentials are external to the adapter;
// this credential inheritance is an unavoidable trust boundary.
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
	if err != nil {
		return nil, fmt.Errorf("probe Codex CLI: %w", err)
	}

	version := versionPattern.FindString(output.String())
	if version == "" {
		version = versionPattern.FindString(diagnostic.String())
	}
	if version == "" {
		return nil, errors.New("probe Codex CLI: version token not found")
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
				{ID: "gpt-5.6-sol", Aliases: []string{"sol"}},
				{ID: "gpt-5.6-terra", Aliases: []string{"terra"}},
			},
		},
		// Read-only movements keep the repository outside the writable
		// workspace. Network is disabled in both command and web-search paths.
		// Path grants remain advisory because writable roots are directory-grain
		// while Partitur grants may be glob-grain.
		Enforcement: protocol.Enforcement{
			PathGrants:    false,
			ReadOnly:      true,
			NetworkGrants: true,
		},
	}, nil
}

func (a *Adapter) Execute(ctx context.Context, request *protocol.ExecuteRequest, sink adapterkit.EventSink) (*protocol.ExecuteResult, error) {
	if ctx.Err() != nil {
		return cancelled(), nil
	}
	binary, err := resolveBinary()
	if err != nil {
		return failed(protocol.FailureAdapterUnavailable, "Codex CLI is unavailable"), nil
	}

	command, err := buildCommand(request, true)
	if err != nil {
		return failed(protocol.FailureProtocolError, err.Error()), nil
	}

	invocation := a.invoke(ctx, binary, command, sink)
	if ctx.Err() != nil {
		return withSession(cancelled(), invocation.stream.sessionID), nil
	}

	if command.resumeID != "" && invocation.staleSession() {
		if err := sink.Log("warn", "Codex session hint was stale; retrying without it"); err != nil {
			return failed(protocol.FailureProtocolError, "emit retry event"), nil
		}
		command, err = buildCommand(request, false)
		if err != nil {
			return failed(protocol.FailureProtocolError, err.Error()), nil
		}
		invocation = a.invoke(ctx, binary, command, sink)
		if ctx.Err() != nil {
			return withSession(cancelled(), invocation.stream.sessionID), nil
		}
	}

	result := invocation.result()
	if result == nil {
		result = adapterkit.CollectResult(request.OutputDir, sessionRedactingSink{
			EventSink: sink,
			sensitive: invocation.stream.sensitive,
		})
	}
	return withSession(result, invocation.stream.sessionID, invocation.stream.sensitive...), nil
}

func (a *Adapter) invoke(ctx context.Context, binary string, command commandSpec, sink adapterkit.EventSink) invocationResult {
	state := streamState{sink: sink}
	if command.resumeID != "" {
		state.sensitive = []string{command.resumeID}
	}
	diagnostic := newDiagnosticWriter(a.stderr)
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
		stderr:  diagnostic.String(),
		stream:  state,
	}
}

func resolveBinary() (string, error) {
	binary := os.Getenv(binaryEnv)
	if binary == "" {
		binary = "codex"
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return "", fmt.Errorf("resolve Codex CLI %q: %w", binary, err)
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
	written, err := w.target.Write(p)
	if remaining := maxDiagnostic - len(w.data); remaining > 0 {
		if len(p) > remaining {
			w.data = append(w.data, p[:remaining]...)
		} else {
			w.data = append(w.data, p...)
		}
	}
	return written, err
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

func buildCommand(request *protocol.ExecuteRequest, includeResume bool) (commandSpec, error) {
	if request == nil {
		return commandSpec{}, errors.New("execute request is required")
	}
	if strings.TrimSpace(request.Workdir) == "" || strings.TrimSpace(request.OutputDir) == "" {
		return commandSpec{}, errors.New("workdir and output_dir are required")
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

	writeMovement := len(request.Grants.PathsRW) > 0
	childDir := request.OutputDir
	if writeMovement {
		childDir = request.Workdir
	}

	args := []string{
		"exec",
		"--json",
		"--model", request.Model,
		"--sandbox", "workspace-write",
		"-C", childDir,
		"--skip-git-repo-check",
		"--ignore-user-config",
		"--ignore-rules",
		"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
		"-c", "sandbox_workspace_write.exclude_slash_tmp=true",
	}
	// Ignoring user config preserves CODEX_HOME authentication but cannot
	// remove managed policy or every integration built into the executable.
	if request.Grants.Network {
		args = append(args,
			"-c", "sandbox_workspace_write.network_access=true",
			"-c", `web_search="live"`,
		)
	} else {
		args = append(args,
			"-c", "sandbox_workspace_write.network_access=false",
			"-c", `web_search="disabled"`,
		)
	}
	if !request.Grants.Shell {
		args = append(args, "-c", "features.shell_tool=false")
	}
	if effort != "" {
		args = append(args, "-c", `model_reasoning_effort="`+escapeTOMLString(effort)+`"`)
	}
	if writeMovement {
		for _, directory := range addDirectories(request) {
			args = append(args, "--add-dir", directory)
		}
	}
	if resumeID != "" {
		args = append(args, "resume", resumeID, "-")
	} else {
		args = append(args, "-")
	}

	return commandSpec{
		args:     args,
		dir:      childDir,
		prompt:   adapterkit.RenderPrompt(request),
		resumeID: resumeID,
	}, nil
}

func addDirectories(request *protocol.ExecuteRequest) []string {
	// Codex writable roots are directories, so an external glob grant must be
	// widened to its containing directory. Probe therefore reports path_grants
	// as false even though the OS sandbox enforces the resulting roots.
	directories := []string{filepath.Clean(request.OutputDir)}
	for _, pattern := range request.Grants.PathsRW {
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

func absolutePattern(workdir, pattern string) (string, error) {
	if strings.TrimSpace(pattern) == "" {
		return "", errors.New("empty path pattern")
	}
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(workdir, pattern)
	}
	return filepath.Clean(pattern), nil
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
		return "", errors.New("extensions.codex must be an object")
	}
	// Extension fields are forward-compatible: this adapter consumes effort
	// and intentionally ignores fields it does not understand.
	effortRaw, exists := extension["effort"]
	if !exists {
		return "", nil
	}
	var effort string
	if err := json.Unmarshal(effortRaw, &effort); err != nil {
		return "", errors.New("extensions.codex.effort must be a string")
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

func escapeTOMLString(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return replacer.Replace(value)
}
