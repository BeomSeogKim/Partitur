//go:build mutation

package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationCommandMatrixWitnessReconciliation(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name             string
		source           string
		mutate           func(string) (string, error)
		failureSignature string
	}{
		{
			name:   "document count alone cannot complete the row",
			source: "docs/COMPLETION.md",
			mutate: replaceCommandWitnessText(
				"| Currently red: executed behavioural witnesses completed for 18 of the 96 parsed command-matrix catalog IDs. This row is green only when the witnessed and denominator counts are equal. |",
				"| Currently green: executed behavioural witnesses completed for 96 of the 96 parsed command-matrix catalog IDs. This row is green only when the witnessed and denominator counts are equal. |",
			),
			failureSignature: "COMPLETION states 96 completed command witnesses, executed witnesses completed 18",
		},
		{
			name:   "new returned witness requires a document count update",
			source: "cmd/partitur/command_matrix_witness_test.go",
			mutate: replaceCommandWitnessText(
				"\trunApproveCommandWitnesses(t, registry)\n\treconcileCommandWitnesses",
				"\trunApproveCommandWitnesses(t, registry)\n\tregistry.run(t, \"AMEND-001\", witnessDischarged, 0, func(*testing.T) {}) // mutation: document count not updated\n\treconcileCommandWitnesses",
			),
			failureSignature: "COMPLETION states 18 completed command witnesses, executed witnesses completed 19",
		},
		{
			name:   "completed ID must belong to the parsed denominator",
			source: "cmd/partitur/command_matrix_witness_test.go",
			mutate: replaceCommandWitnessText(
				`registry.run(t, "ANSWER-001", witnessDischarged`,
				`registry.run(t, "ANSWER-999", witnessDischarged`,
			),
			failureSignature: "completed command witnesses outside parsed denominator: stale=1 [ANSWER-999]",
		},
		{
			name:   "skipped fixture cannot record completion",
			source: "cmd/partitur/command_matrix_witness_test.go",
			mutate: replaceCommandWitnessText(
				"registry.run(t, \"ANSWER-001\", witnessDischarged, 0, func(t *testing.T) {\n\t\troot, store := resumeAttemptFixture(t)",
				"registry.run(t, \"ANSWER-001\", witnessDischarged, 0, func(t *testing.T) {\n\t\tt.Skip(\"mutation: fixture did not return\")\n\t\troot, store := resumeAttemptFixture(t)",
			),
			failureSignature: "COMPLETION states 18 completed command witnesses, executed witnesses completed 17",
		},
		{
			name:             "omitted fixture invocation cannot record completion",
			source:           "cmd/partitur/command_matrix_witness_test.go",
			mutate:           omitFirstCommandWitness,
			failureSignature: "COMPLETION states 18 completed command witnesses, executed witnesses completed 17",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertCommandWitnessMutationKilled(t, environment, mutation.source, mutation.mutate, mutation.failureSignature)
		})
	}
}

func replaceCommandWitnessText(before, after string) func(string) (string, error) {
	return func(contents string) (string, error) {
		if count := strings.Count(contents, before); count != 1 {
			return "", &commandWitnessMutationAnchorError{count: count, anchor: before}
		}
		return strings.Replace(contents, before, after, 1), nil
	}
}

func omitFirstCommandWitness(contents string) (string, error) {
	const startMarker = "\tregistry.run(t, \"ANSWER-001\""
	const endMarker = "\n\n\tregistry.run(t, \"ANSWER-002\""
	start := strings.Index(contents, startMarker)
	if start == -1 {
		return "", &commandWitnessMutationAnchorError{anchor: startMarker}
	}
	relativeEnd := strings.Index(contents[start:], endMarker)
	if relativeEnd == -1 {
		return "", &commandWitnessMutationAnchorError{anchor: endMarker}
	}
	end := start + relativeEnd + 2
	return contents[:start] + "\t// mutation: ANSWER-001 witness invocation omitted\n" + contents[end:], nil
}

type commandWitnessMutationAnchorError struct {
	count  int
	anchor string
}

func (err *commandWitnessMutationAnchorError) Error() string {
	return "command-witness mutation anchor count=" + strconv.Itoa(err.count) + ", want 1 for " + strconv.Quote(err.anchor)
}

func assertCommandWitnessMutationKilled(
	t *testing.T,
	environment mutationtest.GoEnvironment,
	source string,
	mutate func(string) (string, error),
	failureSignature string,
) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve command-witness mutation source directory")
	}
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	if err := copyDraftResultMutationRepository(copyRoot, repositoryRoot); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(copyRoot, source)
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	mutated, err := mutate(string(contents))
	if err != nil {
		t.Fatal(err)
	}
	if mutated == string(contents) {
		t.Fatal("command-witness mutation left its source unchanged")
	}
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(applied) != mutated {
		t.Fatal("command-witness mutation did not persist before the child test ran")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const testName = "TestCommandMatrixWitnesses"
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     "./cmd/partitur",
		TestPattern: testName,
		TestNames:   []string{testName},
		Environment: environment.ChildEnvironment(os.Environ()),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation outcome=%s, want %s: %s\n%s", result.Outcome, mutationtest.Killed, result.Reason, result.Diagnostic())
	}
	if !strings.Contains(result.Output, failureSignature) {
		t.Fatalf("mutation killed without expected signature %q\n%s", failureSignature, result.Diagnostic())
	}
	t.Logf("mutation positively reached %s and failed with %q", testName, failureSignature)
}
