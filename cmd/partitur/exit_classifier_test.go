package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	validation "github.com/BeomSeogKim/Partitur/internal/validate"
)

var errInjectedJournalSync = errors.New("injected journal fsync failure")
var errInjectedLeaseRead = errors.New("injected lease read failure")

type journalFailureFS struct {
	failSync         bool
	failSyncAt       int
	journalSyncs     int
	failAppend       bool
	failAppendAt     int
	journalAppends   int
	reached          bool
	afterJournalSync func()
	failLeaseRead    bool
	leaseReadReached bool
	afterLeaseRead   func()
}

func (filesystem *journalFailureFS) MkdirAll(path string, mode fs.FileMode) error {
	return os.MkdirAll(path, mode)
}

func (filesystem *journalFailureFS) ReadFile(path string) ([]byte, error) {
	if filesystem.failLeaseRead && filepath.Base(path) == "driver.lease" {
		filesystem.leaseReadReached = true
		return nil, errInjectedLeaseRead
	}
	contents, err := os.ReadFile(path)
	if filepath.Base(path) == "driver.lease" && filesystem.afterLeaseRead != nil {
		after := filesystem.afterLeaseRead
		filesystem.afterLeaseRead = nil
		after()
	}
	return contents, err
}

func (filesystem *journalFailureFS) WriteTemp(directory, pattern string, contents []byte, mode fs.FileMode) (string, error) {
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	name := file.Name()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func (filesystem *journalFailureFS) Append(path string, contents []byte, mode fs.FileMode) error {
	if filepath.Base(path) == "journal.jsonl" {
		filesystem.journalAppends++
	}
	if (filesystem.failAppend || filesystem.failAppendAt != 0 && filesystem.journalAppends == filesystem.failAppendAt) && filepath.Base(path) == "journal.jsonl" {
		filesystem.reached = true
		return errors.New("injected journal append interruption")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (filesystem *journalFailureFS) SyncFile(path string) error {
	if filepath.Base(path) == "journal.jsonl" {
		filesystem.journalSyncs++
		if filesystem.failSync && (filesystem.failSyncAt == 0 || filesystem.journalSyncs == filesystem.failSyncAt) {
			filesystem.reached = true
			return errInjectedJournalSync
		}
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return err
	}
	if filepath.Base(path) == "journal.jsonl" && filesystem.afterJournalSync != nil {
		after := filesystem.afterJournalSync
		filesystem.afterJournalSync = nil
		after()
	}
	return nil
}

func (filesystem *journalFailureFS) SyncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (filesystem *journalFailureFS) Rename(source, destination string) error {
	return os.Rename(source, destination)
}

func (filesystem *journalFailureFS) Remove(path string) error { return os.Remove(path) }

func (filesystem *journalFailureFS) Truncate(path string, size int64) error {
	return os.Truncate(path, size)
}

func (filesystem *journalFailureFS) Stat(path string) (fs.FileInfo, error) {
	return os.Stat(path)
}

func installJournalFailureStore(t *testing.T, filesystem *journalFailureFS) {
	t.Helper()
	previous := newRunStore
	newRunStore = func(root string, probe faultpoint.Probe, observers ...runstore.ReceiptObserver) (*runstore.Store, error) {
		return runstore.NewWithFileSystem(root, probe, filesystem, observers...)
	}
	t.Cleanup(func() { newRunStore = previous })
}

func assertJournalGrew(t *testing.T, store *runstore.Store, before int, filesystem *journalFailureFS) {
	t.Helper()
	if !filesystem.reached {
		t.Fatal("injected journal fsync failure was not reached")
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) <= before {
		t.Fatalf("journal event count=%d, want new appended state after %d fixture events", len(journal.Events), before)
	}
}

func journalLength(t *testing.T, store *runstore.Store) int {
	t.Helper()
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	return len(journal.Events)
}

func assertExitSevenRejectsPriorCode(t *testing.T, code, priorCode int, stderr string) {
	t.Helper()
	if code == priorCode {
		t.Fatalf("exit=%d stderr=%q, returned the prior wrong code", code, stderr)
	}
	if code != 7 {
		t.Fatalf("exit=%d stderr=%q, want exit 7", code, stderr)
	}
}

func appendPendingCLIDecision(t *testing.T, store *runstore.Store, decisionType string, blockers ...runstate.FindingReference) string {
	t.Helper()
	decisionID := decisionType + "-1"
	payload := map[string]any{"decision_id": decisionID, "decision_type": decisionType}
	var routePayload map[string]any
	switch decisionType {
	case "question":
		payload["question"] = "Continue?"
		payload["emitted_id"] = "emitted-1"
	case "human_gate":
		payload["gate_id"] = "gate-attempt-1"
		payload["gate_mode"] = "always"
		payload["subject_tree"] = "git-sha1:tree"
		blockingFindings := make([]any, len(blockers))
		for index, blocker := range blockers {
			blockingFindings[index] = map[string]any{"artifact_instance_id": blocker.ArtifactInstanceID, "finding_id": blocker.FindingID}
		}
		payload["blocking_findings"] = blockingFindings
	case "amendment":
		payload["proposal_id"] = "proposal-1"
		payload["routed_reason"] = "requires_decision"
		payload["blocking"] = true
	case "finalization":
		payload["proposal_id"] = "proposal-1"
		payload["routed_reason"] = "draft_phase"
	}
	if decisionType == "amendment" || decisionType == "finalization" {
		input, err := store.LoadRunInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		reason := "requires_decision"
		if decisionType == "finalization" {
			reason = "draft_phase"
		}
		routePayload = map[string]any{
			"proposal_id": "proposal-1", "reason": reason, "decision_type": decisionType, "blocking": true,
			"proposal_record_hash": "sha256:proposal", "base_revision": 1, "base_hash": string(input.Projection.State.ScoreHead.SemanticHash),
			"classifier_version": 1, "decision_id": decisionID, "typed_delta": []any{},
			"actual_impact": map[string]any{
				"score_changes": []any{},
				"authority": map[string]any{
					"allowed_paths": map[string]any{"added": []any{}, "removed": []any{}},
					"grants":        []any{}, "side_effects": map[string]any{"added": []any{}, "removed": []any{}},
				},
				"budget": map[string]any{},
			},
			"identity_versions": resumeIdentityVersions(),
		}
	}
	err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		if routePayload != nil {
			if _, err := transaction.At("fixture.amendment.routed_human").Append(runstate.Event{
				RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1",
				Type: runstate.EventAmendmentRoutedHuman, Payload: resumePayload(t, routePayload),
			}); err != nil {
				return err
			}
		}
		_, err := transaction.At("fixture.decision.requested").Append(runstate.Event{
			RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1",
			Type: runstate.EventDecisionRequested, Payload: resumePayload(t, payload),
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return decisionID
}

func TestApplicableCommandsMapUnconfirmedJournalDurabilityToExitSeven(t *testing.T) {
	t.Run("run", func(t *testing.T) {
		repository := t.TempDir()
		writeValidateInputs(t, repository, runScore(), runCast())
		runGit(t, repository, "init")
		runGit(t, repository, "config", "user.name", "Partitur Test")
		runGit(t, repository, "config", "user.email", "partitur@example.invalid")
		runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
		runGit(t, repository, "commit", "-m", "fixture")
		t.Chdir(repository)
		preparation, preparationResult := validation.Prepare()
		if preparationResult.Refusal != nil || preparationResult.HasDiagnostics() {
			t.Fatalf("run preparation refusal=%v entries=%v", preparationResult.Refusal, preparationResult.Entries)
		}
		filesystem := &journalFailureFS{failSync: true, failSyncAt: 2}
		storeFactory := func(root string, probe faultpoint.Probe, observers ...runstore.ReceiptObserver) (*runstore.Store, error) {
			return runstore.NewWithFileSystem(root, probe, filesystem, observers...)
		}
		var stdout, stderr bytes.Buffer
		code := runWithRunners([]string{"run"}, &stdout, &stderr,
			func() validation.Result { return validation.Result{} },
			func() (*validation.Preparation, validation.Result) {
				return preparation, validation.Result{}
			},
			func(_ context.Context, _ *validation.Preparation, started driver.StartedObserver) driver.Result {
				execution := productionExecutionDependencies(faultpoint.Nop{})
				execution.StoreFactory = storeFactory
				return driver.RunWithExecutionDependencies(context.Background(), preparation, started, execution)
			},
		)
		assertExitSevenRejectsPriorCode(t, code, 6, stderr.String())
		if !filesystem.reached {
			t.Fatal("real run journal fsync injection was not reached")
		}
		store, err := runstore.New(repository, faultpoint.Nop{})
		if err != nil {
			t.Fatal(err)
		}
		runIDs, err := store.RunIDs()
		if err != nil {
			t.Fatal(err)
		}
		if len(runIDs) != 1 {
			t.Fatalf("new run state ids=%v, want one run created by the injected append", runIDs)
		}
		journal, err := store.ReadJournal(runIDs[0])
		if err != nil {
			t.Fatal(err)
		}
		if len(journal.Events) != 2 || journal.Events[0].Type != runstate.EventRunStarted || journal.Events[1].Type != runstate.EventAuthorityGranted {
			t.Fatalf("new run journal state=%v, want run.started plus appended authority.granted", journal.Events)
		}
	})

	t.Run("answer", func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "question")
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failSync: true}
		installJournalFailureStore(t, filesystem)
		var stderr bytes.Buffer
		code := runAnswer(decisionID, "yes", &stderr)
		assertExitSevenRejectsPriorCode(t, code, 2, stderr.String())
		assertJournalGrew(t, store, before, filesystem)
	})

	t.Run("approve", func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "human_gate")
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failSync: true}
		installJournalFailureStore(t, filesystem)
		var stderr bytes.Buffer
		code := runApprove(decisionID, true, nil, "", &stderr)
		assertExitSevenRejectsPriorCode(t, code, 2, stderr.String())
		assertJournalGrew(t, store, before, filesystem)
	})

	t.Run("amend", func(t *testing.T) {
		root, store := amendCommandFixture(t, true)
		patchPath := filepath.Join(root, "patch.json")
		if err := os.WriteFile(patchPath, []byte(`[{"op":"replace","path":"/goal","value":"fsync-witness"}]`), 0o600); err != nil {
			t.Fatal(err)
		}
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failSync: true}
		installJournalFailureStore(t, filesystem)
		var stdout, stderr bytes.Buffer
		code := runAmend("", patchPath, "fsync witness", "", &stdout, &stderr)
		assertExitSevenRejectsPriorCode(t, code, 6, stderr.String())
		assertJournalGrew(t, store, before, filesystem)
	})

	t.Run("cancel", func(t *testing.T) {
		root, store := resumeFixture(t, "")
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failSync: true}
		installJournalFailureStore(t, filesystem)
		var stdout, stderr bytes.Buffer
		code := runCancel("run-1", &stdout, &stderr, cancel)
		assertExitSevenRejectsPriorCode(t, code, 6, stderr.String())
		assertJournalGrew(t, store, before, filesystem)
	})

	t.Run("resume", func(t *testing.T) {
		root, store := resumeFixture(t, "")
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failSync: true}
		installJournalFailureStore(t, filesystem)
		var stdout, stderr bytes.Buffer
		code := runResume("run-1", &stdout, &stderr, resume)
		assertExitSevenRejectsPriorCode(t, code, 6, stderr.String())
		assertJournalGrew(t, store, before, filesystem)
	})

	t.Run("apply", func(t *testing.T) {
		root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failSync: true}
		installJournalFailureStore(t, filesystem)
		var stderr bytes.Buffer
		code := runApply("run-1", false, &stderr)
		assertExitSevenRejectsPriorCode(t, code, 6, stderr.String())
		assertJournalGrew(t, store, before, filesystem)
	})

	t.Run("promote-score", func(t *testing.T) {
		root, store, _ := promotionFixture(t, true)
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failSync: true}
		installJournalFailureStore(t, filesystem)
		var stderr bytes.Buffer
		code := runPromoteScore("run-1", false, &stderr)
		assertExitSevenRejectsPriorCode(t, code, 6, stderr.String())
		assertJournalGrew(t, store, before, filesystem)
	})
}

func TestDecisionCommandsMapPostTransactionInterruptionToExitSix(t *testing.T) {
	for _, test := range []struct {
		name         string
		decisionType string
		invoke       func(string, *bytes.Buffer) int
	}{
		{name: "answer", decisionType: "question", invoke: func(id string, stderr *bytes.Buffer) int { return runAnswer(id, "yes", stderr) }},
		{name: "approve", decisionType: "human_gate", invoke: func(id string, stderr *bytes.Buffer) int { return runApprove(id, true, nil, "", stderr) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, store := resumeAttemptFixture(t)
			decisionID := appendPendingCLIDecision(t, store, test.decisionType)
			t.Chdir(root)
			filesystem := &journalFailureFS{failAppend: true}
			installJournalFailureStore(t, filesystem)
			var stderr bytes.Buffer
			code := test.invoke(decisionID, &stderr)
			if !filesystem.reached || code != 6 || code == 2 || !strings.Contains(stderr.String(), "run interrupted") {
				t.Fatalf("injection_reached=%t exit=%d stderr=%q, want post-transaction exit 6 and explicit rejection of prior exit 2", filesystem.reached, code, stderr.String())
			}
		})
	}
}

func TestResumeExplicitMissingRunIsARefusedPrecondition(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := runResume("missing-run", &stdout, &stderr, resume)
	if code != 2 || code == 6 || !strings.Contains(stderr.String(), "precondition refused") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 2 and explicit rejection of prior exit 6", code, stdout.String(), stderr.String())
	}
}
