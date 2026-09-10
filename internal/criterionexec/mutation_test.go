//go:build mutation

package criterionexec

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

func TestMutationLaunchFailureStderrDrainIsRequired(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve criterion executor mutation source directory")
	}
	copyRoot := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	if err := copyCriterionExecMutationRepository(copyRoot, repository); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(copyRoot, "internal", "criterionexec", "criterionexec.go")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	const before = "go func() { stderrDone <- copyBounded(stderr, stderrRead) }()"
	const after = "go func() { _ = stderr; _ = stderrRead; stderrDone <- false }()"
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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     "./internal/criterionexec",
		TestPattern: "TestRunCapturesTrampolineStderrWhenIdentityPublicationFails",
		TestNames:   []string{"TestRunCapturesTrampolineStderrWhenIdentityPublicationFails"},
		Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("launch stderr drain mutation: %s", result.Diagnostic())
	}
}

func TestCopyCriterionExecMutationRepositorySkipsPartiturStateDirectory(t *testing.T) {
	source := t.TempDir()
	destination := filepath.Join(t.TempDir(), "copy")
	for path, contents := range map[string]string{
		filepath.Join(".partitur", "runs", "some-run", "journal.jsonl"):           "journal\n",
		filepath.Join(".partitur", "work", "some-attempt", "worktree", "file.go"): "package fixture\n",
		"z-after-partitur.txt": "copied\n",
	} {
		path = filepath.Join(source, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := mutationtest.CopyRepository(destination, source); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, ".partitur")); !os.IsNotExist(err) {
		t.Fatalf("copied .partitur directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "z-after-partitur.txt")); err != nil {
		t.Fatalf("entry after .partitur was not copied: %v", err)
	}
}

func copyCriterionExecMutationRepository(destination, source string) error {
	return mutationtest.CopyRepository(destination, source)
}
