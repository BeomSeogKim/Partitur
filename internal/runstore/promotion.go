package runstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

var ErrPromotionNotAllowed = errors.New("score promotion is not allowed")

// ErrPromotionInterrupted means the surviving projection is PROMOTING, so the
// command can truthfully direct the caller to the --recover continuation.
var ErrPromotionInterrupted = errors.New("score promotion transaction was interrupted")

type PromotionOutcome string

const (
	PromotionOutcomePromoted         PromotionOutcome = "promoted"
	PromotionOutcomeAlreadyPromoted  PromotionOutcome = "already_promoted"
	PromotionOutcomeRecoveryRequired PromotionOutcome = "recovery_required"
)

type PromotionResult struct {
	Outcome PromotionOutcome
	Detail  string
}

// PromoteScore atomically replaces the editable root score with the exact
// pinned bytes of a succeeded run's latest revision. The state lock covers the
// CAS, journal, and rename so no command can separate the recorded judgment
// from the file it authorizes.
func (store *Store) PromoteScore(ctx context.Context, runID runstate.RunID, recoverOnly bool) (PromotionResult, error) {
	var result PromotionResult
	err := store.Mutate(runID, "", func(transaction *Txn) error {
		initial, err := store.LoadInitialScore(runID)
		if err != nil {
			return err
		}
		state, err := transaction.project(movementSeed(initial))
		if err != nil {
			return err
		}
		if recoverOnly {
			result, err = store.recoverPromotion(ctx, transaction, &state)
		} else {
			result, err = store.promoteScore(ctx, transaction, &state)
		}
		if err == nil || errors.Is(err, ErrPromotionNotAllowed) {
			return err
		}
		result, err = store.promotionFailureOutcome(transaction, movementSeed(initial), err)
		return err
	})
	return result, err
}

func (store *Store) promotionFailureOutcome(transaction *Txn, seed []runstate.MovementSeed, cause error) (PromotionResult, error) {
	state, projectErr := transaction.project(seed)
	if projectErr != nil {
		return PromotionResult{}, fmt.Errorf("%w: %v (projection unreadable: %v)", ErrPromotionInterrupted, cause, projectErr)
	}
	switch state.Promotion.State {
	case runstate.PromotionPromoted:
		return PromotionResult{Outcome: PromotionOutcomePromoted}, nil
	case runstate.PromotionRecoveryRequired:
		return PromotionResult{Outcome: PromotionOutcomeRecoveryRequired, Detail: cause.Error()}, nil
	case runstate.PromotionPromoting:
		return PromotionResult{}, fmt.Errorf("%w: %v", ErrPromotionInterrupted, cause)
	default:
		return PromotionResult{}, cause
	}
}

func (store *Store) promoteScore(ctx context.Context, transaction *Txn, state *runstate.State) (PromotionResult, error) {
	if state.Promotion.State == runstate.PromotionPromoted {
		return PromotionResult{Outcome: PromotionOutcomeAlreadyPromoted}, nil
	}
	if state.Promotion.State != runstate.PromotionNotPromoted {
		return PromotionResult{}, fmt.Errorf("%w: normal promotion is refused from %s", ErrPromotionNotAllowed, state.Promotion.State)
	}
	target, err := store.promotionTarget(transaction.runID, *state)
	if err != nil {
		return PromotionResult{}, err
	}
	if err := promotionPreconditions(*state); err != nil {
		return PromotionResult{}, err
	}
	root, err := store.fs.ReadFile(filepath.Join(store.root, "partitur.yaml"))
	if err != nil {
		return PromotionResult{}, err
	}
	if rawHash(root) != target.expectedHash {
		return PromotionResult{}, fmt.Errorf("%w: root file hash %q does not match expected %q", ErrPromotionNotAllowed, rawHash(root), target.expectedHash)
	}
	txnID, err := promotionTransactionID()
	if err != nil {
		return PromotionResult{}, err
	}
	if err := appendPromotionEvent(transaction, state, runstate.EventScorePromotionStarted, target.startedPayload(txnID)); err != nil {
		return PromotionResult{}, err
	}
	return store.writePromotedScore(transaction, state, target)
}

func (store *Store) recoverPromotion(ctx context.Context, transaction *Txn, state *runstate.State) (PromotionResult, error) {
	_ = ctx // kept symmetrical with the shipping executor surface.
	if state.Promotion.State != runstate.PromotionPromoting && state.Promotion.State != runstate.PromotionRecoveryRequired {
		return PromotionResult{}, fmt.Errorf("%w: --recover is refused from %s", ErrPromotionNotAllowed, state.Promotion.State)
	}
	target, err := store.promotionTarget(transaction.runID, *state)
	if err != nil {
		return PromotionResult{}, err
	}
	if state.Promotion.CandidateID != target.candidate.ID {
		return PromotionResult{}, fmt.Errorf("%w: promotion candidate is unavailable", ErrPromotionNotAllowed)
	}
	recorded, err := store.promotionTransaction(transaction.runID, state.Promotion.TransactionID)
	if err != nil {
		return PromotionResult{}, err
	}
	root, err := store.fs.ReadFile(filepath.Join(store.root, "partitur.yaml"))
	if err != nil {
		return PromotionResult{}, err
	}
	observed := rawHash(root)
	switch observed {
	case recorded.targetHash:
		if err := appendPromotionEvent(transaction, state, runstate.EventScorePromoted, target.promotedPayload(state.Promotion.TransactionID)); err != nil {
			return PromotionResult{}, err
		}
		return PromotionResult{Outcome: PromotionOutcomePromoted}, nil
	case recorded.expectedHash:
		// A PROMOTING recovery re-appends the original started event. Append's
		// idempotency check makes this a durable same-transaction resume rather
		// than a second promotion; RECOVERY_REQUIRED already has that durable
		// record and can proceed directly to the write.
		if state.Promotion.State == runstate.PromotionPromoting {
			if err := appendPromotionEvent(transaction, state, runstate.EventScorePromotionStarted, recorded.payload); err != nil {
				return PromotionResult{}, err
			}
		}
		return store.writePromotedScore(transaction, state, target)
	default:
		return store.haltPromotionForOperator(transaction, state, target, observed)
	}
}

func promotionPreconditions(state runstate.State) error {
	if state.Run != runstate.RunSucceeded || state.ApplicationCandidate == nil {
		return fmt.Errorf("%w: selected run is not succeeded with a candidate", ErrPromotionNotAllowed)
	}
	candidate := state.ApplicationCandidate
	if candidate.Revision != state.ScoreHead.Revision {
		return fmt.Errorf("%w: candidate is not bound to the latest revision", ErrPromotionNotAllowed)
	}
	if state.Application.State != runstate.ApplicationApplied || state.Application.CandidateID != candidate.ID {
		return fmt.Errorf("%w: candidate has not completed application", ErrPromotionNotAllowed)
	}
	return nil
}

type promotionTarget struct {
	candidate    runstate.ApplicationCandidate
	versions     map[string]any
	expectedHash string
	targetHash   string
	revision     uint64
	contents     []byte
}

func (target promotionTarget) startedPayload(txnID string) map[string]any {
	return map[string]any{"txn_id": txnID, "candidate_id": target.candidate.ID, "identity_versions": target.versions,
		"expected_root_file_hash": target.expectedHash, "target_snapshot_file_hash": target.targetHash, "target_revision": target.revision}
}

func (target promotionTarget) promotedPayload(txnID string) map[string]any {
	return map[string]any{"txn_id": txnID, "candidate_id": target.candidate.ID, "identity_versions": target.versions,
		"target_revision": target.revision, "target_snapshot_file_hash": target.targetHash}
}

func (store *Store) promotionTarget(runID runstate.RunID, state runstate.State) (promotionTarget, error) {
	if state.ApplicationCandidate == nil {
		return promotionTarget{}, fmt.Errorf("%w: candidate is unavailable", ErrPromotionNotAllowed)
	}
	versions, err := applicationIdentityVersions(*state.ApplicationCandidate)
	if err != nil {
		return promotionTarget{}, err
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		return promotionTarget{}, err
	}
	started, err := runStartedEventFrom(journal.Events)
	if err != nil {
		return promotionTarget{}, err
	}
	startPayload, err := eventPayload(started)
	if err != nil {
		return promotionTarget{}, err
	}
	snapshot := filepath.Join(store.root, ".partitur", "runs", string(runID), "scores", fmt.Sprintf("revision-%d.yaml", state.ScoreHead.Revision))
	contents, err := store.fs.ReadFile(snapshot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return promotionTarget{}, fmt.Errorf("%w: required promotion target snapshot %q is missing", ErrPromotionNotAllowed, snapshot)
		}
		return promotionTarget{}, fmt.Errorf("%w: required promotion target snapshot %q is unreadable: %v", ErrPromotionNotAllowed, snapshot, err)
	}
	if hash := rawHash(contents); hash != string(state.ScoreHead.FileHash) {
		return promotionTarget{}, fmt.Errorf("promotion target hash %q does not match pinned head %q", hash, state.ScoreHead.FileHash)
	}
	return promotionTarget{candidate: *state.ApplicationCandidate, versions: versions,
		expectedHash: stringValue(startPayload, "score_file_hash"), targetHash: string(state.ScoreHead.FileHash),
		revision: state.ScoreHead.Revision, contents: contents}, nil
}

type recordedPromotion struct {
	expectedHash string
	targetHash   string
	payload      map[string]any
}

func (store *Store) promotionTransaction(runID runstate.RunID, txnID string) (recordedPromotion, error) {
	journal, err := store.ReadJournal(runID)
	if err != nil {
		return recordedPromotion{}, err
	}
	for _, event := range journal.Events {
		if event.Type != runstate.EventScorePromotionStarted {
			continue
		}
		payload, err := eventPayload(event)
		if err != nil {
			return recordedPromotion{}, err
		}
		if stringValue(payload, "txn_id") == txnID {
			return recordedPromotion{expectedHash: stringValue(payload, "expected_root_file_hash"), targetHash: stringValue(payload, "target_snapshot_file_hash"), payload: payload}, nil
		}
	}
	return recordedPromotion{}, fmt.Errorf("%w: original promotion transaction is missing", ErrPromotionNotAllowed)
}

func (store *Store) writePromotedScore(transaction *Txn, state *runstate.State, target promotionTarget) (PromotionResult, error) {
	root := filepath.Join(store.root, "partitur.yaml")
	temporary, err := store.fs.WriteTemp(store.root, ".partitur.yaml.promote-*", target.contents, 0o600)
	if err != nil {
		return PromotionResult{}, fmt.Errorf("write promotion temporary: %w", err)
	}
	cleanup := func() { _ = store.fs.Remove(temporary) }
	if err := store.fs.SyncFile(temporary); err != nil {
		cleanup()
		return PromotionResult{}, fmt.Errorf("sync promotion temporary: %w", err)
	}
	if contents, err := store.fs.ReadFile(temporary); err != nil || !bytes.Equal(contents, target.contents) {
		cleanup()
		if err != nil {
			return PromotionResult{}, fmt.Errorf("verify promotion temporary: %w", err)
		}
		return PromotionResult{}, errors.New("promotion temporary bytes differ from target snapshot")
	}
	store.probe.Reached(faultpoint.PointPromotionBeforeRootRename)
	current, err := store.fs.ReadFile(root)
	if err != nil {
		cleanup()
		return PromotionResult{}, fmt.Errorf("re-read root score before promotion rename: %w", err)
	}
	observed := rawHash(current)
	if observed != target.expectedHash {
		cleanup()
		if observed == target.targetHash {
			if err := appendPromotionEvent(transaction, state, runstate.EventScorePromoted, target.promotedPayload(state.Promotion.TransactionID)); err != nil {
				return PromotionResult{}, err
			}
			return PromotionResult{Outcome: PromotionOutcomePromoted}, nil
		}
		return store.haltPromotionForOperator(transaction, state, target, observed)
	}
	if err := store.fs.Rename(temporary, root); err != nil {
		cleanup()
		return PromotionResult{}, fmt.Errorf("rename promoted root score: %w", err)
	}
	if err := store.fs.SyncDir(store.root); err != nil {
		return PromotionResult{}, fmt.Errorf("sync promoted root score directory: %w", err)
	}
	store.probe.Reached(faultpoint.PointPromotionRootRenamed)
	if err := appendPromotionEvent(transaction, state, runstate.EventScorePromoted, target.promotedPayload(state.Promotion.TransactionID)); err != nil {
		return PromotionResult{}, err
	}
	return PromotionResult{Outcome: PromotionOutcomePromoted}, nil
}

func (store *Store) haltPromotionForOperator(transaction *Txn, state *runstate.State, target promotionTarget, observed string) (PromotionResult, error) {
	if state.Promotion.State == runstate.PromotionPromoting {
		if err := appendPromotionEvent(transaction, state, runstate.EventScorePromotionRecoveryRequired, map[string]any{
			"txn_id": state.Promotion.TransactionID, "candidate_id": target.candidate.ID,
			"identity_versions": target.versions, "observed_root_file_hash": observed,
			"failure_detail": "root file hash matches neither expected nor target",
		}); err != nil {
			return PromotionResult{}, err
		}
	}
	return PromotionResult{Outcome: PromotionOutcomeRecoveryRequired, Detail: "root file hash matches neither expected nor target"}, nil
}

func appendPromotionEvent(transaction *Txn, state *runstate.State, eventType runstate.EventType, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	event := runstate.Event{RunID: transaction.runID, ScoreRevision: state.ScoreHead.Revision, Type: eventType, Payload: encoded}
	next, err := runstate.Apply(*state, event)
	if err != nil {
		return err
	}
	if _, err := transaction.At(faultpoint.ReceiptAddress(eventType)).Append(event); err != nil {
		return err
	}
	*state = next
	return nil
}

func promotionTransactionID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "promotion-" + hex.EncodeToString(bytes[:]), nil
}
