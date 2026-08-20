//go:build mutation

package main

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

// statedCommandWitnessCount reads the completion count out of the document the
// reconciliation checks against, so a batch of new witnesses updates one place
// rather than two. Hardcoding it here let the anchors go stale silently: the
// substitution matched nothing, no mutation landed, and the proof reported a
// missing signature rather than a missing mutation.
func statedCommandWitnessCount(t *testing.T) int {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), "docs", "COMPLETION.md"))
	if err != nil {
		t.Fatal(err)
	}
	matches := commandWitnessCountPattern.FindStringSubmatch(string(contents))
	if matches == nil {
		t.Fatal("COMPLETION.md states no command-witness completion count")
	}
	count, err := strconv.Atoi(matches[1])
	if err != nil {
		t.Fatal(err)
	}
	return count
}

var commandWitnessCountPattern = regexp.MustCompile(`executed behavioural witnesses completed for (\d+) of the 96 parsed`)

// firstUnwitnessedCatalogID picks a real ID the witness file does not register,
// so the "+1 without a document update" mutation stays valid as batches land.
// Naming one directly broke the moment its batch arrived: registering an
// already-registered ID fails as a duplicate, not as a count mismatch, and the
// proof reported a missing signature rather than a stale anchor.
func firstUnwitnessedCatalogID(t *testing.T) string {
	t.Helper()

	registered, err := os.ReadFile(filepath.Join(repositoryRoot(t), "cmd", "partitur", "command_matrix_witness_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range commandMatrixCatalogIDs(t) {
		if !strings.Contains(string(registered), "\""+id+"\"") {
			return id
		}
	}
	t.Fatal("every catalog ID is registered; this mutation must become a removal instead")
	return ""
}

func TestMutationCommandMatrixWitnessReconciliation(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	stated := statedCommandWitnessCount(t)
	unwitnessed := firstUnwitnessedCatalogID(t)
	statedRow := "| Currently red: executed behavioural witnesses completed for " + strconv.Itoa(stated) + " of the 96 parsed command-matrix catalog IDs. This row is green only when the witnessed and denominator counts are equal. |"
	countSignature := func(document, executed int) string {
		return "COMPLETION states " + strconv.Itoa(document) + " completed command witnesses, executed witnesses completed " + strconv.Itoa(executed)
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
				statedRow,
				"| Currently green: executed behavioural witnesses completed for 96 of the 96 parsed command-matrix catalog IDs. This row is green only when the witnessed and denominator counts are equal. |",
			),
			failureSignature: countSignature(96, stated),
		},
		{
			name:   "new returned witness requires a document count update",
			source: "cmd/partitur/command_matrix_witness_test.go",
			mutate: replaceCommandWitnessText(
				"\trunInitCommandWitnesses(t, registry)\n\treconcileCommandWitnesses",
				"\trunInitCommandWitnesses(t, registry)\n\tregistry.run(t, \""+unwitnessed+"\", witnessDischarged, 0, func(*testing.T) {}) // mutation: document count not updated\n\treconcileCommandWitnesses",
			),
			failureSignature: countSignature(stated, stated+1),
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
				"registry.run(t, \"VERSION-001\", witnessDischarged, 0, func(t *testing.T) {\n\t\troot := t.TempDir()",
				"registry.run(t, \"VERSION-001\", witnessDischarged, 0, func(t *testing.T) {\n\t\tt.Skip(\"mutation: fixture did not return\")\n\t\troot := t.TempDir()",
			),
			failureSignature: countSignature(stated, stated-1),
		},
		{
			name:             "omitted fixture invocation cannot record completion",
			source:           "cmd/partitur/command_matrix_witness_test.go",
			mutate:           omitFirstCommandWitness,
			failureSignature: countSignature(stated, stated-1),
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
