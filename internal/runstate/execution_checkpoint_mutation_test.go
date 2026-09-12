//go:build mutation

package runstate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

// The evidence-based clamp formula and the checkpoint projection are load-bearing:
// mutating either must fail the tests that pin #442(ii)'s accounting.
func TestMutationEvidenceBasedClampLock(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, path, before, after, explanation, testName string
	}{
		{
			name:        "clamp drops the accounting grace",
			path:        "internal/runstate/types.go",
			before:      "charge := checkpointMS + AccountingGraceMS",
			after:       "charge := checkpointMS",
			explanation: "the clamp no longer adds the 35s accounting grace to the checkpoint baseline",
			testName:    "TestClampedChargeAddsTheAccountingGrace",
		},
		{
			name:        "clamp inverts its remaining bound",
			path:        "internal/runstate/types.go",
			before:      "if charge > remainingAtStart {",
			after:       "if charge < remainingAtStart {",
			explanation: "the clamp no longer bounds the charge by remaining_at_start",
			testName:    "TestClampedChargeIsBoundedByRemaining",
		},
		{
			name:        "clamp close hides the checkpoint audit id",
			path:        "internal/runstate/types.go",
			before:      "if interval.LatestCheckpointEventID != \"\" {",
			after:       "if interval.LatestCheckpointEventID == \"\" {",
			explanation: "elapsed_checkpoint_event_id is no longer recorded from the read checkpoint",
			testName:    "TestClampedCloseFieldsCarryCheckpointBaselineAndGrace",
		},
		{
			name:        "checkpoint projection stops requiring a strict increase",
			path:        "internal/runstate/apply.go",
			before:      "if cumulative <= state.OpenExecution.LatestCheckpointMS {",
			after:       "if cumulative < state.OpenExecution.LatestCheckpointMS {",
			explanation: "cumulative_elapsed_ms is no longer required to strictly increase",
			testName:    "TestElapsedCheckpointedRecordsLatestBaselineAndMustStrictlyIncrease",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			runExecutionCheckpointMutation(t, environment, mutation.path, mutation.before, mutation.after, mutation.explanation, mutation.testName)
		})
	}
}

func runExecutionCheckpointMutation(
	t *testing.T,
	environment mutationtest.GoEnvironment,
	path, before, after, explanation, testName string,
) {
	t.Helper()

	copyRoot := copyRunstateMutationRepository(t)
	mutationPath := filepath.Join(copyRoot, path)
	contents, err := os.ReadFile(mutationPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1 for %q", count, before)
	}
	mutated := strings.Replace(string(contents), before, after, 1)
	if err := os.WriteFile(mutationPath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstate"),
		Package:     ".",
		TestPattern: testName,
		TestNames:   []string{testName},
		Environment: environment.ChildEnvironment(os.Environ()),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s; %s\n%s", explanation, result.Reason, result.Diagnostic())
	}
}
