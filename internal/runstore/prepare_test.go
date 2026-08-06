package runstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestCompleteOrAbandonPrepareCommitsUnfencedPlan(t *testing.T) {
	store, prepare := preparedCommitStore(t, nil)
	if err := store.CompleteOrAbandonPrepare(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare != nil || input.Projection.State.ScoreHead.Revision != 2 {
		t.Fatalf("committed prepare state = %+v", input.Projection.State)
	}
	for _, path := range []string{
		filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "prepares", string(prepare.ID)+".json"),
		filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "driver.quiesced."+string(prepare.ID)),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cleanup %s err=%v, want absent", path, err)
		}
	}
}

func TestCompleteOrAbandonPrepareClassifiesSilenceLimitPrepare(t *testing.T) {
	start, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	dead := Lease{Epoch: 1, Token: "dead-prepare", PID: os.Getpid(), Start: distinctStartIdentity(t, start)}
	store, _ := preparedCommitStore(t, &dead)
	if err := store.CompleteOrAbandonPrepare(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
}

func TestAcknowledgePrepareKeepsReceiptsAliveDuringSweep(t *testing.T) {
	store, driver, prepare := preparedAcknowledgementDriver(t)
	started := make(chan struct{})
	release := make(chan struct{})
	store.quiesceReceiptCadence = 5 * time.Millisecond
	store.sweepSessions = func(ctx context.Context, _ runstate.State) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	acknowledged := make(chan error, 1)
	go func() { acknowledged <- store.AcknowledgePrepare(context.Background(), driver, prepare.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("quiesce sweep did not start")
	}
	mutated := make(chan error, 1)
	go func() { mutated <- store.Mutate("run-1", "", func(*Txn) error { return nil }) }()
	select {
	case err := <-mutated:
		if err != nil {
			t.Fatalf("concurrent state mutation = %v", err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		<-acknowledged
		t.Fatal("quiesce sweep retained the state lock")
	}
	for deadline := time.After(2 * time.Second); ; {
		if quiesceReceiptRounds(t, store) >= 3 {
			break
		}
		select {
		case <-deadline:
			close(release)
			<-acknowledged
			t.Fatal("long quiesce sweep did not emit receipts at the configured cadence")
		case <-time.After(time.Millisecond):
		}
	}
	close(release)
	if err := <-acknowledged; err != nil {
		t.Fatal(err)
	}
	assertContiguousQuiesceReceipts(t, store)
}

func TestAcknowledgePrepareRefusesReceiptAfterQuiescedSidecar(t *testing.T) {
	store, driver, prepare := preparedAcknowledgementDriver(t)
	if err := store.AcknowledgePrepare(context.Background(), driver, prepare.ID); err != nil {
		t.Fatal(err)
	}
	err := store.AcknowledgePrepare(context.Background(), driver, prepare.ID)
	if !errors.Is(err, runstate.ErrIllegalTransition) || !strings.Contains(err.Error(), "prepare_pending") {
		t.Fatalf("receipt after sidecar error = %v, want prepare_pending refusal", err)
	}
}

func TestPrepareCommitUsesLatestDurableReceiptWithoutRecoveryRefresh(t *testing.T) {
	start, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	lease := Lease{Epoch: 1, Token: "live-receipt", PID: os.Getpid(), Start: start}
	store, prepare := preparedCommitStore(t, &lease)
	appendQuiesceReceiptEvent(t, store, prepare.ID, 1)
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	latest := input.Projection.State.PendingPrepare.LatestQuiesceObservedAt
	if latest == "" {
		t.Fatal("durable receipt was not replayed")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, latest)
	if err != nil {
		t.Fatal(err)
	}
	prepareCommitNow = func() time.Time { return observedAt.Add(59 * time.Second) }
	t.Cleanup(func() { prepareCommitNow = time.Now })
	next, _, err := classifyPreparedCommit(t, store, func(state *runstate.State) {
		state.PendingPrepare.PreparedAt = observedAt.Add(-2 * time.Minute).Format(time.RFC3339Nano)
	})
	if err != nil || next != prepareCommitWaiting {
		t.Fatalf("receipt-backed pre-expiry classification = %v, %v", next, err)
	}
	resumed, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := resumed.Projection.State.PendingPrepare.LatestQuiesceObservedAt; got != latest {
		t.Fatalf("resume refreshed durable receipt from %q to %q", latest, got)
	}
	prepareCommitNow = func() time.Time { return observedAt.Add(61 * time.Second) }
	next, _, err = classifyPreparedCommit(t, store, nil)
	if err != nil || next != prepareCommitFence {
		t.Fatalf("receipt-backed post-expiry classification = %v, %v", next, err)
	}
}

func TestCompleteOrAbandonPrepareSweepsBeforeSilenceFence(t *testing.T) {
	start, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	lease := Lease{Epoch: 1, Token: "gone-after-silence", PID: os.Getpid(), Start: distinctStartIdentity(t, start)}
	store, _ := preparedCommitStore(t, &lease)
	store.sweepSessions = func(context.Context, runstate.State) error { return errors.New("sweep failed") }
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	preparedAt, err := time.Parse(time.RFC3339Nano, input.Projection.State.PendingPrepare.PreparedAt)
	if err != nil {
		t.Fatal(err)
	}
	prepareCommitNow = func() time.Time { return preparedAt.Add(61 * time.Second) }
	t.Cleanup(func() { prepareCommitNow = time.Now })
	err = store.CompleteOrAbandonPrepare(context.Background(), "run-1")
	if err == nil || !strings.Contains(err.Error(), "sweep failed") {
		t.Fatalf("silence fence error = %v, want sweep failure", err)
	}
	input, err = store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare == nil {
		t.Fatal("silence fence committed without its required sweep")
	}
}

func TestCompleteOrAbandonPrepareRejectsMissingPlan(t *testing.T) {
	store, prepare := preparedCommitStore(t, nil)
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		_, err := transaction.At("test/remove-plan").RemoveDurable(Path(filepath.ToSlash(filepath.Join("prepares", string(prepare.ID)+".json"))))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err := store.CompleteOrAbandonPrepare(context.Background(), "run-1")
	if !errors.Is(err, ErrMissingPreparePlan) {
		t.Fatalf("error = %v, want ErrMissingPreparePlan", err)
	}
}

func TestCompleteOrAbandonPrepareRejectsMissingOrMismatchedSnapshot(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, runstate.PendingPrepare)
	}{
		{
			name: "missing",
			mutate: func(t *testing.T, store *Store, prepare runstate.PendingPrepare) {
				t.Helper()
				path := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "scores", "revision-"+strconv.FormatUint(prepare.NewHead.Revision, 10)+".yaml")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "raw hash mismatch",
			mutate: func(t *testing.T, store *Store, prepare runstate.PendingPrepare) {
				t.Helper()
				path := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "scores", "revision-"+strconv.FormatUint(prepare.NewHead.Revision, 10)+".yaml")
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(contents, []byte("# raw-hash mutation\n")...), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, prepare := preparedCommitStore(t, nil)
			test.mutate(t, store, prepare)
			if err := store.CompleteOrAbandonPrepare(context.Background(), "run-1"); !errors.Is(err, ErrMissingPinnedSnapshot) {
				t.Fatalf("error = %v, want ErrMissingPinnedSnapshot", err)
			}
		})
	}
}

func TestPrepareCommitClassifiesEveryNonFenceTableRow(t *testing.T) {
	t.Run("cancellation hands off before approval", func(t *testing.T) {
		store, _ := preparedCommitStore(t, nil)
		if err := store.Mutate("run-1", "", func(transaction *Txn) error {
			_, err := transaction.At("test.cancel").Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventCancelRequested, Payload: recoveryPayload(t, map[string]any{"requested_by": "cli"})})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		next, _, err := classifyPreparedCommit(t, store, nil)
		if err != nil || next != prepareCommitCancelled {
			t.Fatalf("cancelled commit classification = %v, %v", next, err)
		}
	})
	t.Run("matching sidecar approves", func(t *testing.T) {
		start, err := procid.Read(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		lease := Lease{Epoch: 1, Token: "quiesced", PID: os.Getpid(), Start: distinctStartIdentity(t, start)}
		store, prepare := preparedCommitStore(t, &lease)
		probe := &recordingProbe{}
		store.probe = probe
		if err := store.Mutate("run-1", "", func(transaction *Txn) error {
			_, err := transaction.At("test.quiesce").CompareMoveLease(lease.Identity(), Path("driver.quiesced."+string(prepare.ID)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteOrAbandonPrepare(context.Background(), "run-1"); err != nil {
			t.Fatal(err)
		}
		input, err := store.LoadRunInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if input.Projection.State.PendingPrepare != nil || input.Projection.State.Authority.Epoch != 1 {
			t.Fatalf("sidecar approval state = %+v", input.Projection.State)
		}
		if len(probe.points) != 1 || probe.points[0] != faultpoint.PointQuiesceCommitLockHeld {
			t.Fatalf("sidecar commit points = %v", probe.points)
		}
	})
	t.Run("unverifiable owner before silence expiry waits", func(t *testing.T) {
		lease := Lease{Epoch: 1, Token: "unverifiable", PID: os.Getpid(), Start: otherPlatformStartIdentity()}
		store, _ := preparedCommitStore(t, &lease)
		next, _, err := classifyPreparedCommit(t, store, nil)
		if next != prepareCommitWaiting || err != nil {
			t.Fatalf("unverifiable pre-expiry classification = %v, %v", next, err)
		}
	})
	t.Run("matching owner before silence expiry waits", func(t *testing.T) {
		start, err := procid.Read(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		lease := Lease{Epoch: 1, Token: "live", PID: os.Getpid(), Start: start}
		store, _ := preparedCommitStore(t, &lease)
		next, _, err := classifyPreparedCommit(t, store, nil)
		if err != nil || next != prepareCommitWaiting {
			t.Fatalf("waiting commit classification = %v, %v", next, err)
		}
	})
	t.Run("matching owner after silence expiry fences", func(t *testing.T) {
		start, err := procid.Read(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		lease := Lease{Epoch: 1, Token: "expired", PID: os.Getpid(), Start: start}
		store, _ := preparedCommitStore(t, &lease)
		prepareCommitNow = func() time.Time { return time.Now() }
		t.Cleanup(func() { prepareCommitNow = time.Now })
		next, _, err := classifyPreparedCommit(t, store, func(state *runstate.State) {
			state.PendingPrepare.PreparedAt = time.Now().Add(-61 * time.Second).UTC().Format(time.RFC3339Nano)
		})
		if err != nil || next != prepareCommitFence {
			t.Fatalf("silence-expired matching owner classification = %v, %v", next, err)
		}
	})
}

func TestPrepareCommitDefensiveConsistencyAbandonmentCleansUpBeforeAppending(t *testing.T) {
	for _, test := range []struct {
		name   string
		reason string
		mutate func(*runstate.State)
	}{
		{
			name:   "base head changed",
			reason: "base_head_changed",
			mutate: func(state *runstate.State) { state.ScoreHead.Revision++ },
		},
		{
			name:   "plan invalidated",
			reason: "plan_invalidated",
			mutate: func(state *runstate.State) { state.PendingPrepare.ProposalID = "other" },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, prepare := preparedCommitStore(t, nil)
			runRoot := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1")
			snapshot := filepath.Join(runRoot, "scores", "revision-2.yaml")
			plan := filepath.Join(runRoot, "prepares", string(prepare.ID)+".json")
			sidecar := filepath.Join(runRoot, "driver.quiesced."+string(prepare.ID))
			snapshotBytes, err := os.ReadFile(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Mutate("run-1", "", func(transaction *Txn) error {
				_, err := transaction.At("test.sidecar").PublishImmutable(Path("driver.quiesced."+string(prepare.ID)), []byte("sidecar"), Hash(rawHash([]byte("sidecar"))))
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{snapshot, plan, sidecar} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("fixture path %s: %v", path, err)
				}
			}

			var addresses []faultpoint.ReceiptAddress
			store.receiptObserver = receiptObserverFunc(func(receipt DurabilityReceipt) {
				addresses = append(addresses, receipt.Address)
			})
			next, _, err := classifyPreparedCommit(t, store, test.mutate)
			if err != nil || next != prepareCommitDone {
				t.Fatalf("defensive abandonment classification = %v, %v", next, err)
			}
			wantAddresses := []faultpoint.ReceiptAddress{
				"prepare.commit.snapshot",
				"prepare.commit.plan",
				"prepare.commit.sidecar",
				"prepare.commit.abandoned",
			}
			if len(addresses) != len(wantAddresses) {
				t.Fatalf("durable operation order = %v, want %v", addresses, wantAddresses)
			}
			for index, want := range wantAddresses {
				if addresses[index] != want {
					t.Fatalf("durable operation %d = %q, want %q (all=%v)", index, addresses[index], want, addresses)
				}
			}

			quarantined := filepath.Join(runRoot, "quarantine", "abandoned_prepare", strings.TrimPrefix(rawHash(snapshotBytes), "sha256:"), "revision-2.yaml")
			quarantinedBytes, err := os.ReadFile(quarantined)
			if err != nil || !bytes.Equal(quarantinedBytes, snapshotBytes) {
				t.Fatalf("quarantined snapshot = %q, %v; want original snapshot", quarantined, err)
			}
			for _, path := range []string{snapshot, plan, sidecar} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("cleanup %s err=%v, want absent", path, err)
				}
			}
			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			last := journal.Events[len(journal.Events)-1]
			if last.Type != runstate.EventAmendmentApprovalAbandoned {
				t.Fatalf("last event = %s, want %s", last.Type, runstate.EventAmendmentApprovalAbandoned)
			}
			var payload map[string]any
			if err := json.Unmarshal(last.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if got, _ := payload["reason"].(string); got != test.reason {
				t.Fatalf("abandonment reason = %q, want %q", got, test.reason)
			}
		})
	}
}

func classifyPreparedCommit(t *testing.T, store *Store, mutate func(*runstate.State)) (prepareCommitNext, Lease, error) {
	t.Helper()
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	state := input.Projection.State
	if mutate != nil {
		mutate(&state)
	}
	var next prepareCommitNext
	var lease Lease
	err = store.Mutate("run-1", "", func(transaction *Txn) error {
		var classifyErr error
		next, lease, classifyErr = transaction.classifyPrepareCommit(state)
		return classifyErr
	})
	return next, lease, err
}

func preparedAcknowledgementDriver(t *testing.T) (*Store, *Driver, runstate.PendingPrepare) {
	t.Helper()
	start, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	lease := Lease{Epoch: 1, Token: "quiesce-driver", PID: os.Getpid(), Start: start}
	store, prepare := preparedCommitStore(t, &lease)
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	return store, &Driver{store: store, runID: "run-1", seed: movementSeed(input.Score), lease: lease}, prepare
}

func appendQuiesceReceiptEvent(t *testing.T, store *Store, prepareID runstate.PrepareID, round uint64) {
	t.Helper()
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		payload := recoveryPayload(t, map[string]any{"prepare_id": string(prepareID), "sweep_round": round})
		_, err := transaction.At("test.quiesce-observed").Append(runstate.Event{
			RunID: "run-1", ScoreRevision: 1, Type: runstate.EventAmendmentQuiesceObserved, Payload: payload,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func quiesceReceiptRounds(t *testing.T, store *Store) int {
	t.Helper()
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range journal.Events {
		if event.Type == runstate.EventAmendmentQuiesceObserved {
			count++
		}
	}
	return count
}

func assertContiguousQuiesceReceipts(t *testing.T, store *Store) {
	t.Helper()
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(1)
	for _, event := range journal.Events {
		if event.Type != runstate.EventAmendmentQuiesceObserved {
			continue
		}
		var payload struct {
			SweepRound uint64 `json:"sweep_round"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.SweepRound != want {
			t.Fatalf("quiesce receipt round = %d, want %d", payload.SweepRound, want)
		}
		want++
	}
	if want < 4 {
		t.Fatalf("quiesce receipt count = %d, want at least 3", want-1)
	}
}

func otherPlatformStartIdentity() runstate.StartIdentity {
	if runtime.GOOS == "darwin" {
		return runstate.LinuxStartIdentity{BootID: "foreign", StartTicks: "1"}
	}
	return runstate.DarwinStartIdentity{StartTVSec: 1, StartTVUsec: 1}
}

func preparedCommitStore(t *testing.T, lease *Lease) (*Store, runstate.PendingPrepare) {
	t.Helper()
	store := recoveryStore(t)
	observedEpoch := uint64(0)
	if lease != nil {
		appendRecoveryAuthorityAndLease(t, store, *lease)
		observedEpoch = lease.Epoch
	}
	updatedBytes := recoveryScoreJSON(t, 2, "approved recovery/control fixture")
	updated, diagnostics := score.Compile(updatedBytes)
	if len(diagnostics) != 0 {
		t.Fatalf("updated score diagnostics = %v", diagnostics)
	}
	updatedHash, err := updated.Hash()
	if err != nil {
		t.Fatal(err)
	}
	base, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	envelope := "NARROW_PATHS"
	plan := runstate.ApprovalPlan{
		Schema:              runstate.ApprovalPlanSchema,
		ProposalID:          "proposal-prepare",
		Mode:                "auto",
		EnvelopeClass:       &envelope,
		BaseRevision:        1,
		BaseHash:            base.Projection.State.ScoreHead.SemanticHash,
		ClassifierVersion:   1,
		NewRevision:         2,
		NewSnapshotHash:     runstate.Hash(updatedHash),
		NewSnapshotFileHash: runstate.Hash(rawHash(updatedBytes)),
		TypedDelta:          []any{},
		ActualImpact:        recoveryActualImpact(),
		HeadMovements: []runstate.HeadMovement{
			{ID: "write", Initial: runstate.MovementPending, RepoWrite: true},
			{ID: "read", Initial: runstate.MovementPending, HasDependencies: true},
		},
		SupersededAttemptIDs: []runstate.AttemptID{},
		ObsoletedDecisionIDs: []string{},
		Finalization:         false,
		IdentityVersions:     recoveryVersions(),
	}
	planBytes, err := runstate.EncodeApprovalPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	prepare := runstate.PendingPrepare{
		ID: "prepare-1", ProposalID: plan.ProposalID, Mode: plan.Mode, EnvelopeClass: envelope,
		BaseHead:       base.Projection.State.ScoreHead,
		NewHead:        runstate.ScoreHead{Revision: plan.NewRevision, SemanticHash: plan.NewSnapshotHash, FileHash: plan.NewSnapshotFileHash},
		PlanRecordHash: Hash(rawHash(planBytes)), ObservedAuthorityEpoch: observedEpoch,
		QuiesceSilenceLimitMS: 60_000, ClassifierVersion: plan.ClassifierVersion,
		TargetAttemptIDs: []runstate.AttemptID{},
	}
	if err := store.Mutate("run-1", "", func(transaction *Txn) error {
		if _, err := transaction.At("test/prepared-score").PublishImmutable("scores/revision-2.yaml", updatedBytes, Hash(rawHash(updatedBytes))); err != nil {
			return err
		}
		if _, err := transaction.At("test/prepared-plan").PublishImmutable("prepares/prepare-1.json", planBytes, Hash(rawHash(planBytes))); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{
			"prepare_id": string(prepare.ID), "proposal_id": string(prepare.ProposalID), "mode": prepare.Mode, "envelope_class": prepare.EnvelopeClass,
			"base_revision": prepare.BaseHead.Revision, "base_hash": string(prepare.BaseHead.SemanticHash), "new_revision": prepare.NewHead.Revision,
			"new_snapshot_hash": string(prepare.NewHead.SemanticHash), "new_snapshot_file_hash": string(prepare.NewHead.FileHash),
			"plan_record_hash": string(prepare.PlanRecordHash), "target_attempt_ids": []any{}, "observed_authority_epoch": prepare.ObservedAuthorityEpoch,
			"quiesce_silence_limit_ms": 60_000, "classifier_version": prepare.ClassifierVersion, "identity_versions": recoveryVersions(),
		})
		if err != nil {
			return err
		}
		_, err = transaction.At("test/prepared").Append(runstate.Event{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventAmendmentApprovalPrepared, Payload: payload})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return store, prepare
}
