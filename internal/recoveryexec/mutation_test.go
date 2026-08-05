//go:build mutation

// Mutation proofs run the default behavioral tests against deliberately broken copies.
// The default suite exercises those paths, but it does not establish that every guard
// rejects a faulty implementation. Run `go test -tags=mutation ./...` for that proof.
package recoveryexec

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationRecoveryCompositionTerminalStopsBeforeCreatingTargetAttempt(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestRecoveryCompositionTerminalStopsBeforeCreatingTargetAttempt(t)
	assertRecoveryMutationKilled(t, "TestRecoveryCompositionTerminalStopsBeforeCreatingTargetAttempt", goEnvironment, filepath.Join("internal", "driver", "movement_composition.go"), "return MovementBase{}, ErrCompositionTerminalized", "return MovementBase{}, errors.New(\"driver: injected non-terminal composition failure\")")
}

func TestMutationRecoveryFanInSuccessorMaterializesAtComposedBase(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestRecoveryFanInSuccessorMaterializesAtComposedBase(t)
	assertRecoveryMutationKilled(t, "TestRecoveryFanInSuccessorMaterializesAtComposedBase", goEnvironment, filepath.Join("internal", "recoveryexec", "handlers.go"), "workspace.CreateRecoveredAttemptAtBase(execution.Store, execution.Driver, input, movementID, baseCommit)", "workspace.CreateRecoveredAttempt(execution.Store, execution.Driver, input, movementID)")
}

func TestMutationRecoveryFinalGateRejectionEndsAtomically(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestRecoveryFinalGateRejectionEndsAtomically(t)
	assertRecoveryMutationKilled(t, "TestRecoveryFinalGateRejectionEndsAtomically", goEnvironment,
		"internal/recoveryconsequence/consequence.go", "\"subject_tree\": action.SubjectTree, \"run_failed\": state.FinalMovements[movementID]",
		"\"subject_tree\": action.SubjectTree, \"run_failed\": false")
}

func TestMutationRecoveryNonFinalGateRejectionCascades(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestRecoveryNonFinalGateRejectionCascades(t)
	assertRecoveryMutationKilled(t, "TestRecoveryNonFinalGateRejectionCascades", goEnvironment,
		"internal/recoveryconsequence/consequence.go", "\"subject_tree\": action.SubjectTree, \"run_failed\": state.FinalMovements[movementID]",
		"\"subject_tree\": action.SubjectTree, \"run_failed\": true")
}

func TestMutationAppendCompositionTerminalSerializesCancellationAfterEvidence(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestAppendCompositionTerminalSerializesCancellationAfterEvidence(t)
	assertRecoveryMutationKilledWithReplacements(t, "TestAppendCompositionTerminalSerializesCancellationAfterEvidence", goEnvironment, "internal/recoveryconsequence/consequence.go", compositionTerminalSerializationMutations())
}

func TestMutationAppendCompositionTerminalInterleaveSurvivesDurableCancellation(t *testing.T) {
	goEnvironment := mutationGoEnvironment(t)
	TestAppendCompositionTerminalYieldsToDurableCancellation(t)
	assertRecoveryMutationSurvivesWithReplacements(t, "TestAppendCompositionTerminalYieldsToDurableCancellation", goEnvironment, "internal/recoveryconsequence/consequence.go", compositionTerminalSerializationMutations())
}

type recoveryMutationReplacement struct {
	before string
	after  string
}

func compositionTerminalSerializationMutations() []recoveryMutationReplacement {
	return []recoveryMutationReplacement{
		{
			before: "\t\"slices\"\n",
			after:  "\t\"slices\"\n\t\"time\"\n",
		},
		{
			before: `func AppendCompositionTerminal(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.CompositionTerminal == nil {
		return errors.New("recovery composition terminal requires store, driver, and evidence")
	}
	terminal := action.CompositionTerminal
	journal, err := execution.Store.ReadJournal(execution.Driver.RunID())
	if err != nil {
		return err
	}
	cause, err := LatestEventID(journal.Events, func(event runstate.Event) bool {
		return (event.Type == runstate.EventCompositionConflicted || event.Type == runstate.EventCompositionFailed) &&
			event.EventID == terminal.EvidenceEventID && event.ScoreRevision == terminal.ScoreRevision &&
			payloadString(event.Payload, "scope") == terminal.Scope && payloadString(event.Payload, "target_id") == terminal.TargetID
	})
	if err != nil {
		return err
	}
	terminalPoint, err := compositionTerminalPoint(terminal.Scope)
	if err != nil {
		return err
	}
	err = execution.Driver.Mutate(func(transaction *runstore.Txn, state runstate.State) error {
		if state.CancelRequested || terminal.ScoreRevision != state.ScoreHead.Revision {
			return ErrReplan
		}
		if execution.AfterCompositionEvidence != nil {
			execution.AfterCompositionEvidence()
		}
		var event runstate.Event
		var address faultpoint.ReceiptAddress
		switch terminal.Scope {
		case "movement":
			event = runstate.Event{RunID: execution.Driver.RunID(), ScoreRevision: state.ScoreHead.Revision, MovementID: runstate.MovementID(terminal.TargetID), Type: runstate.EventMovementFailed, CausationID: cause, Payload: RecoveryPayload(map[string]any{"reason": terminal.Reason, "run_failed": false})}
			address = "recovery.movement.failed.composition"
		case "candidate":
			event = runstate.Event{RunID: execution.Driver.RunID(), ScoreRevision: state.ScoreHead.Revision, Type: runstate.EventRunFailed, CausationID: cause, Payload: RecoveryPayload(map[string]any{"reason": terminal.Reason})}
			address = "recovery.run.failed.composition"
		default:
			return fmt.Errorf("recovery composition terminal has invalid scope %q", terminal.Scope)
		}
		if _, err := runstate.Apply(state, event); err != nil {
			return err
		}
		_, err := transaction.At(address).Append(event)
		return err
	})
	if err != nil {
		return err
	}
	execution.Store.Reached(terminalPoint)
	return nil
}`,
			after: `func AppendCompositionTerminal(_ context.Context, execution HandlerContext, action recovery.Action) error {
	if execution.Store == nil || execution.Driver == nil || action.CompositionTerminal == nil {
		return errors.New("recovery composition terminal requires store, driver, and evidence")
	}
	terminal := action.CompositionTerminal
	journal, err := execution.Store.ReadJournal(execution.Driver.RunID())
	if err != nil {
		return err
	}
	cause, err := LatestEventID(journal.Events, func(event runstate.Event) bool {
		return (event.Type == runstate.EventCompositionConflicted || event.Type == runstate.EventCompositionFailed) &&
			event.EventID == terminal.EvidenceEventID && event.ScoreRevision == terminal.ScoreRevision &&
			payloadString(event.Payload, "scope") == terminal.Scope && payloadString(event.Payload, "target_id") == terminal.TargetID
	})
	if err != nil {
		return err
	}
	terminalPoint, err := compositionTerminalPoint(terminal.Scope)
	if err != nil {
		return err
	}
	state, err := execution.Driver.State()
	if err != nil {
		return err
	}
	if state.CancelRequested || terminal.ScoreRevision != state.ScoreHead.Revision {
		return ErrReplan
	}
	if execution.AfterCompositionEvidence != nil {
		execution.AfterCompositionEvidence()
	}
	time.Sleep(50 * time.Millisecond)
	var event runstate.Event
	var address faultpoint.ReceiptAddress
	switch terminal.Scope {
	case "movement":
		event = runstate.Event{RunID: execution.Driver.RunID(), ScoreRevision: state.ScoreHead.Revision, MovementID: runstate.MovementID(terminal.TargetID), Type: runstate.EventMovementFailed, CausationID: cause, Payload: RecoveryPayload(map[string]any{"reason": terminal.Reason, "run_failed": false})}
		address = "recovery.movement.failed.composition"
	case "candidate":
		event = runstate.Event{RunID: execution.Driver.RunID(), ScoreRevision: state.ScoreHead.Revision, Type: runstate.EventRunFailed, CausationID: cause, Payload: RecoveryPayload(map[string]any{"reason": terminal.Reason})}
		address = "recovery.run.failed.composition"
	default:
		return fmt.Errorf("recovery composition terminal has invalid scope %q", terminal.Scope)
	}
	if _, err := runstate.Apply(state, event); err != nil {
		return err
	}
	if _, err := execution.Driver.Append(event, address); err != nil {
		return err
	}
	execution.Store.Reached(terminalPoint)
	return nil
}`,
		},
	}
}

func mutationGoEnvironment(t *testing.T) mutationtest.GoEnvironment {
	t.Helper()
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	return environment
}

func assertRecoveryMutationKilled(t *testing.T, testName string, goEnvironment mutationtest.GoEnvironment, sourceName, before, after string) {
	t.Helper()
	assertRecoveryMutationOutcome(t, mutationtest.Killed, testName, goEnvironment, sourceName, []recoveryMutationReplacement{{before: before, after: after}})
}

func assertRecoveryMutationKilledWithReplacements(t *testing.T, testName string, goEnvironment mutationtest.GoEnvironment, sourceName string, replacements []recoveryMutationReplacement) {
	t.Helper()
	assertRecoveryMutationOutcome(t, mutationtest.Killed, testName, goEnvironment, sourceName, replacements)
}

func assertRecoveryMutationSurvivesWithReplacements(t *testing.T, testName string, goEnvironment mutationtest.GoEnvironment, sourceName string, replacements []recoveryMutationReplacement) {
	t.Helper()
	assertRecoveryMutationOutcome(t, mutationtest.Survived, testName, goEnvironment, sourceName, replacements)
}

func assertRecoveryMutationOutcome(t *testing.T, want mutationtest.Outcome, testName string, goEnvironment mutationtest.GoEnvironment, sourceName string, replacements []recoveryMutationReplacement) {
	t.Helper()
	lockMutationSource(t)
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve recovery test source directory")
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
	for _, replacement := range replacements {
		if count := strings.Count(string(contents), replacement.before); count == 0 {
			t.Fatalf("mutation anchor %q is absent from %s", replacement.before, sourcePath)
		}
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
	mutated := string(contents)
	for _, replacement := range replacements {
		mutated = strings.ReplaceAll(mutated, replacement.before, replacement.after)
	}
	if err := os.WriteFile(sourcePath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range replacements {
		if !strings.Contains(string(applied), replacement.after) ||
			(!strings.Contains(replacement.after, replacement.before) && strings.Contains(string(applied), replacement.before)) {
			t.Fatalf("mutation was not applied to %s before the child test ran", sourcePath)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "recoveryexec"),
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
	case want:
		return
	case mutationtest.Survived:
		t.Fatalf("mutation survived: %s still passed after %q\n%s", testName, mutationReplacementSummary(replacements), result.Diagnostic())
	default:
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func mutationReplacementSummary(replacements []recoveryMutationReplacement) string {
	parts := make([]string, 0, len(replacements))
	for _, replacement := range replacements {
		parts = append(parts, strconv.Quote(replacement.before)+" became "+strconv.Quote(replacement.after))
	}
	return strings.Join(parts, "; ")
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
