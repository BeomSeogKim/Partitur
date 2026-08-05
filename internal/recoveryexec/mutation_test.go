//go:build mutation

// Mutation proofs run the default behavioral tests against deliberately broken copies.
// The default suite exercises those paths, but it does not establish that every guard
// rejects a faulty implementation. Run `go test -tags=mutation ./...` for that proof.
package recoveryexec

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

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationRecoveryCompositionTerminalStopsBeforeCreatingTargetAttempt(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestRecoveryCompositionTerminalStopsBeforeCreatingTargetAttempt(t)
	assertRecoveryMutationKilled(t, "TestRecoveryCompositionTerminalStopsBeforeCreatingTargetAttempt", goEnvironment, filepath.Join("internal", "driver", "movement_composition.go"), "return MovementBase{}, ErrCompositionTerminalized", "return MovementBase{}, errors.New(\"driver: injected non-terminal composition failure\")")
}

func TestMutationRecoveryFanInSuccessorMaterializesAtComposedBase(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestRecoveryFanInSuccessorMaterializesAtComposedBase(t)
	assertRecoveryMutationKilled(t, "TestRecoveryFanInSuccessorMaterializesAtComposedBase", goEnvironment, filepath.Join("internal", "recoveryexec", "handlers.go"), "workspace.CreateRecoveredAttemptAtBase(execution.Store, execution.Driver, input, movementID, baseCommit)", "workspace.CreateRecoveredAttempt(execution.Store, execution.Driver, input, movementID)")
}

func TestMutationRecoveryFinalGateRejectionEndsAtomically(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestRecoveryFinalGateRejectionEndsAtomically(t)
	assertRecoveryMutationKilled(t, "TestRecoveryFinalGateRejectionEndsAtomically", goEnvironment,
		"internal/recoveryconsequence/consequence.go", "\"subject_tree\": action.SubjectTree, \"run_failed\": state.FinalMovements[movementID]",
		"\"subject_tree\": action.SubjectTree, \"run_failed\": false")
}

func TestMutationRecoveryNonFinalGateRejectionCascades(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestRecoveryNonFinalGateRejectionCascades(t)
	assertRecoveryMutationKilled(t, "TestRecoveryNonFinalGateRejectionCascades", goEnvironment,
		"internal/recoveryconsequence/consequence.go", "\"subject_tree\": action.SubjectTree, \"run_failed\": state.FinalMovements[movementID]",
		"\"subject_tree\": action.SubjectTree, \"run_failed\": true")
}

func TestMutationAppendCompositionTerminalSerializesCancellationAfterEvidence(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestAppendCompositionTerminalSerializesCancellationAfterEvidence(t)
	assertRecoveryMutationKilled(t, "TestAppendCompositionTerminalSerializesCancellationAfterEvidence", goEnvironment, "internal/recoveryconsequence/consequence.go", `if state.CancelRequested || terminal.ScoreRevision != state.ScoreHead.Revision {
			return ErrReplan
		}`, `if false {
			return ErrReplan
		}`)
}

func mutationGoEnvironment(t *testing.T) mutationtest.GoEnvironment {
	t.Helper()
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	return environment
}

func assertRecoveryMutationKilled(t *testing.T, testName string, goEnvironment mutationtest.GoEnvironment, sourceName, before, after string) {
	t.Helper()
	lockMutationSource(t)
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve recovery test source directory")
	}
	temporaryRoot := t.TempDir()
	copyRoot := filepath.Join(temporaryRoot, "partitur-mutation-copy")
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	if err := copyMutationRepository(copyRoot, repositoryRoot); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(copyRoot, sourceName)
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
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "recoveryexec"),
		Package:     ".",
		TestPattern: testName,
		TestNames:   []string{testName},
		Environment: goEnvironment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
	})
	cancel()
	if err := copyFile(sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	cmp := exec.Command("cmp", "-s", backupPath, sourcePath)
	if output, err := cmp.CombinedOutput(); err != nil {
		t.Fatalf("mutation restore comparison failed: %v\n%s", err, output)
	}
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: %s still passed after %q became %q\n%s", testName, before, after, result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
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
