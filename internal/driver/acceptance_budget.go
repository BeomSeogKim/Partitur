package driver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/BeomSeogKim/Partitur/internal/cancellation"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/successor"
)

// TerminalizeAcceptanceBudget closes an active acceptance interval, records
// the core-owned exhausted attempt, then realizes its already-recorded terminal
// disposition. Both live execution and criterion recovery use this sequence.
func TerminalizeAcceptanceBudget(ctx context.Context, terminalization AcceptanceBudgetTerminalization) Result {
	result := Result{RunID: terminalization.RunID}
	if terminalization.RepositoryRoot == "" || terminalization.RunID == "" || terminalization.AttemptID == "" ||
		terminalization.Authority == nil || terminalization.Probe == nil || terminalization.Close == nil {
		return interrupted(result, errors.New("driver: incomplete acceptance budget terminalization"))
	}
	store, err := runstore.New(terminalization.RepositoryRoot, terminalization.Probe)
	if err != nil {
		return stopped(result, err)
	}
	control := terminalization.Control
	if control == nil {
		control, err = cancellation.Watch(store, terminalization.RunID)
		if err != nil {
			return stopped(result, err)
		}
		defer control.Stop()
	}
	if err := terminalization.Close(); err != nil {
		return stopped(result, err)
	}
	disposition, err := classifyAttemptFailure(
		store, terminalization.RunID, terminalization.AttemptID,
		successor.FailureCase{AttemptKind: successor.KindBudgetExhausted}, -1,
	)
	if err != nil {
		return stopped(result, err)
	}
	state, err := terminalization.Authority.State()
	if err != nil {
		return stopped(result, err)
	}
	payload, err := json.Marshal(map[string]any{
		"kind":        successor.KindBudgetExhausted,
		"disposition": dispositionPayload(disposition),
	})
	if err != nil {
		return interrupted(result, err)
	}
	if _, err := terminalization.Authority.Append(runstate.Event{
		RunID: terminalization.RunID, ScoreRevision: state.ScoreHead.Revision,
		AttemptID: terminalization.AttemptID, Type: runstate.EventAttemptFailed,
		Payload: payload,
	}, "attempt.failed.budget_exhausted"); err != nil {
		return stopped(result, err)
	}
	terminal, handled := realizeRecordedNoneDisposition(ctx, result, store, terminalization.Authority, control, dependencies{probe: terminalization.Probe})
	if !handled {
		return interrupted(result, errors.New("driver: budget exhaustion has no terminal realization"))
	}
	return terminal
}

func classifyAttemptFailure(
	store *runstore.Store,
	runID runstate.RunID,
	attemptID runstate.AttemptID,
	failure successor.FailureCase,
	remainingOverride int64,
) (runstate.Disposition, error) {
	input, err := store.LoadRunInput(runID)
	if err != nil {
		return runstate.Disposition{}, err
	}
	current := input.Projection.CurrentHeadAttempt
	if current == nil || current.AttemptID != attemptID {
		return runstate.Disposition{}, errors.New("driver: classification attempt is not current")
	}
	facts := current.FailureClassification
	visited := make(map[string]bool, len(facts.VisitedPerformers)+1)
	for _, performerID := range facts.VisitedPerformers {
		visited[performerID] = true
	}
	visited[facts.CurrentPerformer] = true
	hasUnvisitedFallback := false
	for _, fallback := range facts.Fallbacks {
		if !visited[fallback] {
			hasUnvisitedFallback = true
			break
		}
	}
	remaining := facts.RemainingTimeMS
	if remainingOverride >= 0 {
		remaining = remainingOverride
	}
	return successor.Classify(successor.ClassificationInput{
		Failure:              failure,
		HasUnvisitedFallback: hasUnvisitedFallback,
		RetriesConsumed:      facts.RetriesConsumed,
		RetriesPerMovement:   facts.RetriesPerMovement,
		RemainingTimeMS:      remaining,
	})
}
