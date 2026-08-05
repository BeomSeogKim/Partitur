package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

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

func TestCompleteOrAbandonPrepareFailsClosedWithoutLegacyDeadline(t *testing.T) {
	start, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	dead := Lease{Epoch: 1, Token: "dead-prepare", PID: os.Getpid(), Start: distinctStartIdentity(t, start)}
	store, _ := preparedCommitStore(t, &dead)
	if err := store.CompleteOrAbandonPrepare(context.Background(), "run-1"); err == nil {
		t.Fatal("legacy deadline commit path accepted a silence-limit prepare")
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
	t.Run("unverifiable owner without legacy deadline fails closed", func(t *testing.T) {
		lease := Lease{Epoch: 1, Token: "unverifiable", PID: os.Getpid(), Start: otherPlatformStartIdentity()}
		store, _ := preparedCommitStore(t, &lease)
		next, _, err := classifyPreparedCommit(t, store, nil)
		if next != prepareCommitDone || err == nil {
			t.Fatalf("unverifiable silence-limit classification = %v, %v", next, err)
		}
	})
	t.Run("matching owner before deadline waits", func(t *testing.T) {
		start, err := procid.Read(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		lease := Lease{Epoch: 1, Token: "live", PID: os.Getpid(), Start: start}
		store, prepare := preparedCommitStore(t, &lease)
		prepare.QuiesceDeadline = "2999-01-01T00:00:00.000Z"
		next, _, err := classifyPreparedCommit(t, store, func(state *runstate.State) { state.PendingPrepare.QuiesceDeadline = prepare.QuiesceDeadline })
		if err != nil || next != prepareCommitWaiting {
			t.Fatalf("waiting commit classification = %v, %v", next, err)
		}
	})
	t.Run("matching owner without legacy deadline fails closed", func(t *testing.T) {
		start, err := procid.Read(os.Getpid())
		if err != nil {
			t.Fatal(err)
		}
		lease := Lease{Epoch: 1, Token: "expired", PID: os.Getpid(), Start: start}
		store, _ := preparedCommitStore(t, &lease)
		next, _, err := classifyPreparedCommit(t, store, nil)
		if err == nil || next != prepareCommitDone {
			t.Fatalf("silence-limit matching owner classification = %v, %v", next, err)
		}
	})
	t.Run("base head changed abandons", func(t *testing.T) {
		store, _ := preparedCommitStore(t, nil)
		next, _, err := classifyPreparedCommit(t, store, func(state *runstate.State) { state.ScoreHead.Revision++ })
		if err != nil || next != prepareCommitDone {
			t.Fatalf("base-changed commit classification = %v, %v", next, err)
		}
		assertPreparedCommitTerminal(t, store, runstate.EventAmendmentApprovalAbandoned)
	})
	t.Run("invalidated plan abandons", func(t *testing.T) {
		store, _ := preparedCommitStore(t, nil)
		next, _, err := classifyPreparedCommit(t, store, func(state *runstate.State) { state.PendingPrepare.ProposalID = "other" })
		if err != nil || next != prepareCommitDone {
			t.Fatalf("invalid-plan commit classification = %v, %v", next, err)
		}
		assertPreparedCommitTerminal(t, store, runstate.EventAmendmentApprovalAbandoned)
	})
}

func assertPreparedCommitTerminal(t *testing.T, store *Store, want runstate.EventType) {
	t.Helper()
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) == 0 || journal.Events[len(journal.Events)-1].Type != want {
		t.Fatalf("prepare commit terminal = %+v, want %s", journal.Events, want)
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
		QuiesceDeadline: "2000-01-01T00:00:00.000Z", ClassifierVersion: plan.ClassifierVersion,
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
