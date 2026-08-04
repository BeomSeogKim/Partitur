//go:build mutation

package amendment

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

func TestMutationEvaluatorGuards(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ name, before, after, test string }{
		{"cancellation outranks stale", "if input.State.CancelRequested {", "if false { // mutation", "TestEvaluatePipelineAndPolicy/lifecycle_wins_stale"},
		{"reserved pointer refusal", "if touchesReserved(input.Operations) {", "if false { // mutation", "TestEvaluatePipelineAndPolicy/reserved_wins_before_patch"},
		{"canonical no-op before compilation", "if bytes.Equal(baseBytes, patchedBytes) {", "if false && bytes.Equal(baseBytes, patchedBytes) { // mutation", "TestEvaluatePipelineAndPolicy/test_reserved_is_permitted"},
		{"executed dependency feasibility", "} else if changed {", "} else if false && changed { // mutation", "TestEvaluateFeasibilityPrecedesPolicy"},
		{"candidate feasibility", "} else if condition != \"\" {", "} else if false && condition != \"\" { // mutation", "TestEvaluateCandidateFinalityPrecedesPolicy"},
		{"human guard audit-only", "if input.HumanDecision {", "if false { // mutation", "TestEvaluateHumanDecisionRecordsGuardWithoutRerouting"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertMutationKilled(t, environment, "evaluator.go", mutation.before, mutation.after, mutation.test)
		})
	}
}

func assertMutationKilled(t *testing.T, environment mutationtest.GoEnvironment, sourceName, before, after, testName string) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve amendment source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyRepository(copyRoot, repository); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(copyRoot, "internal", "amendment", sourceName)
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count=%d, want 1 for %q", count, before)
	}
	if err := os.WriteFile(source, []byte(strings.Replace(string(contents), before, after, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	mutated, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mutated), after) || strings.Contains(string(mutated), before) {
		t.Fatal("mutation did not apply")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{Dir: filepath.Join(copyRoot, "internal", "amendment"), Package: ".", TestPattern: testName, TestNames: []string{testName}, Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1")})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation survived: %s", result.Diagnostic())
	}
}

func copyRepository(destination, source string) error {
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
