//go:build mutation

package status

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationGateOnlyWriteRequiresApprovedResolution(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	TestProjectGateOnlyWriteCarriesApprovedMarkWithoutCarryover(t)
	for _, mutation := range []struct {
		testName string
		before   string
		after    string
	}{
		{"TestProjectGateOnlyWriteCarriesApprovedMarkWithoutCarryover/subject_mismatch", "resolution.Scope.SubjectTree == acceptance.SubjectTree", "resolution.Scope.SubjectTree != acceptance.SubjectTree"},
		{"TestProjectGateOnlyWriteCarriesApprovedMarkWithoutCarryover/revision_mismatch", "resolution.ScoreRevision == attempt.ScoreRevision", "resolution.ScoreRevision != attempt.ScoreRevision"},
	} {
		t.Run(mutation.testName, func(t *testing.T) {
			assertStatusMutationKilled(t, mutation.testName, environment, mutation.before, mutation.after, "internal/status", ".")
		})
	}
}

func TestMutationLiveApprovedMarkRequiresApproval(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	assertStatusMutationKilled(
		t,
		"TestRunHumanGateApprovalProjectsApprovedMark",
		environment,
		"resolution.Disposition == \"approved\"",
		"resolution.Disposition == \"rejected\"",
		"cmd/partitur",
		".",
	)
}

func assertStatusMutationKilled(t *testing.T, testName string, environment mutationtest.GoEnvironment, before, after, relativeDirectory, packageName string) {
	t.Helper()
	lockStatusMutationSource(t)
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve status test source directory")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	if err := copyStatusMutationRepository(copyRoot, root); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(copyRoot, "internal", "status", "status.go")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), before, after, 1)
	if err := os.WriteFile(source, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applied), after) || strings.Contains(string(applied), before) {
		t.Fatalf("mutation was not applied to %s before the child test ran", source)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, relativeDirectory),
		Package:     packageName,
		TestPattern: testName,
		TestNames:   []string{testName},
		Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
	})
	if result.Outcome == mutationtest.Killed {
		return
	}
	t.Fatalf("mutation %q -> %q: %s", before, after, result.Diagnostic())
}

func copyStatusMutationRepository(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ".git" && entry.IsDir() {
			return filepath.SkipDir
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

func lockStatusMutationSource(t *testing.T) {
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
