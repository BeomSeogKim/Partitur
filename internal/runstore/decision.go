package runstore

import (
	"encoding/json"
	"path/filepath"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// ResolveQuestion appends one answer for a pending question while holding the
// repository state lock. It deliberately does not require driver authority:
// an answer changes the lifecycle but never launches work.
func (store *Store) ResolveQuestion(runID runstate.RunID, decisionID, answer string) error {
	return store.Mutate(runID, "", func(transaction *Txn) error {
		entries, err := transaction.loadJournal(filepath.Join(transaction.runRoot(), "journal.jsonl"))
		if err != nil {
			return err
		}
		events := make([]runstate.Event, len(entries))
		for index, entry := range entries {
			events[index] = entry.event
		}
		started, err := runStartedEventFrom(events)
		if err != nil {
			return err
		}
		startPayload, err := eventPayload(started)
		if err != nil {
			return err
		}
		initialScore, err := store.loadPinnedScore(runID, started.ScoreRevision, startPayload)
		if err != nil {
			return err
		}
		state, err := transaction.project(movementSeed(initialScore))
		if err != nil {
			return err
		}
		decision, ok := state.PendingDecisions[decisionID]
		if state.Run == runstate.RunNotStarted || state.Run.Terminal() || !ok || decision.Type != "question" {
			return ErrDecisionResolutionNotAllowed
		}
		payload, err := json.Marshal(map[string]any{
			"decision_id": decisionID, "decision_type": "question", "disposition": "answered", "answer": answer,
		})
		if err != nil {
			return err
		}
		event := runstate.Event{
			RunID: runID, ScoreRevision: decision.ScoreRevision, MovementID: decision.MovementID,
			AttemptID: decision.AttemptID, Type: runstate.EventDecisionResolved, Payload: payload,
		}
		if _, err := runstate.Apply(state, event); err != nil {
			return err
		}
		_, err = transaction.At("decision.question.resolved").Append(event)
		return err
	})
}
