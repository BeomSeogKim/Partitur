//go:build mutation

package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationDraftResultBoundary(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, source, before, after, packageName, testName string
	}{
		{
			name:        "empty draft result no longer writes its failure",
			source:      "internal/driver/driver.go",
			before:      "if execution.Score.Status() == \"draft\" && movement.ID == execution.Score.DraftInterviewMovement() && len(report.Raised) == 0 {",
			after:       "if false {",
			packageName: "./cmd/partitur",
			testName:    "TestDraftResultBoundary/empty_result_writes_failure",
		},
		{
			name:   "draft question no longer blocks",
			source: "internal/adapter/execute.go",
			before: `		state.report.Raised = append(state.report.Raised, RaisedDecision{Kind: protocol.EventQuestion, Question: &question})
		state.report.Questions = append(state.report.Questions, *event)
`,
			after: `		state.report.Raised = append(state.report.Raised, RaisedDecision{Kind: protocol.EventQuestion, Question: &question})
		state.report.Questions = append(state.report.Questions, *event)
		delete(state.blockingIDs, event.ID)
`,
			packageName: "./cmd/partitur",
			testName:    "TestDraftResultBoundary/blocking_question_remains_blocked",
		},
		{
			name:   "draft blocking proposal no longer blocks",
			source: "internal/adapter/execute.go",
			before: `		if event.RequiresDecision {
			state.blockingIDs[event.ID] = true
		}
`,
			after: `		if false {
			state.blockingIDs[event.ID] = true
		}
`,
			packageName: "./cmd/partitur",
			testName:    "TestDraftResultBoundary/blocking_proposal_remains_blocked",
		},
		{
			name:        "empty draft result can enter acceptance",
			source:      "internal/driver/driver.go",
			before:      "len(report.Raised) == 0",
			after:       "len(report.Raised) < 0",
			packageName: "./cmd/partitur",
			testName:    "TestDraftResultBoundary/empty_result_never_starts_acceptance",
		},
		{
			name:        "draft recovery requires a completed performer",
			source:      "internal/recovery/planner.go",
			before:      "if attempt.State == runstate.AttemptVerifying &&\n",
			after:       "if ",
			packageName: "./internal/recovery",
			testName:    "TestPlanC2RowsAndAdjacentStates/probed_draft_interview_before_performer_completion_remains_incomplete",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertDraftResultMutationKilled(t, environment, mutation.source, mutation.before, mutation.after, mutation.packageName, mutation.testName)
		})
	}
}

func TestMutationLiveBlockingDecisionRequests(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, source, before, after, packageName, testName string
	}{
		{
			name: "live question request append is skipped", source: "internal/driver/driver.go",
			before:      `_, err = authority.Append(event, "attempt.decision.requested.question")`,
			after:       `_ = event; err = nil // mutation: skip live question request`,
			packageName: "./cmd/partitur", testName: "TestDraftResultBoundary/blocking_question_remains_blocked",
		},
		{
			name: "live proposal request append is skipped", source: "internal/driver/driver.go",
			before:      `_, err = authority.Append(event, "amendment.decision.requested")`,
			after:       `_ = event; err = nil // mutation: skip live proposal request`,
			packageName: "./cmd/partitur", testName: "TestDraftResultBoundary/blocking_proposal_remains_blocked",
		},
		{
			name: "live request materialization loop is skipped", source: "internal/driver/driver.go",
			before:      `for _, appendRequest := range appendRequests {`,
			after:       `for _, appendRequest := range appendRequests[:0] { // mutation: skip all live requests`,
			packageName: "./cmd/partitur", testName: "TestDraftResultBoundary/blocking_questions_remains_blocked",
		},
		{
			name: "waiting ignores a missing blocked proposal route", source: "internal/recovery/planner.go",
			before: `if _, ok := firstBlockedProposalRoute(projection.BlockedProposalRoutes); ok {
		return true
	}`,
			after: `if _, ok := firstBlockedProposalRoute(projection.BlockedProposalRoutes); ok {
		return false // mutation: ignore missing blocked proposal route
	}`,
			packageName: "./internal/recovery", testName: "TestPlanWaitsOnlyAfterEveryDecisionRequestIsDurable/missing_blocked_proposal_route",
		},
		{
			name: "waiting ignores a missing routed proposal request", source: "internal/recovery/planner.go",
			before: `if _, ok := firstMissingRoutedRequest(projection.State); ok {
		return true
	}`,
			after: `if _, ok := firstMissingRoutedRequest(projection.State); ok {
		return false // mutation: ignore missing routed proposal request
	}`,
			packageName: "./internal/recovery", testName: "TestPlanWaitsOnlyAfterEveryDecisionRequestIsDurable/missing_routed_proposal_request",
		},
		{
			name: "waiting ignores a missing question request", source: "internal/recovery/planner.go",
			before: `_, ok := firstMissingQuestionRequest(attempt.QuestionRequests)
	return ok`,
			after: `_, ok := firstMissingQuestionRequest(attempt.QuestionRequests)
	return false && ok // mutation: ignore missing question request`,
			packageName: "./internal/recovery", testName: "TestPlanWaitsOnlyAfterEveryDecisionRequestIsDurable/missing_question_request",
		},
		{
			name: "waiting invents a missing source without an attempt", source: "internal/recovery/planner.go",
			before: `func hasMissingDecisionRequest(projection Projection) bool {
	if _, ok := firstBlockedProposalRoute(projection.BlockedProposalRoutes); ok {
		return true
	}
	if _, ok := firstMissingRoutedRequest(projection.State); ok {
		return true
	}
	attempt := currentHeadAttempt(projection)
	if attempt == nil {
		return false
	}
	_, ok := firstMissingQuestionRequest(attempt.QuestionRequests)
	return ok
}`,
			after: `func hasMissingDecisionRequest(projection Projection) bool {
	if _, ok := firstBlockedProposalRoute(projection.BlockedProposalRoutes); ok {
		return true
	}
	if _, ok := firstMissingRoutedRequest(projection.State); ok {
		return true
	}
	attempt := currentHeadAttempt(projection)
	if attempt == nil {
		return true // mutation: invent missing source
	}
	_, ok := firstMissingQuestionRequest(attempt.QuestionRequests)
	return ok
}`,
			packageName: "./internal/recovery", testName: "TestPlanWaitsOnlyAfterEveryDecisionRequestIsDurable/no_decision_source_is_missing",
		},
		{
			name: "waiting ignores source completeness", source: "internal/recovery/planner.go",
			before:      `!hasMissingDecisionRequest(input.Projection) {`,
			after:       `true { // mutation: ignore request completeness`,
			packageName: "./cmd/partitur", testName: "TestLiveBlockingRequestCrashReachesFixedPoint",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertDraftResultMutationKilled(t, environment, mutation.source, mutation.before, mutation.after, mutation.packageName, mutation.testName)
		})
	}
}

func assertDraftResultMutationKilled(
	t *testing.T,
	environment mutationtest.GoEnvironment,
	source, before, after, packageName, testName string,
) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve command test source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyDraftResultMutationRepository(copyRoot, repository); err != nil {
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
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     packageName,
		TestPattern: testName,
		TestNames:   []string{testName},
		Environment: environment.ChildEnvironment(os.Environ(), "GOFLAGS=-tags=faultprobe"),
	})
	if result.Outcome == mutationtest.Killed {
		t.Logf("mutation %s terminal: %s", testName, draftResultMutationTerminalLine(result.Output, testName))
		return
	}
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestMutationDraftResultRecoveryPrecedence(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve command test source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyDraftResultMutationRepository(copyRoot, repository); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(copyRoot, "internal/recovery/planner.go")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	const block = `	if attempt.State == runstate.AttemptVerifying &&
		input.Projection.Scheduler.Status == "draft" &&
		attempt.MovementID == input.Projection.Scheduler.DraftInterviewMovement &&
		!attempt.AcceptanceStarted {
		return recoveryFailureAction(
			CaseDraftNoBlockingOutput,
			ActionAppendDraftNoBlockingFailure,
			attempt.AttemptID,
			"task_failed",
			"draft_no_blocking_output",
			nil,
		)
	}
`
	const afterPostHocVerification = `	if attempt.State == runstate.AttemptVerifying && (attempt.ChangeSetRecorded || hasVerificationPassed(input.Projection.State, attempt.AttemptID)) && !attempt.AcceptanceStarted {
`
	const captureChangeSet = `	if attempt.State == runstate.AttemptVerifying && movementHasRepoWrite(input.Projection.State, attempt.MovementID) && !attempt.ChangeSetRecorded {
`
	const postHocVerification = `	if attempt.State == runstate.AttemptVerifying && !hasVerificationPassed(input.Projection.State, attempt.AttemptID) {
`
	if count := strings.Count(string(contents), block); count != 1 {
		t.Fatalf("draft recovery block count=%d, want 1", count)
	}
	withoutBlock := strings.Replace(string(contents), block, "", 1)
	if count := strings.Count(withoutBlock, afterPostHocVerification); count != 1 {
		t.Fatalf("post-verification anchor count=%d, want 1", count)
	}
	if count := strings.Count(withoutBlock, captureChangeSet); count != 1 {
		t.Fatalf("change-set capture anchor count=%d, want 1", count)
	}
	if count := strings.Count(withoutBlock, postHocVerification); count != 1 {
		t.Fatalf("post-hoc verification anchor count=%d, want 1", count)
	}
	mutated := strings.Replace(withoutBlock, afterPostHocVerification, block+afterPostHocVerification, 1)
	blockPosition := strings.Index(mutated, block)
	if blockPosition < strings.Index(mutated, captureChangeSet) || blockPosition < strings.Index(mutated, postHocVerification) {
		t.Fatal("draft recovery block did not move below capture and verification")
	}
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
	const testName = "TestAppendixC41SelectsThisPlanner/RA-061"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     "./internal/recovery",
		TestPattern: testName,
		TestNames:   []string{testName},
		Environment: environment.ChildEnvironment(os.Environ(), "GOFLAGS=-tags=faultprobe"),
	})
	if result.Outcome == mutationtest.Killed {
		t.Logf("mutation %s terminal: %s", testName, draftResultMutationTerminalLine(result.Output, testName))
		return
	}
	t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
}

func draftResultMutationTerminalLine(output, testName string) string {
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
		if event.Test != testName || event.Output == "" {
			continue
		}
		for _, line := range strings.Split(event.Output, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "--- FAIL:") {
				last = trimmed
			}
		}
	}
	return last
}

func copyDraftResultMutationRepository(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ".git" || relative == ".codegraph" {
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
