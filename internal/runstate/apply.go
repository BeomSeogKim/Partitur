package runstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

var (
	ErrUnsupportedEventType = errors.New("unsupported event type")
	ErrInvalidEvent         = errors.New("invalid event")
	ErrIllegalTransition    = errors.New("illegal transition")
)

// ValidateEvent validates the supported event's exact payload without applying
// its transition.
func ValidateEvent(event Event) error {
	_, err := validatePayload(event)
	return err
}

// IdempotencyKey returns the Appendix B key for a supported event.
func IdempotencyKey(event Event) (string, error) {
	payload, err := validatePayload(event)
	if err != nil {
		return "", err
	}
	switch event.Type {
	case EventRunStarted, EventRunSucceeded, EventRunFailed, EventRunCancelled, EventCancelRequested:
		return string(event.RunID), nil
	case EventMovementReady, EventMovementStarted:
		return string(event.MovementID), nil
	case EventPerformerSelected, EventAttemptStarted, EventPerformerCompleted, EventAttemptFailed,
		EventAcceptanceStarted, EventAcceptanceFailed:
		return string(event.AttemptID), nil
	case EventCriterionStarted, EventCriterionCompleted:
		return string(event.AttemptID) + "\x00" + mustString(payload, "criterion_id"), nil
	case EventExecutionStarted, EventExecutionStopped:
		return mustString(payload, "interval_id"), nil
	case EventAmendmentApprovalPrepared, EventAmendmentApprovalAbandoned:
		return mustString(payload, "prepare_id"), nil
	case EventAmendmentApproved:
		return mustString(payload, "proposal_id"), nil
	case EventAuthorityGranted:
		return fmt.Sprintf("%d", mustUint(payload, "authority_epoch")), nil
	case EventJournalTailTruncated:
		return fmt.Sprintf("%d", mustUint(payload, "truncated_seq")), nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedEventType, event.Type)
	}
}

// Apply validates and projects one supported event. Its returned state never
// aliases maps or slices in input, including when an error is returned.
func Apply(input State, event Event) (State, error) {
	state := cloneState(input)
	payload, err := validatePayload(event)
	if err != nil {
		return state, err
	}

	switch event.Type {
	case EventRunStarted:
		if state.Run != RunNotStarted {
			return state, transition(event, "run already started")
		}
		state.Run = RunRunning
		state.ScoreHead = ScoreHead{
			Revision:     event.ScoreRevision,
			SemanticHash: Hash(mustString(payload, "score_hash")),
			FileHash:     Hash(mustString(payload, "score_file_hash")),
		}
	case EventRunSucceeded:
		if state.Run != RunRunning {
			return state, transition(event, "run is not RUNNING")
		}
		state.Run = RunSucceeded
	case EventRunFailed:
		if state.Run != RunRunning {
			return state, transition(event, "run is not RUNNING")
		}
		state.Run = RunFailed
	case EventRunCancelled:
		if state.Run == RunNotStarted || state.Run.Terminal() {
			return state, transition(event, "run is not nonterminal")
		}
		if state.PendingPrepare != nil {
			return state, transition(event, "prepare is still pending")
		}
		cancelledMovements := mustStrings(payload, "cancelled_movement_ids")
		wantMovements := cancellableMovementIDs(state)
		if !slices.Equal(cancelledMovements, wantMovements) {
			return state, invalid(event, "cancelled_movement_ids do not match the pre-event projection")
		}
		cancelledAttempts := mustStrings(payload, "cancelled_attempt_ids")
		wantAttempts := cancellableAttemptIDs(state)
		if !slices.Equal(cancelledAttempts, wantAttempts) {
			return state, invalid(event, "cancelled_attempt_ids do not match the pre-event projection")
		}
		for _, id := range wantMovements {
			state.Movements[MovementID(id)] = MovementCancelled
		}
		for _, id := range wantAttempts {
			attempt := state.Attempts[AttemptID(id)]
			attempt.State = AttemptCancelled
			state.Attempts[AttemptID(id)] = attempt
		}
		state.Run = RunCancelled
		if epoch, ok := optionalUint(payload, "fenced_epoch"); ok {
			if epoch <= state.Authority.Epoch {
				return state, invalid(event, "fenced_epoch does not advance authority")
			}
			state.Authority = Authority{Epoch: epoch}
		}
	case EventMovementReady:
		if err := requireMovement(state, event, MovementPending); err != nil {
			return state, err
		}
		state.Movements[event.MovementID] = MovementReady
	case EventMovementStarted:
		if err := requireMovement(state, event, MovementReady); err != nil {
			return state, err
		}
		state.Movements[event.MovementID] = MovementRunning
	case EventPerformerSelected:
		if event.AttemptID == "" || event.MovementID == "" {
			return state, invalid(event, "attempt_id and movement_id are required")
		}
		if _, exists := state.Attempts[event.AttemptID]; exists {
			return state, transition(event, "attempt already exists")
		}
		if state.Movements[event.MovementID] != MovementRunning {
			return state, transition(event, "movement is not RUNNING")
		}
		state.Attempts[event.AttemptID] = Attempt{
			MovementID: event.MovementID,
			State:      AttemptStarting,
		}
	case EventAttemptStarted:
		attempt, err := requireAttempt(state, event, AttemptStarting)
		if err != nil {
			return state, err
		}
		process, err := processIdentity(mustObject(payload, "adapter_process"))
		if err != nil {
			return state, invalid(event, err.Error())
		}
		attempt.State = AttemptRunning
		state.Attempts[event.AttemptID] = attempt
		state.AdapterLaunches[event.AttemptID] = AdapterLaunch{
			AttemptID: event.AttemptID,
			Process:   process,
		}
	case EventPerformerCompleted:
		attempt, err := requireAttempt(state, event, AttemptRunning)
		if err != nil {
			return state, err
		}
		attempt.State = AttemptVerifying
		state.Attempts[event.AttemptID] = attempt
	case EventAttemptFailed:
		attempt, err := requireAttemptOneOf(state, event, AttemptStarting, AttemptRunning, AttemptVerifying)
		if err != nil {
			return state, err
		}
		attempt.State = AttemptFailed
		attempt.Failure = &AttemptFailure{
			Kind:        mustString(payload, "kind"),
			Reason:      stringOrEmpty(payload, "reason"),
			Disposition: disposition(mustObject(payload, "disposition")),
		}
		state.Attempts[event.AttemptID] = attempt
	case EventAcceptanceStarted:
		if _, err := requireAttempt(state, event, AttemptVerifying); err != nil {
			return state, err
		}
		if existing := state.Acceptances[event.AttemptID]; existing.Started {
			return state, transition(event, "acceptance already started")
		}
		state.Acceptances[event.AttemptID] = Acceptance{
			Started:             true,
			SubjectTree:         mustString(payload, "subject_tree"),
			SpecHash:            Hash(mustString(payload, "acceptance_spec_hash")),
			PlannedCriterionIDs: toCriterionIDs(mustStrings(payload, "planned_criterion_ids")),
			Criteria:            make(map[CriterionID]CriterionRecord),
		}
	case EventCriterionStarted:
		if _, err := requireAttempt(state, event, AttemptVerifying); err != nil {
			return state, err
		}
		acceptance := state.Acceptances[event.AttemptID]
		if !acceptance.Started {
			return state, transition(event, "acceptance is not started")
		}
		criterionID := CriterionID(mustString(payload, "criterion_id"))
		if !slices.Contains(acceptance.PlannedCriterionIDs, criterionID) {
			return state, invalid(event, "criterion_id is not in the acceptance plan")
		}
		if mustString(payload, "subject_tree") != acceptance.SubjectTree {
			return state, invalid(event, "criterion subject does not match acceptance")
		}
		if acceptance.Criteria[criterionID].Started {
			return state, transition(event, "criterion already started")
		}
		launch, err := criterionLaunch(payload)
		if err != nil {
			return state, invalid(event, err.Error())
		}
		acceptance.Criteria[criterionID] = CriterionRecord{
			Started:     true,
			SpecHash:    Hash(mustString(payload, "criterion_spec_hash")),
			SubjectTree: mustString(payload, "subject_tree"),
		}
		state.Acceptances[event.AttemptID] = acceptance
		state.CriterionLaunches[CriterionLaunchKey{
			AttemptID:   event.AttemptID,
			CriterionID: criterionID,
		}] = launch
	case EventCriterionCompleted:
		if _, err := requireAttempt(state, event, AttemptVerifying); err != nil {
			return state, err
		}
		criterionID := CriterionID(mustString(payload, "criterion_id"))
		acceptance := state.Acceptances[event.AttemptID]
		record := acceptance.Criteria[criterionID]
		if !record.Started || record.Completed {
			return state, transition(event, "criterion has no open launch")
		}
		if record.SpecHash != Hash(mustString(payload, "criterion_spec_hash")) ||
			record.SubjectTree != mustString(payload, "subject_tree") {
			return state, invalid(event, "criterion completion binding does not match its launch")
		}
		record.Completed = true
		record.Outcome = mustString(payload, "outcome")
		acceptance.Criteria[criterionID] = record
		state.Acceptances[event.AttemptID] = acceptance
	case EventAcceptanceFailed:
		attempt, err := requireAttempt(state, event, AttemptVerifying)
		if err != nil {
			return state, err
		}
		acceptance := state.Acceptances[event.AttemptID]
		if !acceptance.Started || acceptance.SubjectTree != mustString(payload, "subject_tree") {
			return state, invalid(event, "acceptance failure binding does not match its start")
		}
		attempt.State = AttemptFailed
		attempt.Failure = &AttemptFailure{
			Kind:        mustString(payload, "reason"),
			Disposition: disposition(mustObject(payload, "disposition")),
		}
		state.Attempts[event.AttemptID] = attempt
	case EventExecutionStarted:
		if state.OpenExecution != nil {
			return state, transition(event, "execution interval already open")
		}
		state.OpenExecution = &ExecutionInterval{
			ID:               IntervalID(mustString(payload, "interval_id")),
			Phase:            mustString(payload, "phase"),
			WallStart:        mustString(payload, "wall_start"),
			RemainingAtStart: mustInt(payload, "remaining_at_start"),
		}
	case EventExecutionStopped:
		intervalID := IntervalID(mustString(payload, "interval_id"))
		if state.OpenExecution == nil || state.OpenExecution.ID != intervalID {
			return state, transition(event, "matching execution interval is not open")
		}
		state.ConsumedBudgetMS += mustInt(payload, "charged_duration")
		state.OpenExecution = nil
	case EventAmendmentApprovalPrepared:
		if state.PendingPrepare != nil {
			return state, transition(event, "prepare already pending")
		}
		if state.Run == RunNotStarted || state.Run.Terminal() {
			return state, transition(event, "run is not nonterminal")
		}
		baseRevision := mustUint(payload, "base_revision")
		baseHash := Hash(mustString(payload, "base_hash"))
		if state.ScoreHead.Revision != baseRevision || state.ScoreHead.SemanticHash != baseHash {
			return state, transition(event, "prepare base does not match score head")
		}
		if mustUint(payload, "new_revision") != baseRevision+1 {
			return state, invalid(event, "new_revision must immediately follow base_revision")
		}
		targetAttemptIDs := mustStrings(payload, "target_attempt_ids")
		if !slices.Equal(targetAttemptIDs, cancellableAttemptIDs(state)) {
			return state, invalid(event, "target_attempt_ids do not match the pre-event projection")
		}
		identityVersions, encodeErr := json.Marshal(payload["identity_versions"])
		if encodeErr != nil {
			return state, invalid(event, encodeErr.Error())
		}
		state.PendingPrepare = &PendingPrepare{
			ID:            PrepareID(mustString(payload, "prepare_id")),
			ProposalID:    ProposalID(mustString(payload, "proposal_id")),
			Mode:          mustString(payload, "mode"),
			EnvelopeClass: mustString(payload, "envelope_class"),
			BaseHead:      state.ScoreHead,
			NewHead: ScoreHead{
				Revision:     mustUint(payload, "new_revision"),
				SemanticHash: Hash(mustString(payload, "new_snapshot_hash")),
				FileHash:     Hash(mustString(payload, "new_snapshot_file_hash")),
			},
			PlanRecordHash:         Hash(mustString(payload, "plan_record_hash")),
			ObservedAuthorityEpoch: mustUint(payload, "observed_authority_epoch"),
			QuiesceDeadline:        mustString(payload, "quiesce_deadline"),
			TargetAttemptIDs:       toAttemptIDs(targetAttemptIDs),
			ClassifierVersion:      mustUint(payload, "classifier_version"),
			IdentityVersions:       identityVersions,
		}
	case EventAmendmentApprovalAbandoned:
		if state.PendingPrepare == nil ||
			state.PendingPrepare.ID != PrepareID(mustString(payload, "prepare_id")) ||
			state.PendingPrepare.ProposalID != ProposalID(mustString(payload, "proposal_id")) {
			return state, transition(event, "matching prepare is not pending")
		}
		if state.PendingPrepare.BaseHead.Revision != mustUint(payload, "base_revision") ||
			state.PendingPrepare.BaseHead.SemanticHash != Hash(mustString(payload, "base_hash")) {
			return state, invalid(event, "abandoned base does not match pending prepare")
		}
		state.PendingPrepare = nil
	case EventAmendmentApproved:
		if state.PendingPrepare == nil ||
			state.PendingPrepare.ProposalID != ProposalID(mustString(payload, "proposal_id")) {
			return state, transition(event, "matching prepare is not pending")
		}
		if mustUint(payload, "base_revision") != state.PendingPrepare.BaseHead.Revision ||
			Hash(mustString(payload, "base_hash")) != state.PendingPrepare.BaseHead.SemanticHash {
			return state, invalid(event, "approved base does not match pending prepare")
		}
		if mustString(payload, "mode") != state.PendingPrepare.Mode ||
			mustString(payload, "envelope_class") != state.PendingPrepare.EnvelopeClass ||
			mustUint(payload, "classifier_version") != state.PendingPrepare.ClassifierVersion {
			return state, invalid(event, "approved policy binding does not match pending prepare")
		}
		approvedHead := ScoreHead{
			Revision:     mustUint(payload, "new_revision"),
			SemanticHash: Hash(mustString(payload, "new_snapshot_hash")),
			FileHash:     Hash(mustString(payload, "new_snapshot_file_hash")),
		}
		if approvedHead != state.PendingPrepare.NewHead {
			return state, invalid(event, "approved head does not match pending prepare")
		}
		if event.ScoreRevision != approvedHead.Revision {
			return state, invalid(event, "approval envelope revision does not match the new head")
		}
		state.ScoreHead = approvedHead
		supersededAttemptIDs := mustStrings(payload, "superseded_attempt_ids")
		if !slices.Equal(supersededAttemptIDs, cancellableAttemptIDs(state)) {
			return state, invalid(event, "superseded_attempt_ids do not match the pre-event projection")
		}
		for _, id := range supersededAttemptIDs {
			attemptID := AttemptID(id)
			attempt, ok := state.Attempts[attemptID]
			if !ok || attempt.State.terminal() {
				return state, invalid(event, "superseded_attempt_ids contains a non-live attempt")
			}
			attempt.State = AttemptSuperseded
			state.Attempts[attemptID] = attempt
		}
		if epoch, ok := optionalUint(payload, "fenced_epoch"); ok {
			if epoch <= state.Authority.Epoch {
				return state, invalid(event, "fenced_epoch does not advance authority")
			}
			state.Authority = Authority{Epoch: epoch}
		}
		state.PendingPrepare = nil
	case EventAuthorityGranted:
		epoch := mustUint(payload, "authority_epoch")
		if state.Run == RunNotStarted || state.Run.Terminal() || epoch <= state.Authority.Epoch {
			return state, transition(event, "authority epoch cannot be granted")
		}
		start, err := startIdentity(mustObject(payload, "owner_start_identity"))
		if err != nil {
			return state, invalid(event, err.Error())
		}
		ownerPID := mustInt(payload, "owner_pid")
		if ownerPID <= 0 {
			return state, invalid(event, "owner_pid must be positive")
		}
		state.Authority = Authority{
			Epoch: epoch,
			Owner: &AuthorityOwner{PID: int(ownerPID), Start: start},
		}
	case EventCancelRequested:
		if state.Run == RunNotStarted || state.Run.Terminal() || state.CancelRequested {
			return state, transition(event, "cancellation cannot be requested")
		}
		state.CancelRequested = true
	case EventJournalTailTruncated:
		if mustUint(payload, "truncated_seq") != event.Seq {
			return state, invalid(event, "truncated_seq must equal the repair event sequence")
		}
		// Audit event; no state effect.
	default:
		if isRegistryEvent(event.Type) {
			return state, fmt.Errorf("%w: %s", ErrUnsupportedEventType, event.Type)
		}
		return state, invalid(event, "event type is not in the registry")
	}
	return state, nil
}

func transition(event Event, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrIllegalTransition, event.Type, reason)
}

func invalid(event Event, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalidEvent, event.Type, reason)
}

func requireMovement(state State, event Event, want MovementState) error {
	if event.MovementID == "" {
		return invalid(event, "movement_id is required")
	}
	if state.Movements[event.MovementID] != want {
		return transition(event, fmt.Sprintf("movement is not %s", want))
	}
	return nil
}

func requireAttempt(state State, event Event, want AttemptState) (Attempt, error) {
	return requireAttemptOneOf(state, event, want)
}

func requireAttemptOneOf(state State, event Event, wants ...AttemptState) (Attempt, error) {
	if event.AttemptID == "" {
		return Attempt{}, invalid(event, "attempt_id is required")
	}
	attempt, ok := state.Attempts[event.AttemptID]
	if !ok || !slices.Contains(wants, attempt.State) {
		return Attempt{}, transition(event, "attempt is not in a legal source state")
	}
	return attempt, nil
}

func cancellableMovementIDs(state State) []string {
	var ids []string
	for id, movementState := range state.Movements {
		if movementState == MovementPending || movementState == MovementReady || movementState == MovementRunning {
			ids = append(ids, string(id))
		}
	}
	slices.Sort(ids)
	return ids
}

func cancellableAttemptIDs(state State) []string {
	var ids []string
	for id, attempt := range state.Attempts {
		if !attempt.State.terminal() {
			ids = append(ids, string(id))
		}
	}
	slices.Sort(ids)
	return ids
}

func toAttemptIDs(ids []string) []AttemptID {
	output := make([]AttemptID, len(ids))
	for index, id := range ids {
		output[index] = AttemptID(id)
	}
	return output
}

func toCriterionIDs(ids []string) []CriterionID {
	output := make([]CriterionID, len(ids))
	for index, id := range ids {
		output[index] = CriterionID(id)
	}
	return output
}

func disposition(value map[string]any) Disposition {
	return Disposition{
		Charged:          mustString(value, "charged"),
		MovementTerminal: mustBool(value, "movement_terminal"),
	}
}

func criterionLaunch(payload map[string]any) (CriterionLaunch, error) {
	processValue, hasProcess := payload["criterion_process"]
	spawnFailedValue, hasSpawnFailed := payload["spawn_failed"]
	switch {
	case hasProcess && hasSpawnFailed:
		return nil, errors.New("criterion_process and spawn_failed are mutually exclusive")
	case hasProcess:
		object, ok := processValue.(map[string]any)
		if !ok {
			return nil, errors.New("criterion_process must be an object")
		}
		process, err := processIdentity(object)
		if err != nil {
			return nil, err
		}
		return SpawnedCriterionLaunch{Process: process}, nil
	case hasSpawnFailed:
		value, ok := spawnFailedValue.(bool)
		if !ok || !value {
			return nil, errors.New("spawn_failed must be true")
		}
		return SpawnFailedCriterionLaunch{}, nil
	default:
		return InProcessCriterionLaunch{}, nil
	}
}

func processIdentity(value map[string]any) (ProcessIdentity, error) {
	if err := fields(value, []string{"pid", "session_id", "start_identity"}, nil); err != nil {
		return ProcessIdentity{}, err
	}
	start, err := startIdentity(mustObject(value, "start_identity"))
	if err != nil {
		return ProcessIdentity{}, err
	}
	pid := mustInt(value, "pid")
	sessionID := mustInt(value, "session_id")
	if pid <= 0 || sessionID <= 0 {
		return ProcessIdentity{}, errors.New("pid and session_id must be positive")
	}
	return ProcessIdentity{
		PID:       int(pid),
		SessionID: int(sessionID),
		Start:     start,
	}, nil
}

func startIdentity(value map[string]any) (StartIdentity, error) {
	platform, ok := value["platform"].(string)
	if !ok {
		return nil, errors.New("start identity platform is required")
	}
	switch platform {
	case "linux":
		if err := fields(value, []string{"platform", "boot_id", "start_ticks"}, nil); err != nil {
			return nil, err
		}
		bootID := mustString(value, "boot_id")
		startTicks := mustString(value, "start_ticks")
		if bootID == "" || !validUintString(startTicks) {
			return nil, errors.New("Linux start identity fields must be non-empty")
		}
		return LinuxStartIdentity{BootID: bootID, StartTicks: startTicks}, nil
	case "darwin":
		if err := fields(value, []string{"platform", "start_tvsec", "start_tvusec"}, nil); err != nil {
			return nil, err
		}
		if err := namedTypes(value, []string{"platform"}, nil, nil, nil, []string{"start_tvsec", "start_tvusec"}); err != nil {
			return nil, err
		}
		useconds := mustUint(value, "start_tvusec")
		if useconds >= 1_000_000 {
			return nil, errors.New("Darwin start_tvusec is out of range")
		}
		return DarwinStartIdentity{StartTVSec: mustUint(value, "start_tvsec"), StartTVUsec: useconds}, nil
	default:
		return nil, fmt.Errorf("unsupported start identity platform %q", platform)
	}
}

func validatePayload(event Event) (map[string]any, error) {
	value, err := parseJSONExact(event.Payload)
	if err != nil {
		return nil, invalid(event, err.Error())
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return nil, invalid(event, "payload must be an object")
	}
	required, optional, known := payloadFields(event.Type)
	if !known {
		if isRegistryEvent(event.Type) {
			return nil, fmt.Errorf("%w: %s", ErrUnsupportedEventType, event.Type)
		}
		return nil, invalid(event, "event type is not in the registry")
	}
	if err := fields(payload, required, optional); err != nil {
		return nil, invalid(event, err.Error())
	}
	if err := validatePayloadTypes(event.Type, payload); err != nil {
		return nil, invalid(event, err.Error())
	}
	if err := validateNestedPayload(event.Type, payload); err != nil {
		return nil, invalid(event, err.Error())
	}
	if err := validatePayloadValues(event.Type, payload); err != nil {
		return nil, invalid(event, err.Error())
	}
	return payload, nil
}

func payloadFields(eventType EventType) (required, optional []string, known bool) {
	switch eventType {
	case EventRunStarted:
		return []string{"base_commit", "base_tree", "score_hash", "score_file_hash", "resolved_cast_hash", "identity_versions"}, nil, true
	case EventRunSucceeded:
		return []string{"candidate", "waiver", "identity_versions"}, nil, true
	case EventRunFailed:
		return []string{"reason"}, nil, true
	case EventRunCancelled:
		return []string{"cancelled_movement_ids", "cancelled_attempt_ids", "obsoleted_decision_ids"}, []string{"fenced_epoch"}, true
	case EventMovementReady, EventMovementStarted:
		return nil, nil, true
	case EventPerformerSelected:
		return []string{"reason", "performer_id", "adapter_id", "adapter_version", "model", "enforcement", "negotiated_features", "withheld_resolutions", "truncated_resolutions", "advisory_dimensions"}, nil, true
	case EventAttemptStarted:
		return []string{"attempt_number", "execution_dependency_hash", "adapter_process", "granted_authority", "identity_versions"}, []string{"base_composition_hash"}, true
	case EventPerformerCompleted:
		return []string{"session_hint_stored"}, nil, true
	case EventAttemptFailed:
		return []string{"kind", "disposition"}, []string{"reason", "detail"}, true
	case EventAcceptanceStarted:
		return []string{"subject_tree", "acceptance_spec_hash", "planned_criterion_ids", "identity_versions"}, nil, true
	case EventCriterionStarted:
		return []string{"criterion_id", "criterion_spec_hash", "subject_tree", "identity_versions"}, []string{"criterion_process", "spawn_failed"}, true
	case EventCriterionCompleted:
		return []string{"criterion_id", "criterion_spec_hash", "subject_tree", "outcome", "identity_versions"}, []string{"exit_code", "duration_ms", "output_ref", "error_detail"}, true
	case EventAcceptanceFailed:
		return []string{"reason", "subject_tree", "disposition"}, []string{"failed_criterion_id"}, true
	case EventExecutionStarted:
		return []string{"interval_id", "phase", "wall_start", "remaining_at_start"}, nil, true
	case EventExecutionStopped:
		return []string{"interval_id", "reason", "charging", "charged_duration"}, []string{"observed_at"}, true
	case EventAmendmentApprovalPrepared:
		return []string{"prepare_id", "proposal_id", "mode", "envelope_class", "base_revision", "base_hash", "new_revision", "new_snapshot_hash", "new_snapshot_file_hash", "plan_record_hash", "target_attempt_ids", "observed_authority_epoch", "quiesce_deadline", "classifier_version", "identity_versions"}, nil, true
	case EventAmendmentApprovalAbandoned:
		return []string{"prepare_id", "proposal_id", "reason", "base_revision", "base_hash", "classifier_version"}, nil, true
	case EventAmendmentApproved:
		return []string{"proposal_id", "mode", "envelope_class", "base_revision", "base_hash", "classifier_version", "new_revision", "new_snapshot_hash", "new_snapshot_file_hash", "typed_delta", "actual_impact", "superseded_attempt_ids", "obsoleted_decision_ids", "finalization", "identity_versions"}, []string{"emitted_id", "candidate_id", "fenced_epoch"}, true
	case EventAuthorityGranted:
		return []string{"authority_epoch", "owner_pid", "owner_start_identity"}, []string{"reclaimed_from_epoch"}, true
	case EventCancelRequested:
		return []string{"requested_by"}, nil, true
	case EventJournalTailTruncated:
		return []string{"truncated_seq", "discarded_bytes"}, nil, true
	default:
		return nil, nil, false
	}
}

func validateNestedPayload(eventType EventType, payload map[string]any) error {
	if versions, ok := payload["identity_versions"].(map[string]any); ok {
		if err := validateIdentityVersions(versions); err != nil {
			return err
		}
	}
	switch eventType {
	case EventRunSucceeded:
		candidate := mustObject(payload, "candidate")
		if err := fields(candidate,
			[]string{"candidate_id", "base_tree", "result_tree", "ordered_change_sets", "contributors", "candidate_composition_dependency_hash"},
			nil,
		); err != nil {
			return fmt.Errorf("candidate: %w", err)
		}
		if err := namedTypes(
			candidate,
			[]string{"candidate_id", "base_tree", "result_tree", "candidate_composition_dependency_hash"},
			nil,
			[]string{"ordered_change_sets", "contributors"},
			nil,
			nil,
		); err != nil {
			return fmt.Errorf("candidate: %w", err)
		}
		if err := stringArray(candidate, "ordered_change_sets"); err != nil {
			return fmt.Errorf("candidate: %w", err)
		}
		if err := validateContributors(candidate["contributors"].([]any)); err != nil {
			return fmt.Errorf("candidate: %w", err)
		}
		waiver := mustObject(payload, "waiver")
		if err := fields(waiver, []string{"reason"}, nil); err != nil {
			return fmt.Errorf("waiver: %w", err)
		}
		if _, ok := waiver["reason"].(string); !ok {
			return errors.New("waiver.reason must be a string")
		}
	case EventPerformerSelected:
		enforcement := mustObject(payload, "enforcement")
		names := []string{"path_grants", "read_only", "network_grants", "shell_grants", "read_grants"}
		if err := fields(enforcement, names, nil); err != nil {
			return fmt.Errorf("enforcement: %w", err)
		}
		if err := namedTypes(enforcement, nil, nil, nil, names, nil); err != nil {
			return fmt.Errorf("enforcement: %w", err)
		}
		for _, raw := range payload["withheld_resolutions"].([]any) {
			entry, ok := raw.(map[string]any)
			if !ok {
				return errors.New("withheld_resolutions must contain objects")
			}
			if err := fields(entry, []string{"decision_id", "why"}, nil); err != nil {
				return fmt.Errorf("withheld_resolutions: %w", err)
			}
			if err := namedTypes(entry, []string{"decision_id", "why"}, nil, nil, nil, nil); err != nil {
				return fmt.Errorf("withheld_resolutions: %w", err)
			}
		}
	case EventAttemptStarted:
		grants := mustObject(payload, "granted_authority")
		if err := fields(grants, []string{"paths_rw", "paths_ro", "shell", "network"}, nil); err != nil {
			return fmt.Errorf("granted_authority: %w", err)
		}
		if err := namedTypes(grants, nil, nil, []string{"paths_rw", "paths_ro"}, []string{"shell", "network"}, nil); err != nil {
			return fmt.Errorf("granted_authority: %w", err)
		}
		if err := stringArray(grants, "paths_rw"); err != nil {
			return fmt.Errorf("granted_authority: %w", err)
		}
		if err := stringArray(grants, "paths_ro"); err != nil {
			return fmt.Errorf("granted_authority: %w", err)
		}
	case EventAmendmentApproved:
		if err := validateTypedDelta(payload["typed_delta"].([]any)); err != nil {
			return err
		}
		if err := validateActualImpact(mustObject(payload, "actual_impact")); err != nil {
			return err
		}
	}
	return nil
}

func validateIdentityVersions(value map[string]any) error {
	if err := fields(value, []string{"canonical_encoding", "projections"}, []string{"classifier", "composition"}); err != nil {
		return fmt.Errorf("identity_versions: %w", err)
	}
	if err := namedTypes(
		value,
		nil,
		[]string{"projections"},
		nil,
		nil,
		append([]string{"canonical_encoding"}, optionalNames(value, "classifier", "composition")...),
	); err != nil {
		return fmt.Errorf("identity_versions: %w", err)
	}
	for domain, raw := range mustObject(value, "projections") {
		number, ok := raw.(float64)
		if !ok || number < 0 || number > 1<<53-1 || number != float64(uint64(number)) {
			return fmt.Errorf("identity_versions.projections.%s must be a non-negative safe integer", domain)
		}
	}
	return nil
}

func validateContributors(values []any) error {
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok {
			return errors.New("contributors must contain objects")
		}
		if err := fields(entry, []string{"movement_id", "change_set_id"}, nil); err != nil {
			return err
		}
		if err := namedTypes(entry, []string{"movement_id", "change_set_id"}, nil, nil, nil, nil); err != nil {
			return err
		}
	}
	return nil
}

func validateTypedDelta(values []any) error {
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok {
			return errors.New("typed_delta must contain objects")
		}
		if err := fields(entry, []string{"selector", "operation"}, []string{"before_hash", "after_hash"}); err != nil {
			return fmt.Errorf("typed_delta: %w", err)
		}
		names := append([]string{"selector", "operation"}, optionalNames(entry, "before_hash", "after_hash")...)
		if err := namedTypes(entry, names, nil, nil, nil, nil); err != nil {
			return fmt.Errorf("typed_delta: %w", err)
		}
	}
	return nil
}

func validateActualImpact(value map[string]any) error {
	if err := fields(value, []string{"score_changes", "authority", "budget"}, nil); err != nil {
		return fmt.Errorf("actual_impact: %w", err)
	}
	if err := namedTypes(value, nil, []string{"authority", "budget"}, []string{"score_changes"}, nil, nil); err != nil {
		return fmt.Errorf("actual_impact: %w", err)
	}
	if err := validateTypedDelta(value["score_changes"].([]any)); err != nil {
		return fmt.Errorf("actual_impact.score_changes: %w", err)
	}
	authority := mustObject(value, "authority")
	if err := fields(authority, []string{"allowed_paths", "grants", "side_effects"}, nil); err != nil {
		return fmt.Errorf("actual_impact.authority: %w", err)
	}
	for _, name := range []string{"allowed_paths", "side_effects"} {
		change := mustObject(authority, name)
		if err := fields(change, []string{"added", "removed"}, nil); err != nil {
			return fmt.Errorf("actual_impact.authority.%s: %w", name, err)
		}
		if err := namedTypes(change, nil, nil, []string{"added", "removed"}, nil, nil); err != nil {
			return fmt.Errorf("actual_impact.authority.%s: %w", name, err)
		}
		if err := stringArray(change, "added"); err != nil {
			return err
		}
		if err := stringArray(change, "removed"); err != nil {
			return err
		}
	}
	grants, ok := authority["grants"].([]any)
	if !ok {
		return errors.New("actual_impact.authority.grants must be an array")
	}
	for _, raw := range grants {
		entry, ok := raw.(map[string]any)
		if !ok {
			return errors.New("actual_impact.authority.grants must contain objects")
		}
		if err := fields(entry, []string{"movement_id", "added", "removed"}, nil); err != nil {
			return err
		}
		if err := namedTypes(entry, []string{"movement_id"}, nil, []string{"added", "removed"}, nil, nil); err != nil {
			return err
		}
		if err := stringArray(entry, "added"); err != nil {
			return err
		}
		if err := stringArray(entry, "removed"); err != nil {
			return err
		}
	}
	budget := mustObject(value, "budget")
	if err := fields(budget, nil, []string{"active_wall_clock_min", "retries_per_movement"}); err != nil {
		return fmt.Errorf("actual_impact.budget: %w", err)
	}
	for _, name := range optionalNames(budget, "active_wall_clock_min", "retries_per_movement") {
		change := mustObject(budget, name)
		if err := fields(change, []string{"from", "to"}, nil); err != nil {
			return fmt.Errorf("actual_impact.budget.%s: %w", name, err)
		}
		if err := namedTypes(change, nil, nil, nil, nil, []string{"from", "to"}); err != nil {
			return fmt.Errorf("actual_impact.budget.%s: %w", name, err)
		}
	}
	return nil
}

func validatePayloadTypes(eventType EventType, payload map[string]any) error {
	var strings, objects, arrays, bools, integers []string
	switch eventType {
	case EventRunStarted:
		strings = []string{"base_commit", "base_tree", "score_hash", "score_file_hash", "resolved_cast_hash"}
		objects = []string{"identity_versions"}
	case EventRunSucceeded:
		objects = []string{"candidate", "waiver", "identity_versions"}
	case EventRunFailed:
		strings = []string{"reason"}
	case EventRunCancelled:
		arrays = []string{"cancelled_movement_ids", "cancelled_attempt_ids", "obsoleted_decision_ids"}
		integers = optionalNames(payload, "fenced_epoch")
	case EventPerformerSelected:
		strings = []string{"reason", "performer_id", "adapter_id", "adapter_version", "model"}
		objects = []string{"enforcement"}
		arrays = []string{"negotiated_features", "withheld_resolutions", "truncated_resolutions", "advisory_dimensions"}
	case EventAttemptStarted:
		strings = append([]string{"execution_dependency_hash"}, optionalNames(payload, "base_composition_hash")...)
		objects = []string{"adapter_process", "granted_authority", "identity_versions"}
		integers = []string{"attempt_number"}
	case EventPerformerCompleted:
		bools = []string{"session_hint_stored"}
	case EventAttemptFailed:
		strings = append([]string{"kind"}, optionalNames(payload, "reason", "detail")...)
		objects = []string{"disposition"}
	case EventAcceptanceStarted:
		strings = []string{"subject_tree", "acceptance_spec_hash"}
		arrays = []string{"planned_criterion_ids"}
		objects = []string{"identity_versions"}
	case EventCriterionStarted:
		strings = []string{"criterion_id", "criterion_spec_hash", "subject_tree"}
		objects = append([]string{"identity_versions"}, optionalNames(payload, "criterion_process")...)
		bools = optionalNames(payload, "spawn_failed")
	case EventCriterionCompleted:
		strings = append([]string{"criterion_id", "criterion_spec_hash", "subject_tree", "outcome"}, optionalNames(payload, "output_ref", "error_detail")...)
		objects = []string{"identity_versions"}
		integers = optionalNames(payload, "exit_code", "duration_ms")
	case EventAcceptanceFailed:
		strings = append([]string{"reason", "subject_tree"}, optionalNames(payload, "failed_criterion_id")...)
		objects = []string{"disposition"}
	case EventExecutionStarted:
		strings = []string{"interval_id", "phase", "wall_start"}
		integers = []string{"remaining_at_start"}
	case EventExecutionStopped:
		strings = append([]string{"interval_id", "reason", "charging"}, optionalNames(payload, "observed_at")...)
		integers = []string{"charged_duration"}
	case EventAmendmentApprovalPrepared:
		strings = []string{"prepare_id", "proposal_id", "mode", "envelope_class", "base_hash", "new_snapshot_hash", "new_snapshot_file_hash", "plan_record_hash", "quiesce_deadline"}
		arrays = []string{"target_attempt_ids"}
		objects = []string{"identity_versions"}
		integers = []string{"base_revision", "new_revision", "observed_authority_epoch", "classifier_version"}
	case EventAmendmentApprovalAbandoned:
		strings = []string{"prepare_id", "proposal_id", "reason", "base_hash"}
		integers = []string{"base_revision", "classifier_version"}
	case EventAmendmentApproved:
		strings = append([]string{"proposal_id", "mode", "envelope_class", "base_hash", "new_snapshot_hash", "new_snapshot_file_hash"}, optionalNames(payload, "emitted_id", "candidate_id")...)
		arrays = []string{"typed_delta", "superseded_attempt_ids", "obsoleted_decision_ids"}
		objects = []string{"actual_impact", "identity_versions"}
		bools = []string{"finalization"}
		integers = append([]string{"base_revision", "classifier_version", "new_revision"}, optionalNames(payload, "fenced_epoch")...)
	case EventAuthorityGranted:
		objects = []string{"owner_start_identity"}
		integers = append([]string{"authority_epoch", "owner_pid"}, optionalNames(payload, "reclaimed_from_epoch")...)
	case EventCancelRequested:
		strings = []string{"requested_by"}
	case EventJournalTailTruncated:
		integers = []string{"truncated_seq", "discarded_bytes"}
	}
	if err := namedTypes(payload, strings, objects, arrays, bools, integers); err != nil {
		return err
	}
	for _, name := range arrays {
		if name == "typed_delta" || name == "withheld_resolutions" {
			continue
		}
		if err := stringArray(payload, name); err != nil {
			return err
		}
	}
	if dispositionValue, ok := payload["disposition"].(map[string]any); ok {
		if err := fields(dispositionValue, []string{"charged", "movement_terminal"}, nil); err != nil {
			return fmt.Errorf("disposition: %w", err)
		}
		if _, ok := dispositionValue["charged"].(string); !ok {
			return errors.New("disposition.charged must be a string")
		}
		if _, ok := dispositionValue["movement_terminal"].(bool); !ok {
			return errors.New("disposition.movement_terminal must be a boolean")
		}
		charged := mustString(dispositionValue, "charged")
		if charged != "quality_retry" && charged != "fallback" && charged != "none" {
			return errors.New("disposition.charged is invalid")
		}
	}
	return nil
}

func optionalNames(value map[string]any, names ...string) []string {
	var present []string
	for _, name := range names {
		if _, ok := value[name]; ok {
			present = append(present, name)
		}
	}
	return present
}

func namedTypes(value map[string]any, strings, objects, arrays, bools, integers []string) error {
	for _, name := range strings {
		if _, ok := value[name].(string); !ok {
			return fmt.Errorf("%s must be a string", name)
		}
	}
	for _, name := range objects {
		if _, ok := value[name].(map[string]any); !ok {
			return fmt.Errorf("%s must be an object", name)
		}
	}
	for _, name := range arrays {
		if _, ok := value[name].([]any); !ok {
			return fmt.Errorf("%s must be an array", name)
		}
	}
	for _, name := range bools {
		if _, ok := value[name].(bool); !ok {
			return fmt.Errorf("%s must be a boolean", name)
		}
	}
	for _, name := range integers {
		number, ok := value[name].(float64)
		if !ok || number < 0 || number > 1<<53-1 || number != float64(uint64(number)) {
			return fmt.Errorf("%s must be a non-negative safe integer", name)
		}
	}
	return nil
}

func stringArray(value map[string]any, name string) error {
	for _, item := range value[name].([]any) {
		if _, ok := item.(string); !ok {
			return fmt.Errorf("%s must contain only strings", name)
		}
	}
	return nil
}

func validatePayloadValues(eventType EventType, payload map[string]any) error {
	switch eventType {
	case EventRunCancelled:
		return sortedStringFields(payload, "cancelled_movement_ids", "cancelled_attempt_ids", "obsoleted_decision_ids")
	case EventAmendmentApprovalPrepared:
		if mustString(payload, "mode") != "auto" {
			return errors.New("only auto amendment prepares are supported")
		}
		if !validEnvelopeClass(mustString(payload, "envelope_class")) {
			return errors.New("invalid auto envelope_class")
		}
		return sortedStringFields(payload, "target_attempt_ids")
	case EventAmendmentApproved:
		if mustString(payload, "mode") != "auto" {
			return errors.New("only auto amendment approvals are supported")
		}
		if !validEnvelopeClass(mustString(payload, "envelope_class")) {
			return errors.New("invalid auto envelope_class")
		}
		if mustBool(payload, "finalization") {
			return errors.New("auto finalization is outside the supported projector")
		}
		return sortedStringFields(payload, "superseded_attempt_ids", "obsoleted_decision_ids")
	case EventExecutionStarted:
		if mustInt(payload, "remaining_at_start") < 0 {
			return errors.New("remaining_at_start must be non-negative")
		}
	case EventExecutionStopped:
		if mustInt(payload, "charged_duration") < 0 {
			return errors.New("charged_duration must be non-negative")
		}
		charging := mustString(payload, "charging")
		_, observed := payload["observed_at"]
		if (charging == "clamped") != observed {
			return errors.New("observed_at is required iff charging is clamped")
		}
	case EventCriterionCompleted:
		outcome := mustString(payload, "outcome")
		if outcome != "PASS" && outcome != "FAIL" && outcome != "ERROR" {
			return errors.New("invalid criterion outcome")
		}
		_, detail := payload["error_detail"]
		if (outcome == "ERROR") != detail {
			return errors.New("error_detail is required iff outcome is ERROR")
		}
	case EventCancelRequested:
		if mustString(payload, "requested_by") != "cli" {
			return errors.New("requested_by must be cli")
		}
	}
	return nil
}

func fields(value map[string]any, required, optional []string) error {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
		if _, ok := value[name]; !ok {
			return fmt.Errorf("required field %q is absent", name)
		}
	}
	for _, name := range optional {
		allowed[name] = true
	}
	for name := range value {
		if !allowed[name] {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}

func sortedStringFields(payload map[string]any, names ...string) error {
	for _, name := range names {
		values := mustStrings(payload, name)
		if !slices.IsSorted(values) {
			return fmt.Errorf("%s must be sorted", name)
		}
		for index := 1; index < len(values); index++ {
			if values[index] == values[index-1] {
				return fmt.Errorf("%s must not contain duplicates", name)
			}
		}
	}
	return nil
}

func mustObject(value map[string]any, name string) map[string]any {
	object, _ := value[name].(map[string]any)
	return object
}

func mustString(value map[string]any, name string) string {
	result, _ := value[name].(string)
	return result
}

func stringOrEmpty(value map[string]any, name string) string {
	result, _ := value[name].(string)
	return result
}

func mustStrings(value map[string]any, name string) []string {
	raw, _ := value[name].([]any)
	result := make([]string, len(raw))
	for index, item := range raw {
		result[index], _ = item.(string)
	}
	return result
}

func mustBool(value map[string]any, name string) bool {
	result, _ := value[name].(bool)
	return result
}

func mustInt(value map[string]any, name string) int64 {
	number, _ := value[name].(float64)
	return int64(number)
}

func mustUint(value map[string]any, name string) uint64 {
	return uint64(mustInt(value, name))
}

func optionalUint(value map[string]any, name string) (uint64, bool) {
	raw, ok := value[name]
	if !ok {
		return 0, false
	}
	number, _ := raw.(float64)
	return uint64(number), true
}

func validEnvelopeClass(value string) bool {
	return value == "NARROW_PATHS" || value == "NARROW_GRANTS" || value == "BUDGET_DECREASE"
}

var registryEvents = map[EventType]bool{
	"run.started": true, "run.succeeded": true, "run.failed": true, "run.cancelled": true,
	"movement.ready": true, "movement.started": true, "movement.succeeded": true, "movement.failed": true,
	"movement.cancelled": true, "performer.selected": true, "attempt.started": true,
	"performer.completed": true, "attempt.completed": true, "attempt.blocked": true,
	"attempt.failed": true, "attempt.cancelled": true, "attempt.superseded": true,
	"execution.started": true, "execution.stopped": true, "artifact.recorded": true,
	"change_set.recorded": true, "verification.passed": true, "composition.conflicted": true,
	"application_candidate.recorded": true, "acceptance.started": true, "criterion.started": true,
	"criterion.completed": true, "acceptance.failed": true, "acceptance.evaluation_completed": true,
	"decision.requested": true, "decision.resolved": true, "decision.obsoleted": true,
	"amendment.rejected": true, "amendment.approval_prepared": true,
	"amendment.approval_abandoned": true, "amendment.routed_human": true,
	"amendment.approved": true, "amendment.human_rejected": true,
	"apply.started": true, "apply.completed": true, "apply.failed": true,
	"apply.recovery_required": true, "apply.recovery_resolved": true,
	"score.promotion_started": true, "score.promoted": true,
	"score.promotion_recovery_required": true, "authority.granted": true,
	"cancel.requested": true, "journal.tail_truncated": true, "log": true, "progress": true,
}

func isRegistryEvent(eventType EventType) bool {
	return registryEvents[eventType]
}

func isSupportedEvent(eventType EventType) bool {
	_, _, known := payloadFields(eventType)
	return known
}
