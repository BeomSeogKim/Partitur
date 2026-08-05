//go:build mutation

// Mutation proofs run the default behavioral tests against deliberately broken copies.
// The default suite exercises those paths, but it does not establish that every guard
// rejects a faulty implementation. Run `go test -tags=mutation ./...` for that proof.
package driver

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationLiveMovementCompositionTerminalSerializesCancellationAfterEvidence(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestLiveMovementCompositionTerminalSerializesCancellationAfterEvidence(t)
	assertDriverMutationKilled(t, "TestLiveMovementCompositionTerminalSerializesCancellationAfterEvidence", goEnvironment, "movement_composition.go", `func appendMovementCompositionTerminal(store *runstore.Store, authority *runstore.Driver, stopped, evidence, terminal runstate.Event, stoppedAddress, evidenceAddress, terminalAddress faultpoint.ReceiptAddress, afterEvidence func()) error {
	return authority.Mutate(func(transaction *runstore.Txn, state runstate.State) error {
		if state.CancelRequested {
			return ErrCompositionCancelled
		}
		next, err := runstate.Apply(state, stopped)
		if err != nil {
			return err
		}
		if _, err := transaction.At(stoppedAddress).Append(stopped); err != nil {
			return err
		}
		state = next
		next, err = runstate.Apply(state, evidence)
		if err != nil {
			return err
		}
		evidenceReceipt, err := transaction.At(evidenceAddress).Append(evidence)
		if err != nil {
			return err
		}
		store.Reached(faultpoint.PointCompositionMovementEvidence)
		if afterEvidence != nil {
			afterEvidence()
		}
		terminal.CausationID = evidenceReceipt.Mutation.EventID
		if _, err := runstate.Apply(next, terminal); err != nil {
			return err
		}
		if _, err := transaction.At(terminalAddress).Append(terminal); err != nil {
			return err
		}
		store.Reached(faultpoint.PointCompositionMovementTerminal)
		return nil
	})
}`,
		`func appendMovementCompositionTerminal(store *runstore.Store, authority *runstore.Driver, stopped, evidence, terminal runstate.Event, stoppedAddress, evidenceAddress, terminalAddress faultpoint.ReceiptAddress, afterEvidence func()) error {
	state, err := authority.State()
	if err != nil {
		return err
	}
	if state.CancelRequested {
		return ErrCompositionCancelled
	}
	var evidenceReceipt runstore.DurabilityReceipt
	err = authority.Mutate(func(transaction *runstore.Txn, _ runstate.State) error {
		next, err := runstate.Apply(state, stopped)
		if err != nil {
			return err
		}
		if _, err := transaction.At(stoppedAddress).Append(stopped); err != nil {
			return err
		}
		state = next
		next, err = runstate.Apply(state, evidence)
		if err != nil {
			return err
		}
		evidenceReceipt, err = transaction.At(evidenceAddress).Append(evidence)
		return err
	})
	if err != nil {
		return err
	}
	store.Reached(faultpoint.PointCompositionMovementEvidence)
	if afterEvidence != nil {
		afterEvidence()
	}
	time.Sleep(50 * time.Millisecond)
	terminal.CausationID = evidenceReceipt.Mutation.EventID
	if _, err := runstate.Apply(state, terminal); err != nil {
		return err
	}
	if _, err := authority.Append(terminal, terminalAddress); err != nil {
		return err
	}
	store.Reached(faultpoint.PointCompositionMovementTerminal)
	return nil
}`)
}

func TestMutationPrepareMovementBaseUsesIdentityForZeroContributors(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestPrepareMovementBaseUsesIdentityForZeroContributors(t)
	assertDriverMutationKilled(t, "TestPrepareMovementBaseUsesIdentityForZeroContributors", goEnvironment, "movement_composition.go", "if len(contributors) == 0 {", "if len(contributors) == 1 {")
}

func TestMutationComposeMovementBaseReportsEachMissingOperand(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestComposeMovementBaseReportsEachMissingOperand(t)
	for _, name := range []string{"store", "authority", "contributors", "now", "newID"} {
		t.Run(name, func(t *testing.T) {
			assertDriverMutationKilled(t, "TestComposeMovementBaseReportsEachMissingOperand/"+name, goEnvironment, "movement_composition.go", "missing = append(missing, \""+name+"\")", "missing = append(missing, \"mutated-"+name+"\")")
		})
	}
}

func TestMutationLiveCompositionConflictStopsBeforeCreatingTargetAttempt(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestLiveCompositionConflictStopsBeforeCreatingTargetAttempt(t)
	assertDriverMutationKilled(t, "TestLiveCompositionConflictStopsBeforeCreatingTargetAttempt", goEnvironment, "movement_composition.go", "return MovementBase{}, ErrCompositionTerminalized", "return MovementBase{}, errors.New(\"driver: injected non-terminal composition failure\")")
}

func TestMutationLiveFanInCreatesTargetAtPinnedBaseCommit(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestLiveFanInCreatesTargetAtPinnedBaseCommit(t)
	assertDriverMutationKilled(t, "TestLiveFanInCreatesTargetAtPinnedBaseCommit", goEnvironment, "driver.go", "attempt, err = run.CreateAttemptAtBase(movement.ID, baseCommit)", "attempt, err = run.CreateAttempt(movement.ID)")
}

func TestMutationAutoApprovalRefusesCommitWhileNormalDriverAuthorityRemains(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestCompleteAutoApprovalRefusesCommitWhileNormalDriverAuthorityRemains(t)
	// The mutant keeps `present` in the condition so the mutated copy still
	// compiles — dropping it makes the binding unused, which Go rejects, and a
	// build failure is a non-result rather than a killed mutant.
	assertDriverMutationKilled(t, "TestCompleteAutoApprovalRefusesCommitWhileNormalDriverAuthorityRemains", goEnvironment, "driver.go", "} else if present {", "} else if present && false { // mutation: normal driver authority ignored")
}

func TestMutationExecutionDependencyHashBindsDeliveredArtifactID(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestExecutionDependencyHashBindsDeliveredArtifactInstance(t)
	assertDriverMutationKilled(t, "TestExecutionDependencyHashBindsDeliveredArtifactInstance", goEnvironment, "driver.go", `"artifact_id":  input.ArtifactID,`, "// mutation: delivered logical artifact id omitted")
}

func TestMutationExecutionDependencyHashBindsDeliveredArtifactKind(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestExecutionDependencyHashBindsDeliveredArtifactInstance(t)
	assertDriverMutationKilled(t, "TestExecutionDependencyHashBindsDeliveredArtifactInstance", goEnvironment, "driver.go", `"kind":         input.Kind,`, "// mutation: delivered artifact kind omitted")
}

func TestMutationExecutionDependencyHashBindsDeliveredInstanceID(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestExecutionDependencyHashBindsDeliveredArtifactInstance(t)
	assertDriverMutationKilled(t, "TestExecutionDependencyHashBindsDeliveredArtifactInstance", goEnvironment, "driver.go", `"instance_id":  input.InstanceID,`, "// mutation: delivered instance id omitted")
}

func TestMutationExecutionDependencyHashBindsDeliveredContentHash(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestExecutionDependencyHashBindsDeliveredArtifactInstance(t)
	assertDriverMutationKilled(t, "TestExecutionDependencyHashBindsDeliveredArtifactInstance", goEnvironment, "driver.go", `"content_hash": input.Hash,`, "// mutation: delivered content hash omitted")
}

func TestMutationDeliveredInputsSortByArtifactID(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestDeliveredInputsSortByArtifactID(t)
	assertDriverMutationKilled(t, "TestDeliveredInputsSortByArtifactID", goEnvironment, "driver.go", `slices.SortFunc(inputs, func(left, right protocol.ArtifactRef) int {
		return strings.Compare(left.ArtifactID, right.ArtifactID)
	})`, "// mutation: delivered input order left unresolved")
}

func TestMutationDeliveredInputsIgnoreUnrelatedSuccessfulAttempt(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestExecutionDependencyHashBindsDeliveredArtifactInstance(t)
	assertDriverMutationKilled(t, "TestExecutionDependencyHashBindsDeliveredArtifactInstance", goEnvironment, "driver.go", `result, exists := state.MovementResults[producers[artifactID]]`, `result, exists := state.MovementResults["unrelated"]`)
}

func TestMutationExecutionDependencyHashBindsResolvedQuestions(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestExecuteBriefProjectsResolvedQuestions(t)
	assertDriverMutationKilled(t, "TestExecuteBriefProjectsResolvedQuestions", goEnvironment, "driver.go", `"resolved_questions": resolvedQuestionProjection(compiled.ResolvedQuestions()),`, `"resolved_questions": []any{}, // mutation: resolved score questions omitted`)
}

func TestMutationExecutionDependencyHashBindsScoreBaseForMayPropose(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestExecutionDependencyHashBindsScoreBaseForMayPropose(t)
	assertDriverMutationKilled(t, "TestExecutionDependencyHashBindsScoreBaseForMayPropose", goEnvironment, "driver.go", `if movement.MayPropose {
		scoreBaseHash, err := compiled.Hash()
		if err != nil {
			return nil, err
		}
		movementValue["score_base_hash"] = scoreBaseHash
	}`, `if movement.MayPropose {
		// mutation: semantic score-base hash omitted
	}`)
}

func TestMutationAdapterProbedRecordsReachedA5Closure(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestExecuteAttemptRefusesProposalWaitingHumanUntilRoutedRequestExists(t)
	assertDriverMutationKilled(t, "TestExecuteAttemptRefusesProposalWaitingHumanUntilRoutedRequestExists", goEnvironment, "driver.go", `domains, err := executiondep.V3ProjectionDomains(value)`, `domains := []canonical.Domain{canonical.DomainExecutionDependency} // mutation: A.5 inner closure omitted
	_ = executiondep.V3ProjectionDomains`)
}

func TestMutationExecutionDependencyHashBindsDraftPhase(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestExecutionDependencyHashBindsDraftPhase(t)
	assertDriverMutationKilled(t, "TestExecutionDependencyHashBindsDraftPhase", goEnvironment, "driver.go", `if movement.Phase == "draft" {
		movementValue["phase"] = movement.Phase
	}`, `if movement.Phase == "draft" {
		// mutation: draft phase omitted
	}`)
}

func TestMutationReviewSubjectInputRendersReservedBriefContract(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestLiveReviewSubjectInputRendersReservedBriefContract(t)
	assertDriverMutationKilled(t, "TestLiveReviewSubjectInputRendersReservedBriefContract", goEnvironment, "driver.go", `ArtifactID: "partitur.subject-tree",`, `ArtifactID: fmt.Sprintf("partitur.subject-tree@%s@%d", movement.ID, revision),`)
}

func TestMutationCandidateConflictFailsRunAtCandidateScope(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestComposeCandidateConflictFailsRunAtCandidateScope(t)
	assertDriverMutationKilled(t, "TestComposeCandidateConflictFailsRunAtCandidateScope", goEnvironment, "candidate_composition.go", "return ErrCompositionTerminalized", "return errors.New(\"driver: injected non-terminal candidate composition failure\")")
}

func TestMutationCandidateCompositionRejectsDeclarationOrder(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestComposeCandidateUsesPinnedTopologicalDeclarationOrder(t)
	assertDriverMutationKilled(t, "TestComposeCandidateUsesPinnedTopologicalDeclarationOrder", goEnvironment, "candidate_composition.go", `ordered, err := stableTopologicalMovementIDs(movements, included)
	if err != nil {
		return nil, err
	}`, `ordered := make([]runstate.MovementID, 0, len(movements))
	for _, movement := range movements {
		ordered = append(ordered, runstate.MovementID(movement.ID))
	}
	slices.Sort(ordered)`)
}

func TestMutationWaivedNoOpWriterPinsCandidateRef(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestComposeCandidateWaivedNoOpWriterPinsBaseCandidate(t)
	assertDriverMutationKilledAt(t, "TestComposeCandidateWaivedNoOpWriterPinsBaseCandidate", goEnvironment, filepath.Join("internal", "workspace", "recovery.go"), `if _, err := ensureRef(
			run.git, run.repositoryRoot, candidateRef(run.id), commit, run.id,
			receiptCandidateRef, refExistingMustMatchObject,
		); err != nil {
			return err
		}`, "_ = commit // mutation: waived candidate ref omitted")
}

func mutationGoEnvironment(t *testing.T) mutationtest.GoEnvironment {
	t.Helper()
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	return environment
}

func assertDriverMutationKilled(t *testing.T, testName string, goEnvironment mutationtest.GoEnvironment, sourceName, before, after string) {
	t.Helper()
	assertDriverMutationKilledAt(t, testName, goEnvironment, filepath.Join("internal", "driver", sourceName), before, after)
}

func assertDriverMutationKilledAt(t *testing.T, testName string, goEnvironment mutationtest.GoEnvironment, sourceName, before, after string) {
	t.Helper()
	lockMutationSource(t)
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve driver test source directory")
	}
	temporaryRoot := t.TempDir()
	copyRoot := filepath.Join(temporaryRoot, "partitur-mutation-copy")
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	if err := copyMutationRepository(copyRoot, repositoryRoot); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(copyRoot, sourceName)
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count == 0 {
		t.Fatalf("mutation anchor %q is absent from %s", before, sourcePath)
	}
	backup, err := os.CreateTemp(t.TempDir(), "partitur-mutation-backup-")
	if err != nil {
		t.Fatal(err)
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	copyFile := func(destination, source string) error {
		input, err := os.Open(source)
		if err != nil {
			return err
		}
		defer input.Close()
		info, err := input.Stat()
		if err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	if err := copyFile(backupPath, sourcePath); err != nil {
		t.Fatal(err)
	}
	mutated := strings.ReplaceAll(string(contents), before, after)
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applied), after) || strings.Contains(string(applied), before) {
		t.Fatalf("mutation was not applied to %s before the child test ran", sourcePath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "driver"),
		Package:     ".",
		TestPattern: testName,
		TestNames:   []string{testName},
		Environment: goEnvironment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
	})
	cancel()
	if err := copyFile(sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}
	cmp := exec.Command("cmp", "-s", backupPath, sourcePath)
	if output, err := cmp.CombinedOutput(); err != nil {
		t.Fatalf("mutation restore comparison failed: %v\n%s", err, output)
	}
	switch result.Outcome {
	case mutationtest.Killed:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: %s still passed after %q became %q\n%s", testName, before, after, result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func copyMutationRepository(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
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
	})
}

func lockMutationSource(t *testing.T) {
	t.Helper()
	lock, err := os.OpenFile(filepath.Join(os.TempDir(), "partitur-mutation-source.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
			t.Errorf("release mutation source lock: %v", err)
		}
		if err := lock.Close(); err != nil {
			t.Errorf("close mutation source lock: %v", err)
		}
	})
}
