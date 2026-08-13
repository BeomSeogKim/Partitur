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

func TestMutationAcceptanceSubjectPinnedToStarted(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, source, before, after, testName, failureSignature string
	}{
		{
			name:             "left boundary is not exposed after the durable ref",
			source:           "internal/driver/driver.go",
			before:           "\t\tdependencies.probe.Reached(faultpoint.PointAcceptanceSubjectPinned)\n",
			after:            "",
			testName:         "TestAcceptanceSubjectProbeOracle",
			failureSignature: "acceptance.subject_pinned emitted 0 times, want exactly once",
		},
		{
			name:             "recovery dispatch omits acceptance start",
			source:           "internal/recoveryexec/handlers.go",
			before:           "\t\trecovery.ActionAppendAcceptanceStarted:      appendAcceptanceStarted,\n",
			after:            "",
			testName:         "TestAcceptanceSubjectPinnedToStartedKillCuts/subject_pinned",
			failureSignature: "recovery action is unreachable in this slice: append_acceptance_started",
		},
		{
			name:             "recovery does not recreate the missing subject ref",
			source:           "internal/workspace/recovery.go",
			before:           "\treturn attempt.CaptureAcceptanceSubject()\n",
			after:            "\t_ = attempt\n\treturn AcceptanceSubject{Tree: input.BaseTree}, nil // mutation\n",
			testName:         "TestAcceptanceSubjectPinnedToStartedKillCuts/subject_pinned_ref_lost",
			failureSignature: `reason="recovery_subject_mismatch"`,
		},
		{
			name:             "started presence is required",
			source:           "cmd/partitur/acceptance_subject_boundary_test.go",
			before:           "\tif started != wantStartedCount {\n",
			after:            "\tif started > wantStartedCount { // mutation\n",
			testName:         "TestAcceptanceSubjectEndpointValidator/reject_missing_started",
			failureSignature: "validator error=<nil>, wantError=true",
		},
		{
			name:             "started cardinality is exact",
			source:           "cmd/partitur/acceptance_subject_boundary_test.go",
			before:           "\tif started != wantStartedCount {\n",
			after:            "\tif started < wantStartedCount { // mutation\n",
			testName:         "TestAcceptanceSubjectEndpointValidator/reject_duplicate_started",
			failureSignature: "validator error=<nil>, wantError=true",
		},
		{
			name:             "started subject tree is not bound to the ref",
			source:           "cmd/partitur/acceptance_subject_boundary_test.go",
			before:           "\t\t\tif payload.SubjectTree != subjectTree {\n",
			after:            "\t\t\tif false { // mutation\n",
			testName:         "TestAcceptanceSubjectEndpointValidator/reject_wrong_subject_tree",
			failureSignature: "validator error=<nil>, wantError=true",
		},
		{
			name:             "worktree loss failure is required",
			source:           "cmd/partitur/acceptance_subject_boundary_test.go",
			before:           "\tif failedLost != wantLostCount {\n",
			after:            "\tif failedLost > wantLostCount { // mutation\n",
			testName:         "TestAcceptanceSubjectEndpointValidator/reject_missing_worktree_lost",
			failureSignature: "validator error=<nil>, wantError=true",
		},
		{
			name:             "worktree loss cardinality is exact",
			source:           "cmd/partitur/acceptance_subject_boundary_test.go",
			before:           "\tif failedLost != wantLostCount {\n",
			after:            "\tif failedLost < wantLostCount { // mutation\n",
			testName:         "TestAcceptanceSubjectEndpointValidator/reject_duplicate_worktree_lost",
			failureSignature: "validator error=<nil>, wantError=true",
		},
		{
			name:             "worktree loss reason is bound",
			source:           "cmd/partitur/acceptance_subject_boundary_test.go",
			before:           "\t\t\tif payload.Reason == \"worktree_lost\" {\n",
			after:            "\t\t\tif true || payload.Reason == \"worktree_lost\" { // mutation\n",
			testName:         "TestAcceptanceSubjectEndpointValidator/reject_other_failure_reason",
			failureSignature: "validator error=<nil>, wantError=true",
		},
		{
			name:             "subject ref must resolve to a tree",
			source:           "cmd/partitur/acceptance_subject_boundary_test.go",
			before:           "\tif err != nil {\n\t\treturn \"\", fmt.Errorf(\"subject ref %q is not recoverable: %w: %s\", ref, err, strings.TrimSpace(string(output)))\n\t}\n",
			after:            "\tif false && err != nil { // mutation\n\t\treturn \"\", fmt.Errorf(\"subject ref %q is not recoverable: %w: %s\", ref, err, strings.TrimSpace(string(output)))\n\t}\n",
			testName:         "TestAcceptanceSubjectRefValidatorRejectsMissingRef",
			failureSignature: "missing subject ref was accepted",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertAcceptanceSubjectMutationKilled(t, environment, mutation.source, mutation.before, mutation.after, mutation.testName, mutation.failureSignature)
		})
	}
}

func assertAcceptanceSubjectMutationKilled(t *testing.T, environment mutationtest.GoEnvironment, source, before, after, testName, failureSignature string) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve acceptance subject mutation source directory")
	}
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyDraftResultMutationRepository(copyRoot, filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(copyRoot, source)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count=%d, want 1 for %q", count, before)
	}
	mutated := strings.Replace(string(contents), before, after, 1)
	if err := os.WriteFile(path, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	// This child builds three fixture binaries before running a real adapter.
	// Keep the outer bound well above the harness's 15-second missing-probe bound
	// so a timeout cannot be classified as a mutation kill.
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir: copyRoot, Package: "./cmd/partitur", TestPattern: testName,
		TestNames:   []string{testName},
		Environment: environment.ChildEnvironment(os.Environ(), "GOFLAGS=-tags=faultprobe"),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation survived: %s\n%s", result.Reason, result.Diagnostic())
	}
	if !strings.Contains(decodedChildOutput(result.Output), failureSignature) {
		t.Fatalf("mutation failed for the wrong reason; want signature %q\n%s", failureSignature, result.Diagnostic())
	}
}

// decodedChildOutput joins the Output fields of a `go test -json` stream.
// Signatures are matched against this rather than against Result.Output,
// which is the raw stream. In the raw stream a message is a JSON string
// literal, so the angle brackets a Go %v prints around a nil error are
// escaped and a readable signature never matches - every mutation would
// then be reported as failing for the wrong reason even when it is the
// right one, which is how this was found.
func decodedChildOutput(stream string) string {
	var decoded strings.Builder
	for _, line := range strings.Split(stream, "\n") {
		if !strings.HasPrefix(line, "{") {
			decoded.WriteString(line)
			decoded.WriteString("\n")
			continue
		}
		var event struct {
			Output string `json:"Output"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		decoded.WriteString(event.Output)
	}
	return decoded.String()
}
