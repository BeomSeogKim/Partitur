package runstate

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestApplyDoesNotAliasInputOnSuccessOrError(t *testing.T) {
	input := NewState([]MovementSeed{{ID: "m1", Initial: MovementPending}})
	input.Acceptances["a1"] = Acceptance{
		Started:             true,
		PlannedCriterionIDs: []CriterionID{"c1"},
		Criteria:            map[CriterionID]CriterionRecord{"c1": {Started: true}},
	}
	input.PendingPrepare = &PendingPrepare{
		ID:               "p1",
		TargetAttemptIDs: []AttemptID{"a1"},
		IdentityVersions: json.RawMessage(`{"x":1}`),
	}

	success, err := Apply(input, fixtureEvent(EventMovementReady, map[string]any{}, func(event *Event) {
		event.MovementID = "m1"
	}))
	if err != nil {
		t.Fatal(err)
	}
	success.Movements["m2"] = MovementReady
	success.Acceptances["a1"].Criteria["c2"] = CriterionRecord{Started: true}
	acceptance := success.Acceptances["a1"]
	acceptance.PlannedCriterionIDs[0] = "changed"
	success.Acceptances["a1"] = acceptance
	success.PendingPrepare.TargetAttemptIDs[0] = "changed"
	success.PendingPrepare.IdentityVersions[0] = '['
	if _, ok := input.Movements["m2"]; ok {
		t.Fatal("success result aliases movement map")
	}
	if _, ok := input.Acceptances["a1"].Criteria["c2"]; ok {
		t.Fatal("success result aliases nested criterion map")
	}
	if input.Acceptances["a1"].PlannedCriterionIDs[0] != "c1" {
		t.Fatal("success result aliases planned criterion slice")
	}
	if input.PendingPrepare.TargetAttemptIDs[0] != "a1" ||
		string(input.PendingPrepare.IdentityVersions) != `{"x":1}` {
		t.Fatal("success result aliases pending prepare slices")
	}

	failed, err := Apply(input, fixtureEvent(EventMovementStarted, map[string]any{}, func(event *Event) {
		event.MovementID = "m1"
	}))
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("error = %v, want ErrIllegalTransition", err)
	}
	failed.Movements["m2"] = MovementReady
	failed.Acceptances["a1"].Criteria["c2"] = CriterionRecord{Started: true}
	if _, ok := input.Movements["m2"]; ok {
		t.Fatal("error result aliases movement map")
	}
	if _, ok := input.Acceptances["a1"].Criteria["c2"]; ok {
		t.Fatal("error result aliases nested criterion map")
	}
}

func TestUnsupportedRegistryEventFailsDistinctly(t *testing.T) {
	state := NewState(nil)
	_, err := Apply(state, fixtureEvent("change_set.recorded", map[string]any{}, nil))
	if !errors.Is(err, ErrUnsupportedEventType) {
		t.Fatalf("error = %v, want ErrUnsupportedEventType", err)
	}
	if errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("unsupported valid registry type was classified as invalid: %v", err)
	}
}

func TestEScopedSupportedEventSetHasFortyOneTypes(t *testing.T) {
	var count int
	for eventType := range registryEvents {
		if isSupportedEvent(eventType) {
			count++
		}
	}
	if count != 41 {
		t.Fatalf("supported event count = %d, want 41", count)
	}
	for _, eventType := range []EventType{EventMovementCancelled, EventAttemptCancelled, EventAttemptSuperseded} {
		if isSupportedEvent(eventType) {
			t.Fatalf("derived %s must not be accepted as an authoritative event", eventType)
		}
	}
}

func TestObservationsValidateWithoutChangingProjection(t *testing.T) {
	state := runningAttemptState(t)
	for _, event := range []Event{
		fixtureEvent(EventLog, map[string]any{
			"level": "info", "message": "one",
		}, attemptEnvelope),
		fixtureEvent(EventProgress, map[string]any{
			"message": "halfway",
		}, attemptEnvelope),
	} {
		next, err := Apply(state, event)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(next, state) {
			t.Fatalf("%s changed projection", event.Type)
		}
		key, err := IdempotencyKey(event)
		if err != nil || key != "" {
			t.Fatalf("%s key=%q error=%v", event.Type, key, err)
		}
	}
}

func TestAttemptFailedStopsAtAttemptAndPreservesDisposition(t *testing.T) {
	state := runningAttemptState(t)
	event := fixtureEvent(EventAttemptFailed, map[string]any{
		"kind": "task_failed",
		"disposition": map[string]any{
			"charged":           "none",
			"movement_terminal": true,
		},
	}, func(event *Event) {
		event.MovementID = "m1"
		event.AttemptID = "a1"
	})
	next, err := Apply(state, event)
	if err != nil {
		t.Fatal(err)
	}
	if next.Attempts["a1"].State != AttemptFailed {
		t.Fatalf("attempt state = %s", next.Attempts["a1"].State)
	}
	if next.Movements["m1"] != MovementRunning || next.Run != RunRunning {
		t.Fatalf("attempt failure cascaded: movement=%s run=%s", next.Movements["m1"], next.Run)
	}
	if got := next.Attempts["a1"].Failure.Disposition; got.Charged != "none" || !got.MovementTerminal {
		t.Fatalf("disposition = %+v", got)
	}
}

func TestFailureAndBlockingEventContracts(t *testing.T) {
	tests := []struct {
		name         string
		state        func(*testing.T) State
		event        Event
		want         func(State) bool
		illegalState func(*testing.T) State
		invalid      Event
		key          string
	}{
		{
			name:  "attempt blocked",
			state: probedAttemptState,
			event: fixtureEvent(EventAttemptBlocked, blockedPayload(), attemptEnvelope),
			want: func(state State) bool {
				return state.Attempts["a1"].State == AttemptBlocked
			},
			illegalState: runningAttemptState,
			invalid: fixtureEvent(EventAttemptBlocked, map[string]any{
				"raised": []any{map[string]any{
					"decision_id": "d1", "emitted_id": "q1", "kind": "question", "question": "Continue?", "blocking": true,
				}},
				"pending_decision_ids": []any{},
			}, attemptEnvelope),
			key: "a1",
		},
		{
			name:  "movement failed",
			state: runningAttemptState,
			event: fixtureEvent(EventMovementFailed, map[string]any{
				"reason": "retries_exhausted", "run_failed": false,
			}, func(event *Event) { event.MovementID = "m1" }),
			want: func(state State) bool {
				return state.Movements["m1"] == MovementFailed && state.Run == RunRunning &&
					state.Attempts["a1"].State == AttemptRunning
			},
			illegalState: func(t *testing.T) State {
				state := runningAttemptState(t)
				state.Movements["m1"] = MovementReady
				return state
			},
			invalid: fixtureEvent(EventMovementFailed, map[string]any{
				"reason": "unknown", "run_failed": false,
			}, func(event *Event) { event.MovementID = "m1" }),
			key: "m1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next, err := Apply(test.state(t), test.event)
			if err != nil {
				t.Fatal(err)
			}
			if !test.want(next) {
				t.Fatalf("projection = %+v", next)
			}
			if _, err := Apply(test.illegalState(t), test.event); !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("illegal-from error = %v, want ErrIllegalTransition", err)
			}
			if _, err := Apply(test.state(t), test.invalid); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("invalid payload error = %v, want ErrInvalidEvent", err)
			}
			key, err := IdempotencyKey(test.event)
			if err != nil || key != test.key {
				t.Fatalf("idempotency key = %q, error = %v, want %q", key, err, test.key)
			}
		})
	}
}

func TestMovementFailedHumanGateAtomicallyFailsWaitingFinalMovement(t *testing.T) {
	state := verifyingAttemptState(t)
	state.Run = RunWaitingHuman
	state.Movements["m1"] = MovementWaitingHuman
	event := fixtureEvent(EventMovementFailed, map[string]any{
		"reason": "human_gate_rejected", "decision_id": "gate-1", "subject_tree": "git-sha1:tree", "run_failed": true,
	}, attemptEnvelope)
	next, err := Apply(state, event)
	if err != nil {
		t.Fatal(err)
	}
	if next.Run != RunFailed || next.Movements["m1"] != MovementFailed ||
		next.Attempts["a1"].State != AttemptFailed {
		t.Fatalf("human-gate projection = %+v", next)
	}
	invalid := event
	invalid.Payload = mustPayload(t, map[string]any{
		"reason": "human_gate_rejected", "decision_id": "gate-1", "subject_tree": "git-sha1:tree", "run_failed": false,
	})
	if _, err := Apply(state, invalid); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid final human-gate payload error = %v, want ErrInvalidEvent", err)
	}
}

func TestDerivedCancellationAndSupersessionContracts(t *testing.T) {
	cancelledState := runningAttemptState(t)
	cancelledSource := fixtureEvent(EventRunCancelled, map[string]any{
		"cancelled_movement_ids": []any{"m1"},
		"cancelled_attempt_ids":  []any{"a1"},
		"obsoleted_decision_ids": []any{},
	}, nil)
	next, err := Apply(cancelledState, cancelledSource)
	if err != nil {
		t.Fatal(err)
	}
	if next.Run != RunCancelled || next.Movements["m1"] != MovementCancelled ||
		next.Attempts["a1"].State != AttemptCancelled {
		t.Fatalf("cancellation projection = %+v", next)
	}
	if _, err := Apply(NewState(nil), cancelledSource); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("illegal cancellation source error = %v, want ErrIllegalTransition", err)
	}
	invalidCancellation := cancelledSource
	invalidCancellation.Payload = mustPayload(t, map[string]any{
		"cancelled_movement_ids": []any{}, "cancelled_attempt_ids": []any{"a1"}, "obsoleted_decision_ids": []any{},
	})
	if _, err := Apply(runningAttemptState(t), invalidCancellation); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid cancellation payload error = %v, want ErrInvalidEvent", err)
	}

	for _, test := range []struct {
		event Event
		key   string
	}{
		{fixtureEvent(EventMovementCancelled, map[string]any{}, func(event *Event) {
			event.MovementID, event.CausationID = "m1", "source-1"
		}), "source-1\x00m1"},
		{fixtureEvent(EventAttemptCancelled, map[string]any{}, func(event *Event) {
			event.AttemptID, event.CausationID = "a1", "source-1"
		}), "source-1\x00a1"},
		{fixtureEvent(EventAttemptSuperseded, map[string]any{}, func(event *Event) {
			event.AttemptID, event.CausationID = "a1", "source-2"
		}), "source-2\x00a1"},
	} {
		if err := ValidateEvent(test.event); err != nil {
			t.Fatalf("validate %s: %v", test.event.Type, err)
		}
		key, err := IdempotencyKey(test.event)
		if err != nil || key != test.key {
			t.Fatalf("%s key = %q, error = %v, want %q", test.event.Type, key, err, test.key)
		}
		if _, err := Apply(runningAttemptState(t), test.event); !errors.Is(err, ErrUnsupportedEventType) {
			t.Fatalf("%s direct apply error = %v, want ErrUnsupportedEventType", test.event.Type, err)
		}
	}

	supersededState := runningAttemptState(t)
	prepared := fixtureEvent(EventAmendmentApprovalPrepared, autoPreparePayload(), nil)
	var prepareErr error
	supersededState, prepareErr = Apply(supersededState, prepared)
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	approval := autoApprovalEvent()
	next, err = Apply(supersededState, approval)
	if err != nil {
		t.Fatal(err)
	}
	if next.Attempts["a1"].State != AttemptSuperseded {
		t.Fatalf("supersession projection = %+v", next)
	}
	if _, err := Apply(runningAttemptState(t), approval); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("illegal supersession source error = %v, want ErrIllegalTransition", err)
	}
	invalidApproval := autoApprovalEvent()
	invalidApproval.Payload = mustPayload(t, map[string]any{
		"proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS",
		"base_revision": 1, "base_hash": "sha256:score-1", "classifier_version": 1,
		"new_revision": 2, "new_snapshot_hash": "sha256:score-2", "new_snapshot_file_hash": "sha256:file-2",
		"typed_delta": []any{}, "actual_impact": emptyActualImpact(), "superseded_attempt_ids": []any{},
		"obsoleted_decision_ids": []any{}, "finalization": false, "identity_versions": testIdentityVersions(),
	})
	if _, err := Apply(supersededState, invalidApproval); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid supersession payload error = %v, want ErrInvalidEvent", err)
	}
}

func TestAutoPrepareCancellationCombination(t *testing.T) {
	state := runningAttemptState(t)
	prepared := fixtureEvent(EventAmendmentApprovalPrepared, autoPreparePayload(), nil)
	var err error
	state, err = Apply(state, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingPrepare == nil ||
		state.PendingPrepare.QuiesceDeadline != "2026-07-26T00:00:00.000Z" ||
		!reflect.DeepEqual(state.PendingPrepare.TargetAttemptIDs, []AttemptID{"a1"}) {
		t.Fatalf("pending prepare = %+v", state.PendingPrepare)
	}
	abandoned := fixtureEvent(EventAmendmentApprovalAbandoned, map[string]any{
		"prepare_id":         "prepare-1",
		"proposal_id":        "proposal-1",
		"reason":             "cancelled",
		"base_revision":      1,
		"base_hash":          "sha256:score-1",
		"classifier_version": 1,
	}, nil)
	state, err = Apply(state, abandoned)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := fixtureEvent(EventRunCancelled, map[string]any{
		"cancelled_movement_ids": []any{"m1"},
		"cancelled_attempt_ids":  []any{"a1"},
		"obsoleted_decision_ids": []any{},
	}, nil)
	state, err = Apply(state, cancelled)
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingPrepare != nil || state.Run != RunCancelled ||
		state.Movements["m1"] != MovementCancelled ||
		state.Attempts["a1"].State != AttemptCancelled {
		t.Fatalf("cancel projection = %+v", state)
	}
}

func TestAutoApprovalCommitsPreparedHeadAndSupersedesExactAttempts(t *testing.T) {
	state := runningAttemptState(t)
	var err error
	state, err = Apply(state, fixtureEvent(EventAmendmentApprovalPrepared, autoPreparePayload(), nil))
	if err != nil {
		t.Fatal(err)
	}
	approval := fixtureEvent(EventAmendmentApproved, map[string]any{
		"proposal_id": "proposal-1",
		"mode":        "auto", "envelope_class": "NARROW_PATHS",
		"base_revision": 1, "base_hash": "sha256:score-1",
		"classifier_version": 1,
		"new_revision":       2, "new_snapshot_hash": "sha256:score-2",
		"new_snapshot_file_hash": "sha256:file-2",
		"typed_delta":            []any{},
		"actual_impact": map[string]any{
			"score_changes": []any{},
			"authority": map[string]any{
				"allowed_paths": map[string]any{"added": []any{}, "removed": []any{}},
				"grants":        []any{},
				"side_effects":  map[string]any{"added": []any{}, "removed": []any{}},
			},
			"budget": map[string]any{},
		},
		"superseded_attempt_ids": []any{"a1"},
		"obsoleted_decision_ids": []any{},
		"finalization":           false,
		"identity_versions":      testIdentityVersions(),
	}, func(event *Event) {
		event.ScoreRevision = 2
	})
	state, err = Apply(state, approval)
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingPrepare != nil ||
		state.ScoreHead != (ScoreHead{Revision: 2, SemanticHash: "sha256:score-2", FileHash: "sha256:file-2"}) ||
		state.Attempts["a1"].State != AttemptSuperseded {
		t.Fatalf("approved projection = %+v", state)
	}
}

func TestCriterionLaunchVariantsAreDistinct(t *testing.T) {
	cases := []struct {
		name     string
		extra    map[string]any
		wantType any
	}{
		{
			name: "spawned",
			extra: map[string]any{
				"criterion_process": map[string]any{
					"pid": 10, "session_id": 10,
					"start_identity": map[string]any{
						"platform": "linux", "boot_id": "boot", "start_ticks": "12",
					},
				},
			},
			wantType: SpawnedCriterionLaunch{},
		},
		{name: "spawn failed", extra: map[string]any{"spawn_failed": true}, wantType: SpawnFailedCriterionLaunch{}},
		{name: "in process", extra: map[string]any{}, wantType: InProcessCriterionLaunch{}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			state := verifiedAttemptState(t)
			state, err := Apply(state, fixtureEvent(EventAcceptanceStarted, map[string]any{
				"subject_tree":          "git-sha1:tree",
				"acceptance_spec_hash":  "sha256:acceptance",
				"planned_criterion_ids": []any{"c1"},
				"identity_versions":     testIdentityVersions(),
			}, func(event *Event) { event.AttemptID = "a1" }))
			if err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{
				"criterion_id":        "c1",
				"criterion_spec_hash": "sha256:criterion",
				"subject_tree":        "git-sha1:tree",
				"identity_versions":   testIdentityVersions(),
			}
			for key, value := range test.extra {
				payload[key] = value
			}
			state, err = Apply(state, fixtureEvent(EventCriterionStarted, payload, func(event *Event) {
				event.AttemptID = "a1"
			}))
			if err != nil {
				t.Fatal(err)
			}
			got := state.CriterionLaunches[CriterionLaunchKey{AttemptID: "a1", CriterionID: "c1"}]
			if reflect.TypeOf(got) != reflect.TypeOf(test.wantType) {
				t.Fatalf("launch type = %T, want %T", got, test.wantType)
			}
		})
	}
}

func TestStartIdentityRejectsMixedPlatformFields(t *testing.T) {
	state := NewState(nil)
	state.Run = RunRunning
	event := fixtureEvent(EventAuthorityGranted, map[string]any{
		"authority_epoch": 1,
		"owner_pid":       42,
		"owner_start_identity": map[string]any{
			"platform": "linux", "boot_id": "boot", "start_ticks": "3",
			"start_tvsec": 4,
		},
	}, nil)
	_, err := Apply(state, event)
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("error = %v, want ErrInvalidEvent", err)
	}
}

func TestShippingRecoveryProjectionUsesJournalTransactions(t *testing.T) {
	state := NewState(nil)
	state.Run = RunSucceeded
	state.ApplicationCandidate = &ApplicationCandidate{ID: "candidate"}

	apply := func(event Event) {
		t.Helper()
		var err error
		state, err = Apply(state, event)
		if err != nil {
			t.Fatal(err)
		}
	}
	apply(fixtureEvent(EventApplyStarted, map[string]any{
		"txn_id": "apply-1", "candidate_id": "candidate", "before_tree": "base", "result_tree": "result",
		"touched_paths": []any{}, "recovery": map[string]any{"base_tree": "base", "result_tree": "result"},
		"identity_versions": testIdentityVersions(),
	}, nil))
	apply(fixtureEvent(EventApplyRecoveryRequired, map[string]any{
		"txn_id": "apply-1", "candidate_id": "candidate", "failure_detail": "tree mismatch",
		"identity_versions": testIdentityVersions(),
	}, nil))
	if state.Application.State != ApplicationRecoveryRequired || state.Application.Reason != "tree mismatch" {
		t.Fatalf("application = %+v", state.Application)
	}
	apply(fixtureEvent(EventApplyCompleted, map[string]any{
		"txn_id": "apply-1", "candidate_id": "candidate", "result_tree": "result",
		"identity_versions": testIdentityVersions(),
	}, nil))
	if state.Application.State != ApplicationApplied || state.Application.Reason != "" {
		t.Fatalf("application = %+v", state.Application)
	}

	apply(fixtureEvent(EventScorePromotionStarted, map[string]any{
		"txn_id": "promotion-1", "candidate_id": "candidate", "expected_root_file_hash": "sha256:before",
		"target_snapshot_file_hash": "sha256:after", "target_revision": 1,
		"identity_versions": testIdentityVersions(),
	}, nil))
	apply(fixtureEvent(EventScorePromotionRecoveryRequired, map[string]any{
		"txn_id": "promotion-1", "candidate_id": "candidate", "failure_detail": "root changed",
		"identity_versions": testIdentityVersions(),
	}, nil))
	if state.Promotion.State != PromotionRecoveryRequired || state.Promotion.Reason != "root changed" {
		t.Fatalf("promotion = %+v", state.Promotion)
	}
	apply(fixtureEvent(EventScorePromoted, map[string]any{
		"txn_id": "promotion-1", "candidate_id": "candidate", "target_revision": 1,
		"target_snapshot_file_hash": "sha256:after", "identity_versions": testIdentityVersions(),
	}, nil))
	if state.Promotion.State != PromotionPromoted || state.Promotion.Reason != "" {
		t.Fatalf("promotion = %+v", state.Promotion)
	}
}

func runningAttemptState(t *testing.T) State {
	t.Helper()
	state := NewState([]MovementSeed{{ID: "m1", Initial: MovementPending}})
	var err error
	for _, event := range []Event{
		fixtureEvent(EventRunStarted, runStartedPayload(), nil),
		fixtureEvent(EventMovementReady, map[string]any{}, func(event *Event) { event.MovementID = "m1" }),
		fixtureEvent(EventMovementStarted, map[string]any{}, func(event *Event) { event.MovementID = "m1" }),
		fixtureEvent(EventPerformerSelected, performerSelectedPayload(), func(event *Event) {
			event.MovementID = "m1"
			event.AttemptID = "a1"
		}),
		fixtureEvent(EventAttemptStarted, attemptStartedPayload(), func(event *Event) {
			event.MovementID = "m1"
			event.AttemptID = "a1"
		}),
	} {
		state, err = Apply(state, event)
		if err != nil {
			t.Fatalf("apply %s: %v", event.Type, err)
		}
	}
	return state
}

func verifyingAttemptState(t *testing.T) State {
	t.Helper()
	state := probedAttemptState(t)
	state, err := Apply(state, fixtureEvent(EventPerformerCompleted, map[string]any{
		"session_hint_stored": false,
	}, func(event *Event) {
		event.MovementID = "m1"
		event.AttemptID = "a1"
	}))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func probedAttemptState(t *testing.T) State {
	t.Helper()
	state := runningAttemptState(t)
	state, err := Apply(state, fixtureEvent(EventAdapterProbed, adapterProbedPayload(), func(event *Event) {
		event.MovementID = "m1"
		event.AttemptID = "a1"
	}))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func verifiedAttemptState(t *testing.T) State {
	t.Helper()
	state := verifyingAttemptState(t)
	state, err := Apply(state, fixtureEvent(EventVerificationPassed, map[string]any{}, func(event *Event) {
		event.MovementID = "m1"
		event.AttemptID = "a1"
	}))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func fixtureEvent(eventType EventType, payload any, edit func(*Event)) Event {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	event := Event{
		EventID:       "event-1",
		Seq:           1,
		Timestamp:     "2026-07-26T00:00:00.000Z",
		RunID:         "run-1",
		ScoreRevision: 1,
		Type:          eventType,
		Payload:       encoded,
	}
	if edit != nil {
		edit(&event)
	}
	return event
}

func runStartedPayload() map[string]any {
	return map[string]any{
		"base_commit":        "git-sha1:commit",
		"base_tree":          "git-sha1:tree",
		"score_hash":         "sha256:score-1",
		"score_file_hash":    "sha256:file-1",
		"resolved_cast_hash": "sha256:cast",
		"identity_versions":  testIdentityVersions(),
	}
}

func performerSelectedPayload() map[string]any {
	return map[string]any{
		"reason": "initial", "performer_id": "p1", "adapter_id": "adapter",
		"model": "model",
	}
}

func adapterProbedPayload() map[string]any {
	return map[string]any{
		"adapter_version": "1",
		"capabilities": map[string]any{
			"repo_read": true, "repo_write": false, "shell": false,
			"network": false, "resumable_sessions": false,
		},
		"enforcement": map[string]any{
			"path_grants": false, "read_only": false, "network_grants": false,
			"shell_grants": false, "read_grants": false,
		},
		"negotiated_features":       []any{},
		"truncated_resolutions":     []any{},
		"advisory_dimensions":       []any{},
		"execution_dependency_hash": "sha256:dependency",
		"identity_versions":         testIdentityVersions(),
	}
}

func attemptStartedPayload() map[string]any {
	return map[string]any{
		"attempt_number": 1,
		"adapter_process": map[string]any{
			"pid": 10, "session_id": 10,
			"start_identity": map[string]any{
				"platform": "linux", "boot_id": "boot", "start_ticks": "12",
			},
		},
		"granted_authority": map[string]any{
			"paths_rw": []any{}, "paths_ro": []any{}, "shell": false, "network": false,
		},
		"identity_versions": testIdentityVersions(),
	}
}

func autoPreparePayload() map[string]any {
	return map[string]any{
		"prepare_id": "prepare-1", "proposal_id": "proposal-1",
		"mode": "auto", "envelope_class": "NARROW_PATHS",
		"base_revision": 1, "base_hash": "sha256:score-1",
		"new_revision": 2, "new_snapshot_hash": "sha256:score-2",
		"new_snapshot_file_hash":   "sha256:file-2",
		"plan_record_hash":         "sha256:plan",
		"target_attempt_ids":       []any{"a1"},
		"observed_authority_epoch": 0,
		"quiesce_deadline":         "2026-07-26T00:00:00.000Z",
		"classifier_version":       1,
		"identity_versions":        testIdentityVersions(),
	}
}

func blockedPayload() map[string]any {
	return map[string]any{
		"raised": []any{map[string]any{
			"decision_id": "d1", "emitted_id": "q1", "kind": "question", "question": "Continue?", "blocking": true,
		}},
		"pending_decision_ids": []any{"d1"},
	}
}

func autoApprovalEvent() Event {
	return fixtureEvent(EventAmendmentApproved, map[string]any{
		"proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS",
		"base_revision": 1, "base_hash": "sha256:score-1", "classifier_version": 1,
		"new_revision": 2, "new_snapshot_hash": "sha256:score-2", "new_snapshot_file_hash": "sha256:file-2",
		"typed_delta": []any{}, "actual_impact": emptyActualImpact(), "superseded_attempt_ids": []any{"a1"},
		"obsoleted_decision_ids": []any{}, "finalization": false, "identity_versions": testIdentityVersions(),
	}, func(event *Event) {
		event.ScoreRevision = 2
	})
}

func emptyActualImpact() map[string]any {
	return map[string]any{
		"score_changes": []any{},
		"authority": map[string]any{
			"allowed_paths": map[string]any{"added": []any{}, "removed": []any{}},
			"grants":        []any{},
			"side_effects":  map[string]any{"added": []any{}, "removed": []any{}},
		},
		"budget": map[string]any{},
	}
}

func mustPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func testIdentityVersions() map[string]any {
	return map[string]any{
		"canonical_encoding": 1,
		"projections":        map[string]any{},
	}
}
