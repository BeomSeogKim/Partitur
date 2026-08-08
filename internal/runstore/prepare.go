package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

const (
	preparePlanAddress    = faultpoint.ReceiptAddress("prepare.commit.plan")
	prepareSidecarAddress = faultpoint.ReceiptAddress("prepare.commit.sidecar")
	quiesceReceiptAddress = faultpoint.ReceiptAddress("prepare.quiesce_observed")
	// The cadence leaves scheduler and append time below §6's 10000 ms maximum.
	quiesceReceiptCadence = 9 * time.Second
)

var prepareCommitNow = time.Now

// AcknowledgePrepare performs the driver's §6 step-2 quiesce acknowledgement.
// Process control happens before the short compare-move transaction; callers
// retain their driver authority until the move succeeds.
func (store *Store) AcknowledgePrepare(ctx context.Context, driver *Driver, prepareID runstate.PrepareID) error {
	if store == nil || driver == nil || driver.store == nil || prepareID == "" {
		return errors.New("prepare acknowledgement requires its owning driver")
	}
	store = driver.store
	state, err := driver.State()
	if err != nil {
		return err
	}
	if state.PendingPrepare == nil || state.PendingPrepare.ID != prepareID || state.CancelRequested {
		return ErrLeaseConflict
	}
	round := state.PendingPrepare.LatestQuiesceRound
	if err := store.appendQuiesceReceipt(driver, prepareID, round+1); err != nil {
		return err
	}
	sweepContext, cancelSweep := context.WithCancel(ctx)
	defer cancelSweep()
	swept := make(chan error, 1)
	go func() { swept <- store.sweep(sweepContext, state) }()
	ticker := time.NewTicker(store.quiesceCadence())
	defer ticker.Stop()
	for {
		select {
		case err := <-swept:
			if err != nil {
				return err
			}
			if err := store.appendQuiesceReceipt(driver, prepareID, round+2); err != nil {
				return err
			}
			goto sessionsSwept
		case <-ticker.C:
			round++
			if err := store.appendQuiesceReceipt(driver, prepareID, round+1); err != nil {
				cancelSweep()
				<-swept
				return err
			}
		}
	}

sessionsSwept:
	store.probe.Reached(faultpoint.PointQuiesceSessionsSwept)
	if state.OpenExecution != nil {
		err := driver.Mutate(func(transaction *Txn, current runstate.State) error {
			if current.PendingPrepare == nil || current.PendingPrepare.ID != prepareID || current.CancelRequested {
				return ErrLeaseConflict
			}
			event, err := controlStopEvent(transaction, current, driver.runID, "superseded")
			if err != nil {
				return err
			}
			if _, err := runstate.Apply(current, event); err != nil {
				return err
			}
			_, err = transaction.At("prepare.ack.execution.stopped").Append(event)
			return err
		})
		if err != nil {
			return err
		}
	}
	return store.Mutate(driver.runID, "", func(transaction *Txn) error {
		current, err := transaction.project(driver.seed)
		if err != nil {
			return err
		}
		if current.PendingPrepare == nil || current.PendingPrepare.ID != prepareID || current.CancelRequested ||
			current.Authority.Epoch != driver.lease.Epoch {
			return ErrLeaseConflict
		}
		if _, err := transaction.At("prepare.ack.lease").CompareMoveLease(driver.lease.Identity(), Path("driver.quiesced."+string(prepareID))); err != nil {
			return err
		}
		store.probe.Reached(faultpoint.PointQuiesceLeaseMoved)
		return nil
	})
}

func (store *Store) quiesceCadence() time.Duration {
	if store.quiesceReceiptCadence <= 0 || store.quiesceReceiptCadence > quiesceReceiptCadence {
		return quiesceReceiptCadence
	}
	return store.quiesceReceiptCadence
}

func (store *Store) sweep(ctx context.Context, state runstate.State) error {
	if store.sweepSessions != nil {
		return store.sweepSessions(ctx, state)
	}
	return sweepRecordedSessions(ctx, state)
}

func (store *Store) appendQuiesceReceipt(driver *Driver, prepareID runstate.PrepareID, round uint64) error {
	return store.Mutate(driver.runID, "", func(transaction *Txn) error {
		state, err := transaction.project(driver.seed)
		if err != nil {
			return err
		}
		if state.PendingPrepare == nil || state.PendingPrepare.ID != prepareID || state.CancelRequested {
			return ErrLeaseConflict
		}
		if _, present, err := transaction.readLeaseAt(Path("driver.quiesced." + string(prepareID))); err != nil {
			return err
		} else if present {
			return fmt.Errorf("%w: amendment.quiesce_observed: prepare_pending", runstate.ErrIllegalTransition)
		}
		if state.Authority.Epoch != driver.lease.Epoch || state.Authority.Owner == nil ||
			state.Authority.Owner.PID != driver.lease.PID || !startIdentitiesEqual(state.Authority.Owner.Start, driver.lease.Start) {
			return ErrLeaseConflict
		}
		lease, present, err := transaction.ReadLease()
		if err != nil {
			return err
		}
		if !present || !leaseMatches(lease, driver.lease.Identity()) {
			return ErrLeaseConflict
		}
		match := lease.MatchOwner()
		if match.Status == procid.Unverifiable {
			return fmt.Errorf("%w: %v", ErrLeaseOwnerUnverifiable, match.Err)
		}
		if match.Status != procid.MatchingAndLive {
			return ErrLeaseConflict
		}
		if state.PendingPrepare.LatestQuiesceRound+1 != round {
			return fmt.Errorf("%w: amendment.quiesce_observed sweep round", runstate.ErrIllegalTransition)
		}
		payload, err := json.Marshal(map[string]any{"prepare_id": string(prepareID), "sweep_round": round})
		if err != nil {
			return err
		}
		event := runstate.Event{RunID: driver.runID, ScoreRevision: state.ScoreHead.Revision, Type: runstate.EventAmendmentQuiesceObserved, Payload: payload}
		if _, err := runstate.Apply(state, event); err != nil {
			return err
		}
		_, err = transaction.At(quiesceReceiptAddress).Append(event)
		return err
	})
}

// CompleteOrAbandonPrepare owns the shared §6 step-3 table. It deliberately
// works under Store.Mutate rather than recovery driver authority: acquiring a
// new authority would invalidate the pending prepare's observed epoch.
func (store *Store) CompleteOrAbandonPrepare(ctx context.Context, runID runstate.RunID) error {
	input, err := store.LoadRunInput(runID)
	if err != nil {
		return err
	}
	seed := movementSeed(input.Score)
	var next prepareCommitNext
	var sweepState runstate.State
	var lease Lease
	err = store.Mutate(runID, "", func(transaction *Txn) error {
		state, err := transaction.project(seed)
		if err != nil {
			return err
		}
		next, lease, err = transaction.classifyPrepareCommit(state)
		if err != nil || next != prepareCommitFence {
			return err
		}
		sweepState = state
		return nil
	})
	if err != nil {
		return err
	}
	switch next {
	case prepareCommitCancelled:
		return store.ExecuteCancellation(ctx, runID)
	case prepareCommitWaiting:
		return ErrPrepareWaiting
	case prepareCommitDone:
		return nil
	case prepareCommitFence:
		if err := store.sweep(ctx, sweepState); err != nil {
			return err
		}
		store.probe.Reached(faultpoint.PointSupersedeSessionsSwept)
		match := lease.MatchOwner()
		if match.Status == procid.MatchingAndLive {
			if err := store.TerminateLeaseOwner(ctx, runID, lease.Identity(), cancellationSweepGrace); err != nil {
				return err
			}
		} else if match.Status == procid.Unverifiable {
			return fmt.Errorf("%w: %v", ErrLeaseOwnerUnverifiable, match.Err)
		}
		return store.commitFencedPrepare(runID, seed, lease)
	default:
		return errors.New("unknown prepare commit result")
	}
}

type prepareCommitNext uint8

const (
	prepareCommitDone prepareCommitNext = iota
	prepareCommitCancelled
	prepareCommitWaiting
	prepareCommitFence
)

func (transaction *Txn) classifyPrepareCommit(state runstate.State) (prepareCommitNext, Lease, error) {
	prepare := state.PendingPrepare
	if prepare == nil {
		return prepareCommitDone, Lease{}, ErrLeaseConflict
	}
	if state.CancelRequested {
		return prepareCommitCancelled, Lease{}, nil
	}
	plan, invalid, err := transaction.preparePlan(*prepare)
	if err != nil {
		return prepareCommitDone, Lease{}, err
	}
	if err := transaction.validatePrepareSnapshot(*prepare); err != nil {
		return prepareCommitDone, Lease{}, err
	}
	if state.ScoreHead != prepare.BaseHead {
		return prepareCommitDone, Lease{}, transaction.abandonPrepare(state, *prepare, "base_head_changed")
	}
	if invalid {
		return prepareCommitDone, Lease{}, transaction.abandonPrepare(state, *prepare, "plan_invalidated")
	}
	sidecar, sidecarPresent, err := transaction.readLeaseAt(Path("driver.quiesced." + string(prepare.ID)))
	if err != nil {
		return prepareCommitDone, Lease{}, err
	}
	if sidecarPresent {
		if !leaseMatchesPrepare(sidecar, state, *prepare) {
			return prepareCommitDone, Lease{}, ErrPrepareLeaseEpochMismatch
		}
		transaction.store.probe.Reached(faultpoint.PointQuiesceCommitLockHeld)
		return prepareCommitDone, Lease{}, transaction.approvePrepare(state, plan, nil)
	}
	lease, present, err := transaction.ReadLease()
	if err != nil {
		return prepareCommitDone, Lease{}, err
	}
	if !present {
		return prepareCommitDone, Lease{}, transaction.approvePrepare(state, plan, nil)
	}
	if !leaseMatchesPrepare(lease, state, *prepare) {
		return prepareCommitDone, Lease{}, ErrPrepareLeaseEpochMismatch
	}
	silenceExpired, err := prepareSilenceExpired(*prepare)
	if err != nil {
		return prepareCommitDone, Lease{}, err
	}
	match := lease.MatchOwner()
	if match.Status == procid.Unverifiable {
		if !silenceExpired {
			return prepareCommitWaiting, lease, nil
		}
		return prepareCommitDone, Lease{}, fmt.Errorf("%w: %v", ErrLeaseOwnerUnverifiable, match.Err)
	}
	if match.Status == procid.MatchingAndLive && !silenceExpired {
		return prepareCommitWaiting, lease, nil
	}
	return prepareCommitFence, lease, nil
}

func prepareSilenceExpired(prepare runstate.PendingPrepare) (bool, error) {
	baseline := prepare.PreparedAt
	if prepare.LatestQuiesceObservedAt != "" {
		baseline = prepare.LatestQuiesceObservedAt
	}
	observedAt, err := time.Parse(time.RFC3339Nano, baseline)
	if err != nil {
		return false, fmt.Errorf("invalid prepare quiesce receipt timestamp: %w", err)
	}
	limit := time.Duration(prepare.QuiesceSilenceLimitMS) * time.Millisecond
	return !prepareCommitNow().Before(observedAt.Add(limit)), nil
}

func (store *Store) commitFencedPrepare(runID runstate.RunID, seed []runstate.MovementSeed, expected Lease) error {
	return store.Mutate(runID, "", func(transaction *Txn) error {
		state, err := transaction.project(seed)
		if err != nil {
			return err
		}
		prepare := state.PendingPrepare
		if prepare == nil || state.CancelRequested {
			return ErrLeaseConflict
		}
		plan, invalid, err := transaction.preparePlan(*prepare)
		if err != nil {
			return err
		}
		if err := transaction.validatePrepareSnapshot(*prepare); err != nil {
			return err
		}
		if state.ScoreHead != prepare.BaseHead {
			return transaction.abandonPrepare(state, *prepare, "base_head_changed")
		}
		if invalid {
			return transaction.abandonPrepare(state, *prepare, "plan_invalidated")
		}
		if sidecar, present, err := transaction.readLeaseAt(Path("driver.quiesced." + string(prepare.ID))); err != nil {
			return err
		} else if present {
			if !leaseMatchesPrepare(sidecar, state, *prepare) {
				return ErrPrepareLeaseEpochMismatch
			}
			return transaction.approvePrepare(state, plan, nil)
		}
		lease, present, err := transaction.ReadLease()
		if err != nil {
			return err
		}
		if !present || !leaseMatches(lease, expected.Identity()) || !leaseMatchesPrepare(lease, state, *prepare) {
			return ErrLeaseConflict
		}
		if state.OpenExecution != nil {
			event, err := controlStopEvent(transaction, state, runID, "superseded")
			if err != nil {
				return err
			}
			state, err = appendPrepareEvent(transaction, state, event, "prepare.commit.execution.stopped")
			if err != nil {
				return err
			}
		}
		epoch := state.Authority.Epoch + 1
		store.probe.Reached(faultpoint.PointSupersedeFenceDecided)
		if err := transaction.approvePrepare(state, plan, &epoch); err != nil {
			return err
		}
		_, err = transaction.At("prepare.commit.lease").CompareRemoveLease(lease.Identity())
		return err
	})
}

func (transaction *Txn) preparePlan(prepare runstate.PendingPrepare) (runstate.ApprovalPlan, bool, error) {
	path := Path(filepath.ToSlash(filepath.Join("prepares", string(prepare.ID)+".json")))
	resolved, err := transaction.resolve(path)
	if err != nil {
		return runstate.ApprovalPlan{}, false, err
	}
	contents, err := transaction.store.fs.ReadFile(resolved)
	if errors.Is(err, fs.ErrNotExist) {
		return runstate.ApprovalPlan{}, false, ErrMissingPreparePlan
	}
	if err != nil {
		return runstate.ApprovalPlan{}, false, fmt.Errorf("read prepare plan: %w", err)
	}
	if Hash(digest(contents)) != prepare.PlanRecordHash {
		return runstate.ApprovalPlan{}, false, ErrMissingPreparePlan
	}
	plan, err := runstate.DecodeApprovalPlan(contents)
	if err != nil || !plan.MatchesPrepare(prepare) {
		return runstate.ApprovalPlan{}, true, nil
	}
	return plan, false, nil
}

func (transaction *Txn) validatePrepareSnapshot(prepare runstate.PendingPrepare) error {
	path := Path(fmt.Sprintf("scores/revision-%d.yaml", prepare.NewHead.Revision))
	resolved, err := transaction.resolve(path)
	if err != nil {
		return err
	}
	contents, err := transaction.store.fs.ReadFile(resolved)
	if err != nil {
		return fmt.Errorf("%w: read prepared snapshot: %v", ErrMissingPinnedSnapshot, err)
	}
	if Hash(rawHash(contents)) != prepare.NewHead.FileHash {
		return ErrMissingPinnedSnapshot
	}
	compiled, diagnostics := score.Compile(contents)
	if len(diagnostics) != 0 || compiled.Revision() != prepare.NewHead.Revision {
		return ErrMissingPinnedSnapshot
	}
	hash, err := compiled.Hash()
	if err != nil || Hash(hash) != prepare.NewHead.SemanticHash {
		return ErrMissingPinnedSnapshot
	}
	return nil
}

func (transaction *Txn) approvePrepare(state runstate.State, plan runstate.ApprovalPlan, fencedEpoch *uint64) error {
	payload, err := plan.ApprovedPayload(fencedEpoch)
	if err != nil {
		return err
	}
	event := runstate.Event{RunID: transaction.runID, ScoreRevision: plan.NewRevision, MovementID: plan.MovementID, Type: runstate.EventAmendmentApproved, Payload: payload}
	if _, err := appendPrepareEvent(transaction, state, event, "prepare.commit.approved"); err != nil {
		return err
	}
	if _, err := transaction.At(preparePlanAddress).RemoveDurable(Path(filepath.ToSlash(filepath.Join("prepares", string(state.PendingPrepare.ID)+".json")))); err != nil {
		return err
	}
	_, err = transaction.At(prepareSidecarAddress).RemoveDurable(Path("driver.quiesced." + string(state.PendingPrepare.ID)))
	return err
}

func (transaction *Txn) abandonPrepare(state runstate.State, prepare runstate.PendingPrepare, reason string) error {
	snapshot := Path(fmt.Sprintf("scores/revision-%d.yaml", prepare.NewHead.Revision))
	if _, err := transaction.QuarantineAs("abandoned_prepare").At("prepare.commit.snapshot").Quarantine(snapshot); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := transaction.At(preparePlanAddress).RemoveDurable(Path(filepath.ToSlash(filepath.Join("prepares", string(prepare.ID)+".json")))); err != nil {
		return err
	}
	if _, err := transaction.At(prepareSidecarAddress).RemoveDurable(Path("driver.quiesced." + string(prepare.ID))); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"prepare_id": string(prepare.ID), "proposal_id": string(prepare.ProposalID), "reason": reason,
		"base_revision": prepare.BaseHead.Revision, "base_hash": string(prepare.BaseHead.SemanticHash), "classifier_version": prepare.ClassifierVersion,
	})
	if err != nil {
		return err
	}
	_, err = appendPrepareEvent(transaction, state, runstate.Event{RunID: transaction.runID, ScoreRevision: state.ScoreHead.Revision, Type: runstate.EventAmendmentApprovalAbandoned, Payload: payload}, "prepare.commit.abandoned")
	return err
}

func (transaction *Txn) readLeaseAt(path Path) (Lease, bool, error) {
	resolved, err := transaction.resolve(path)
	if err != nil {
		return Lease{}, false, err
	}
	contents, err := transaction.store.fs.ReadFile(resolved)
	if errors.Is(err, fs.ErrNotExist) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, err
	}
	lease, err := decodeLease(contents)
	return lease, err == nil, err
}

func leaseMatchesPrepare(lease Lease, state runstate.State, prepare runstate.PendingPrepare) bool {
	return lease.Epoch == prepare.ObservedAuthorityEpoch && state.Authority.Epoch == prepare.ObservedAuthorityEpoch &&
		state.Authority.Owner != nil && lease.PID == state.Authority.Owner.PID && startIdentitiesEqual(lease.Start, state.Authority.Owner.Start)
}

func appendPrepareEvent(transaction *Txn, state runstate.State, event runstate.Event, address faultpoint.ReceiptAddress) (runstate.State, error) {
	next, err := runstate.Apply(state, event)
	if err != nil {
		return state, err
	}
	if _, err := transaction.At(address).Append(event); err != nil {
		return state, err
	}
	return next, nil
}
