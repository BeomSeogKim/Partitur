//go:build mutation

package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestDecisionResumeHintMutations(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	const epochGuard = "\tif lease.Epoch != authority.Epoch || authority.Owner == nil {\n\t\treturn ResumeLeaseProjectionMismatch\n\t}"
	const ownerUnverifiableDiagnostic = "\tif resolution.ownerUnverifiable {\n\t\tfmt.Fprintf(stderr, \"run blocked: run_id=%q state=%q reason=%q\\n\", string(resolution.runID), \"nonterminal\", \"owner_unverifiable\")\n\t\treturn\n\t}"
	const currentSnapshot = "\tsnapshot, err := store.ClassifyCurrentResumeLease(runID)\n\tif err != nil {\n\t\treturn result\n\t}\n\treturn classifyDecisionResumeEligibility(result, &snapshot.Projection, snapshot.LeaseStatus)"
	const terminalFirst = "\tif projection.Run.Terminal() {\n\t\treturn result\n\t}"
	const failClosedLeaseStatus = "\tcase runstore.ResumeLeaseAvailable, runstore.ResumeLeaseProjectionMismatch:\n\t\t// Only statuses known to permit a resume attempt may print the hint.\n\tdefault:\n\t\treturn result\n\t}"
	const initialSeedReconciliation = "func (store *Store) ClassifyCurrentResumeLease(runID runstate.RunID) (ResumeLeaseSnapshot, error) {\n\tvar result ResumeLeaseSnapshot\n\terr := store.MutateProjected(runID, func(transaction *Txn, state runstate.State) error {\n\t\tlease, present, err := transaction.ReadLease()\n\t\tresult = ResumeLeaseSnapshot{\n\t\t\tProjection:  decisionResolution(state),\n\t\t\tLeaseStatus: classifyResumeLease(lease, present, err, state.Authority),\n\t\t}\n\t\treturn nil\n\t})\n\treturn result, err\n}"
	for _, mutation := range []struct {
		name        string
		source      string
		pkg         string
		before      string
		after       string
		testPattern string
		testName    string
	}{
		{
			name:        "decision-time authority is not reconciled",
			source:      filepath.Join("cmd", "partitur", "main.go"),
			before:      currentSnapshot,
			after:       "\tsnapshot, err := store.ClassifyCurrentResumeLease(runID)\n\tif err != nil {\n\t\treturn result\n\t}\n\treturn classifyDecisionResumeEligibility(result, &snapshot.Projection, runstore.ResumeLeaseAvailable) // mutation: ignore current live owner",
			testPattern: "TestDecisionResolutionReconcilesDriverAcquiredAfterDecision",
			testName:    "TestDecisionResolutionReconcilesDriverAcquiredAfterDecision",
		},
		{
			name:        "current lifecycle is not carried through reconciliation",
			source:      filepath.Join("cmd", "partitur", "main.go"),
			before:      currentSnapshot,
			after:       "\tsnapshot, err := store.ClassifyCurrentResumeLease(runID)\n\tif err != nil {\n\t\treturn result\n\t}\n\treturn classifyDecisionResumeEligibility(result, &runstore.DecisionResolution{Run: runstate.RunRunning}, snapshot.LeaseStatus) // mutation: ignore current lifecycle",
			testPattern: "TestDecisionResolutionReconciliationCarriesCurrentWaitingLifecycle",
			testName:    "TestDecisionResolutionReconciliationCarriesCurrentWaitingLifecycle",
		},
		{
			name:        "LoadRunInput preflight suppresses current snapshot hint",
			source:      filepath.Join("cmd", "partitur", "main.go"),
			before:      currentSnapshot,
			after:       "\tif _, err := store.LoadRunInput(runID); err != nil {\n\t\treturn result\n\t}\n" + currentSnapshot,
			testPattern: "TestRoutedApprovalResolvedCastReloadFailureStillPrintsHint",
			testName:    "TestRoutedApprovalResolvedCastReloadFailureStillPrintsHint",
		},
		{
			name:        "lease health outranks terminality",
			source:      filepath.Join("cmd", "partitur", "main.go"),
			before:      terminalFirst,
			after:       "\tif projection.Run.Terminal() && (leaseStatus != runstore.ResumeLeaseUnverifiable) { // mutation: inspect unverifiable lease before terminality\n\t\treturn result\n\t}",
			testPattern: "TestDecisionResolutionTerminalWithUnverifiableResidualLeaseHasEmptyStderr",
			testName:    "TestDecisionResolutionTerminalWithUnverifiableResidualLeaseHasEmptyStderr",
		},
		{
			name:        "unknown lease status prints resume hint",
			source:      filepath.Join("cmd", "partitur", "main.go"),
			before:      failClosedLeaseStatus,
			after:       "\tcase runstore.ResumeLeaseAvailable, runstore.ResumeLeaseProjectionMismatch:\n\t\t// Only statuses known to permit a resume attempt may print the hint.\n\tdefault:\n\t\t// mutation: unknown statuses fall through to eligibility\n\t}",
			testPattern: "TestDecisionResolutionUnknownLeaseStatusHasEmptyStderr",
			testName:    "TestDecisionResolutionUnknownLeaseStatusHasEmptyStderr",
		},
		{
			name:        "current score seeds full journal reconciliation",
			source:      filepath.Join("internal", "runstore", "lease.go"),
			before:      initialSeedReconciliation,
			after:       "func (store *Store) ClassifyCurrentResumeLease(runID runstate.RunID) (ResumeLeaseSnapshot, error) {\n\tinput, err := store.LoadRunInput(runID)\n\tif err != nil {\n\t\treturn ResumeLeaseSnapshot{}, err\n\t}\n\tvar result ResumeLeaseSnapshot\n\terr = store.Mutate(runID, \"\", func(transaction *Txn) error {\n\t\tstate, err := transaction.project(movementSeed(input.Score))\n\t\tif err != nil {\n\t\t\treturn err\n\t\t}\n\t\tlease, present, err := transaction.ReadLease()\n\t\tresult = ResumeLeaseSnapshot{\n\t\t\tProjection:  decisionResolution(state),\n\t\t\tLeaseStatus: classifyResumeLease(lease, present, err, state.Authority),\n\t\t}\n\t\treturn nil\n\t})\n\treturn result, err\n}",
			testPattern: "TestDecisionResolutionReconciliationUsesInitialSeedAfterRevision",
			testName:    "TestDecisionResolutionReconciliationUsesInitialSeedAfterRevision",
		},
		{
			name:        "matching live owner is treated as resumable",
			pkg:         "./internal/runstore",
			before:      "\tcase procid.MatchingAndLive:\n\t\treturn ResumeLeaseLiveOwner\n",
			after:       "\tcase procid.MatchingAndLive:\n\t\treturn ResumeLeaseAvailable // mutation\n",
			testPattern: "TestResumeLeaseClassificationAgreesWithRecoveryPlanner/matching_owner",
			testName:    "TestResumeLeaseClassificationAgreesWithRecoveryPlanner/matching_owner",
		},
		{
			name:        "different live pid is treated as resumable",
			pkg:         "./internal/runstore",
			before:      epochGuard,
			after:       epochGuard + "\n\tif lease.PID != authority.Owner.PID {\n\t\treturn ResumeLeaseAvailable\n\t}",
			testPattern: "TestResumeLeaseClassificationAgreesWithRecoveryPlanner/wrong_pid",
			testName:    "TestResumeLeaseClassificationAgreesWithRecoveryPlanner/wrong_pid",
		},
		{
			name:        "different live start is treated as resumable",
			pkg:         "./internal/runstore",
			before:      epochGuard,
			after:       epochGuard + "\n\tif !startIdentitiesEqual(lease.Start, authority.Owner.Start) {\n\t\treturn ResumeLeaseAvailable\n\t}",
			testPattern: "TestResumeLeaseClassificationAgreesWithRecoveryPlanner/wrong_start",
			testName:    "TestResumeLeaseClassificationAgreesWithRecoveryPlanner/wrong_start",
		},
		{
			name:        "ownerless authority is treated as live-owned",
			pkg:         "./internal/runstore",
			before:      epochGuard,
			after:       "\tif lease.Epoch != authority.Epoch {\n\t\treturn ResumeLeaseAvailable\n\t}",
			testPattern: "TestResumeLeaseClassificationAgreesWithRecoveryPlanner/ownerless_positive_epoch",
			testName:    "TestResumeLeaseClassificationAgreesWithRecoveryPlanner/ownerless_positive_epoch",
		},
		{
			name:        "dead or reused matching owner suppresses hint",
			before:      "\tcase procid.GoneOrReused:\n\t\treturn ResumeLeaseAvailable\n",
			after:       "\tcase procid.GoneOrReused:\n\t\treturn ResumeLeaseLiveOwner // mutation\n",
			testPattern: "TestDecisionResolutionWithDeadOrReusedMatchingLeasePrintsResumeHint",
			testName:    "TestDecisionResolutionWithDeadOrReusedMatchingLeasePrintsResumeHint",
		},
		{
			name:        "unreadable lease is treated as resumable",
			before:      "\tif err != nil {\n\t\treturn ResumeLeaseUnverifiable\n\t}",
			after:       "\tif err != nil {\n\t\treturn ResumeLeaseAvailable // mutation\n\t}",
			testPattern: "TestDecisionResolutionWithUnreadableLeasePrintsOwnerUnverifiableDiagnostic",
			testName:    "TestDecisionResolutionWithUnreadableLeasePrintsOwnerUnverifiableDiagnostic",
		},
		{
			name:        "unverifiable owner is treated as resumable",
			before:      "\tdefault:\n\t\treturn ResumeLeaseUnverifiable\n",
			after:       "\tdefault:\n\t\treturn ResumeLeaseAvailable // mutation\n",
			testPattern: "TestDecisionResolutionWithUnverifiableOwnerPrintsOwnerUnverifiableDiagnostic",
			testName:    "TestDecisionResolutionWithUnverifiableOwnerPrintsOwnerUnverifiableDiagnostic",
		},
		{
			name:        "owner-unverifiable diagnostic is omitted",
			source:      filepath.Join("cmd", "partitur", "main.go"),
			before:      ownerUnverifiableDiagnostic,
			after:       "\tif resolution.ownerUnverifiable {\n\t\treturn // mutation\n\t}",
			testPattern: "TestDecisionResolutionWithUnreadableLeasePrintsOwnerUnverifiableDiagnostic",
			testName:    "TestDecisionResolutionWithUnreadableLeasePrintsOwnerUnverifiableDiagnostic",
		},
		{
			name:        "owner-unverifiable diagnostic becomes resume hint",
			source:      filepath.Join("cmd", "partitur", "main.go"),
			before:      ownerUnverifiableDiagnostic,
			after:       "\tif resolution.ownerUnverifiable {\n\t\tfmt.Fprintf(stderr, \"run waiting: state=%q resume=%q\\n\", \"nonterminal\", \"partitur resume \"+string(resolution.runID))\n\t\treturn\n\t}",
			testPattern: "TestDecisionResolutionWithUnverifiableOwnerPrintsOwnerUnverifiableDiagnostic",
			testName:    "TestDecisionResolutionWithUnverifiableOwnerPrintsOwnerUnverifiableDiagnostic",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			copyRoot := copyDecisionResumeHintMutationRepository(t)
			source := mutation.source
			if source == "" {
				source = filepath.Join("internal", "runstore", "lease.go")
			}
			sourcePath := filepath.Join(copyRoot, source)
			contents, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			if count := strings.Count(string(contents), mutation.before); count != 1 {
				t.Fatalf("mutation anchor count=%d, want 1", count)
			}
			mutated := strings.Replace(string(contents), mutation.before, mutation.after, 1)
			if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
				t.Fatal(err)
			}

			buildStart := time.Now()
			build := exec.Command("go", "build", "-o", os.DevNull, "./...")
			build.Dir = copyRoot
			build.Env = environment.ChildEnvironment(os.Environ())
			buildOutput, buildErr := build.CombinedOutput()
			buildExit := decisionResumeHintCommandExit(buildErr)
			t.Logf("go build exit=%d duration=%s", buildExit, time.Since(buildStart).Round(time.Millisecond))
			if buildExit != 0 {
				t.Fatalf("mutated tree did not compile: %v\n%s", buildErr, buildOutput)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			testStart := time.Now()
			pkg := mutation.pkg
			if pkg == "" {
				pkg = "./cmd/partitur"
			}
			result := mutationtest.Run(ctx, mutationtest.Child{
				Dir:         copyRoot,
				Package:     pkg,
				TestPattern: mutation.testPattern,
				TestNames:   []string{mutation.testName},
				Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
			})
			cancel()
			t.Logf("go test exit=%d outcome=%s duration=%s", result.ExitCode, result.Outcome, time.Since(testStart).Round(time.Millisecond))
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation outcome=%s, want %s: %s\n%s", result.Outcome, mutationtest.Killed, result.Reason, result.Diagnostic())
			}
		})
	}
}

func copyDecisionResumeHintMutationRepository(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve resume-hint mutation source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	destination := filepath.Join(t.TempDir(), "partitur-mutation-copy")
	if err := filepath.WalkDir(repository, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		if entry.IsDir() && (relative == ".git" || relative == filepath.Join(".partitur", "work")) {
			return filepath.SkipDir
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, info.Mode().Perm())
	}); err != nil {
		t.Fatal(err)
	}
	return destination
}

func decisionResumeHintCommandExit(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}
