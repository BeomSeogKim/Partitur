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

func TestMutationDerivedEventSourceProjectionLock(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, path, before, after, explanation, testName string
	}{
		{
			name:        "derived classification cannot lose a marking",
			path:        "docs/DESIGN.md",
			before:      "| `movement.cancelled` *derived* |",
			after:       "| `movement.cancelled` |",
			explanation: "Appendix B derived classification no longer matches Go",
		},
		{
			name:        "terminal projection cannot lose its assigned marking",
			path:        "docs/DESIGN.md",
			before:      "| `run.succeeded` | ✓ | run_id | Run `RUNNING` | Run → `SUCCEEDED`. On the **waived** path also carries the full candidate payload and binding (§8). **Run-terminal source:** `always` |",
			after:       "| `run.succeeded` | ✓ | run_id | Run `RUNNING` | Run → `SUCCEEDED`. On the **waived** path also carries the full candidate payload and binding (§8). |",
			explanation: "a terminal Go projection can disappear from the marked denominator",
		},
		{
			name:        "derived source cannot drift from its authoritative event",
			path:        "docs/DESIGN.md",
			before:      "Derives from `amendment.approved` **or** from every Appendix B row carrying a **Run-terminal source:** marking",
			after:       "Derives from `run.started` **or** from every Appendix B row carrying a **Run-terminal source:** marking",
			explanation: "a parsed source transition can lose its executed fixture",
		},
		{
			name:        "source requires a production append site",
			path:        "internal/runstore/cancellation.go",
			before:      "Type: runstate.EventRunCancelled, Payload: payload,",
			after:       "Type: runstate.EventCancelRequested, Payload: payload,",
			explanation: "run.cancelled no longer has a non-test append site",
		},
		{
			name:        "executed cancellation fixture observes the derived effect",
			path:        "internal/runstate/apply.go",
			before:      "state.Movements[MovementID(id)] = MovementCancelled",
			after:       "state.Movements[MovementID(id)] = MovementPending",
			explanation: "the executed source fixture no longer observes movement cancellation",
			testName:    "TestDerivedEventSourceProjectionLock/movement.cancelled_<-_run.cancelled_[]",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			testName := mutation.testName
			if testName == "" {
				testName = "TestDerivedEventSourceProjectionLock"
			}
			runDerivedEventSourceProjectionMutation(t, environment, mutation.path, mutation.before, mutation.after, mutation.explanation, testName)
		})
	}
}

func runDerivedEventSourceProjectionMutation(
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
	applied, err := os.ReadFile(mutationPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(applied) != mutated {
		t.Fatal("mutation did not persist")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstate"),
		Package:     ".",
		TestPattern: "TestDerivedEventSourceProjectionLock",
		TestNames:   []string{testName},
		Environment: environment.ChildEnvironment(os.Environ()),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s; %s\n%s", explanation, result.Reason, result.Diagnostic())
	}
}
