package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

func expectedDecisionResumeHint(runID string) string {
	return fmt.Sprintf("run waiting: state=%q resume=%q\n", "nonterminal", "partitur resume "+runID)
}

func expectedOwnerUnverifiableDiagnostic(runID string) string {
	return fmt.Sprintf("run blocked: run_id=%q state=%q reason=%q\n", runID, "nonterminal", "owner_unverifiable")
}

func TestDecisionResolutionResumeHintAfterLastBlocker(t *testing.T) {
	for _, test := range []struct {
		name         string
		decisionType string
		invoke       func(string, *bytes.Buffer) int
	}{
		{name: "answer", decisionType: "question", invoke: func(id string, stderr *bytes.Buffer) int {
			return runAnswer(id, "continue", stderr)
		}},
		{name: "approve", decisionType: "human_gate", invoke: func(id string, stderr *bytes.Buffer) int {
			return runApprove(id, true, nil, "", stderr)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, store := resumeAttemptFixture(t)
			decisionID := appendPendingCLIDecision(t, store, test.decisionType)
			t.Chdir(root)

			var stderr bytes.Buffer
			if code := test.invoke(decisionID, &stderr); code != 0 {
				t.Fatalf("exit=%d stderr=%q, want success", code, stderr.String())
			}
			if got, want := stderr.String(), expectedDecisionResumeHint("run-1"); got != want {
				t.Fatalf("stderr=%q, want %q", got, want)
			}
		})
	}
}

func TestDecisionResolutionWithLiveOwnerHasEmptyStderr(t *testing.T) {
	root, store := resumeAttemptFixture(t)
	decisionID := appendPendingCLIDecision(t, store, "question")
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Release() })
	wake := make(chan os.Signal, 1)
	signal.Notify(wake, syscall.SIGUSR1)
	t.Cleanup(func() { signal.Stop(wake) })
	t.Chdir(root)

	var stderr bytes.Buffer
	if code := runAnswer(decisionID, "continue", &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want success", code, stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want empty", got)
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("live lease owner did not receive the best-effort wake")
	}
}

func TestDecisionResolutionReconcilesDriverAcquiredAfterDecision(t *testing.T) {
	_, store := resumeAttemptFixture(t)
	decisionID := appendPendingCLIDecision(t, store, "question")
	if err := store.ResolveQuestion("run-1", decisionID, "continue"); err != nil {
		t.Fatal(err)
	}
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Release() })
	lease, present, err := store.ReadLease("run-1")
	if err != nil || !present {
		t.Fatalf("read acquired lease: present=%t err=%v", present, err)
	}
	resolution := classifyDecisionResolution(store, "run-1", false)
	var stderr bytes.Buffer
	renderDecisionResumeHint(&stderr, resolution)
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want no hint for the newly acquired live owner", got)
	}

	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	decision := recovery.Plan(recovery.Input{
		Projection: input.Projection,
		Observations: recovery.Observations{Lease: recovery.LeaseObservation{
			Exists: true, Readable: true, Epoch: lease.Epoch, Owner: recovery.OwnerLive,
		}},
	})
	if decision.CaseID != recovery.CaseLiveOwner {
		t.Fatalf("fresh recovery decision=%s, want %s", decision.CaseID, recovery.CaseLiveOwner)
	}
}

func TestDecisionResolutionAlwaysReconcilesSameEpochReleasedOwner(t *testing.T) {
	root, store := resumeAttemptFixture(t)
	decisionID := appendPendingCLIDecision(t, store, "question")
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	driverReleased := false
	t.Cleanup(func() {
		if !driverReleased {
			_ = driver.Release()
		}
	})
	lease, present, err := store.ReadLease("run-1")
	if err != nil || !present {
		t.Fatalf("read live lease: present=%t err=%v", present, err)
	}
	ownerEpoch := lease.Epoch
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Authority.Epoch != ownerEpoch || input.Projection.State.Authority.Owner == nil {
		t.Fatalf("authority=%+v lease_epoch=%d, want the live owner already established", input.Projection.State.Authority, ownerEpoch)
	}

	wake := make(chan os.Signal, 1)
	signal.Notify(wake, syscall.SIGUSR1)
	t.Cleanup(func() { signal.Stop(wake) })
	filesystem := &journalFailureFS{}
	newBlockerReleased := false
	filesystem.afterLeaseRead = func() {
		payload := resumePayload(t, map[string]any{
			"decision_id":       "human_gate-2",
			"decision_type":     "human_gate",
			"gate_id":           "gate-attempt-2",
			"gate_mode":         "always",
			"subject_tree":      "git-sha1:tree",
			"blocking_findings": []any{},
		})
		if _, err := driver.Append(runstate.Event{
			RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1",
			Type: runstate.EventDecisionRequested, Payload: payload,
		}, "fixture.resume_hint.same_epoch_human_gate"); err != nil {
			t.Fatal(err)
		}
		if err := driver.Release(); err != nil {
			t.Fatal(err)
		}
		driverReleased = true
		newBlockerReleased = true
	}
	installJournalFailureStore(t, filesystem)
	t.Chdir(root)

	var stderr bytes.Buffer
	if code := runAnswer(decisionID, "continue", &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want committed success", code, stderr.String())
	}
	if !newBlockerReleased {
		t.Fatal("same-epoch owner did not append the new blocker and release its lease")
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want empty for the current waiting lifecycle", got)
	}
	lease, present, err = store.ReadLease("run-1")
	if err != nil || present {
		t.Fatalf("released same-epoch lease=%+v present=%t err=%v", lease, present, err)
	}
	input, err = store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Authority.Epoch != ownerEpoch {
		t.Fatalf("current epoch=%d, want unchanged epoch %d", input.Projection.State.Authority.Epoch, ownerEpoch)
	}
	decision := recovery.Plan(recovery.Input{Projection: input.Projection})
	if decision.CaseID != recovery.CaseHumanGateWaiting {
		t.Fatalf("fresh recovery decision=%s, want %s", decision.CaseID, recovery.CaseHumanGateWaiting)
	}
}

func TestDecisionResolutionUnknownLeaseStatusHasEmptyStderr(t *testing.T) {
	resolution := classifyDecisionResumeEligibility(
		decisionResolution{runID: "run-1"},
		&runstore.DecisionResolution{Run: runstate.RunRunning},
		runstore.ResumeLeaseStatus(255),
	)

	var stderr bytes.Buffer
	renderDecisionResumeHint(&stderr, resolution)
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want empty for unknown lease status", got)
	}
}

func TestDecisionResolutionReconciliationUsesInitialSeedAfterRevision(t *testing.T) {
	_, store := resumeAttemptFixture(t)
	if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		_, err := transaction.At("fixture.resume_hint.pre_revision_ready").Append(runstate.Event{
			RunID: "run-1", ScoreRevision: 1, MovementID: "review", Type: runstate.EventMovementReady,
			Payload: resumePayload(t, map[string]any{}),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	appendResumeApprovedSnapshot(t, store)
	pid := os.Getpid()
	start, err := procid.Read(pid)
	if err != nil {
		t.Fatal(err)
	}
	lease := runstore.Lease{Epoch: 1, Token: "revised-run-fixture", PID: pid, Start: start}
	if err := store.MutateProjected("run-1", func(transaction *runstore.Txn, state runstate.State) error {
		payload, err := json.Marshal(map[string]any{
			"authority_epoch":      lease.Epoch,
			"owner_pid":            lease.PID,
			"owner_start_identity": resumeHintStartIdentity(t, lease.Start),
		})
		if err != nil {
			return err
		}
		event := runstate.Event{
			RunID: "run-1", ScoreRevision: state.ScoreHead.Revision,
			Type: runstate.EventAuthorityGranted, Payload: payload,
		}
		if _, err := runstate.Apply(state, event); err != nil {
			return err
		}
		if _, err := transaction.At("fixture.resume_hint.revised_authority").Append(event); err != nil {
			return err
		}
		_, err = transaction.At("fixture.resume_hint.revised_lease").CreateLease(true, lease)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
			_, err := transaction.At("fixture.resume_hint.revised_lease_cleanup").CompareRemoveLease(lease.Identity())
			return err
		})
	})

	snapshot, err := store.ClassifyCurrentResumeLease("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LeaseStatus != runstore.ResumeLeaseLiveOwner {
		t.Fatalf("lease status=%d, want %d", snapshot.LeaseStatus, runstore.ResumeLeaseLiveOwner)
	}
}

func TestDecisionResolutionReconciliationCarriesCurrentWaitingLifecycle(t *testing.T) {
	_, store := resumeAttemptFixture(t)
	decisionID := appendPendingCLIDecision(t, store, "question")
	if err := store.ResolveQuestion("run-1", decisionID, "continue"); err != nil {
		t.Fatal(err)
	}
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	lease, present, err := store.ReadLease("run-1")
	if err != nil || !present {
		t.Fatalf("read newer driver lease: present=%t err=%v", present, err)
	}
	payload := resumePayload(t, map[string]any{
		"decision_id":       "human_gate-2",
		"decision_type":     "human_gate",
		"gate_id":           "gate-attempt-2",
		"gate_mode":         "always",
		"subject_tree":      "git-sha1:tree",
		"blocking_findings": []any{},
	})
	if _, err := driver.Append(runstate.Event{
		RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1",
		Type: runstate.EventDecisionRequested, Payload: payload,
	}, "fixture.resume_hint.concurrent_human_gate"); err != nil {
		t.Fatal(err)
	}
	if err := driver.Release(); err != nil {
		t.Fatal(err)
	}
	resolution := classifyDecisionResolution(store, "run-1", false)
	var stderr bytes.Buffer
	renderDecisionResumeHint(&stderr, resolution)
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want exactly empty for the current waiting lifecycle", got)
	}
	lease, present, err = store.ReadLease("run-1")
	if err != nil || present {
		t.Fatalf("released concurrent lease=%+v present=%t err=%v", lease, present, err)
	}
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	decision := recovery.Plan(recovery.Input{Projection: input.Projection})
	if decision.CaseID != recovery.CaseHumanGateWaiting {
		t.Fatalf("fresh recovery decision=%s, want %s", decision.CaseID, recovery.CaseHumanGateWaiting)
	}
}

func TestDecisionResolutionReconciliationFailureStaysSuccessfulWithoutHint(t *testing.T) {
	root, store := resumeAttemptFixture(t)
	decisionID := appendPendingCLIDecision(t, store, "question")
	t.Chdir(root)
	filesystem := &journalFailureFS{}
	var driver *runstore.Driver
	filesystem.afterLeaseRead = func() {
		var err error
		driver, err = store.AcquireRecoveryDriver("run-1")
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{
			filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml"),
			filepath.Join(root, ".partitur", "runs", "run-1", "resolved-cast.yaml"),
		} {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
	}
	installJournalFailureStore(t, filesystem)
	t.Cleanup(func() {
		if driver != nil {
			_ = driver.Release()
		}
	})

	var stderr bytes.Buffer
	if code := runAnswer(decisionID, "continue", &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want committed success", code, stderr.String())
	}
	if driver == nil {
		t.Fatal("reconciliation race did not acquire a recovery driver")
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want no advisory output after reconciliation failure", got)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) < 2 || journal.Events[len(journal.Events)-2].Type != runstate.EventDecisionResolved || journal.Events[len(journal.Events)-1].Type != runstate.EventAuthorityGranted {
		t.Fatalf("journal=%v, want durable decision followed by concurrent authority", journal.Events)
	}
}

func TestDecisionResolutionWithLiveInvalidLeasePrintsResumeHint(t *testing.T) {
	for _, relation := range []string{"stale", "orphan"} {
		t.Run(relation, func(t *testing.T) {
			root, store := resumeAttemptFixture(t)
			decisionID := appendPendingCLIDecision(t, store, "question")
			driver, err := store.AcquireRecoveryDriver("run-1")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = driver.Release() })
			lease, present, err := store.ReadLease("run-1")
			if err != nil || !present {
				t.Fatalf("read live lease: present=%t err=%v", present, err)
			}
			makeLiveLeaseInvalid(t, store, lease, relation)
			wake := make(chan os.Signal, 1)
			signal.Notify(wake, syscall.SIGUSR1)
			t.Cleanup(func() { signal.Stop(wake) })
			t.Chdir(root)

			var stderr bytes.Buffer
			if code := runAnswer(decisionID, "continue", &stderr); code != 0 {
				t.Fatalf("exit=%d stderr=%q, want success", code, stderr.String())
			}
			if got, want := stderr.String(), expectedDecisionResumeHint("run-1"); got != want {
				t.Fatalf("stderr=%q, want %q for live %s lease", got, want, relation)
			}
			select {
			case <-wake:
			case <-time.After(time.Second):
				t.Fatalf("live %s lease owner did not receive the best-effort wake", relation)
			}
		})
	}
}

func TestDecisionResolutionWithDeadOrReusedMatchingLeasePrintsResumeHint(t *testing.T) {
	_, store := resumeAttemptFixture(t)
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Release() })
	lease, present, err := store.ReadLease("run-1")
	if err != nil || !present {
		t.Fatalf("read live lease: present=%t err=%v", present, err)
	}
	replacement := lease
	replacement.Start = differentResumeHintStartIdentity(t, lease.Start)
	replaceResumeHintLease(t, store, lease, replacement)
	if match := replacement.MatchOwner(); match.Status != procid.GoneOrReused {
		t.Fatalf("replacement owner status=%s err=%v, want %s", match.Status, match.Err, procid.GoneOrReused)
	}

	resolution := classifyDecisionResolution(store, "run-1", false)
	var stderr bytes.Buffer
	renderDecisionResumeHint(&stderr, resolution)
	if got, want := stderr.String(), expectedDecisionResumeHint("run-1"); got != want {
		t.Fatalf("stderr=%q, want %q", got, want)
	}
}

func TestDecisionResolutionWithUnreadableLeasePrintsOwnerUnverifiableDiagnostic(t *testing.T) {
	root, store := resumeAttemptFixture(t)
	decisionID := appendPendingCLIDecision(t, store, "question")
	t.Chdir(root)
	filesystem := &journalFailureFS{failLeaseRead: true}
	installJournalFailureStore(t, filesystem)

	var stderr bytes.Buffer
	if code := runAnswer(decisionID, "continue", &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want committed success", code, stderr.String())
	}
	if !filesystem.leaseReadReached {
		t.Fatal("injected unreadable lease was not observed")
	}
	if got, want := stderr.String(), expectedOwnerUnverifiableDiagnostic("run-1"); got != want {
		t.Fatalf("stderr=%q, want %q", got, want)
	}
}

func TestDecisionResolutionWithUnverifiableOwnerPrintsOwnerUnverifiableDiagnostic(t *testing.T) {
	root, store := resumeAttemptFixture(t)
	decisionID := appendPendingCLIDecision(t, store, "question")
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = driver.Release() })
	lease, present, err := store.ReadLease("run-1")
	if err != nil || !present {
		t.Fatalf("read live lease: present=%t err=%v", present, err)
	}
	replacement := lease
	replacement.Start = differentResumeHintPlatformIdentity(t, lease.Start)
	replaceResumeHintLease(t, store, lease, replacement)
	if match := replacement.MatchOwner(); match.Status != procid.Unverifiable {
		t.Fatalf("replacement owner status=%s err=%v, want %s", match.Status, match.Err, procid.Unverifiable)
	}
	t.Chdir(root)

	var stderr bytes.Buffer
	if code := runAnswer(decisionID, "continue", &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want committed success", code, stderr.String())
	}
	if got, want := stderr.String(), expectedOwnerUnverifiableDiagnostic("run-1"); got != want {
		t.Fatalf("stderr=%q, want %q", got, want)
	}
}

func TestDecisionResolutionTerminalWithUnverifiableResidualLeaseHasEmptyStderr(t *testing.T) {
	root, store := resumeAttemptFixture(t)
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Append(resumeEvent("run-1", runstate.EventRunFailed, map[string]any{"reason": "fixture"}), "fixture.resume_hint.failed"); err != nil {
		t.Fatal(err)
	}
	lease, present, err := store.ReadLease("run-1")
	if err != nil || !present {
		t.Fatalf("terminal residual lease=%+v present=%t err=%v", lease, present, err)
	}
	filesystem := &journalFailureFS{failLeaseRead: true}
	classifiedStore, err := runstore.NewWithFileSystem(root, faultpoint.Nop{}, filesystem)
	if err != nil {
		t.Fatal(err)
	}

	resolution := classifyDecisionResolution(classifiedStore, "run-1", false)
	if !filesystem.leaseReadReached {
		t.Fatal("terminal residual lease was not classified as unreadable")
	}
	var stderr bytes.Buffer
	renderDecisionResumeHint(&stderr, resolution)
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want terminality to suppress lease diagnostic", got)
	}
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	decision := recovery.Plan(recovery.Input{
		Projection: input.Projection,
		Observations: recovery.Observations{Lease: recovery.LeaseObservation{
			Exists: true, Readable: false, Epoch: lease.Epoch,
		}},
	})
	if decision.CaseID != recovery.CaseTerminal {
		t.Fatalf("fresh recovery decision=%s, want %s", decision.CaseID, recovery.CaseTerminal)
	}
}

func replaceResumeHintLease(t *testing.T, store *runstore.Store, current, replacement runstore.Lease) {
	t.Helper()
	if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		if _, err := transaction.At("fixture.resume_hint.current_lease.remove").CompareRemoveLease(current.Identity()); err != nil {
			return err
		}
		_, err := transaction.At("fixture.resume_hint.replacement_lease.create").CreateLease(true, replacement)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func makeLiveLeaseInvalid(t *testing.T, store *runstore.Store, lease runstore.Lease, relation string) {
	t.Helper()
	switch relation {
	case "stale":
		payload, err := json.Marshal(map[string]any{
			"authority_epoch":      lease.Epoch + 1,
			"owner_pid":            lease.PID,
			"owner_start_identity": resumeHintStartIdentity(t, lease.Start),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
			_, err := transaction.At("fixture.resume_hint.stale_authority").Append(runstate.Event{
				RunID: "run-1", ScoreRevision: 1, Type: runstate.EventAuthorityGranted, Payload: payload,
			})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	case "orphan":
		replacement := lease
		replacement.Epoch++
		if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
			if _, err := transaction.At("fixture.resume_hint.current_lease.remove").CompareRemoveLease(lease.Identity()); err != nil {
				return err
			}
			_, err := transaction.At("fixture.resume_hint.orphan_lease.create").CreateLease(true, replacement)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown live lease relation %q", relation)
	}
}

func resumeHintStartIdentity(t *testing.T, identity runstate.StartIdentity) map[string]any {
	t.Helper()
	switch value := identity.(type) {
	case runstate.LinuxStartIdentity:
		return map[string]any{"platform": "linux", "boot_id": value.BootID, "start_ticks": value.StartTicks}
	case runstate.DarwinStartIdentity:
		return map[string]any{"platform": "darwin", "start_tvsec": value.StartTVSec, "start_tvusec": value.StartTVUsec}
	default:
		t.Fatalf("unsupported live lease identity %T", identity)
		return nil
	}
}

func differentResumeHintStartIdentity(t *testing.T, identity runstate.StartIdentity) runstate.StartIdentity {
	t.Helper()
	switch value := identity.(type) {
	case runstate.LinuxStartIdentity:
		value.BootID += "-different"
		return value
	case runstate.DarwinStartIdentity:
		value.StartTVUsec++
		return value
	default:
		t.Fatalf("unsupported live lease identity %T", identity)
		return nil
	}
}

func differentResumeHintPlatformIdentity(t *testing.T, identity runstate.StartIdentity) runstate.StartIdentity {
	t.Helper()
	switch identity.(type) {
	case runstate.LinuxStartIdentity:
		return runstate.DarwinStartIdentity{StartTVSec: 1}
	case runstate.DarwinStartIdentity:
		return runstate.LinuxStartIdentity{BootID: "different-platform", StartTicks: "1"}
	default:
		t.Fatalf("unsupported live lease identity %T", identity)
		return nil
	}
}

func TestDecisionResolutionWithAnotherBlockerHasEmptyStderr(t *testing.T) {
	root, store := resumeAttemptFixture(t)
	decisionID := appendPendingCLIDecision(t, store, "question")
	appendPendingCLIDecision(t, store, "human_gate")
	t.Chdir(root)

	var stderr bytes.Buffer
	if code := runAnswer(decisionID, "continue", &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want success", code, stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want empty while another blocking decision remains", got)
	}
}

func TestDecisionResolutionWithPendingCancellationPrintsResumeHint(t *testing.T) {
	root, store := resumeAttemptFixture(t)
	decisionID := appendPendingCLIDecision(t, store, "question")
	appendPendingCLIDecision(t, store, "human_gate")
	if err := store.RequestCancellation("run-1"); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	var stderr bytes.Buffer
	if code := runAnswer(decisionID, "continue", &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want success", code, stderr.String())
	}
	if got, want := stderr.String(), expectedDecisionResumeHint("run-1"); got != want {
		t.Fatalf("stderr=%q, want %q", got, want)
	}
}

func TestDecisionResolutionCurrentSnapshotFailureStaysSuccessfulWithoutHint(t *testing.T) {
	root, store := resumeAttemptFixture(t)
	decisionID := appendPendingCLIDecision(t, store, "question")
	t.Chdir(root)
	filesystem := &journalFailureFS{}
	filesystem.afterJournalSync = func() {
		for _, path := range []string{
			filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml"),
			filepath.Join(root, ".partitur", "runs", "run-1", "resolved-cast.yaml"),
		} {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
	}
	installJournalFailureStore(t, filesystem)

	var stderr bytes.Buffer
	if code := runAnswer(decisionID, "continue", &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want committed success", code, stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want no advisory output after current snapshot failure", got)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := journal.Events[len(journal.Events)-1].Type; got != "decision.resolved" {
		t.Fatalf("last event=%q, want durable decision.resolved", got)
	}
}

func TestRoutedApprovalResumeHint(t *testing.T) {
	root, _, decisionID, _ := routedAmendmentCommandFixture(t)
	t.Chdir(root)

	var stderr bytes.Buffer
	if code := runApprove(decisionID, true, nil, "", &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want success", code, stderr.String())
	}
	if got, want := stderr.String(), expectedDecisionResumeHint("run-1"); got != want {
		t.Fatalf("stderr=%q, want %q", got, want)
	}
}

func TestRoutedApprovalResolvedCastReloadFailureStillPrintsHint(t *testing.T) {
	root, store, decisionID, _ := routedAmendmentCommandFixture(t)
	t.Chdir(root)
	filesystem := &journalFailureFS{}
	var afterJournalSync func()
	afterJournalSync = func() {
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if journal.Events[len(journal.Events)-1].Type != runstate.EventAmendmentApproved {
			filesystem.afterJournalSync = afterJournalSync
			return
		}
		path := filepath.Join(root, ".partitur", "runs", "run-1", "resolved-cast.yaml")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	filesystem.afterJournalSync = afterJournalSync
	installJournalFailureStore(t, filesystem)

	var stderr bytes.Buffer
	if code := runApprove(decisionID, true, nil, "", &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%q, want committed success", code, stderr.String())
	}
	if got, want := stderr.String(), expectedDecisionResumeHint("run-1"); got != want {
		t.Fatalf("stderr=%q, want %q when only the LoadRunInput dependency is absent", got, want)
	}
	if _, err := store.LoadRunInput("run-1"); err == nil {
		t.Fatal("LoadRunInput succeeded without resolved-cast.yaml")
	}
	if _, err := store.ClassifyCurrentResumeLease("run-1"); err != nil {
		t.Fatalf("current snapshot failed with immutable initial seed and journal intact: %v", err)
	}
	initialSeed := filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml")
	if _, err := os.Stat(initialSeed); err != nil {
		t.Fatalf("immutable initial seed is not intact: %v", err)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := journal.Events[len(journal.Events)-1].Type; got != runstate.EventAmendmentApproved {
		t.Fatalf("last event=%q, want durable amendment.approved", got)
	}
}

func TestRoutedApprovalDecisionTimeRejectionWakesOrHints(t *testing.T) {
	for _, liveOwner := range []bool{false, true} {
		name := "no_owner"
		if liveOwner {
			name = "live_owner"
		}
		t.Run(name, func(t *testing.T) {
			root, store, decisionID, proposalID := routedAmendmentCommandFixture(t)
			appendPendingCLIDecision(t, store, "human_gate")
			if err := store.RequestCancellation("run-1"); err != nil {
				t.Fatal(err)
			}
			var wake chan os.Signal
			if liveOwner {
				driver, err := store.AcquireRecoveryDriver("run-1")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = driver.Release() })
				wake = make(chan os.Signal, 1)
				signal.Notify(wake, syscall.SIGUSR1)
				t.Cleanup(func() { signal.Stop(wake) })
			}
			t.Chdir(root)

			var stderr bytes.Buffer
			if code := runApprove(decisionID, true, nil, "", &stderr); code != 3 {
				t.Fatalf("exit=%d stderr=%q, want decision-time rejection", code, stderr.String())
			}
			want := fmt.Sprintf("amendment rejected: proposal_id=%q reason=%q\n", proposalID, "run_cancelling")
			if !liveOwner {
				want += expectedDecisionResumeHint("run-1")
			}
			if got := stderr.String(); got != want {
				t.Fatalf("stderr=%q, want %q", got, want)
			}
			if liveOwner {
				select {
				case <-wake:
				case <-time.After(time.Second):
					t.Fatal("live lease owner did not receive the best-effort wake")
				}
			}
		})
	}
}
