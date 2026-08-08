//go:build mutation

package main

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

func TestMutationInitialMovementStateRequiresBothConjunctsAndEqualities(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, after, testName string
	}{
		{
			name: "status conjunct omitted", after: `if phase == "draft" {`,
			testName: "TestDraftSchedulingSelectsOnlyInterviewInRunAndResume",
		},
		{
			name: "phase conjunct omitted", after: `if status == "finalized" {`,
			testName: "TestFinalizedDraftMovementIsInapplicableAndRunTerminates",
		},
		{
			name: "status equality inverted", after: `if status != "finalized" && phase == "draft" {`,
			testName: "TestDraftSchedulingSelectsOnlyInterviewInRunAndResume",
		},
		{
			name: "phase equality inverted", after: `if status == "finalized" && phase != "draft" {`,
			testName: "TestFinalizedDraftMovementIsInapplicableAndRunTerminates",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertDraftSchedulingMutationKilled(t, environment, mutation.after, mutation.testName)
		})
	}
}

func assertDraftSchedulingMutationKilled(t *testing.T, environment mutationtest.GoEnvironment, replacement, testName string) {
	t.Helper()
	copyRoot := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve command mutation source")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	if err := copyPrepareQuiesceRepository(copyRoot, repositoryRoot); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(copyRoot, "internal", "runstate", "state.go")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	const before = `if status == "finalized" && phase == "draft" {`
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count=%d, want 1", count)
	}
	mutated := strings.Replace(string(contents), before, replacement, 1)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(sourcePath)
	if err != nil || !strings.Contains(string(applied), replacement) || strings.Contains(string(applied), before) {
		t.Fatalf("draft scheduling mutation did not apply: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	targets := []string{testName}
	if testName == "TestDraftSchedulingSelectsOnlyInterviewInRunAndResume" {
		targets = append(targets, testName+"/live_run", testName+"/interrupted_resume")
	}
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     "./cmd/partitur",
		TestPattern: testName,
		TestNames:   targets,
		Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}
