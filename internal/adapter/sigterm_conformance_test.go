//go:build darwin || linux

package adapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

const (
	sigtermVendorModeEnv = "PARTITUR_SIGTERM_VENDOR_MODE"
	sigtermVendorFIFOEnv = "PARTITUR_SIGTERM_VENDOR_FIFO"
	sigtermVendorFDEnv   = "PARTITUR_SIGTERM_VENDOR_FD"
	sigtermVendorWaitEnv = "PARTITUR_SIGTERM_VENDOR_WAIT_FD"
)

func TestFirstPartyAdaptersHandleSIGTERM(t *testing.T) {
	directory := t.TempDir()
	for _, adapterID := range []string{"claude", "codex"} {
		buildAdapter(t, directory, adapterID)
	}
	vendorBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, adapterID := range []string{"claude", "codex"} {
		for _, method := range []string{"probe", "execute"} {
			t.Run(adapterID+"/"+method, func(t *testing.T) {
				testAdapterSIGTERM(t, filepath.Join(directory, "partitur-adapter-"+adapterID), vendorBinary, adapterID, method, "leader")
			})
		}
	}
}

func TestFirstPartyAdapterWaitsForTermIgnoringVendor(t *testing.T) {
	directory := t.TempDir()
	buildAdapter(t, directory, "codex")
	vendorBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	testAdapterSIGTERM(
		t,
		filepath.Join(directory, "partitur-adapter-codex"),
		vendorBinary,
		"codex",
		"execute",
		"leader-ignore-term",
	)
}

func testAdapterSIGTERM(t *testing.T, adapterPath, vendorPath, adapterID, method, vendorMode string) {
	t.Helper()

	fifoPath := filepath.Join(t.TempDir(), "vendor-liveness")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}
	fifoReader, err := os.OpenFile(fifoPath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fifoReader.Close()
	fifoGuard, err := os.OpenFile(fifoPath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer fifoGuard.Close()
	if err := syscall.SetNonblock(int(fifoReader.Fd()), false); err != nil {
		t.Fatal(err)
	}

	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinWriter.Close()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	command := exec.Command(adapterPath)
	command.Env = replaceEnvironment(os.Environ(), map[string]string{
		"PARTITUR_CLAUDE_BIN": vendorPath,
		"PARTITUR_CODEX_BIN":  vendorPath,
		sigtermVendorModeEnv:  vendorMode,
		sigtermVendorFIFOEnv:  fifoPath,
	})
	command.Stdin = stdinReader
	command.Stdout = stdoutWriter
	command.Stderr = stderrWriter
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	adapterPID := command.Process.Pid
	_ = stdinReader.Close()
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	var stdout, stderr bytes.Buffer
	stdoutDone := copyPipe(&stdout, stdoutReader)
	stderrDone := copyPipe(&stderr, stderrReader)
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- command.Wait()
	}()

	var vendorGroup int
	adapterExited := false
	t.Cleanup(func() {
		if !adapterExited {
			_ = syscall.Kill(adapterPID, syscall.SIGKILL)
		}
		if vendorGroup > 0 {
			_ = syscall.Kill(-vendorGroup, syscall.SIGKILL)
		}
	})

	request, err := sigtermRequest(method, adapterID, t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stdinWriter.Write(append(request, '\n')); err != nil {
		t.Fatal(err)
	}

	readiness := bufio.NewScanner(fifoReader)
	ready := make(chan []string, 1)
	go func() {
		var lines []string
		for len(lines) < 2 && readiness.Scan() {
			lines = append(lines, readiness.Text())
		}
		ready <- lines
	}()
	select {
	case lines := <-ready:
		if len(lines) != 2 {
			t.Fatalf("%s/%s: vendor readiness = %q", adapterID, method, lines)
		}
		for _, line := range lines {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				t.Fatalf("%s/%s: malformed vendor readiness %q", adapterID, method, line)
			}
			pid, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatalf("%s/%s: malformed vendor pid %q", adapterID, method, line)
			}
			if fields[0] == "leader" {
				vendorGroup = pid
			}
		}
		if vendorGroup <= 0 {
			t.Fatalf("%s/%s: leader readiness absent: %q", adapterID, method, lines)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s/%s: timed out waiting for vendor readiness", adapterID, method)
	}
	if err := fifoGuard.Close(); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Kill(adapterPID, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case waitErr := <-waitDone:
		adapterExited = true
		if waitErr != nil {
			t.Logf("%s/%s: adapter exit status (non-normative): %v", adapterID, method, waitErr)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("%s/%s: timed out waiting for adapter shutdown", adapterID, method)
	}
	<-stdoutDone
	<-stderrDone

	if err := syscall.SetNonblock(int(fifoReader.Fd()), true); err != nil {
		t.Fatal(err)
	}
	var data [1]byte
	count, readErr := syscall.Read(int(fifoReader.Fd()), data[:])
	if count != 0 || readErr != nil {
		t.Fatalf(
			"%s/%s: vendor FIFO read = count %d, error %v; want EOF after adapter exit\nstdout:\n%s\nstderr:\n%s",
			adapterID,
			method,
			count,
			readErr,
			&stdout,
			&stderr,
		)
	}
	vendorGroup = 0
	t.Logf("SIGTERM_CONFORMANCE adapter=%s method=%s vendor_mode=%s vendor_fifo=EOF", adapterID, method, vendorMode)
}

func copyPipe(target io.Writer, source *os.File) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer source.Close()
		_, _ = io.Copy(target, source)
	}()
	return done
}

func sigtermRequest(method, adapterID, workdir, outputDir string) ([]byte, error) {
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      method + "-1",
		"method":  method,
		"params":  map[string]any{},
	}
	if method == "execute" {
		model := "gpt-5.6-terra"
		if adapterID == "claude" {
			model = "claude-sonnet-5"
		}
		request["params"] = protocol.ExecuteRequest{
			RunID:         "run-1",
			MovementID:    "movement-1",
			AttemptID:     "attempt-1",
			ScoreRevision: 1,
			Model:         model,
			Brief: protocol.Brief{
				Goal:        "Wait for termination",
				Instruction: "Wait for termination",
				Outputs:     []protocol.OutputSpec{},
			},
			Inputs:            []protocol.ArtifactRef{},
			Feedback:          []protocol.Feedback{},
			ResolvedDecisions: []protocol.ResolvedDecision{},
			Workdir:           workdir,
			OutputDir:         outputDir,
			Grants:            protocol.Grants{},
			Budget:            protocol.Budget{RemainingMS: 60_000},
		}
	}
	return json.Marshal(request)
}

func runSIGTERMVendorHelper(mode string) int {
	switch mode {
	case "leader":
		return runSIGTERMVendorLeader(false)
	case "leader-ignore-term":
		return runSIGTERMVendorLeader(true)
	case "child":
		return runSIGTERMVendorChild(false)
	case "child-ignore-term":
		return runSIGTERMVendorChild(true)
	default:
		return 90
	}
}

func runSIGTERMVendorLeader(ignoreTerm bool) int {
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}()
	writer, err := os.OpenFile(os.Getenv(sigtermVendorFIFOEnv), os.O_WRONLY, 0)
	if err != nil {
		return 91
	}
	defer writer.Close()

	var signals chan os.Signal
	if ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	} else {
		signals = make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		defer signal.Stop(signals)
	}

	waitReader, waitWriter, err := os.Pipe()
	if err != nil {
		return 92
	}
	defer waitReader.Close()
	defer waitWriter.Close()

	childMode := "child"
	if ignoreTerm {
		childMode = "child-ignore-term"
	}
	child := exec.Command(os.Args[0])
	child.Env = replaceEnvironment(os.Environ(), map[string]string{
		sigtermVendorModeEnv: childMode,
		sigtermVendorFDEnv:   "3",
		sigtermVendorWaitEnv: "4",
	})
	child.ExtraFiles = []*os.File{writer, waitReader}
	if err := child.Start(); err != nil {
		return 93
	}
	_ = waitReader.Close()
	if _, err := fmt.Fprintf(writer, "leader %d\n", os.Getpid()); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		return 94
	}
	if ignoreTerm {
		blockReader, blockWriter, err := os.Pipe()
		if err != nil {
			return 95
		}
		defer blockReader.Close()
		defer blockWriter.Close()
		_, _ = io.Copy(io.Discard, blockReader)
		return 0
	}
	<-signals
	_ = child.Wait()
	return 0
}

func runSIGTERMVendorChild(ignoreTerm bool) int {
	fd, err := strconv.Atoi(os.Getenv(sigtermVendorFDEnv))
	if err != nil {
		return 96
	}
	writer := os.NewFile(uintptr(fd), "vendor-liveness")
	if writer == nil {
		return 97
	}
	defer writer.Close()
	if ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	if _, err := fmt.Fprintf(writer, "child %d\n", os.Getpid()); err != nil {
		return 98
	}
	waitFD, err := strconv.Atoi(os.Getenv(sigtermVendorWaitEnv))
	if err != nil {
		return 99
	}
	waitReader := os.NewFile(uintptr(waitFD), "vendor-wait")
	if waitReader == nil {
		return 100
	}
	defer waitReader.Close()
	_, _ = io.Copy(io.Discard, waitReader)
	return 0
}
