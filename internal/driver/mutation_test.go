//go:build mutation

// Mutation proofs run the default behavioral tests against deliberately broken copies.
// The default suite exercises those paths, but it does not establish that every guard
// rejects a faulty implementation. Run `go test -tags=mutation ./...` for that proof.
package driver

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMutationLiveMovementCompositionTerminalSerializesCancellationAfterEvidence(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestLiveMovementCompositionTerminalSerializesCancellationAfterEvidence(t)
	assertDriverMutationKilled(t, "TestLiveMovementCompositionTerminalSerializesCancellationAfterEvidence", goEnvironment, "movement_composition.go", `func appendMovementCompositionTerminal(authority *runstore.Driver, stopped, evidence, terminal runstate.Event, stoppedAddress, evidenceAddress, terminalAddress faultpoint.ReceiptAddress, afterEvidence func()) error {
	return authority.Mutate(func(transaction *runstore.Txn, state runstate.State) error {
		if state.CancelRequested {
			return ErrCompositionCancelled
		}
		next, err := runstate.Apply(state, stopped)
		if err != nil {
			return err
		}
		if _, err := transaction.At(stoppedAddress).Append(stopped); err != nil {
			return err
		}
		state = next
		next, err = runstate.Apply(state, evidence)
		if err != nil {
			return err
		}
		evidenceReceipt, err := transaction.At(evidenceAddress).Append(evidence)
		if err != nil {
			return err
		}
		if afterEvidence != nil {
			afterEvidence()
		}
		terminal.CausationID = evidenceReceipt.Mutation.EventID
		if _, err := runstate.Apply(next, terminal); err != nil {
			return err
		}
		_, err = transaction.At(terminalAddress).Append(terminal)
		return err
	})
}`,
		`func appendMovementCompositionTerminal(authority *runstore.Driver, stopped, evidence, terminal runstate.Event, stoppedAddress, evidenceAddress, terminalAddress faultpoint.ReceiptAddress, afterEvidence func()) error {
	state, err := authority.State()
	if err != nil {
		return err
	}
	if state.CancelRequested {
		return ErrCompositionCancelled
	}
	var evidenceReceipt runstore.DurabilityReceipt
	err = authority.Mutate(func(transaction *runstore.Txn, _ runstate.State) error {
		next, err := runstate.Apply(state, stopped)
		if err != nil {
			return err
		}
		if _, err := transaction.At(stoppedAddress).Append(stopped); err != nil {
			return err
		}
		state = next
		next, err = runstate.Apply(state, evidence)
		if err != nil {
			return err
		}
		evidenceReceipt, err = transaction.At(evidenceAddress).Append(evidence)
		return err
	})
	if err != nil {
		return err
	}
	if afterEvidence != nil {
		afterEvidence()
	}
	time.Sleep(50 * time.Millisecond)
	terminal.CausationID = evidenceReceipt.Mutation.EventID
	if _, err := runstate.Apply(state, terminal); err != nil {
		return err
	}
	_, err = authority.Append(terminal, terminalAddress)
	return err
}`)
}

func TestMutationPrepareMovementBaseUsesIdentityForZeroContributors(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestPrepareMovementBaseUsesIdentityForZeroContributors(t)
	assertDriverMutationKilled(t, "TestPrepareMovementBaseUsesIdentityForZeroContributors", goEnvironment, "movement_composition.go", "if len(contributors) == 0 {", "if len(contributors) == 1 {")
}

func TestMutationComposeMovementBaseReportsEachMissingOperand(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestComposeMovementBaseReportsEachMissingOperand(t)
	for _, name := range []string{"store", "authority", "contributors", "now", "newID"} {
		t.Run(name, func(t *testing.T) {
			assertDriverMutationKilled(t, "TestComposeMovementBaseReportsEachMissingOperand/"+name, goEnvironment, "movement_composition.go", "missing = append(missing, \""+name+"\")", "missing = append(missing, \"mutated-"+name+"\")")
		})
	}
}

func TestMutationLiveCompositionConflictStopsBeforeCreatingTargetAttempt(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestLiveCompositionConflictStopsBeforeCreatingTargetAttempt(t)
	assertDriverMutationKilled(t, "TestLiveCompositionConflictStopsBeforeCreatingTargetAttempt", goEnvironment, "movement_composition.go", "return MovementBase{}, ErrCompositionTerminalized", "return MovementBase{}, errors.New(\"driver: injected non-terminal composition failure\")")
}

func TestMutationLiveFanInCreatesTargetAtPinnedBaseCommit(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestLiveFanInCreatesTargetAtPinnedBaseCommit(t)
	assertDriverMutationKilled(t, "TestLiveFanInCreatesTargetAtPinnedBaseCommit", goEnvironment, "driver.go", "attempt, err = run.CreateAttemptAtBase(movement.ID, baseCommit)", "attempt, err = run.CreateAttempt(movement.ID)")
}

type mutationGoCaches struct {
	moduleCache string
	goPath      string
	buildCache  string
}

func mutationGoEnvironment(t *testing.T) mutationGoCaches {
	t.Helper()
	command := exec.Command("go", "env", "GOMODCACHE", "GOPATH", "GOCACHE")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	values := strings.Fields(string(output))
	if len(values) != 3 {
		t.Fatalf("go env returned %d cache values, want 3", len(values))
	}
	return mutationGoCaches{moduleCache: values[0], goPath: values[1], buildCache: values[2]}
}

func assertDriverMutationKilled(t *testing.T, testName string, goEnvironment mutationGoCaches, sourceName, before, after string) {
	t.Helper()
	lockMutationSource(t)
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve driver test source directory")
	}
	temporaryRoot := t.TempDir()
	copyRoot := filepath.Join(temporaryRoot, "partitur-mutation-copy")
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	if err := copyMutationRepository(copyRoot, repositoryRoot); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(copyRoot, "internal", "driver", sourceName)
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count == 0 {
		t.Fatalf("mutation anchor %q is absent from %s", before, sourcePath)
	}
	backup, err := os.CreateTemp(t.TempDir(), "partitur-mutation-backup-")
	if err != nil {
		t.Fatal(err)
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	copyFile := func(destination, source string) error {
		input, err := os.Open(source)
		if err != nil {
			return err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	if err := copyFile(backupPath, sourcePath); err != nil {
		t.Fatal(err)
	}
	mutated := strings.ReplaceAll(string(contents), before, after)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applied), after) || strings.Contains(string(applied), before) {
		t.Fatalf("mutation was not applied to %s before the child test ran", sourcePath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	command := exec.CommandContext(ctx, "go", "test", ".", "-run", "^"+testName+"$", "-count=1")
	command.Dir = filepath.Join(copyRoot, "internal", "driver")
	command.Env = append(os.Environ(),
		"PARTITUR_MUTATION_CHILD=1",
		"GOMODCACHE="+goEnvironment.moduleCache,
		"GOPATH="+goEnvironment.goPath,
		"GOCACHE="+goEnvironment.buildCache,
	)
	output, runErr := command.CombinedOutput()
	cancel()
	if err := copyFile(sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	cmp := exec.Command("cmp", "-s", backupPath, sourcePath)
	if output, err := cmp.CombinedOutput(); err != nil {
		t.Fatalf("mutation restore comparison failed: %v\n%s", err, output)
	}
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("mutation non-result: child test timed out\n%s", output)
	}
	if runErr == nil {
		t.Fatalf("mutation survived: %s still passed after %q became %q", testName, before, after)
	}
	if strings.Contains(string(output), "[build failed]") {
		t.Fatalf("mutation non-result: child build failed\n%s", output)
	}
	if strings.Contains(string(output), "panic:") {
		t.Fatalf("mutation non-result: child panicked\n%s", output)
	}
	if !strings.Contains(string(output), "--- FAIL: "+testName) {
		t.Fatalf("mutation non-result: child did not fail the targeted assertion: %v\n%s", runErr, output)
	}
}

func copyMutationRepository(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func lockMutationSource(t *testing.T) {
	t.Helper()
	lock, err := os.OpenFile(filepath.Join(os.TempDir(), "partitur-mutation-source.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
			t.Errorf("release mutation source lock: %v", err)
		}
		if err := lock.Close(); err != nil {
			t.Errorf("close mutation source lock: %v", err)
		}
	})
}
