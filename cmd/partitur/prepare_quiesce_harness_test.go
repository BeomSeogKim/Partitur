package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// This is a harness capability exercise, not an E.2 crash fixture. It proves
// that a parent can inject a control prepare after the child acquired authority
// and before it begins normal driver work. The B3 fixture owns the crash cuts.
func TestParentInjectsPrepareIntoPausedLiveDriver(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository, environment := killHarnessRepository(t, bin, vendor)
	child := pauseRunAtPoint(t, partitur, repository, environment, faultpoint.PointAuthorityLeaseCreated)
	defer child.stop(t)

	runID, err := soleRunID(repository)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Authority.Owner == nil {
		t.Fatal("paused driver has no authority owner")
	}
	if err := appendRecoveryControlPrepare(store, runID, repository); err != nil {
		t.Fatal(err)
	}

	child.releaseAndExpect(t, []faultpoint.PointID{
		faultpoint.PointPrepareObserved,
		faultpoint.PointQuiesceSessionsSwept,
		faultpoint.PointQuiesceLeaseMoved,
	})
	if child.stdout.String() != string(runID)+"\n" || child.stderr.Len() != 0 {
		t.Fatalf("run stdout=%q stderr=%q", child.stdout.String(), child.stderr.String())
	}
	input, err = store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	prepare := input.Projection.State.PendingPrepare
	if prepare == nil || prepare.ObservedAuthorityEpoch != input.Projection.State.Authority.Epoch {
		t.Fatalf("pending prepare=%+v authority=%+v", prepare, input.Projection.State.Authority)
	}
	if _, err := os.Stat(runstorePath(repository, runID, "driver.quiesced."+string(prepare.ID))); err != nil {
		t.Fatalf("quiesced sidecar: %v", err)
	}
}

type pausedRun struct {
	command      *exec.Cmd
	notify       *os.File
	release      *os.File
	scanner      *bufio.Scanner
	stdout       bytes.Buffer
	stderr       bytes.Buffer
	processPID   int
	paused       bool
	commandEnded bool
}

func pauseRunAtPoint(t *testing.T, binary, repository string, environment []string, target faultpoint.PointID) *pausedRun {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		_ = notifyRead.Close()
		_ = notifyWrite.Close()
		t.Fatal(err)
	}
	files := make([]*os.File, 0, 8)
	for range 6 {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	files = append(files, notifyWrite, releaseRead)
	child := &pausedRun{notify: notifyRead, release: releaseWrite, scanner: bufio.NewScanner(notifyRead)}
	child.command = exec.Command(binary, "run")
	child.command.Dir = repository
	child.command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_FAULTPOINT_HARNESS":    "1",
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  "9",
		"PARTITUR_FAULTPOINT_RELEASE_FD": "10",
	})
	child.command.ExtraFiles = files
	child.command.Stdout = &child.stdout
	child.command.Stderr = &child.stderr
	if err := child.command.Start(); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		defer file.Close()
	}
	if err := notifyWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := releaseRead.Close(); err != nil {
		t.Fatal(err)
	}
	child.processPID = child.command.Process.Pid
	for {
		point, pid := nextKillPoint(t, child.scanner)
		if pid != child.processPID {
			t.Fatalf("faultpoint %q pid=%d, want child pid=%d", point, pid, child.processPID)
		}
		if point == target {
			child.paused = true
			return child
		}
		if _, err := child.release.Write([]byte{1}); err != nil {
			t.Fatalf("release %q: %v", point, err)
		}
	}
}

func (child *pausedRun) releaseAndExpect(t *testing.T, want []faultpoint.PointID) {
	t.Helper()
	if !child.paused {
		t.Fatal("child is not paused")
	}
	if _, err := child.release.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	for _, expected := range want {
		point, pid := nextKillPoint(t, child.scanner)
		if pid != child.processPID || point != expected {
			t.Fatalf("faultpoint=%q pid=%d, want %q from pid=%d", point, pid, expected, child.processPID)
		}
		if _, err := child.release.Write([]byte{1}); err != nil {
			t.Fatalf("release %q: %v", point, err)
		}
	}
	if err := child.release.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.command.Wait(); err != nil {
		t.Fatalf("run after prepare injection: %v\nstdout:\n%s\nstderr:\n%s", err, &child.stdout, &child.stderr)
	}
	child.commandEnded = true
	if child.scanner.Scan() {
		t.Fatalf("unexpected faultpoint %q", child.scanner.Text())
	}
	if err := child.scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

func (child *pausedRun) stop(t *testing.T) {
	t.Helper()
	if child == nil || child.commandEnded {
		return
	}
	if child.command.Process != nil {
		_ = child.command.Process.Kill()
	}
	_ = child.release.Close()
	_ = child.command.Wait()
	_ = child.notify.Close()
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(root, "..", ".."))
}

func runstorePath(repository string, runID runstate.RunID, name string) string {
	return filepath.Join(repository, ".partitur", "runs", string(runID), name)
}
