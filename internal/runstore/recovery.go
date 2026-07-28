package runstore

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

// RecoveryInput is the complete durable input available before recovery
// observes leases, processes, worktrees, or references.
type RecoveryInput struct {
	Projection recovery.Projection
	Score      *score.Score
	Cast       *cast.Cast
}

// LoadRecoveryInput reads only the selected run's journal and authoritative
// run-owned inputs. It never reads or recompiles the repository root score or
// current cast layers.
func (store *Store) LoadRecoveryInput(runID runstate.RunID) (RecoveryInput, error) {
	journal, err := store.ReadJournal(runID)
	if err != nil {
		return RecoveryInput{}, err
	}
	started, err := runStartedEventFrom(journal.Events)
	if err != nil {
		return RecoveryInput{}, err
	}
	startPayload, err := eventPayload(started)
	if err != nil {
		return RecoveryInput{}, err
	}

	initialScore, err := store.loadPinnedScore(runID, started.ScoreRevision, startPayload)
	if err != nil {
		return RecoveryInput{}, err
	}
	seed := movementSeed(initialScore)
	replay, err := store.ReadReplay(runID, seed)
	if err != nil {
		return RecoveryInput{}, err
	}
	currentScore, err := store.loadPinnedScore(runID, replay.State.ScoreHead.Revision, map[string]any{
		"score_hash":      string(replay.State.ScoreHead.SemanticHash),
		"score_file_hash": string(replay.State.ScoreHead.FileHash),
	})
	if err != nil {
		return RecoveryInput{}, err
	}
	resolvedCast, err := store.loadResolvedCast(runID, startPayload)
	if err != nil {
		return RecoveryInput{}, err
	}

	return RecoveryInput{
		Projection: recoveryProjection(replay.State, journal.Events, currentScore),
		Score:      currentScore,
		Cast:       resolvedCast,
	}, nil
}

func (store *Store) loadPinnedScore(runID runstate.RunID, revision uint64, payload map[string]any) (*score.Score, error) {
	path := filepath.Join(store.root, ".partitur", "runs", string(runID), "scores", fmt.Sprintf("revision-%d.yaml", revision))
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pinned score revision %d: %w", revision, err)
	}
	if rawHash(contents) != stringValue(payload, "score_file_hash") {
		return nil, errors.New("pinned score file hash does not match journal")
	}
	compiled, diagnostics := score.Compile(contents)
	if len(diagnostics) != 0 {
		return nil, fmt.Errorf("compile pinned score revision %d: %v", revision, diagnostics)
	}
	semanticHash, err := compiled.Hash()
	if err != nil {
		return nil, err
	}
	if semanticHash != stringValue(payload, "score_hash") {
		return nil, errors.New("pinned score semantic hash does not match journal")
	}
	return compiled, nil
}

func (store *Store) loadResolvedCast(runID runstate.RunID, payload map[string]any) (*cast.Cast, error) {
	path := filepath.Join(store.root, ".partitur", "runs", string(runID), "resolved-cast.yaml")
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read resolved cast: %w", err)
	}
	resolved, diagnostics := cast.Resolve([]cast.Layer{{Origin: "run-owned resolved cast", Data: contents}})
	if len(diagnostics) != 0 {
		return nil, fmt.Errorf("parse resolved cast: %v", diagnostics)
	}
	hash, err := resolved.Hash()
	if err != nil {
		return nil, err
	}
	if hash != stringValue(payload, "resolved_cast_hash") {
		return nil, errors.New("resolved cast hash does not match journal")
	}
	return resolved, nil
}

func recoveryProjection(state runstate.State, events []runstate.Event, pinned *score.Score) recovery.Projection {
	facts := replayFacts(events)
	projection := recovery.Projection{
		State:     state,
		Scheduler: schedulerFromScore(state, pinned),
	}
	current := facts.currentHeadAttempt(state)
	if current == nil {
		return projection
	}
	projection.CurrentHeadAttempt = current
	if acceptance, ok := state.Acceptances[current.AttemptID]; ok && acceptance.Started {
		projection.Acceptance = facts.acceptance(current.AttemptID, current.MovementID, pinned)
	}
	return projection
}

func schedulerFromScore(state runstate.State, pinned *score.Score) recovery.Scheduler {
	execution := pinned.Execution()
	policy := pinned.EffectivePolicy()
	scheduler := recovery.Scheduler{
		GateWaived:    execution.GateWaived,
		RemainingTime: policy.ActiveWallClockMin*60*1000 - state.ConsumedBudgetMS,
	}
	if scheduler.RemainingTime < 0 {
		scheduler.RemainingTime = 0
	}
	for _, movement := range pinned.Movements() {
		needs := make([]runstate.MovementID, len(movement.Needs))
		for index, need := range movement.Needs {
			needs[index] = runstate.MovementID(need)
		}
		scheduler.Movements = append(scheduler.Movements, recovery.ScheduledMovement{
			ID:        runstate.MovementID(movement.ID),
			Needs:     needs,
			RepoWrite: hasGrant(movement.Grants, "repo_write"),
			Final:     movement.ID == execution.FinalMovementID,
		})
	}
	return scheduler
}

func movementSeed(pinned *score.Score) []runstate.MovementSeed {
	movements := pinned.Movements()
	seed := make([]runstate.MovementSeed, 0, len(movements))
	for _, movement := range movements {
		initial := runstate.MovementPending
		if movement.Phase == "draft" {
			initial = runstate.MovementInapplicable
		}
		seed = append(seed, runstate.MovementSeed{
			ID: runstate.MovementID(movement.ID), Initial: initial,
			RepoWrite: hasGrant(movement.Grants, "repo_write"),
		})
	}
	return seed
}

type replayFact struct {
	attempts map[runstate.AttemptID]*recovery.AttemptRecovery
	gates    map[runstate.AttemptID]*recovery.GateRecovery
	sequence map[runstate.AttemptID]uint64
	failed   map[runstate.AttemptID]bool
}

func replayFacts(events []runstate.Event) replayFact {
	facts := replayFact{
		attempts: make(map[runstate.AttemptID]*recovery.AttemptRecovery),
		gates:    make(map[runstate.AttemptID]*recovery.GateRecovery),
		sequence: make(map[runstate.AttemptID]uint64),
		failed:   make(map[runstate.AttemptID]bool),
	}
	type questionRef struct {
		attemptID runstate.AttemptID
		index     int
	}
	requests := make(map[string]questionRef)
	gateDecisions := make(map[string]runstate.AttemptID)
	for _, event := range events {
		payload, err := eventPayload(event)
		if err != nil {
			continue // ReadJournal has already validated this event.
		}
		switch event.Type {
		case runstate.EventPerformerSelected:
			facts.attempts[event.AttemptID] = &recovery.AttemptRecovery{AttemptID: event.AttemptID, MovementID: event.MovementID, ScoreRevision: event.ScoreRevision}
			facts.sequence[event.AttemptID] = event.Seq
		case runstate.EventAttemptBlocked:
			attempt := facts.attempts[event.AttemptID]
			if attempt == nil {
				continue
			}
			for _, raised := range arrayValue(payload, "raised") {
				entry, ok := raised.(map[string]any)
				if !ok || stringValue(entry, "kind") != "question" {
					continue
				}
				request := recovery.QuestionRequest{DecisionID: stringValue(entry, "decision_id")}
				attempt.QuestionRequests = append(attempt.QuestionRequests, request)
				requests[request.DecisionID] = questionRef{attemptID: event.AttemptID, index: len(attempt.QuestionRequests) - 1}
			}
		case runstate.EventDecisionRequested:
			if stringValue(payload, "decision_type") == "question" {
				if reference, ok := requests[stringValue(payload, "decision_id")]; ok {
					facts.attempts[reference.attemptID].QuestionRequests[reference.index].Durable = true
				}
			}
			if stringValue(payload, "decision_type") == "human_gate" {
				gate := &recovery.GateRecovery{Required: true, Requested: true, DecisionID: stringValue(payload, "decision_id"), GateID: stringValue(payload, "gate_id")}
				facts.gates[event.AttemptID] = gate
				gateDecisions[gate.DecisionID] = event.AttemptID
			}
		case runstate.EventDecisionResolved:
			if attemptID, ok := gateDecisions[stringValue(payload, "decision_id")]; ok {
				gate := facts.gates[attemptID]
				gate.Resolved = true
				gate.Approved = stringValue(payload, "disposition") == "approved"
			}
		case runstate.EventChangeSetRecorded:
			if attempt := facts.attempts[event.AttemptID]; attempt != nil {
				attempt.ChangeSetRecorded = true
			}
		case runstate.EventAcceptanceStarted:
			if attempt := facts.attempts[event.AttemptID]; attempt != nil {
				attempt.AcceptanceStarted = true
			}
		case runstate.EventMovementSucceeded:
			if attempt := facts.attempts[event.AttemptID]; attempt != nil {
				attempt.MovementSucceeded = true
			}
		case runstate.EventMovementFailed:
			if attempt := facts.attempts[event.AttemptID]; attempt != nil {
				attempt.MovementFailed = true
				attempt.FailureDispositionRealized = true
				attempt.FinalGateRejected = stringValue(payload, "reason") == "human_gate_rejected"
			}
		case runstate.EventAttemptFailed, runstate.EventAcceptanceFailed:
			if attempt := facts.attempts[event.AttemptID]; attempt != nil {
				disposition := runstate.Disposition{Charged: stringValue(objectValue(payload, "disposition"), "charged"), MovementTerminal: boolValue(objectValue(payload, "disposition"), "movement_terminal"), TerminalReason: stringValue(objectValue(payload, "disposition"), "terminal_reason")}
				attempt.RecordedDisposition = &disposition
				if event.Type == runstate.EventAcceptanceFailed {
					facts.failed[event.AttemptID] = true
				}
			}
		}
	}
	return facts
}

func (facts replayFact) currentHeadAttempt(state runstate.State) *recovery.AttemptRecovery {
	var selected *recovery.AttemptRecovery
	for _, attempt := range facts.attempts {
		actual, ok := state.Attempts[attempt.AttemptID]
		if !ok || attempt.ScoreRevision != state.ScoreHead.Revision || actual.State == runstate.AttemptSuperseded {
			continue
		}
		copy := *attempt
		copy.State = actual.State
		if actual.Failure != nil && copy.RecordedDisposition == nil {
			disposition := actual.Failure.Disposition
			copy.RecordedDisposition = &disposition
		}
		if selected == nil || facts.sequence[copy.AttemptID] > facts.sequence[selected.AttemptID] {
			selected = &copy
		}
	}
	return selected
}

func (facts replayFact) acceptance(attemptID runstate.AttemptID, movementID runstate.MovementID, pinned *score.Score) *recovery.AcceptanceRecovery {
	result := &recovery.AcceptanceRecovery{Failed: facts.failed[attemptID]}
	for _, movement := range pinned.Movements() {
		if movement.ID == string(movementID) && movement.Acceptance.HumanGate == "always" {
			result.Gate.Required = true
			break
		}
	}
	if gate := facts.gates[attemptID]; gate != nil {
		result.Gate = *gate
	}
	return result
}

func runStartedEventFrom(events []runstate.Event) (runstate.Event, error) {
	for _, event := range events {
		if event.Type == runstate.EventRunStarted {
			return event, nil
		}
	}
	return runstate.Event{}, errors.New("run.started is missing")
}

func eventPayload(event runstate.Event) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func rawHash(contents []byte) string {
	digest := sha256.Sum256(contents)
	return fmt.Sprintf("sha256:%x", digest)
}

func hasGrant(grants []string, want string) bool {
	for _, grant := range grants {
		if grant == want {
			return true
		}
	}
	return false
}

func stringValue(value map[string]any, name string) string {
	result, _ := value[name].(string)
	return result
}
func boolValue(value map[string]any, name string) bool {
	result, _ := value[name].(bool)
	return result
}
func objectValue(value map[string]any, name string) map[string]any {
	result, _ := value[name].(map[string]any)
	return result
}
func arrayValue(value map[string]any, name string) []any {
	result, _ := value[name].([]any)
	return result
}
