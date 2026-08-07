//go:build mutation

package amendmentexec

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

func TestMutationDispositionerGuards(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, source, before, after, target string
	}{
		{"blocking rejection closes derived decision", "internal/amendmentexec/dispositioner.go", "if proposal.Event.RequiresDecision || proposal.humanDecision {", "if false { // mutation", "TestDispositionerRejectsBlockingProposalBeforeAttemptBlocked"},
		{"frozen descriptor retains evaluated reason", "internal/amendmentexec/dispositioner.go", `"reason": outcome.Reason, "decision_type": "amendment",`, `"reason": "auto_disabled", "decision_type": "amendment",`, "TestDispositionerPublishesFrozenRouteThenAppendsItAfterDriverSource"},
		{"pending prepare guard refuses a second prepare", "internal/amendmentexec/dispositioner.go", "if state.PendingPrepare != nil {", "if false { // mutation", "TestDispositionerPreparesAutoApproval"},
		{"routed marker follows blocked source", "internal/driver/driver.go", "for _, appendRoute := range appendRoutes {", "for _, appendRoute := range appendRoutes[:0] { // mutation", "TestDispositionerAppendsRoutedHumanAfterDriverBlockedSource"},
		{"recovery preserves the frozen descriptor", "internal/recoveryconsequence/consequence.go", `payload["emitted_id"] = immutable.EmittedID`, `payload["emitted_id"] = "invented"`, "TestRecoveryCompletesFrozenBlockingProposalRouteThenRequest"},
		{"recovery request reads routed reason", "internal/recoveryconsequence/consequence.go", `"routed_reason": routed["reason"],`, `"routed_reason": "invented",`, "TestRecoveryCompletesFrozenBlockingProposalRouteThenRequest"},
		{"human decision re-run bypasses routing but retains audit", "internal/amendmentexec/dispositioner.go", "RequiresDecision: proposal.Event.RequiresDecision, HumanDecision: proposal.humanDecision,", "RequiresDecision: proposal.Event.RequiresDecision, HumanDecision: false,", "TestApproveRoutedPreparesHumanApprovalFromImmutableRecord"},
		{"human approval rechecks the immutable proposal bytes", "internal/amendmentexec/dispositioner.go", "if !ok || rawHash(contents) != recordHash {", "if !ok && rawHash(contents) != recordHash { // mutation", "TestApproveRoutedRejectsTamperedImmutableRecord"},
		{"human approval validates routed decision under the projected-state lock", "internal/amendmentexec/dispositioner.go", "if err := verifyRoutedProposal(proposal.Store, proposal.RunID, proposal.DecisionID, proposal.ProposalID, proposal.immutableRecord, state); err != nil {", "if err := error(nil); err != nil { // mutation", "TestApproveRoutedRequiresLiveRoutedDecision"},
		{"human approval resolves rather than obsoletes its own decision", "internal/amendmentexec/dispositioner.go", "if decisionID != nil {", "if false { // mutation", "TestApproveRoutedPreparesHumanApprovalFromImmutableRecord"},
		{"projector excludes the directly resolved human decision", "internal/runstate/apply.go", "obsoleted = pendingDecisionIDsExcept(state, *state.PendingPrepare.DecisionID)", "obsoleted = pendingDecisionIDs(state) // mutation", "TestApproveRoutedPreparesHumanApprovalFromImmutableRecord"},
		{"human audit preserves the evaluated guard reason", "internal/amendmentexec/dispositioner.go", `evaluation["guard_failure_reason"] = outcome.Reason`, `evaluation["guard_failure_reason"] = "runtime_scope_started"`, "TestEnvelopeEvaluationForPreservesDecisionTimeGuardReason"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertMutationKilled(t, environment, mutation.source, mutation.before, mutation.after, mutation.target)
		})
	}
}

func assertMutationKilled(t *testing.T, environment mutationtest.GoEnvironment, sourceName, before, after, target string) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve amendmentexec source directory")
	}
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyMutationRepository(copyRoot, filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(copyRoot, sourceName)
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
	result := mutationtest.Run(ctx, mutationtest.Child{Dir: copyRoot, Package: "./internal/amendmentexec", TestPattern: target, TestNames: []string{target}, Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1")})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation survived: %s", result.Diagnostic())
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
