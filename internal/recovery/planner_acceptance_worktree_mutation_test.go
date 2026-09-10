//go:build mutation

package recovery

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

func TestMutationPlanStartAcceptanceWriterWorktreeRequirement(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve recovery mutation source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyRecoveryMutationRepository(copyRoot, repository); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(copyRoot, "internal", "recovery", "planner.go")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const before = "\t\t!attempt.AcceptanceStarted && input.Observations.Worktree == WorktreeMissing {\n"
	const after = "\t\t!attempt.AcceptanceStarted && input.Observations.Worktree != WorktreeMissing { // mutation\n"
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count=%d, want 1", count)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(contents), before, after, 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const testName = "TestPlanStartAcceptanceWriterWorktreeRequirement"
	// Targets are the leaves, not the parent: the harness treats a failure whose
	// name is not a target or an ancestor of one as a non-target failure. Naming
	// both leaves is also what makes the kill two-sided - inverting the conjunct
	// has to break the missing case and the present case, and leave the reader
	// case passing, or this is not a result.
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir: copyRoot, Package: "./internal/recovery", TestPattern: testName,
		TestNames: []string{
			testName + "/missing_writer_worktree_fails_before_acceptance",
			testName + "/present_writer_worktree_starts_acceptance",
		},
		Environment: environment.ChildEnvironment(os.Environ()),
	})
	// Killed requires every target to reach terminal failure, so naming both
	// leaves above is what makes this a two-sided kill: a mutation that broke
	// only the missing case would report mixed outcomes, not a kill.
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestCopyRecoveryMutationRepositorySkipsPartiturStateDirectory(t *testing.T) {
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

func copyRecoveryMutationRepository(destination, source string) error {
	return mutationtest.CopyRepository(destination, source)
}
