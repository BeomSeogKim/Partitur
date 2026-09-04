//go:build mutation

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationFinalizationLifecycle(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, source, before, after, packageName, testName string
	}{
		{
			name:        "finalization rebuild loses C1 selection",
			source:      "internal/recovery/planner.go",
			before:      "if input.Projection.FinalizationEligible {",
			after:       "if false { // mutation",
			packageName: "./cmd/partitur", testName: "TestFinalizationProducerRoutesExactlyOnce",
		},
		{
			name:        "finalization accepts a non-draft score",
			source:      "internal/runstore/recovery.go",
			before:      `pinned == nil || pinned.Status() != "draft"`,
			after:       `pinned == nil || pinned.Status() != "finalized"`,
			packageName: "./cmd/partitur", testName: "TestFinalizationProducerRoutesExactlyOnce",
		},
		{
			name:        "finalization accepts a non-interview movement",
			source:      "internal/runstore/recovery.go",
			before:      "attempt.MovementID != runstate.MovementID(pinned.DraftInterviewMovement())",
			after:       "attempt.MovementID == runstate.MovementID(pinned.DraftInterviewMovement())",
			packageName: "./cmd/partitur", testName: "TestFinalizationProducerRoutesExactlyOnce",
		},
		{
			name:        "blocked interview is not terminal for finalization",
			source:      "internal/runstore/recovery.go",
			before:      "case runstate.AttemptBlocked, runstate.AttemptCompleted, runstate.AttemptFailed:",
			after:       "case runstate.AttemptCompleted, runstate.AttemptFailed:",
			packageName: "./cmd/partitur", testName: "TestFinalizationProducerRoutesExactlyOnce",
		},
		{
			name:        "core producer does not mark its amendment finalization",
			source:      "internal/amendmentexec/dispositioner.go",
			before:      "\t\tFinalization: proposal.finalization,\n\t})",
			after:       "\t\tFinalization: false,\n\t})",
			packageName: "./cmd/partitur", testName: "TestFinalizationProducerRoutesExactlyOnce",
		},
		{
			name:   "finalization rebuild loses its C1 race to the current attempt",
			source: "internal/recovery/planner.go",
			before: `	if input.Projection.FinalizationEligible {
		return action(CaseFinalizationRebuild, ActionRebuildFinalization, true)
	}
	if currentHeadAttempt(input.Projection) != nil {
		decision := action(CaseContinue, ActionProceedAttempt, false)
		decision.Action.Continuation = ContinuationC2
		return decision
	}
`,
			after: `	if currentHeadAttempt(input.Projection) != nil {
		decision := action(CaseContinue, ActionProceedAttempt, false)
		decision.Action.Continuation = ContinuationC2
		return decision
	}
	if input.Projection.FinalizationEligible {
		return action(CaseFinalizationRebuild, ActionRebuildFinalization, true)
	}
`,
			packageName: "./internal/recovery", testName: "TestFinalizationRebuildPrecedesCurrentAttempt",
		},
		{
			name:        "finalization approval does not succeed the interview movement",
			source:      "internal/runstate/apply.go",
			before:      "\t\t\tmovements[event.MovementID] = MovementSucceeded\n\t\t}",
			after:       "\t\t\tmovements[event.MovementID] = MovementPending\n\t\t}",
			packageName: "./cmd/partitur", testName: "TestDraftFinalizationLifecycle",
		},
		{
			name:        "finalization rebuild has no executor handler",
			source:      "internal/recoveryexec/executor.go",
			before:      "if kind == recovery.ActionRebuildFinalization {",
			after:       "if false { // mutation",
			packageName: "./cmd/partitur", testName: "TestFinalizationProducerRoutesExactlyOnce",
		},
		{
			name:        "finalization ignores a missing verification expectation",
			source:      "internal/runstore/recovery.go",
			before:      "if !pinned.Execution().VerificationExpectationPresent {",
			after:       "if false { // mutation",
			packageName: "./cmd/partitur", testName: "TestDraftResultBoundaryKillCuts",
		},
		{
			name:        "delivered score base revision cannot pass the stale check",
			source:      "internal/driver/driver.go",
			before:      `"base_revision": float64(compiled.Revision()),`,
			after:       `"base_revision": float64(compiled.Revision() + 1),`,
			packageName: "./cmd/partitur", testName: "TestDraftInterviewConvergesThroughDeliveredScoreBase",
		},
		{
			name:        "delivered score base hash cannot pass the stale check",
			source:      "internal/driver/driver.go",
			before:      `"base_hash":     baseHash,`,
			after:       `"base_hash":     strings.Replace(baseHash, "sha256:", "sha256:mutated", 1),`,
			packageName: "./cmd/partitur", testName: "TestDraftInterviewConvergesThroughDeliveredScoreBase",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertFinalizationMutationKilled(
				t,
				environment,
				mutation.source,
				mutation.before,
				mutation.after,
				mutation.packageName,
				mutation.testName,
			)
		})
	}
}

func assertFinalizationMutationKilled(t *testing.T, environment mutationtest.GoEnvironment, source, before, after, packageName, testName string) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve finalization mutation source directory")
	}
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyDraftResultMutationRepository(copyRoot, filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(copyRoot, source)
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count=%d, want 1 for %q", count, before)
	}
	mutated := strings.Replace(string(contents), before, after, 1)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(applied) != mutated {
		t.Fatal("mutation did not persist")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	testNames := []string{testName}
	if testName == "TestDraftResultBoundaryKillCuts" {
		testNames = append(testNames,
			"TestDraftResultBoundaryKillCuts/lifecycle.draft_performer_completed_to_no_blocking_failure/performer_completed",
			"TestDraftResultBoundaryKillCuts/lifecycle.draft_performer_completed_to_no_blocking_failure/attempt_failed",
		)
	}
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     packageName,
		TestPattern: testName,
		TestNames:   testNames,
		Environment: environment.ChildEnvironment(os.Environ(), "GOFLAGS=-tags=faultprobe"),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
	terminal := finalizationMutationTerminalLine(result.Output, testName)
	if terminal == "" {
		t.Fatalf("mutation terminal line is absent\n%s", result.Diagnostic())
	}
	t.Logf("mutation %s terminal: %s", testName, terminal)
}

func finalizationMutationTerminalLine(output, testName string) string {
	decoder := json.NewDecoder(strings.NewReader(output))
	last := ""
	for {
		var event struct {
			Test   string
			Output string
		}
		if err := decoder.Decode(&event); err != nil {
			break
		}
		if event.Test != testName && !strings.HasPrefix(event.Test, testName+"/") {
			continue
		}
		for _, line := range strings.Split(event.Output, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.Contains(trimmed, ".go:") {
				last = trimmed
			}
		}
	}
	return last
}
