package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const cancellationSweepGrace = 30 * time.Second

// RequestCancellation appends the run-scoped B.7 cancellation request while
// holding the state lock. The request is idempotent by run ID.
func (store *Store) RequestCancellation(runID runstate.RunID) error {
	input, err := store.LoadRunInput(runID)
	if err != nil {
		return err
	}
	seed := movementSeed(input.Score)
	return store.Mutate(runID, "", func(transaction *Txn) error {
		state, err := transaction.project(seed)
		if err != nil {
			return err
		}
		if state.Run == runstate.RunNotStarted {
			return ErrCancellationNotAllowed
		}
		if state.Run.Terminal() {
			// §7 maps an already-terminal cancel through RC-RESUME-002; it is
			// idempotent and must not append cancel.requested.
			return nil
		}
		event := runstate.Event{
			RunID:         runID,
			ScoreRevision: state.ScoreHead.Revision,
			Type:          runstate.EventCancelRequested,
			Payload:       cancellationPayload(map[string]any{"requested_by": "cli"}),
		}
		if !state.CancelRequested {
			if _, err := runstate.Apply(state, event); err != nil {
				return err
			}
		}
		_, err = transaction.At("cancellation.cancel.requested").Append(event)
		return err
	})
}

// ExecuteCancellation runs the §6 cancellation oracle as one ordered operation.
func (store *Store) ExecuteCancellation(ctx context.Context, runID runstate.RunID) error {
	input, err := store.LoadRunInput(runID)
	if err != nil {
		return err
	}
	state := input.Projection.State
	if state.Run == runstate.RunNotStarted || state.Run.Terminal() || !state.CancelRequested {
		return ErrLeaseConflict
	}
	if err := sweepCancellationSessions(ctx, state); err != nil {
		return err
	}
	store.probe.Reached(faultpoint.PointCancelSessionsSwept)
	return store.executeCancellation(runID)
}

func (store *Store) executeCancellation(runID runstate.RunID) error {
	input, err := store.LoadRunInput(runID)
	if err != nil {
		return err
	}
	seed := movementSeed(input.Score)
	return store.Mutate(runID, "", func(transaction *Txn) error {
		state, err := transaction.project(seed)
		if err != nil {
			return err
		}
		if state.Run == runstate.RunNotStarted || state.Run.Terminal() || !state.CancelRequested {
			return ErrLeaseConflict
		}
		if state.PendingPrepare != nil {
			if err := abandonCancellationPrepare(transaction, *state.PendingPrepare, store.probe); err != nil {
				return err
			}
			state, err = appendCancellationEvent(transaction, state, runstate.Event{
				RunID: runID, ScoreRevision: state.ScoreHead.Revision, Type: runstate.EventAmendmentApprovalAbandoned,
				Payload: cancellationPayload(map[string]any{
					"prepare_id": string(state.PendingPrepare.ID), "proposal_id": string(state.PendingPrepare.ProposalID),
					"reason": "cancelled", "base_revision": state.PendingPrepare.BaseHead.Revision,
					"base_hash": string(state.PendingPrepare.BaseHead.SemanticHash), "classifier_version": state.PendingPrepare.ClassifierVersion,
				}),
			}, "cancellation.amendment.approval_abandoned")
			if err != nil {
				return err
			}
		}
		if state.OpenExecution != nil {
			event, err := cancellationStopEvent(transaction, state, runID)
			if err != nil {
				return err
			}
			state, err = appendCancellationEvent(transaction, state, event, "cancellation.execution.stopped")
			if err != nil {
				return err
			}
			store.probe.Reached(faultpoint.PointCancelExecutionStopped)
		}

		lease, present, err := transaction.ReadLease()
		if err != nil {
			return err
		}
		var fencedEpoch *uint64
		if present && lease.Epoch == state.Authority.Epoch {
			epoch := state.Authority.Epoch + 1
			fencedEpoch = &epoch
		}
		store.probe.Reached(faultpoint.PointCancelFenceDecided)

		payload := cancellationPayload(runstate.CancellationPayload(state, fencedEpoch))
		state, err = appendCancellationEvent(transaction, state, runstate.Event{
			RunID: runID, ScoreRevision: state.ScoreHead.Revision, Type: runstate.EventRunCancelled, Payload: payload,
		}, "cancellation.run.cancelled")
		if err != nil {
			return err
		}
		store.probe.Reached(faultpoint.PointCancelRunCancelled)
		if fencedEpoch != nil {
			if !state.Run.Terminal() {
				return errors.New("cancellation lease cleanup requires terminal run")
			}
			if _, err := transaction.At("cancellation.driver.lease").CompareRemoveLease(lease.Identity()); err != nil {
				return err
			}
			store.probe.Reached(faultpoint.PointCancelLeaseRemoved)
		}
		return nil
	})
}

func sweepCancellationSessions(ctx context.Context, state runstate.State) error {
	attemptIDs := make([]runstate.AttemptID, 0, len(state.AdapterLaunches))
	for attemptID := range state.AdapterLaunches {
		attemptIDs = append(attemptIDs, attemptID)
	}
	slices.Sort(attemptIDs)
	for _, attemptID := range attemptIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := adapter.SweepSession(state.AdapterLaunches[attemptID].Process, cancellationSweepGrace); err != nil {
			return fmt.Errorf("%w: adapter %s: %v", runstate.ErrSweepUnverifiable, attemptID, err)
		}
	}

	criterionKeys := make([]runstate.CriterionLaunchKey, 0, len(state.CriterionLaunches))
	for key := range state.CriterionLaunches {
		criterionKeys = append(criterionKeys, key)
	}
	slices.SortFunc(criterionKeys, func(left, right runstate.CriterionLaunchKey) int {
		if left.AttemptID != right.AttemptID {
			return strings.Compare(string(left.AttemptID), string(right.AttemptID))
		}
		return strings.Compare(string(left.CriterionID), string(right.CriterionID))
	})
	for _, key := range criterionKeys {
		if err := ctx.Err(); err != nil {
			return err
		}
		launch, ok := state.CriterionLaunches[key].(runstate.SpawnedCriterionLaunch)
		if !ok {
			continue
		}
		if err := adapter.SweepSession(launch.Process, cancellationSweepGrace); err != nil {
			return fmt.Errorf("%w: criterion %s: %v", runstate.ErrSweepUnverifiable, key.CriterionID, err)
		}
	}
	return nil
}

func abandonCancellationPrepare(transaction *Txn, prepare runstate.PendingPrepare, probe faultpoint.Probe) error {
	snapshot := Path(fmt.Sprintf("scores/revision-%d.yaml", prepare.NewHead.Revision))
	if err := quarantineCancellationSnapshot(transaction, snapshot, prepare.NewHead.FileHash); err != nil {
		return err
	}
	probe.Reached(faultpoint.PointCancelSnapshotQuarantined)
	if _, err := transaction.At("cancellation.prepare.plan").RemoveDurable(Path(filepath.ToSlash(filepath.Join("prepares", string(prepare.ID)+".json")))); err != nil {
		return err
	}
	_, err := transaction.At("cancellation.prepare.sidecar").RemoveDurable(Path("driver.quiesced." + string(prepare.ID)))
	return err
}

func quarantineCancellationSnapshot(transaction *Txn, source Path, fileHash runstate.Hash) error {
	if _, err := transaction.QuarantineAs("cancelled_prepare").At("cancellation.prepare.snapshot").Quarantine(source); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	destination := Path(filepath.ToSlash(filepath.Join("quarantine", "cancelled_prepare", strings.TrimPrefix(string(fileHash), "sha256:"), filepath.Base(string(source)))))
	present, err := transaction.fileMatchesHash(destination, fileHash)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("cancellation snapshot %q is absent", source)
	}
	return nil
}

func (transaction *Txn) fileMatchesHash(path Path, expected runstate.Hash) (bool, error) {
	resolved, err := transaction.resolve(path)
	if err != nil {
		return false, err
	}
	contents, err := transaction.store.fs.ReadFile(resolved)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return Hash(digest(contents)) == Hash(expected), nil
}

func cancellationStopEvent(transaction *Txn, state runstate.State, runID runstate.RunID) (runstate.Event, error) {
	interval := state.OpenExecution
	if interval == nil {
		return runstate.Event{}, errors.New("cancellation interval is absent")
	}
	started, err := time.Parse(time.RFC3339Nano, interval.WallStart)
	if err != nil {
		return runstate.Event{}, fmt.Errorf("parse interval wall_start: %w", err)
	}
	observed := time.Now().UTC()
	duration := observed.Sub(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	if duration > interval.RemainingAtStart {
		duration = interval.RemainingAtStart
	}
	journal, err := transaction.loadJournal(filepath.Join(transaction.runRoot(), "journal.jsonl"))
	if err != nil {
		return runstate.Event{}, err
	}
	var causationID string
	for index := len(journal) - 1; index >= 0; index-- {
		event := journal[index].event
		if event.Type != runstate.EventExecutionStarted || payloadIntervalID(event.Payload) != string(interval.ID) {
			continue
		}
		causationID = event.EventID
		break
	}
	if causationID == "" {
		return runstate.Event{}, errors.New("cancellation execution start source is absent")
	}
	return runstate.Event{
		RunID: runID, ScoreRevision: state.ScoreHead.Revision, Type: runstate.EventExecutionStopped, CausationID: causationID,
		Payload: cancellationPayload(map[string]any{
			"interval_id": string(interval.ID), "reason": "cancelled", "charging": "clamped", "charged_duration": duration,
			"observed_at": observed.Format("2006-01-02T15:04:05.000Z"),
		}),
	}, nil
}

func appendCancellationEvent(transaction *Txn, state runstate.State, event runstate.Event, address faultpoint.ReceiptAddress) (runstate.State, error) {
	next, err := runstate.Apply(state, event)
	if err != nil {
		return state, err
	}
	if _, err := transaction.At(address).Append(event); err != nil {
		return state, err
	}
	return next, nil
}

func cancellationPayload(payload map[string]any) json.RawMessage {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return encoded
}

func payloadIntervalID(payload json.RawMessage) string {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	intervalID, _ := value["interval_id"].(string)
	return intervalID
}
