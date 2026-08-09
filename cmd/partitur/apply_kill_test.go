package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	statusprojection "github.com/BeomSeogKim/Partitur/internal/status"
)

// TestApplyKillCutsResolveToBothSides kills a real `apply` subprocess on each
// side of its durable seam. The seam is what makes recovery decidable: the
// transaction is recorded before the checkout is touched, so a crash either
// left the base tree in place or left the result tree in place, and
// `--recover` must reach the outcome the §8 exit table names for each.
func TestApplyKillCutsResolveToBothSides(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")

	for _, cut := range []struct {
		name        string
		point       faultpoint.PointID
		recoverCode int
		applied     string
		resolution  runstate.EventType
	}{
		{
			name:  "crash before the checkout is touched rolls back",
			point: faultpoint.PointApplyTransactionStarted,
			// The base tree is still in place, so the rollback is verified, not assumed.
			recoverCode: 4, applied: "", resolution: runstate.EventApplyRecoveryResolved,
		},
		{
			name:  "crash after the checkout is written completes",
			point: faultpoint.PointApplyCheckoutMutated,
			// The candidate is already on disk; recovery owes it the missing event.
			recoverCode: 0, applied: "candidate result\n", resolution: runstate.EventApplyCompleted,
		},
	} {
		t.Run(cut.name, func(t *testing.T) {
			root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
			environment := applyKillEnvironment(t)
			killAtPoint(t, partitur, partiturRepository(t, root), environment, cut.point, "apply", "run-1")

			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if countEvents(journal.Events, runstate.EventApplyStarted) != 1 ||
				countEvents(journal.Events, runstate.EventApplyCompleted) != 0 ||
				countEvents(journal.Events, runstate.EventApplyFailed) != 0 {
				t.Fatalf("crash left journal=%v", eventKinds(journal.Events))
			}

			// APPLYING is not a normal entry state: the interrupted transaction has
			// to be named before anything else may touch the checkout.
			code, stdout, stderr := runCommandBinary(t, partitur, root, environment, "apply", "run-1")
			if code != 2 || stdout != "" || !strings.Contains(stderr, "normal apply is refused") {
				t.Fatalf("normal apply after crash exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}

			code, stdout, stderr = runCommandBinary(t, partitur, root, environment, "apply", "run-1", "--recover")
			if code != cut.recoverCode || stdout != "" {
				t.Fatalf("recover exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if contents := applyReadFile(t, root, "applied.txt"); contents != cut.applied {
				t.Fatalf("checkout after recovery=%q, want %q", contents, cut.applied)
			}
			journal, err = store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if last := journal.Events[len(journal.Events)-1].Type; last != cut.resolution {
				t.Fatalf("recovery journal tail=%v", eventKinds(journal.Events))
			}

			// Recovery is a fixed point: a second pass changes no bytes.
			before := applyReadJournalBytes(t, root)
			runCommandBinary(t, partitur, root, environment, "apply", "run-1", "--recover")
			if after := applyReadJournalBytes(t, root); after != before {
				t.Fatalf("second recover rewrote the journal:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

// TestApplyKillLeavingAnUnverifiableCheckoutHalts covers the third outcome: a
// checkout matching neither candidate tree. Recovery may not guess, so it names
// the halt durably and every later pass reproduces it unchanged.
func TestApplyKillLeavingAnUnverifiableCheckoutHalts(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")
	root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
	environment := applyKillEnvironment(t)
	killAtPoint(t, partitur, partiturRepository(t, root), environment, faultpoint.PointApplyCheckoutMutated, "apply", "run-1")

	// Something else edited the checkout between the crash and the recovery, so
	// it is now neither the base tree nor the result tree.
	if err := os.WriteFile(filepath.Join(root, "applied.txt"), []byte("third state\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCommandBinary(t, partitur, root, environment, "apply", "run-1", "--recover")
	if code != 5 || stdout != "" || !strings.Contains(stderr, "matches neither candidate tree") {
		t.Fatalf("recover exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventApplyRecoveryRequired) != 1 ||
		countEvents(journal.Events, runstate.EventApplyCompleted) != 0 {
		t.Fatalf("halt journal=%v", eventKinds(journal.Events))
	}

	// The halt is the fixed point: repeating it appends no second cause.
	before := applyReadJournalBytes(t, root)
	code, _, stderr = runCommandBinary(t, partitur, root, environment, "apply", "run-1", "--recover")
	if code != 5 || !strings.Contains(stderr, "matches neither candidate tree") {
		t.Fatalf("second recover exit=%d stderr=%q", code, stderr)
	}
	if after := applyReadJournalBytes(t, root); after != before {
		t.Fatalf("second recover rewrote the journal:\nbefore=%s\nafter=%s", before, after)
	}
}

func applyKillEnvironment(t *testing.T) []string {
	t.Helper()
	return replaceEnvironment(os.Environ(), map[string]string{"HOME": t.TempDir()})
}

// partiturRepository is the fixture root, named for what killAtPoint expects.
func partiturRepository(t *testing.T, root string) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".partitur", "runs", "run-1")); err != nil {
		t.Fatal(err)
	}
	return root
}

func applyReadFile(t *testing.T, root, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(contents)
}

func applyReadJournalBytes(t *testing.T, root string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

// TestApplyUnderTheLockStaysObservable holds a real `apply` at its seam, with
// the repository state lock taken, and reads the repository from outside.
// `status` and `logs` never take that lock, so an application in flight must
// stay observable rather than blocking whoever asks about it.
func TestApplyUnderTheLockStaysObservable(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")
	root, _, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
	environment := applyKillEnvironment(t)

	release := pauseAtPoint(t, partitur, root, environment, faultpoint.PointApplyTransactionStarted, "apply", "run-1")

	// Bounded, because the failure this guards against is an unbounded wait.
	code, stdout, stderr := runCommandBinaryWithin(t, 30*time.Second, partitur, root, environment, "status", "run-1", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("status under the lock exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var report statusprojection.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if report.Run.Lifecycle != string(runstate.RunSucceeded) ||
		report.Application.State != string(runstate.ApplicationApplying) {
		t.Fatalf("status under the lock: run=%q application=%q", report.Run.Lifecycle, report.Application.State)
	}
	if code, _, stderr := runCommandBinaryWithin(t, 30*time.Second, partitur, root, environment, "logs", "run-1"); code != 0 || stderr != "" {
		t.Fatalf("logs under the lock exit=%d stderr=%q", code, stderr)
	}

	// A second apply must not proceed beside the first; it waits on the lock,
	// so it may not return an outcome while the first still holds it.
	second := make(chan int, 1)
	go func() {
		code, _, _ := runCommandBinary(t, partitur, root, environment, "apply", "run-1")
		second <- code
	}()
	select {
	case code := <-second:
		t.Fatalf("a second apply returned %d while the first held the state lock", code)
	case <-time.After(2 * time.Second):
	}

	// Released, the held apply finishes its own transaction normally, and only
	// then does the waiter get the lock and find the work already done.
	if code := release(); code != 0 {
		t.Fatalf("released apply exit=%d", code)
	}
	if contents := applyReadFile(t, root, "applied.txt"); contents != "candidate result\n" {
		t.Fatalf("released apply left checkout=%q", contents)
	}
	if code := <-second; code != 0 {
		t.Fatalf("second apply after release exit=%d, want the idempotent already-applied result", code)
	}
}

// pauseAtPoint starts the command, lets it run to the target point, and leaves
// it blocked there holding whatever it holds. The returned function releases it
// — and every point after it, so the command finishes on its own terms rather
// than dying with its probe pipe — and reports its exit code.
func pauseAtPoint(
	t *testing.T,
	binary, repository string,
	environment []string,
	target faultpoint.PointID,
	arguments ...string,
) func() int {
	t.Helper()
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	files := make([]*os.File, 0, 8)
	for range 6 {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		defer file.Close()
	}
	files = append(files, notifyWrite, releaseRead)

	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, arguments...)
	command.Dir = repository
	command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_FAULTPOINT_HARNESS":    "1",
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  "9",
		"PARTITUR_FAULTPOINT_RELEASE_FD": "10",
	})
	command.ExtraFiles = files
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = notifyWrite.Close()
	_ = releaseRead.Close()

	scanner := bufio.NewScanner(notifyRead)
	for {
		point, _ := nextKillPoint(t, scanner)
		if point == target {
			break
		}
		if _, err := releaseWrite.Write([]byte{1}); err != nil {
			t.Fatalf("release %q: %v", point, err)
		}
	}
	released := false
	t.Cleanup(func() {
		if !released {
			_ = releaseWrite.Close()
			_ = command.Wait()
		}
		_ = notifyRead.Close()
	})
	return func() int {
		released = true
		// Keep releasing: the held point first, then any the command reaches on
		// its way out. Closing the pipe instead would terminate it mid-work.
		go func() {
			for {
				if _, err := releaseWrite.Write([]byte{1}); err != nil {
					return
				}
				if !scanner.Scan() {
					return
				}
			}
		}()
		err := command.Wait()
		_ = releaseWrite.Close()
		if err == nil {
			return 0
		}
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("paused %v: %v\nstdout:\n%s\nstderr:\n%s", arguments, err, &stdout, &stderr)
		}
		t.Logf("released %v exited %d\nstdout:\n%s\nstderr:\n%s", arguments, exit.ExitCode(), &stdout, &stderr)
		return exit.ExitCode()
	}
}
