//go:build mutation

package adapter

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationExecuteCancellationLivenessBoundMustStayBelowTheClientDeadline(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve adapter mutation source directory")
	}
	copyRoot := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	if err := copyAdapterMutationRepository(copyRoot, repository); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(copyRoot, "internal", "adapter", "execute_test.go")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	const before = "const executeCancellation" + "LivenessBound = 6 * time.Second"
	const after = "const executeCancellation" + "LivenessBound = incidentalTestDeadline"
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count=%d, want 1", count)
	}
	mutated := strings.Replace(string(contents), before, after, 1)
	if err := os.WriteFile(source, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(source)
	if err != nil || !strings.Contains(string(applied), after) || strings.Contains(string(applied), before) {
		t.Fatalf("mutation did not apply: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     "./internal/adapter",
		TestPattern: "TestExecuteCancellationLivenessBoundStaysInsideClientDeadline",
		TestNames:   []string{"TestExecuteCancellationLivenessBoundStaysInsideClientDeadline"},
		Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("execute cancellation liveness bound mutation: %s", result.Diagnostic())
	}
}

func copyAdapterMutationRepository(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if (relative == ".git" || relative == ".partitur") && entry.IsDir() {
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
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	})
}
