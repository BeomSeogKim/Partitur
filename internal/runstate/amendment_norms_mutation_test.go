//go:build mutation

package runstate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationBlockingProposalRouteRequiresExactTerminalPath(t *testing.T) {
	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	copyRoot := copyRunstateMutationRepository(t)
	sourcePath := filepath.Join(copyRoot, "internal", "runstate", "apply.go")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "\t\tif routed == rejected {\n"
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, "\t\tif false {\n", 1)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstate"),
		Package:     ".",
		TestPattern: "TestAttemptBlockedBlockingProposalRouteMatchesPriorEvaluation",
		TestNames: []string{
			"TestAttemptBlockedBlockingProposalRouteMatchesPriorEvaluation/passed_proposal_without_route",
			"TestAttemptBlockedBlockingProposalRouteMatchesPriorEvaluation/rejected_proposal_with_route",
		},
		Environment: goEnvironment.ChildEnvironment(os.Environ()),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: blocking proposal route no longer required its exact terminal path\n%s", result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestMutationBlockingProposalRouteIsAdmittedByPayloadSchema(t *testing.T) {
	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	copyRoot := copyRunstateMutationRepository(t)
	sourcePath := filepath.Join(copyRoot, "internal", "runstate", "apply.go")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "\t\t\toptional = []string{\"route\"}\n"
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, "\t\t\toptional = nil\n", 1)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstate"),
		Package:     ".",
		TestPattern: "TestAttemptBlockedBlockingProposalRouteMatchesPriorEvaluation/passed_proposal_carries_frozen_route",
		TestNames:   []string{"TestAttemptBlockedBlockingProposalRouteMatchesPriorEvaluation/passed_proposal_carries_frozen_route"},
		Environment: goEnvironment.ChildEnvironment(os.Environ()),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: attempt.blocked no longer admits a route descriptor\n%s", result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestMutationRoutedProposalE2RowsStayPinned(t *testing.T) {
	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	copyRoot := copyRunstateMutationRepository(t)
	designPath := filepath.Join(copyRoot, "docs", "DESIGN.md")
	contents, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "| `proposal.blocked_route_to_routed` | blocking `attempt.blocked` route descriptor appended `R` | matching `amendment.routed_human` appended `R` | §4 blocking handshake; C.1 `RC-RESUME-049` | Recovery verifies the descriptor-named immutable record and appends the frozen route idempotently; it never re-runs §9 or derives a route from an unreferenced record |"
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, strings.Replace(anchor, "RC-RESUME-049", "RC-RESUME-048", 1), 1)
	if err := os.WriteFile(designPath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstate"),
		Package:     ".",
		TestPattern: "TestRoutedProposalE2EdgesAreSpecified",
		TestNames:   []string{"TestRoutedProposalE2EdgesAreSpecified"},
		Environment: goEnvironment.ChildEnvironment(os.Environ()),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: routed-proposal E.2 row no longer matched its normative guard\n%s", result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestMutationDraftResultE2RationaleKeepsAcceptanceClosed(t *testing.T) {
	runDraftResultNormsMutation(t,
		"; **acceptance never begins**. A genuinely blocking",
		"; acceptance may begin. A genuinely blocking",
		"TestDraftNoBlockingOutputRecoveryEdgeIsSpecified",
		"the draft-result E.2 rationale no longer closes acceptance")
}

func TestMutationDraftResultC2OwnerCannotDisappear(t *testing.T) {
	const row = "| `RC-RESUME-050` | Current-head `performer.completed` on the draft interview movement while the score remains `status: draft` | Append `attempt.failed {kind: task_failed, reason: draft_no_blocking_output, disposition}` after classifying the quality failure under §3.1's first arm, then re-evaluate C.1 and hand its recorded second-arm consequence to `RC-RESUME-039`. **Acceptance never begins.** A genuinely blocking draft result instead makes `attempt.blocked` durable, not `performer.completed`, so this row is selected from the journal alone and does not reconstruct a lost response |\n"
	runDraftResultNormsMutation(t,
		row,
		"",
		"TestDraftNoBlockingOutputRecoveryPrecedesOrdinaryVerification",
		"the draft-result C.2 owner can disappear without orphaning RA-061")
}

func TestMutationDraftResultC2OwnerCannotMoveBelowOrdinaryVerification(t *testing.T) {
	const row = "| `RC-RESUME-050` | Current-head `performer.completed` on the draft interview movement while the score remains `status: draft` | Append `attempt.failed {kind: task_failed, reason: draft_no_blocking_output, disposition}` after classifying the quality failure under §3.1's first arm, then re-evaluate C.1 and hand its recorded second-arm consequence to `RC-RESUME-039`. **Acceptance never begins.** A genuinely blocking draft result instead makes `attempt.blocked` durable, not `performer.completed`, so this row is selected from the journal alone and does not reconstruct a lost response |\n"
	const next = "| `RC-RESUME-016` |"

	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	copyRoot := copyRunstateMutationRepository(t)
	designPath := filepath.Join(copyRoot, "docs", "DESIGN.md")
	contents, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), row); count != 1 {
		t.Fatalf("mutation row count = %d, want 1", count)
	}
	rowIndex := strings.Index(string(contents), row)
	afterRow := string(contents)[rowIndex+len(row):]
	nextEnd := strings.Index(afterRow, "\n")
	if !strings.HasPrefix(afterRow, next) || nextEnd == -1 {
		t.Fatalf("C.2 reorder mutation cannot locate %s immediately after RC-RESUME-050", next)
	}
	ordinary := afterRow[:nextEnd+1]
	mutated := string(contents)[:rowIndex] + ordinary + row + afterRow[nextEnd+1:]
	if err := os.WriteFile(designPath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(applied) != mutated {
		t.Fatal("mutation did not persist its intended C.2 row reorder")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstate"),
		Package:     ".",
		TestPattern: "TestDraftNoBlockingOutputRecoveryPrecedesOrdinaryVerification",
		TestNames:   []string{"TestDraftNoBlockingOutputRecoveryPrecedesOrdinaryVerification"},
		Environment: goEnvironment.ChildEnvironment(os.Environ()),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: the draft-result C.2 owner can move below RC-RESUME-016\n%s", result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func runDraftResultNormsMutation(t *testing.T, anchor, replacement, testPattern, escaped string) {
	t.Helper()

	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	copyRoot := copyRunstateMutationRepository(t)
	designPath := filepath.Join(copyRoot, "docs", "DESIGN.md")
	contents, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, replacement, 1)
	if err := os.WriteFile(designPath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(applied) != mutated {
		t.Fatal("mutation did not persist its intended document change")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstate"),
		Package:     ".",
		TestPattern: testPattern,
		TestNames:   []string{testPattern},
		Environment: goEnvironment.ChildEnvironment(os.Environ()),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: %s\n%s", escaped, result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestMutationApprovalDecisionTypeSyntaxKeepsRejectTypeRestriction(t *testing.T) {
	assertApprovalDecisionTypeSyntaxMutationKilled(t,
		"- Amendment and finalization rejection requires `--reason <text>` under B.5's `human_reason` rule.",
		"- Rejection requires `--reason <text>` under B.5's `human_reason` rule.",
		"approval reject grammar no longer restricts the required-reason form to amendment and finalization")
}

func TestMutationApprovalDecisionTypeSyntaxKeepsAmendmentReasonRequired(t *testing.T) {
	assertApprovalDecisionTypeSyntaxMutationKilled(t,
		"| --reject --reason <text>\n",
		"| --reject [--reason <text>]\n",
		"approval reject grammar no longer requires a reason for amendment and finalization")
}

func TestMutationApprovalDecisionTypeSyntaxKeepsB5ReasonMapping(t *testing.T) {
	assertApprovalDecisionTypeSyntaxMutationKilled(t,
		" under B.5's `human_reason` rule.",
		".",
		"approval reject grammar no longer maps amendment and finalization reasons to B.5 human_reason")
}

func assertApprovalDecisionTypeSyntaxMutationKilled(t *testing.T, anchor, replacement, escaped string) {
	t.Helper()

	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	copyRoot := copyRunstateMutationRepository(t)
	designPath := filepath.Join(copyRoot, "docs", "DESIGN.md")
	contents, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, replacement, 1)
	if err := os.WriteFile(designPath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(applied) != mutated {
		t.Fatal("mutation did not persist its intended document change")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstate"),
		Package:     ".",
		TestPattern: "TestApprovalDecisionTypeSyntaxIsSpecified",
		TestNames:   []string{"TestApprovalDecisionTypeSyntaxIsSpecified"},
		Environment: goEnvironment.ChildEnvironment(os.Environ()),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: %s\n%s", escaped, result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func TestMutationQuiesceReceiptRequiresContiguousRounds(t *testing.T) {
	goEnvironment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	copyRoot := copyRunstateMutationRepository(t)
	sourcePath := filepath.Join(copyRoot, "internal", "runstate", "apply.go")
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	anchor := "\t\tif round != state.PendingPrepare.LatestQuiesceRound+1 {\n"
	if count := strings.Count(string(contents), anchor); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), anchor, "\t\tif false {\n", 1)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "runstate"),
		Package:     ".",
		TestPattern: "TestQuiesceObservationProjectsLatestReceiptAndRequiresContiguousRounds",
		TestNames:   []string{"TestQuiesceObservationProjectsLatestReceiptAndRequiresContiguousRounds"},
		Environment: goEnvironment.ChildEnvironment(os.Environ()),
	})
	cancel()
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: quiesce receipt gaps are no longer refused\n%s", result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func copyRunstateMutationRepository(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve runstate test source directory")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	if err := filepath.WalkDir(repositoryRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repositoryRoot, path)
		if err != nil {
			return err
		}
		if relative == ".git" {
			return filepath.SkipDir
		}
		target := filepath.Join(copyRoot, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}); err != nil {
		t.Fatal(err)
	}
	return copyRoot
}
