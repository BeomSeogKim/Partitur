package adapter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapterkit"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

type requestWriter func(io.Writer, []byte) error
type frameReader func(io.Reader, chan<- frameEvent)
type commandWaiter func(*exec.Cmd) error
type stderrCopier func(io.Writer, io.Reader) (int64, error)
type gatedLauncher func(context.Context, launch.Request) (*launch.Process, error)

// Client owns an immutable environment snapshot and the probe lifecycle.
type Client struct {
	environment []string
	deadline    time.Duration
	grace       time.Duration
	sessions    sessionController
	write       requestWriter
	read        frameReader
	wait        commandWaiter
	copyStderr  stderrCopier
	launch      gatedLauncher
	now         func() time.Time
}

// NewClient snapshots the operator environment for discovery and child launch.
func NewClient() *Client {
	return newClient(os.Environ(), ProbeCompletionDeadline, OuterTerminationGrace)
}

// Resolve returns the absolute executable path for one adapter id using the
// same immutable environment snapshot Execute will pass to the gated peer.
func (c *Client) Resolve(adapterID string) (string, error) {
	if c == nil {
		return "", errors.New("nil adapter client")
	}
	return resolveExecutable(c.environment, adapterID)
}

func newClient(environment []string, deadline, grace time.Duration) *Client {
	return &Client{
		environment: append([]string(nil), environment...),
		deadline:    deadline,
		grace:       grace,
		sessions:    systemSessionController{},
		write:       writeAll,
		read:        readFrames,
		wait:        waitCommand,
		copyStderr:  io.Copy,
		launch:      launch.LaunchContext,
		now:         time.Now,
	}
}

// ProbeAll probes each distinct adapter id once. Failures do not short-circuit
// later adapters.
func (c *Client) ProbeAll(adapterIDs []string) Report {
	unique := make(map[string]struct{}, len(adapterIDs))
	for _, adapterID := range adapterIDs {
		unique[adapterID] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for adapterID := range unique {
		ordered = append(ordered, adapterID)
	}
	sort.Strings(ordered)

	var report Report
	for _, adapterID := range ordered {
		result, diagnostics := c.probeOne(adapterID)
		if result != nil {
			report.Probes = append(report.Probes, Probe{
				AdapterID: adapterID,
				Result:    *result,
			})
		}
		report.Diagnostics = append(report.Diagnostics, diagnostics...)
	}
	sortReport(&report)
	return report
}

func (c *Client) probeOne(adapterID string) (*protocol.ProbeResult, []Diagnostic) {
	path, err := resolveExecutable(c.environment, adapterID)
	if err != nil {
		return nil, []Diagnostic{c.diagnostic(adapterID, DiagnosticExecutableAbsent, err.Error(), "")}
	}
	request, err := encodeProbeRequest()
	if err != nil {
		return nil, []Diagnostic{c.diagnostic(adapterID, DiagnosticRequestIO, err.Error(), "")}
	}

	deadline := time.Now().Add(c.deadline)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	running, startErr := startProbe(path, c.environment, c.read, c.wait, c.copyStderr)
	if startErr != nil {
		if running == nil {
			return nil, []Diagnostic{c.diagnostic(adapterID, DiagnosticSpawnFailed, startErr.Error(), "")}
		}
		diagnostic := c.diagnostic(adapterID, DiagnosticSpawnFailed, startErr.Error(), "")
		return nil, c.fail(running, diagnostic)
	}

	if err := c.write(running.stdin, request); err != nil {
		diagnostic := c.diagnostic(adapterID, DiagnosticRequestIO, err.Error(), "")
		return nil, c.fail(running, diagnostic)
	}
	select {
	case <-timer.C:
		diagnostic := c.diagnostic(adapterID, DiagnosticDeadline, "probe completion deadline expired", "")
		return nil, c.fail(running, diagnostic)
	default:
	}

	var result protocol.ProbeResult
	waitChannel := running.wait
	for {
		select {
		case event := <-running.frames:
			if event.err != nil {
				diagnostic := c.frameDiagnostic(adapterID, event.err)
				return nil, c.fail(running, diagnostic)
			}
			decoded, err := decodeProbeResponse(event.frame, adapterID)
			if err != nil {
				var failure *wireFailure
				if errors.As(err, &failure) {
					diagnostic := c.diagnostic(adapterID, failure.kind, failure.detail, "")
					return nil, c.fail(running, diagnostic)
				}
				diagnostic := c.diagnostic(adapterID, DiagnosticMalformedResponse, err.Error(), "")
				return nil, c.fail(running, diagnostic)
			}
			result = decoded
			goto responseReceived
		case waitErr := <-waitChannel:
			running.waitSeen = true
			running.waitErr = waitErr
			waitChannel = nil
			if waitErr != nil {
				diagnostic := c.diagnostic(adapterID, DiagnosticNonzeroExit, waitErr.Error(), "")
				return nil, c.fail(running, diagnostic)
			}
		case <-timer.C:
			diagnostic := c.diagnostic(adapterID, DiagnosticDeadline, "probe completion deadline expired", "")
			return nil, c.fail(running, diagnostic)
		}
	}

responseReceived:
	if err := running.stdin.Close(); err != nil {
		diagnostic := c.diagnostic(adapterID, DiagnosticRequestIO, err.Error(), "")
		return nil, c.fail(running, diagnostic)
	}
	running.stdin = nil

	stdoutEOF := false
	for !stdoutEOF || !running.waitSeen {
		select {
		case event := <-running.frames:
			if event.err == nil {
				diagnostic := c.diagnostic(adapterID, DiagnosticMalformedResponse, "unexpected frame after probe response", "")
				return nil, c.fail(running, diagnostic)
			}
			if errors.Is(event.err, io.EOF) {
				stdoutEOF = true
				continue
			}
			diagnostic := c.frameDiagnostic(adapterID, event.err)
			return nil, c.fail(running, diagnostic)
		case waitErr := <-waitChannel:
			running.waitSeen = true
			running.waitErr = waitErr
			waitChannel = nil
			if waitErr != nil {
				diagnostic := c.diagnostic(adapterID, DiagnosticNonzeroExit, waitErr.Error(), "")
				return nil, c.fail(running, diagnostic)
			}
		case <-timer.C:
			diagnostic := c.diagnostic(adapterID, DiagnosticDeadline, "probe completion deadline expired", "")
			return nil, c.fail(running, diagnostic)
		}
	}

	<-running.stderrDone
	empty, err := c.sessions.verifyEmpty(running.sid, running.identity)
	if err != nil {
		diagnostic := c.diagnostic(adapterID, DiagnosticCleanupUnverifiable, err.Error(), running.stderr.String())
		return nil, []Diagnostic{diagnostic}
	}
	if !empty {
		diagnostic := c.diagnostic(adapterID, DiagnosticSessionNotEmpty, "adapter session remained live after clean exit", "")
		if cleanupErr := c.sessions.terminate(running.sid, running.identity, c.grace); cleanupErr != nil {
			return nil, []Diagnostic{
				diagnostic,
				c.diagnostic(adapterID, DiagnosticCleanupUnverifiable, cleanupErr.Error(), running.stderr.String()),
			}
		}
		return nil, []Diagnostic{diagnostic}
	}
	return &result, nil
}

func (c *Client) fail(running *runningProbe, cause Diagnostic) []Diagnostic {
	if running.stdin != nil {
		_ = running.stdin.Close()
		running.stdin = nil
	}
	cleanupErr := c.sessions.terminate(running.sid, running.identity, c.grace)
	if cleanupErr == nil {
		if !running.waitSeen {
			running.waitErr = <-running.wait
			running.waitSeen = true
		}
		<-running.stderrDone
	}
	cause.Stderr = adapterkit.SanitizeDiagnostic(running.stderr.String())
	diagnostics := []Diagnostic{cause}
	if cleanupErr != nil {
		diagnostics = append(diagnostics, c.diagnostic(
			cause.AdapterID,
			DiagnosticCleanupUnverifiable,
			cleanupErr.Error(),
			running.stderr.String(),
		))
	}
	return diagnostics
}

func (c *Client) frameDiagnostic(adapterID string, err error) Diagnostic {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, protocol.ErrPartialFrame):
		return c.diagnostic(adapterID, DiagnosticPrematureEOF, err.Error(), "")
	case errors.Is(err, protocol.ErrFrameTooLarge):
		return c.diagnostic(adapterID, DiagnosticOversizedResponse, err.Error(), "")
	default:
		return c.diagnostic(adapterID, DiagnosticResponseIO, err.Error(), "")
	}
}

func (c *Client) diagnostic(adapterID string, kind DiagnosticKind, detail, stderr string) Diagnostic {
	return Diagnostic{
		AdapterID: adapterID,
		Kind:      kind,
		Detail:    adapterkit.SanitizeDiagnostic(detail),
		Stderr:    adapterkit.SanitizeDiagnostic(stderr),
	}
}

type frameEvent struct {
	frame []byte
	err   error
}

type runningProbe struct {
	stdin      io.WriteCloser
	frames     <-chan frameEvent
	wait       <-chan error
	stderrDone <-chan struct{}
	stderr     *limitedBuffer
	sid        int
	identity   string
	waitSeen   bool
	waitErr    error
}

func startProbe(
	path string,
	environment []string,
	read frameReader,
	waitCommand commandWaiter,
	copyStderr stderrCopier,
) (*runningProbe, error) {
	command := exec.Command(path)
	command.Env = append([]string(nil), environment...)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create adapter stdin: %w", err)
	}
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("create adapter stdout: %w", err)
	}
	stderr, childStderr, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		return nil, fmt.Errorf("create adapter stderr: %w", err)
	}
	command.Stdout = childStdout
	command.Stderr = childStderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = childStdout.Close()
		_ = stderr.Close()
		_ = childStderr.Close()
		return nil, fmt.Errorf("start adapter: %w", err)
	}
	// Cmd.Wait closes only files that os/exec created. The client owns these
	// read ends, so a concurrent Wait cannot invalidate a pending read. Closing
	// the parent's write copies after Start leaves the child as the sole writer,
	// and its exit therefore still delivers EOF to both readers.
	_ = childStdout.Close()
	_ = childStderr.Close()

	frames := make(chan frameEvent, 2)
	go func() {
		defer stdout.Close()
		read(stdout, frames)
	}()
	stderrBuffer := &limitedBuffer{limit: MaxProbeStderrBytes}
	stderrDone := make(chan struct{})
	go func() {
		defer stderr.Close()
		_, _ = copyStderr(stderrBuffer, stderr)
		close(stderrDone)
	}()
	wait := make(chan error, 1)
	go func() {
		wait <- waitCommand(command)
	}()

	running := &runningProbe{
		stdin:      stdin,
		frames:     frames,
		wait:       wait,
		stderrDone: stderrDone,
		stderr:     stderrBuffer,
		sid:        command.Process.Pid,
	}
	process, inspectErr := processByPID(command.Process.Pid)
	if inspectErr != nil {
		if isProcessGone(inspectErr) {
			return running, errors.New("adapter exited before its start identity was captured")
		}
		return running, fmt.Errorf("inspect adapter session leader: %w", inspectErr)
	}
	if process.SID != command.Process.Pid {
		return running, fmt.Errorf("adapter pid %d is not its session leader", command.Process.Pid)
	}
	running.identity = process.Start
	return running, nil
}

func waitCommand(command *exec.Cmd) error {
	return command.Wait()
}

func readFrames(reader io.Reader, events chan<- frameEvent) {
	frames := protocol.NewReader(reader)
	for {
		frame, err := frames.ReadFrame()
		events <- frameEvent{frame: frame, err: err}
		if err != nil {
			return
		}
	}
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func resolveExecutable(environment []string, adapterID string) (string, error) {
	name := "partitur-adapter-" + adapterID
	if adapterID == "" || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("%s is absent from PATH", name)
	}
	pathValue, ok := environmentValue(environment, "PATH")
	if !ok {
		return "", fmt.Errorf("%s is absent from PATH", name)
	}
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" {
			directory = "."
		}
		candidate := filepath.Join(directory, name)
		if directory == "." {
			candidate = "." + string(os.PathSeparator) + name
		}
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("%s is absent from PATH", name)
}

func environmentValue(environment []string, name string) (string, bool) {
	prefix := name + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix), true
		}
	}
	return "", false
}

type limitedBuffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(data)
	if remaining := b.limit - b.data.Len(); remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.data.Write(data)
	}
	return original, nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func (b *limitedBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Len()
}
