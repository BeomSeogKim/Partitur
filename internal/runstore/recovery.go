package runstore

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/successor"
)

// RunInput is the complete authoritative input owned by a run. It is shared
// by live execution and recovery before either surface adds its own inputs.
type RunInput struct {
	Projection recovery.Projection
	Score      *score.Score
	Cast       *cast.Cast
	BaseCommit string
	BaseTree   string
}

// AcquireRecoveryDriver establishes authority from the selected run's pinned
// score, never from current repository inputs.
func (store *Store) AcquireRecoveryDriver(runID runstate.RunID) (*Driver, error) {
	input, err := store.LoadRunInput(runID)
	if err != nil {
		return nil, err
	}
	return store.AcquireDriver(runID, movementSeed(input.Score))
}

// ReclaimDeadRecoveryDriver replaces the exact dead lease observed before
// recovery planning with a new recovery driver under one state lock.
func (store *Store) ReclaimDeadRecoveryDriver(runID runstate.RunID, expected LeaseIdentity) (*Driver, error) {
	input, err := store.LoadRunInput(runID)
	if err != nil {
		return nil, err
	}
	pid := os.Getpid()
	start, err := procid.Read(pid)
	if err != nil {
		return nil, fmt.Errorf("read driver identity: %w", err)
	}
	token, err := newDriverToken()
	if err != nil {
		return nil, err
	}
	var acquired Lease
	err = store.Mutate(runID, "", func(transaction *Txn) error {
		state, err := transaction.project(movementSeed(input.Score))
		if err != nil {
			return err
		}
		if state.Run == runstate.RunNotStarted || state.Run.Terminal() ||
			state.Authority.Owner == nil || state.Authority.Epoch != expected.Epoch ||
			state.Authority.Owner.PID != expected.PID ||
			!startIdentitiesEqual(state.Authority.Owner.Start, expected.Start) {
			return ErrLeaseConflict
		}
		lease, present, err := transaction.ReadLease()
		if err != nil {
			return err
		}
		if !present || !leaseMatches(lease, expected) || lease.MatchOwner().Status != procid.GoneOrReused {
			return ErrLeaseConflict
		}
		if _, err := transaction.At("recovery.reclaim_authority.lease").CompareRemoveLease(expected); err != nil {
			return err
		}
		epoch := state.Authority.Epoch + 1
		payload, err := json.Marshal(map[string]any{
			"authority_epoch":      epoch,
			"owner_pid":            pid,
			"owner_start_identity": encodeDriverStart(start),
		})
		if err != nil {
			return err
		}
		event := runstate.Event{
			RunID:         runID,
			ScoreRevision: state.ScoreHead.Revision,
			Type:          runstate.EventAuthorityGranted,
			Payload:       payload,
		}
		if _, err := runstate.Apply(state, event); err != nil {
			return err
		}
		if _, err := transaction.At(receiptAuthorityGranted).Append(event); err != nil {
			return err
		}
		store.probe.Reached(faultpoint.PointAuthorityGranted)
		acquired = Lease{Epoch: epoch, Token: token, PID: pid, Start: start}
		if _, err := transaction.At(receiptDriverLease).CreateLease(true, acquired); err != nil {
			return err
		}
		store.probe.Reached(faultpoint.PointAuthorityLeaseCreated)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Driver{store: store, runID: runID, seed: movementSeed(input.Score), lease: acquired}, nil
}

// LoadRunInput reads only the selected run's journal and authoritative
// run-owned inputs. It never reads or recompiles the repository root score or
// current cast layers.
func (store *Store) LoadRunInput(runID runstate.RunID) (RunInput, error) {
	journal, err := store.ReadJournal(runID)
	if err != nil {
		return RunInput{}, err
	}
	started, err := runStartedEventFrom(journal.Events)
	if err != nil {
		return RunInput{}, err
	}
	startPayload, err := eventPayload(started)
	if err != nil {
		return RunInput{}, err
	}

	initialScore, err := store.loadPinnedScore(runID, started.ScoreRevision, startPayload)
	if err != nil {
		return RunInput{}, err
	}
	seed := movementSeed(initialScore)
	replay, err := replayJournal(journal, seed)
	if err != nil {
		return RunInput{}, err
	}
	currentScore, err := store.loadPinnedScore(runID, replay.State.ScoreHead.Revision, map[string]any{
		"score_hash":      string(replay.State.ScoreHead.SemanticHash),
		"score_file_hash": string(replay.State.ScoreHead.FileHash),
	})
	if err != nil {
		return RunInput{}, err
	}
	resolvedCast, err := store.loadResolvedCast(runID, startPayload)
	if err != nil {
		return RunInput{}, err
	}

	return RunInput{
		Projection: recoveryProjection(replay.State, journal.Events, currentScore, resolvedCast),
		Score:      currentScore,
		Cast:       resolvedCast,
		BaseCommit: stringValue(startPayload, "base_commit"),
		BaseTree:   stringValue(startPayload, "base_tree"),
	}, nil
}

// LoadInitialScore reads the score snapshot bound by run.started. It never
// substitutes another surviving snapshot.
func (store *Store) LoadInitialScore(runID runstate.RunID) (*score.Score, error) {
	journal, err := store.ReadJournal(runID)
	if err != nil {
		return nil, err
	}
	started, err := runStartedEventFrom(journal.Events)
	if err != nil {
		return nil, err
	}
	payload, err := eventPayload(started)
	if err != nil {
		return nil, err
	}
	return store.loadPinnedScore(runID, started.ScoreRevision, payload)
}

// LoadScoreSnapshot reads the immutable score snapshot recorded for revision.
// A revision is authoritative only when run.started or amendment.approved
// bound both its file and semantic hashes in the journal.
func (store *Store) LoadScoreSnapshot(runID runstate.RunID, revision uint64) (*score.Score, error) {
	journal, err := store.ReadJournal(runID)
	if err != nil {
		return nil, err
	}
	for _, event := range journal.Events {
		if event.Type != runstate.EventRunStarted && event.Type != runstate.EventAmendmentApproved {
			continue
		}
		payload, err := eventPayload(event)
		if err != nil {
			return nil, err
		}
		recordedRevision := event.ScoreRevision
		if event.Type == runstate.EventAmendmentApproved {
			var ok bool
			recordedRevision, ok = uintPayload(payload, "new_revision")
			if !ok {
				return nil, fmt.Errorf("%w: amendment score revision is invalid", ErrMissingPinnedSnapshot)
			}
			payload = map[string]any{
				"score_hash":      payload["new_snapshot_hash"],
				"score_file_hash": payload["new_snapshot_file_hash"],
			}
		}
		if recordedRevision == revision {
			return store.loadPinnedScore(runID, revision, payload)
		}
	}
	return nil, fmt.Errorf("%w: no recorded score revision %d", ErrMissingPinnedSnapshot, revision)
}

func (store *Store) loadPinnedScore(runID runstate.RunID, revision uint64, payload map[string]any) (*score.Score, error) {
	if revision == 0 {
		return nil, fmt.Errorf("%w: recorded score revision is zero", ErrMissingPinnedSnapshot)
	}
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
	if compiled.Revision() != revision {
		return nil, fmt.Errorf("%w: pinned score revision does not match journal", ErrMissingPinnedSnapshot)
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

func uintPayload(payload map[string]any, name string) (uint64, bool) {
	value, ok := payload[name].(float64)
	if !ok || value < 0 || value != float64(uint64(value)) {
		return 0, false
	}
	return uint64(value), true
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
		CompositionTerminals: facts.compositionTerminals(events, state.ScoreHead.Revision),
		Scheduler:            schedulerFromScore(state, pinned),
	}
	projection.CompositionRecovery = facts.compositionRecovery(state, projection.Scheduler)
	current := facts.currentHeadAttempt(state)
	if current == nil {
		return projection
	}
	current.FailureClassification = facts.failureClassification(*current, pinned, resolved, projection.Scheduler)
	projection.Scheduler.PendingSuccessor = pendingSuccessor(*current)
	projection.CurrentHeadAttempt = current
	if acceptance, ok := state.Acceptances[current.AttemptID]; ok && acceptance.Started {
		projection.Acceptance = facts.acceptance(state, current.AttemptID, current.MovementID, pinned)
		if projection.Acceptance.Gate.Required && projection.Acceptance.Gate.Requested &&
			projection.Acceptance.Gate.Resolved && !projection.Acceptance.Gate.Approved &&
			state.FinalMovements[current.MovementID] {
			current.FinalGateRejected = true
		}
	}
	return projection
}

func pendingSuccessor(attempt recovery.AttemptRecovery) *recovery.PendingSuccessor {
	if attempt.State != runstate.AttemptFailed || attempt.FailureDispositionRealized || attempt.RecordedDisposition == nil {
		return nil
	}
	facts := attempt.FailureClassification
	realization, err := successor.Realize(successor.RealizationInput{
		Disposition:       *attempt.RecordedDisposition,
		CurrentPerformer:  facts.CurrentPerformer,
		Binding:           cast.BindingView{Fallbacks: facts.Fallbacks},
		VisitedPerformers: facts.VisitedPerformers,
	})
	if err != nil || realization.Action != successor.ActionPendingSuccessor {
		return nil
	}
	return &recovery.PendingSuccessor{
		MovementID: attempt.MovementID,
		AttemptID:  attempt.AttemptID,
		Performer:  realization.Performer,
		Reason:     string(realization.Charge),
	}
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
	execution := pinned.Execution()
	seed := make([]runstate.MovementSeed, 0, len(movements))
	for _, movement := range movements {
		initial := runstate.MovementPending
		if movement.Phase == "draft" {
			initial = runstate.MovementInapplicable
		}
		seed = append(seed, runstate.MovementSeed{
			ID: runstate.MovementID(movement.ID), Initial: initial,
			RepoWrite:       hasGrant(movement.Grants, "repo_write"),
			HasDependencies: len(movement.Needs) != 0,
			Final:           movement.ID == execution.FinalMovementID,
		})
	}
	return seed
}

type replayFact struct {
	attempts          map[runstate.AttemptID]*recovery.AttemptRecovery
	sequence          map[runstate.AttemptID]uint64
	failed            map[runstate.AttemptID]bool
	approvals         []revisionApproval
	compositionCloses map[string]compositionClose
	compositionEvents []recovery.CompositionTerminal
	performers        map[runstate.AttemptID]string
	visitedPerformers map[runstate.MovementID][]string
	retriesConsumed   map[runstate.MovementID]int
	reviewOutcomes    map[runstate.AttemptID]recovery.GateRecovery
}

type revisionApproval struct {
	Revision            uint64
	Finalization        bool
	SupersededAttemptID []runstate.AttemptID
}

type compositionClose struct {
	recovered     bool
	scoreRevision uint64
}

func replayFacts(events []runstate.Event) replayFact {
	facts := replayFact{
		attempts:          make(map[runstate.AttemptID]*recovery.AttemptRecovery),
		sequence:          make(map[runstate.AttemptID]uint64),
		failed:            make(map[runstate.AttemptID]bool),
		compositionCloses: make(map[string]compositionClose),
		performers:        make(map[runstate.AttemptID]string),
		visitedPerformers: make(map[runstate.MovementID][]string),
		retriesConsumed:   make(map[runstate.MovementID]int),
		reviewOutcomes:    make(map[runstate.AttemptID]recovery.GateRecovery),
	}
	type questionRef struct {
		attemptID runstate.AttemptID
		index     int
	}
	requests := make(map[string]questionRef)
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
		case runstate.EventChangeSetRecorded:
			if attempt := facts.attempts[event.AttemptID]; attempt != nil {
				attempt.ChangeSetRecorded = true
			}
		case runstate.EventAcceptanceStarted:
			if attempt := facts.attempts[event.AttemptID]; attempt != nil {
				attempt.AcceptanceStarted = true
			}
		case runstate.EventAcceptanceEvaluationCompleted:
			if outcome := stringValue(payload, "review_outcome"); outcome != "" {
				facts.reviewOutcomes[event.AttemptID] = recovery.GateRecovery{
					ReviewOutcome: outcome, BlockingFindings: findingReferences(arrayValue(payload, "blocking_findings")),
				}
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
				facts.compositionCloses[stringValue(payload, "interval_id")] = compositionClose{scoreRevision: event.ScoreRevision}
			}
		case runstate.EventExecutionStopped:
			intervalID := stringValue(payload, "interval_id")
			if close, tracked := facts.compositionCloses[intervalID]; tracked && stringValue(payload, "reason") == "recovered" {
				close.recovered = true
				facts.compositionCloses[intervalID] = close
			}
		case runstate.EventCompositionConflicted, runstate.EventCompositionFailed:
			evidence := recovery.CompositionTerminal{
				Scope:           stringValue(payload, "scope"),
				TargetID:        stringValue(payload, "target_id"),
				Reason:          "composition_unresolvable",
				EvidenceEventID: event.EventID,
				ScoreRevision:   event.ScoreRevision,
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

func (facts replayFact) compositionTerminals(events []runstate.Event, scoreRevision uint64) []recovery.CompositionTerminal {
	evidenceByEventID := map[string]recovery.CompositionTerminal{}
	completedSubjects := map[string]bool{}
	var evidence []recovery.CompositionTerminal
	for _, event := range events {
		if event.Type != runstate.EventCompositionConflicted && event.Type != runstate.EventCompositionFailed {
			if cause, ok := evidenceByEventID[event.CausationID]; ok && compositionTerminalFollows(event, cause) {
				completedSubjects[compositionSubjectKey(cause)] = true
			}
			continue
		}
		if event.ScoreRevision == scoreRevision {
			payload, err := eventPayload(event)
			if err != nil {
				continue
			}
			terminal := recovery.CompositionTerminal{
				Scope:           stringValue(payload, "scope"),
				TargetID:        stringValue(payload, "target_id"),
				Reason:          "composition_unresolvable",
				EvidenceEventID: event.EventID,
				ScoreRevision:   event.ScoreRevision,
			}
			if event.Type == runstate.EventCompositionFailed {
				terminal.Reason = "composition_failed"
			}
			evidence = append(evidence, terminal)
			evidenceByEventID[event.EventID] = terminal
		}
	}
	var terminals []recovery.CompositionTerminal
	for _, terminal := range evidence {
		if !completedSubjects[compositionSubjectKey(terminal)] {
			terminals = append(terminals, terminal)
		}
	}
	return terminals
}

func compositionTerminalFollows(event runstate.Event, evidence recovery.CompositionTerminal) bool {
	payload, err := eventPayload(event)
	if err != nil {
		return false
	}
	switch evidence.Scope {
	case "movement":
		return event.Type == runstate.EventMovementFailed &&
			event.ScoreRevision == evidence.ScoreRevision &&
			event.CausationID == evidence.EvidenceEventID &&
			event.MovementID == runstate.MovementID(evidence.TargetID) &&
			stringValue(payload, "reason") == evidence.Reason
	case "candidate":
		return event.Type == runstate.EventRunFailed &&
			event.ScoreRevision == evidence.ScoreRevision &&
			event.CausationID == evidence.EvidenceEventID &&
			stringValue(payload, "reason") == evidence.Reason
	default:
		return false
	}
}

func compositionSubjectKey(evidence recovery.CompositionTerminal) string {
	return evidence.Scope + "\x00" + evidence.TargetID
}

func (facts replayFact) compositionRecovery(state runstate.State, scheduler recovery.Scheduler) *recovery.CompositionRecovery {
	for _, recovered := range facts.compositionCloses {
		if !recovered.recovered || recovered.scoreRevision != state.ScoreHead.Revision {
			continue
		}
		if movementID, ok := compositionMovement(state, scheduler); ok && !facts.hasCompositionEvidence("movement", string(movementID), state.ScoreHead.Revision) {
			return &recovery.CompositionRecovery{Scope: "movement", MovementID: movementID, Recovered: true, ScoreRevision: recovered.scoreRevision}
		}
		if candidateCompositionPending(state, scheduler) && !facts.hasCompositionEvidenceScope("candidate", state.ScoreHead.Revision) {
			return &recovery.CompositionRecovery{Scope: "candidate", Recovered: true, ScoreRevision: recovered.scoreRevision}
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

func (facts replayFact) hasCompositionEvidence(scope, targetID string, scoreRevision uint64) bool {
	for _, event := range facts.compositionEvents {
		if event.Scope == scope && event.TargetID == targetID && event.ScoreRevision == scoreRevision {
			return true
		}
	}
	return false
}

func (facts replayFact) hasCompositionEvidenceScope(scope string, scoreRevision uint64) bool {
	for _, event := range facts.compositionEvents {
		if event.Scope == scope && event.ScoreRevision == scoreRevision {
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

func (facts replayFact) acceptance(state runstate.State, attemptID runstate.AttemptID, movementID runstate.MovementID, pinned *score.Score) *recovery.AcceptanceRecovery {
	result := &recovery.AcceptanceRecovery{Failed: facts.failed[attemptID]}
	if review, ok := facts.reviewOutcomes[attemptID]; ok {
		result.Gate.ReviewOutcome = review.ReviewOutcome
		result.Gate.BlockingFindings = append([]runstate.FindingReference(nil), review.BlockingFindings...)
	}
	for _, movement := range pinned.Movements() {
		if movement.ID == string(movementID) {
			result.Gate.Required = movement.Acceptance.HumanGate == "always"
			if movement.Acceptance.HumanGate == "on_contested" {
				if result.Gate.ReviewOutcome == "CONTESTED" {
					result.Gate.Required = true
				}
			}
			break
		}
	}
	// An amendment or terminal source closes pending decisions before its
	// decision.obsoleted audit event is appended, and an amendment supersedes
	// its live attempts. A reachable current-head gate is therefore never lost
	// merely because it is absent from PendingDecisions here.
	for _, decision := range state.PendingDecisions {
		if decision.Type == "human_gate" && decision.AttemptID == attemptID && decision.MovementID == movementID {
			result.Gate.Requested = true
			result.Gate.DecisionID = decision.ID
			result.Gate.GateID = decision.GateID
			break
		}
	}
	if resolution, ok := state.ResolvedHumanGates[attemptID]; ok && resolution.MovementID == movementID {
		result.Gate.Requested = true
		result.Gate.Resolved = true
		result.Gate.Approved = resolution.Disposition == "approved"
		result.Gate.DecisionID = resolution.DecisionID
		result.Gate.GateID = resolution.GateID
	}
	return result
}

func findingReferences(values []any) []runstate.FindingReference {
	result := make([]runstate.FindingReference, len(values))
	for index, value := range values {
		pair := value.(map[string]any)
		result[index] = runstate.FindingReference{ArtifactInstanceID: stringValue(pair, "artifact_instance_id"), FindingID: stringValue(pair, "finding_id")}
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
