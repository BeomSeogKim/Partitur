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

// AcquireRecoveryDriver establishes authority from the selected run's pinned
// score, never from current repository inputs.
func (store *Store) AcquireRecoveryDriver(runID runstate.RunID) (*Driver, error) {
	input, err := store.LoadRecoveryInput(runID)
	if err != nil {
		return nil, err
	}
	return store.AcquireDriver(runID, movementSeed(input.Score))
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
	replay, err := replayJournal(journal, seed)
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
		Projection: recoveryProjection(replay.State, journal.Events, currentScore, resolvedCast),
		Score:      currentScore,
		Cast:       resolvedCast,
	}, nil
}

func (store *Store) loadPinnedScore(runID runstate.RunID, revision uint64, payload map[string]any) (*score.Score, error) {
	path := filepath.Join(store.root, ".partitur", "runs", string(runID), "scores", fmt.Sprintf("revision-%d.yaml", revision))
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read pinned score revision %d: %v", ErrMissingPinnedSnapshot, revision, err)
	}
	if rawHash(contents) != stringValue(payload, "score_file_hash") {
		return nil, fmt.Errorf("%w: pinned score file hash does not match journal", ErrMissingPinnedSnapshot)
	}
	compiled, diagnostics := score.Compile(contents)
	if len(diagnostics) != 0 {
		return nil, fmt.Errorf("%w: compile pinned score revision %d: %v", ErrMissingPinnedSnapshot, revision, diagnostics)
	}
	semanticHash, err := compiled.Hash()
	if err != nil {
		return nil, err
	}
	if semanticHash != stringValue(payload, "score_hash") {
		return nil, fmt.Errorf("%w: pinned score semantic hash does not match journal", ErrMissingPinnedSnapshot)
	}
	return compiled, nil
}

func (store *Store) loadResolvedCast(runID runstate.RunID, payload map[string]any) (*cast.Cast, error) {
	path := filepath.Join(store.root, ".partitur", "runs", string(runID), "resolved-cast.yaml")
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read resolved cast: %v", ErrMissingResolvedCast, err)
	}
	resolved, diagnostics := cast.Resolve([]cast.Layer{{Origin: "run-owned resolved cast", Data: contents}})
	if len(diagnostics) != 0 {
		return nil, fmt.Errorf("%w: parse resolved cast: %v", ErrMissingResolvedCast, diagnostics)
	}
	hash, err := resolved.Hash()
	if err != nil {
		return nil, err
	}
	if hash != stringValue(payload, "resolved_cast_hash") {
		return nil, fmt.Errorf("%w: resolved cast hash does not match journal", ErrMissingResolvedCast)
	}
	return resolved, nil
}

func recoveryProjection(state runstate.State, events []runstate.Event, pinned *score.Score, resolved *cast.Cast) recovery.Projection {
	facts := replayFacts(events)
	projection := recovery.Projection{
		State:                state,
		RevisionRestarts:     facts.revisionRestarts(state),
		CompositionTerminals: facts.compositionTerminals(events),
		Scheduler:            schedulerFromScore(state, pinned),
	}
	projection.CompositionRecovery = facts.compositionRecovery(state, projection.Scheduler)
	current := facts.currentHeadAttempt(state)
	if current == nil {
		return projection
	}
	current.FailureClassification = facts.failureClassification(*current, pinned, resolved, projection.Scheduler)
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
	attempts          map[runstate.AttemptID]*recovery.AttemptRecovery
	gates             map[runstate.AttemptID]*recovery.GateRecovery
	sequence          map[runstate.AttemptID]uint64
	failed            map[runstate.AttemptID]bool
	approvals         []revisionApproval
	compositionCloses map[string]bool
	compositionEvents []recovery.CompositionTerminal
	performers        map[runstate.AttemptID]string
	visitedPerformers map[runstate.MovementID][]string
	retriesConsumed   map[runstate.MovementID]int
}

type revisionApproval struct {
	Revision            uint64
	Finalization        bool
	SupersededAttemptID []runstate.AttemptID
}

func replayFacts(events []runstate.Event) replayFact {
	facts := replayFact{
		attempts:          make(map[runstate.AttemptID]*recovery.AttemptRecovery),
		gates:             make(map[runstate.AttemptID]*recovery.GateRecovery),
		sequence:          make(map[runstate.AttemptID]uint64),
		failed:            make(map[runstate.AttemptID]bool),
		compositionCloses: make(map[string]bool),
		performers:        make(map[runstate.AttemptID]string),
		visitedPerformers: make(map[runstate.MovementID][]string),
		retriesConsumed:   make(map[runstate.MovementID]int),
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
			performer := stringValue(payload, "performer_id")
			facts.performers[event.AttemptID] = performer
			facts.visitedPerformers[event.MovementID] = append(facts.visitedPerformers[event.MovementID], performer)
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
			if stringValue(objectValue(payload, "disposition"), "charged") == "quality_retry" {
				facts.retriesConsumed[event.MovementID]++
			}
		case runstate.EventAmendmentApproved:
			approval := revisionApproval{
				Revision:     uintValue(payload, "new_revision"),
				Finalization: boolValue(payload, "finalization"),
			}
			for _, attemptID := range arrayValue(payload, "superseded_attempt_ids") {
				if value, ok := attemptID.(string); ok {
					approval.SupersededAttemptID = append(approval.SupersededAttemptID, runstate.AttemptID(value))
				}
			}
			facts.approvals = append(facts.approvals, approval)
		case runstate.EventExecutionStarted:
			if stringValue(payload, "phase") == "composition" {
				facts.compositionCloses[stringValue(payload, "interval_id")] = false
			}
		case runstate.EventExecutionStopped:
			intervalID := stringValue(payload, "interval_id")
			if _, tracked := facts.compositionCloses[intervalID]; tracked && stringValue(payload, "reason") == "recovered" {
				facts.compositionCloses[intervalID] = true
			}
		case runstate.EventCompositionConflicted, runstate.EventCompositionFailed:
			evidence := recovery.CompositionTerminal{
				Scope:    stringValue(payload, "scope"),
				TargetID: stringValue(payload, "target_id"),
				Reason:   "composition_unresolvable",
			}
			if event.Type == runstate.EventCompositionFailed {
				evidence.Reason = "composition_failed"
			}
			facts.compositionEvents = append(facts.compositionEvents, evidence)
		}
	}
	return facts
}

func (facts replayFact) failureClassification(
	attempt recovery.AttemptRecovery,
	pinned *score.Score,
	resolved *cast.Cast,
	scheduler recovery.Scheduler,
) recovery.FailureClassification {
	result := recovery.FailureClassification{
		CurrentPerformer:  facts.performers[attempt.AttemptID],
		VisitedPerformers: append([]string(nil), facts.visitedPerformers[attempt.MovementID]...),
		RetriesConsumed:   facts.retriesConsumed[attempt.MovementID],
		RemainingTimeMS:   scheduler.RemainingTime,
	}
	if pinned == nil || resolved == nil {
		return result
	}
	result.RetriesPerMovement = int(pinned.EffectivePolicy().RetriesPerMovement)
	for _, movement := range pinned.Movements() {
		if runstate.MovementID(movement.ID) != attempt.MovementID {
			continue
		}
		if binding, ok := resolved.Binding(movement.PartID); ok {
			result.Fallbacks = append([]string(nil), binding.Fallbacks...)
		}
		break
	}
	return result
}

func (facts replayFact) revisionRestarts(state runstate.State) []recovery.RevisionRestart {
	var restarts []recovery.RevisionRestart
	for _, approval := range facts.approvals {
		if approval.Revision != state.ScoreHead.Revision || approval.Finalization {
			continue
		}
		for _, attemptID := range approval.SupersededAttemptID {
			attempt, ok := facts.attempts[attemptID]
			if !ok || facts.hasAttemptOnRevision(attempt.MovementID, approval.Revision) {
				continue
			}
			restarts = append(restarts, recovery.RevisionRestart{MovementID: attempt.MovementID})
		}
	}
	return restarts
}

func (facts replayFact) hasAttemptOnRevision(movementID runstate.MovementID, revision uint64) bool {
	for _, attempt := range facts.attempts {
		if attempt.MovementID == movementID && attempt.ScoreRevision == revision {
			return true
		}
	}
	return false
}

func (facts replayFact) compositionTerminals(events []runstate.Event) []recovery.CompositionTerminal {
	var terminals []recovery.CompositionTerminal
	for index, event := range events {
		if event.Type != runstate.EventCompositionConflicted && event.Type != runstate.EventCompositionFailed {
			continue
		}
		payload, err := eventPayload(event)
		if err != nil {
			continue
		}
		terminal := recovery.CompositionTerminal{
			Scope:    stringValue(payload, "scope"),
			TargetID: stringValue(payload, "target_id"),
			Reason:   "composition_unresolvable",
		}
		if event.Type == runstate.EventCompositionFailed {
			terminal.Reason = "composition_failed"
		}
		if !compositionTerminalFollows(events[index+1:], terminal) {
			terminals = append(terminals, terminal)
		}
	}
	return terminals
}

func compositionTerminalFollows(events []runstate.Event, terminal recovery.CompositionTerminal) bool {
	for _, event := range events {
		payload, err := eventPayload(event)
		if err != nil {
			continue
		}
		switch terminal.Scope {
		case "movement":
			if event.Type == runstate.EventMovementFailed && event.MovementID == runstate.MovementID(terminal.TargetID) && stringValue(payload, "reason") == terminal.Reason {
				return true
			}
		case "candidate":
			if event.Type == runstate.EventRunFailed && stringValue(payload, "reason") == terminal.Reason {
				return true
			}
		}
	}
	return false
}

func (facts replayFact) compositionRecovery(state runstate.State, scheduler recovery.Scheduler) *recovery.CompositionRecovery {
	for _, recovered := range facts.compositionCloses {
		if !recovered {
			continue
		}
		if movementID, ok := compositionMovement(state, scheduler); ok && !facts.hasCompositionEvidence("movement", string(movementID)) {
			return &recovery.CompositionRecovery{Scope: "movement", MovementID: movementID, Recovered: true}
		}
		if candidateCompositionPending(state, scheduler) && !facts.hasCompositionEvidenceScope("candidate") {
			return &recovery.CompositionRecovery{Scope: "candidate", Recovered: true}
		}
	}
	return nil
}

func compositionMovement(state runstate.State, scheduler recovery.Scheduler) (runstate.MovementID, bool) {
	for _, movement := range scheduler.Movements {
		if state.Movements[movement.ID] == runstate.MovementRunning {
			return movement.ID, true
		}
	}
	return "", false
}

func candidateCompositionPending(state runstate.State, scheduler recovery.Scheduler) bool {
	if state.ApplicationCandidate != nil {
		return false
	}
	for _, movement := range scheduler.Movements {
		if movement.Final || !movement.RepoWrite {
			continue
		}
		if state.Movements[movement.ID] != runstate.MovementSucceeded {
			return false
		}
	}
	return true
}

func (facts replayFact) hasCompositionEvidence(scope, targetID string) bool {
	for _, event := range facts.compositionEvents {
		if event.Scope == scope && event.TargetID == targetID {
			return true
		}
	}
	return false
}

func (facts replayFact) hasCompositionEvidenceScope(scope string) bool {
	for _, event := range facts.compositionEvents {
		if event.Scope == scope {
			return true
		}
	}
	return false
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
func uintValue(value map[string]any, name string) uint64 {
	result, _ := value[name].(float64)
	return uint64(result)
}
func objectValue(value map[string]any, name string) map[string]any {
	result, _ := value[name].(map[string]any)
	return result
}
func arrayValue(value map[string]any, name string) []any {
	result, _ := value[name].([]any)
	return result
}
