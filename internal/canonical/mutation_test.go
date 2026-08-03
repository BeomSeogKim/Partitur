//go:build mutation

package canonical

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

func TestMutationExecutionDependencyProjectionVersionChangesHash(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve canonical test source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	copyRoot := t.TempDir()
	for _, name := range []string{"go.mod", "go.sum"} {
		if err := os.WriteFile(
			filepath.Join(copyRoot, name),
			mustReadFile(t, filepath.Join(repositoryRoot, name)),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	copyPackage := filepath.Join(copyRoot, "internal", "canonical")
	if err := os.CopyFS(copyPackage, os.DirFS(filepath.Join(repositoryRoot, "internal", "canonical"))); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(copyPackage, "domain.go")
	contents := mustReadFile(t, sourcePath)
	const before = "\tProjectionVersionExecutionDependency    = 2\n"
	const after = "\tProjectionVersionExecutionDependency    = 1\n"
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	if err := os.WriteFile(sourcePath, []byte(strings.Replace(string(contents), before, after, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := string(mustReadFile(t, sourcePath)); !strings.Contains(got, after) || strings.Contains(got, before) {
		t.Fatal("execution dependency projection-version mutation was not applied")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyPackage,
		Package:     ".",
		TestPattern: "TestExecutionDependencyHashChangesWithProjectionVersion",
		TestNames:   []string{"TestExecutionDependencyHashChangesWithProjectionVersion"},
		Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("execution dependency projection-version mutation: %s", result.Diagnostic())
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}
