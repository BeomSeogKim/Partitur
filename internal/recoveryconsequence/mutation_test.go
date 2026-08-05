//go:build mutation

package recoveryconsequence

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationFrozenRouteRecordVerification(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve recovery consequence source directory")
	}
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyMutationRepository(copyRoot, filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(copyRoot, "internal", "recoveryconsequence", "consequence.go")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	const before = "if err != nil || rawHash(record) != recordHash {"
	const after = "if err != nil {"
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count=%d, want 1", count)
	}
	if err := os.WriteFile(source, []byte(strings.Replace(string(contents), before, after, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	mutated, err := os.ReadFile(source)
	if err != nil || !strings.Contains(string(mutated), after) || strings.Contains(string(mutated), before) {
		t.Fatalf("mutation did not apply: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{Dir: copyRoot, Package: "./internal/recoveryconsequence", TestPattern: "TestFrozenRoutePayloadRejectsRecordHashMismatch", TestNames: []string{"TestFrozenRoutePayloadRejectsRecordHashMismatch"}, Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1")})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-kill: %s", result.Diagnostic())
	}
}

func copyMutationRepository(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
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
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	})
}
