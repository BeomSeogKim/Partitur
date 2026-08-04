package runstate

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestApplyDoesNotAliasInputOnSuccessOrError(t *testing.T) {
	input := NewState([]MovementSeed{{ID: "m1", Initial: MovementPending}})
	input.Run = RunRunning
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

	success, err := Apply(input, fixtureEvent(EventCancelRequested, map[string]any{"requested_by": "cli"}, nil))
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

func TestNewStateProjectsSeedFinality(t *testing.T) {
	state := NewState([]MovementSeed{
		{ID: "final", Initial: MovementPending, Final: true},
		{ID: "ordinary", Initial: MovementPending},
	})
	if !state.FinalMovements["final"] || state.FinalMovements["ordinary"] {
		t.Fatalf("final movements = %#v", state.FinalMovements)
	}
	next, err := Apply(state, fixtureEvent(EventMovementReady, map[string]any{}, func(event *Event) {
		event.MovementID = "ordinary"
	}))
	if err != nil {
		t.Fatal(err)
	}
	next.FinalMovements["final"] = false
	if !state.FinalMovements["final"] {
		t.Fatal("final movement projection aliases input")
	}
}

func TestAttemptStartedRequiresCompositionHashExactlyForDependencies(t *testing.T) {
	t.Run("missing for dependent movement", func(t *testing.T) {
		state := attemptStartingState(t, true)
		_, err := Apply(state, attemptStartedEvent(attemptStartedPayload()))
		if !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("error = %v, want ErrInvalidEvent", err)
		}
	})

	t.Run("present for independent movement", func(t *testing.T) {
		state := attemptStartingState(t, false)
		payload := attemptStartedPayload()
		payload["base_composition_hash"] = "sha256:composition"
		_, err := Apply(state, attemptStartedEvent(payload))
		if !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("error = %v, want ErrInvalidEvent", err)
		}
	})

	t.Run("present for dependent movement", func(t *testing.T) {
		state := attemptStartingState(t, true)
		payload := attemptStartedPayload()
		payload["base_composition_hash"] = "sha256:composition"
		next, err := Apply(state, attemptStartedEvent(payload))
		if err != nil {
			t.Fatal(err)
		}
		if next.Attempts["a1"].State != AttemptRunning {
			t.Fatalf("attempt state = %s, want RUNNING", next.Attempts["a1"].State)
		}
	})
}

func TestUnregisteredEventFailsAsInvalid(t *testing.T) {
	state := NewState(nil)
	_, err := Apply(state, fixtureEvent("unknown.event", map[string]any{}, nil))
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("error = %v, want ErrInvalidEvent", err)
	}
	if errors.Is(err, ErrUnsupportedEventType) {
		t.Fatalf("unregistered event was classified as supported-registry-only: %v", err)
	}
}

func TestEScopedSupportedEventSetHasFortyNineTypes(t *testing.T) {
	var count int
	for eventType := range registryEvents {
		if isSupportedEvent(eventType) {
			count++
		}
	}
	if count != 49 {
		t.Fatalf("supported event count = %d, want 49", count)
	}
	for _, eventType := range []EventType{EventMovementCancelled, EventAttemptCancelled, EventAttemptSuperseded, EventDecisionObsoleted} {
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

func TestChangeSetRecordedProjectsOnlyForVerifyingRepoWriteAttempt(t *testing.T) {
	state := verifyingAttemptState(t)
	state.RepoWriteMovements["m1"] = true
	event := fixtureEvent(EventChangeSetRecorded, changeSetPayload(), func(event *Event) {
		event.MovementID = "m1"
		event.AttemptID = "a1"
	})

	next, err := Apply(state, event)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := next.ChangeSets["a1"]
	if !ok || got.ChangeSetID != "change-set-1" || got.BaseTree != "git-sha1:base" ||
		got.ResultTree != "git-sha1:result" || got.Commit != "git-sha1:commit" ||
		got.Ref != "refs/partitur/runs/run-1/attempts/a1/changeset" {
		t.Fatalf("change set projection = %+v, present=%t", got, ok)
	}
	if next.VerifiedAttempts["a1"] {
		t.Fatal("change_set.recorded must not infer verification.passed")
	}
	if _, err := Apply(next, event); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("duplicate direct projection error = %v, want ErrIllegalTransition", err)
	}

	wrongPhase := probedAttemptState(t)
	wrongPhase.RepoWriteMovements["m1"] = true
	if _, err := Apply(wrongPhase, event); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("RUNNING change_set.recorded error = %v, want ErrIllegalTransition", err)
	}
	nonWriter := verifyingAttemptState(t)
	if _, err := Apply(nonWriter, event); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("non-writer change_set.recorded error = %v, want ErrIllegalTransition", err)
	}
	invalid := event
	invalid.Payload = mustPayload(t, map[string]any{"change_set_id": "change-set-1"})
	if _, err := Apply(state, invalid); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid change_set.recorded error = %v, want ErrInvalidEvent", err)
	}
}

func TestAttemptFailedStopsAtAttemptAndPreservesDisposition(t *testing.T) {
	state := runningAttemptState(t)
	event := fixtureEvent(EventAttemptFailed, map[string]any{
		"kind": "task_failed",
		"disposition": map[string]any{
			"charged":           "none",
			"movement_terminal": true,
			"terminal_reason":   "retries_exhausted",
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
	if got := next.Attempts["a1"].Failure.Disposition; got.Charged != "none" ||
		!got.MovementTerminal || got.TerminalReason != "retries_exhausted" {
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

func TestAttemptBlockedRaisedSequenceAcceptsWireOrderButRejectsDuplicateID(t *testing.T) {
	payload := map[string]any{
		"raised": []any{
			map[string]any{"decision_id": "dec-2", "emitted_id": "q-2", "kind": "question", "question": "First?", "blocking": true},
			map[string]any{"decision_id": "dec-1", "emitted_id": "q-1", "kind": "question", "question": "Second?", "blocking": true},
		},
		"pending_decision_ids": []any{"dec-1", "dec-2"},
	}
	if err := ValidateEvent(fixtureEvent(EventAttemptBlocked, payload, attemptEnvelope)); err != nil {
		t.Fatalf("wire-ordered raised sequence rejected: %v", err)
	}
	payload["raised"].([]any)[1].(map[string]any)["decision_id"] = "dec-2"
	payload["pending_decision_ids"] = []any{"dec-2", "dec-2"}
	if err := ValidateEvent(fixtureEvent(EventAttemptBlocked, payload, attemptEnvelope)); err == nil {
		t.Fatal("duplicate raised decision id accepted")
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
	invalidFencedEpoch := cancelledSource
	invalidFencedEpoch.Payload = mustPayload(t, map[string]any{
		"cancelled_movement_ids": []any{"m1"}, "cancelled_attempt_ids": []any{"a1"}, "obsoleted_decision_ids": []any{}, "fenced_epoch": 3,
	})
	stateWithObservedEpoch := runningAttemptState(t)
	stateWithObservedEpoch.Authority.Epoch = 1
	if _, err := Apply(stateWithObservedEpoch, invalidFencedEpoch); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("fenced epoch beyond observed plus one error = %v, want ErrInvalidEvent", err)
	}
	invalidPriorFencedEpoch := cancelledSource
	invalidPriorFencedEpoch.Payload = mustPayload(t, map[string]any{
		"cancelled_movement_ids": []any{"m1"}, "cancelled_attempt_ids": []any{"a1"}, "obsoleted_decision_ids": []any{}, "fenced_epoch": 1,
	})
	if _, err := Apply(stateWithObservedEpoch, invalidPriorFencedEpoch); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("fenced epoch before observed successor error = %v, want ErrInvalidEvent", err)
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
		"typed_delta": []any{}, "actual_impact": emptyActualImpact(), "head_movements": headMovementsPayload("m1"), "superseded_attempt_ids": []any{},
		"obsoleted_decision_ids": []any{}, "finalization": false, "identity_versions": testIdentityVersions(),
	})
	if _, err := Apply(supersededState, invalidApproval); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("invalid supersession payload error = %v, want ErrInvalidEvent", err)
	}
}

func TestDecisionAmendmentAndCompositionEvents(t *testing.T) {
	question := fixtureEvent(EventDecisionRequested, map[string]any{
		"decision_id": "decision-1", "decision_type": "question", "emitted_id": "question-1", "question": "Continue?",
	}, attemptEnvelope)
	questionState := func(t *testing.T) State { return runningAttemptState(t) }

	route := fixtureEvent(EventAmendmentRoutedHuman, routedAmendmentPayload(), nil)
	routeState := func(t *testing.T) State {
		state := NewState(nil)
		state.Run = RunRunning
		state.ScoreHead = ScoreHead{Revision: 1, SemanticHash: "sha256:score-1"}
		return state
	}

	tests := []struct {
		name      string
		event     Event
		state     func(*testing.T) State
		illegal   func(*testing.T) State
		invalid   Event
		key       string
		noIllegal bool
		want      func(State) bool
	}{
		{
			name:  "decision requested",
			event: question,
			state: questionState,
			illegal: func(t *testing.T) State {
				state := questionState(t)
				state.PendingDecisions["decision-1"] = PendingDecision{ID: "decision-1"}
				return state
			},
			invalid: fixtureEvent(EventDecisionRequested, map[string]any{
				"decision_id": "decision-1", "decision_type": "question",
			}, attemptEnvelope),
			key: "decision-1",
			want: func(state State) bool {
				return state.Run == RunWaitingHuman && state.Movements["m1"] == MovementWaitingHuman &&
					state.PendingDecisions["decision-1"].Type == "question"
			},
		},
		{
			name: "decision resolved",
			event: fixtureEvent(EventDecisionResolved, map[string]any{
				"decision_id": "decision-1", "decision_type": "question", "disposition": "answered", "answer": "yes",
			}, attemptEnvelope),
			state: func(t *testing.T) State {
				state, err := Apply(questionState(t), question)
				if err != nil {
					t.Fatal(err)
				}
				return state
			},
			illegal: questionState,
			invalid: fixtureEvent(EventDecisionResolved, map[string]any{
				"decision_id": "decision-1", "decision_type": "question", "disposition": "approved", "answer": "yes",
			}, attemptEnvelope),
			key: "decision-1",
			want: func(state State) bool {
				_, pending := state.PendingDecisions["decision-1"]
				return !pending && state.Run == RunRunning && state.Movements["m1"] == MovementRunning
			},
		},
		{
			name:    "amendment rejected",
			event:   fixtureEvent(EventAmendmentRejected, amendmentRejectedPayload(), nil),
			state:   func(t *testing.T) State { return NewState(nil) },
			illegal: func(t *testing.T) State { return NewState(nil) },
			invalid: fixtureEvent(EventAmendmentRejected, map[string]any{
				"proposal_id": "proposal-1", "reason": "candidate_incompatible", "base_revision": 1, "base_hash": "sha256:score-1", "classifier_version": 1,
				"patch_operations_hash": "sha256:patch", "error_location": "patch[0]", "identity_versions": testIdentityVersions(),
			}, nil),
			key:       "proposal-1",
			noIllegal: true,
			want:      func(state State) bool { return len(state.PendingDecisions) == 0 },
		},
		{
			name:  "amendment routed human",
			event: route,
			state: routeState,
			illegal: func(t *testing.T) State {
				state := routeState(t)
				state.Run = RunCancelled
				return state
			},
			invalid: fixtureEvent(EventAmendmentRoutedHuman, map[string]any{
				"proposal_id": "proposal-1", "reason": "draft_phase", "decision_type": "finalization", "blocking": false,
				"proposal_record_hash": "sha256:proposal", "base_revision": 1, "base_hash": "sha256:score-1", "classifier_version": 1,
				"decision_id": "decision-1", "typed_delta": []any{}, "actual_impact": emptyActualImpact(), "identity_versions": testIdentityVersions(),
			}, nil),
			key: "proposal-1",
			want: func(state State) bool {
				return state.RoutedAmendments["proposal-1"].DecisionID == "decision-1"
			},
		},
		{
			name: "amendment human rejected",
			event: fixtureEvent(EventAmendmentHumanRejected, map[string]any{
				"proposal_id": "proposal-1", "decision_id": "decision-1", "human_reason": "not now",
				"base_revision": 1, "base_hash": "sha256:score-1", "classifier_version": 1, "identity_versions": testIdentityVersions(),
			}, nil),
			state: func(t *testing.T) State {
				state, err := Apply(routeState(t), route)
				if err != nil {
					t.Fatal(err)
				}
				state.PendingDecisions["decision-1"] = PendingDecision{ID: "decision-1", Type: "amendment", Blocking: true}
				state.Run = RunWaitingHuman
				return state
			},
			illegal: routeState,
			invalid: fixtureEvent(EventAmendmentHumanRejected, map[string]any{
				"proposal_id": "proposal-1", "decision_id": "decision-1", "human_reason": "",
				"base_revision": 1, "base_hash": "sha256:score-1", "classifier_version": 1, "identity_versions": testIdentityVersions(),
			}, nil),
			key: "proposal-1",
			want: func(state State) bool {
				_, pending := state.PendingDecisions["decision-1"]
				_, routed := state.RoutedAmendments["proposal-1"]
				return !pending && !routed && state.Run == RunRunning
			},
		},
		{
			name:  "composition conflicted",
			event: compositionConflictedEvent(),
			state: runningAttemptState,
			illegal: func(t *testing.T) State {
				state := runningAttemptState(t)
				state.Movements["m1"] = MovementReady
				return state
			},
			invalid: fixtureEvent(EventCompositionConflicted, map[string]any{
				"scope": "movement", "target_id": "m1", "composition_subject_hash": "sha256:subject", "contributors": []any{},
				"conflicted_paths": []any{"z", "a"}, "composition_algorithm_version": "1", "identity_versions": testIdentityVersions(),
			}, func(event *Event) { event.MovementID = "m1" }),
			key:  "movement\x00m1\x00sha256:subject",
			want: func(state State) bool { return state.Movements["m1"] == MovementRunning },
		},
		{
			name:  "composition failed",
			event: compositionFailedEvent(),
			state: runningAttemptState,
			illegal: func(t *testing.T) State {
				state := runningAttemptState(t)
				state.Movements["m1"] = MovementReady
				return state
			},
			invalid: fixtureEvent(EventCompositionFailed, map[string]any{
				"scope": "movement", "target_id": "m1", "composition_subject_hash": "sha256:subject", "cause": "git_exit", "diagnostic": "exit 2",
				"contributors": []any{}, "composition_algorithm_version": "1", "identity_versions": testIdentityVersions(),
			}, func(event *Event) { event.MovementID = "m1" }),
			key:  "movement\x00m1\x00sha256:subject",
			want: func(state State) bool { return state.Movements["m1"] == MovementRunning },
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
			if !test.noIllegal {
				if _, err := Apply(test.illegal(t), test.event); !errors.Is(err, ErrIllegalTransition) {
					t.Fatalf("illegal-from error = %v, want ErrIllegalTransition", err)
				}
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

func TestDecisionResolutionRetainsFullHumanGateFact(t *testing.T) {
	state := NewState([]MovementSeed{{ID: "m1", Initial: MovementPending}})
	state.Run = RunWaitingHuman
	state.Movements["m1"] = MovementWaitingHuman
	state.Attempts["a1"] = Attempt{MovementID: "m1", ScoreRevision: 7, State: AttemptVerifying}
	state.PendingDecisions["gate-decision"] = PendingDecision{
		ID: "gate-decision", Type: "human_gate", MovementID: "m1", AttemptID: "a1", ScoreRevision: 7,
		GateID: "gate-a1", SubjectTree: "git-sha1:subject",
		BlockingFindings: []FindingReference{{ArtifactInstanceID: "findings@a1", FindingID: "F-1"}},
	}

	next := applyFixture(t, state, EventDecisionResolved, map[string]any{
		"decision_id": "gate-decision", "decision_type": "human_gate", "disposition": "approved",
		"gate_id": "gate-a1", "scope": map[string]any{"subject_tree": "git-sha1:subject"},
		"overridden_findings": []any{map[string]any{"artifact_instance_id": "findings@a1", "finding_id": "F-1"}},
		"override_reason":     "human judgment",
	}, attemptEnvelope)

	resolution, ok := next.ResolvedHumanGates["a1"]
	if !ok || resolution.DecisionID != "gate-decision" || resolution.MovementID != "m1" ||
		resolution.AttemptID != "a1" || resolution.ScoreRevision != 7 || resolution.GateID != "gate-a1" ||
		resolution.Scope.SubjectTree != "git-sha1:subject" || resolution.Disposition != "approved" ||
		resolution.OverrideReason != "human judgment" || len(resolution.OverriddenFindings) != 1 ||
		resolution.OverriddenFindings[0] != (FindingReference{ArtifactInstanceID: "findings@a1", FindingID: "F-1"}) {
		t.Fatalf("retained human-gate resolution = %#v", resolution)
	}
	if _, pending := next.PendingDecisions["gate-decision"]; pending {
		t.Fatalf("resolved human gate remained pending: %#v", next.PendingDecisions)
	}

	rejected := applyFixture(t, state, EventDecisionResolved, map[string]any{
		"decision_id": "gate-decision", "decision_type": "human_gate", "disposition": "rejected",
		"gate_id": "gate-a1", "scope": map[string]any{"subject_tree": "git-sha1:subject"},
		"overridden_findings": []any{}, "reason": "not ready",
	}, attemptEnvelope)
	if got := rejected.ResolvedHumanGates["a1"].Reason; got != "not ready" {
		t.Fatalf("rejected human-gate reason = %q", got)
	}

	for _, payload := range []map[string]any{
		{
			"decision_id": "gate-decision", "decision_type": "human_gate", "disposition": "approved",
			"gate_id": "gate-a1", "scope": map[string]any{"subject_tree": "git-sha1:subject"},
			"overridden_findings": []any{map[string]any{"artifact_instance_id": "other@a1", "finding_id": "F-1"}},
			"override_reason":     "human judgment",
		},
		{
			"decision_id": "gate-decision", "decision_type": "human_gate", "disposition": "approved",
			"gate_id": "gate-a1", "scope": map[string]any{"subject_tree": "git-sha1:subject"},
			"overridden_findings": []any{map[string]any{"artifact_instance_id": "findings@a1", "finding_id": "other"}},
			"override_reason":     "human judgment",
		},
		{
			"decision_id": "gate-decision", "decision_type": "human_gate", "disposition": "rejected",
			"gate_id": "gate-a1", "scope": map[string]any{"subject_tree": "git-sha1:subject"},
			"overridden_findings": []any{map[string]any{"artifact_instance_id": "findings@a1", "finding_id": "F-1"}},
			"override_reason":     "human judgment",
		},
	} {
		if _, err := Apply(state, fixtureEvent(EventDecisionResolved, payload, attemptEnvelope)); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("invalid human-gate resolution error = %v, want ErrInvalidEvent", err)
		}
	}
}

func TestHumanGateRequestRetainsBlockingFindings(t *testing.T) {
	state := NewState([]MovementSeed{{ID: "m1", Initial: MovementPending}})
	state.Run = RunRunning
	state.Movements["m1"] = MovementRunning
	next := applyFixture(t, state, EventDecisionRequested, map[string]any{
		"decision_id": "gate-decision", "decision_type": "human_gate", "gate_id": "gate-a1",
		"gate_mode": "on_contested", "subject_tree": "git-sha1:subject", "review_outcome": "CONTESTED",
		"blocking_findings": []any{map[string]any{"artifact_instance_id": "findings@a1", "finding_id": "F-1"}},
	}, attemptEnvelope)
	decision := next.PendingDecisions["gate-decision"]
	if decision.BlockingFindings == nil || len(decision.BlockingFindings) != 1 || decision.BlockingFindings[0] != (FindingReference{ArtifactInstanceID: "findings@a1", FindingID: "F-1"}) {
		t.Fatalf("retained blocking findings = %#v", decision)
	}
}

func TestDecisionObsoletionIsDerivedFromTerminalSources(t *testing.T) {
	state := runningAttemptState(t)
	requested := fixtureEvent(EventDecisionRequested, map[string]any{
		"decision_id": "decision-1", "decision_type": "question", "emitted_id": "question-1", "question": "Continue?",
	}, attemptEnvelope)
	var err error
	state, err = Apply(state, requested)
	if err != nil {
		t.Fatal(err)
	}
	derived := fixtureEvent(EventDecisionObsoleted, map[string]any{"decision_id": "decision-1"}, func(event *Event) {
		event.CausationID = "terminal-1"
	})
	if err := ValidateEvent(derived); err != nil {
		t.Fatal(err)
	}
	if key, err := IdempotencyKey(derived); err != nil || key != "terminal-1\x00decision-1" {
		t.Fatalf("derived key = %q, error = %v", key, err)
	}
	if _, err := Apply(state, derived); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("unresolved derived apply error = %v, want ErrInvalidEvent", err)
	}
	cancelled := fixtureEvent(EventRunCancelled, map[string]any{
		"cancelled_movement_ids": []any{"m1"}, "cancelled_attempt_ids": []any{"a1"}, "obsoleted_decision_ids": []any{"decision-1"},
	}, nil)
	next, err := Apply(state, cancelled)
	if err != nil {
		t.Fatal(err)
	}
	if next.Run != RunCancelled || len(next.PendingDecisions) != 0 {
		t.Fatalf("terminal obsoletion projection = %+v", next)
	}

	state = runningAttemptState(t)
	state, err = Apply(state, requested)
	if err != nil {
		t.Fatal(err)
	}
	state, err = Apply(state, fixtureEvent(EventAmendmentApprovalPrepared, autoPreparePayload(), nil))
	if err != nil {
		t.Fatal(err)
	}
	approval := autoApprovalEvent()
	approval.Payload = mustPayload(t, map[string]any{
		"proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS",
		"base_revision": 1, "base_hash": "sha256:score-1", "classifier_version": 1,
		"new_revision": 2, "new_snapshot_hash": "sha256:score-2", "new_snapshot_file_hash": "sha256:file-2",
		"typed_delta": []any{}, "actual_impact": emptyActualImpact(), "head_movements": headMovementsPayload("m1"), "superseded_attempt_ids": []any{"a1"},
		"obsoleted_decision_ids": []any{"decision-1"}, "finalization": false, "identity_versions": testIdentityVersions(),
	})
	next, err = Apply(state, approval)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.PendingDecisions) != 0 || next.Attempts["a1"].State != AttemptSuperseded {
		t.Fatalf("approval obsoletion projection = %+v", next)
	}
}

func TestDerivedCausationEnforcesEnvelopeAuthority(t *testing.T) {
	derivedTypes := []EventType{
		EventMovementCancelled,
		EventAttemptCancelled,
		EventAttemptSuperseded,
		EventDecisionObsoleted,
	}

	t.Run("requires a non-empty causation id before key construction", func(t *testing.T) {
		for _, eventType := range derivedTypes {
			t.Run(string(eventType), func(t *testing.T) {
				event := derivedCausationEvent(eventType, "", 2)
				if err := ValidateEvent(event); !errors.Is(err, ErrInvalidEvent) {
					t.Fatalf("ValidateEvent error = %v, want ErrInvalidEvent", err)
				}
				if key, err := IdempotencyKey(event); !errors.Is(err, ErrInvalidEvent) || key != "" {
					t.Fatalf("IdempotencyKey = %q, %v; want empty key and ErrInvalidEvent", key, err)
				}
				if _, err := Apply(NewState(nil), event); !errors.Is(err, ErrInvalidEvent) {
					t.Fatalf("Apply error = %v, want ErrInvalidEvent", err)
				}
			})
		}
	})

	t.Run("requires an already applied source event", func(t *testing.T) {
		for _, eventType := range derivedTypes {
			t.Run(string(eventType), func(t *testing.T) {
				event := derivedCausationEvent(eventType, "missing-source", 2)
				if _, err := Apply(runningAttemptState(t), event); !errors.Is(err, ErrInvalidEvent) ||
					!strings.Contains(err.Error(), "already applied") {
					t.Fatalf("Apply error = %v, want unresolved causation ErrInvalidEvent", err)
				}
			})
		}
	})

	t.Run("requires a source earlier than the derived event", func(t *testing.T) {
		state := cancelledSourceState(t, "cancel-source", 12)
		event := derivedCausationEvent(EventMovementCancelled, "cancel-source", 11)
		if _, err := Apply(state, event); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("Apply error = %v, want ErrInvalidEvent", err)
		}
	})

	t.Run("rejects source types outside the Appendix B rule", func(t *testing.T) {
		for _, eventType := range derivedTypes {
			t.Run(string(eventType), func(t *testing.T) {
				state := runningAttemptState(t)
				source := fixtureEvent(EventCancelRequested, map[string]any{"requested_by": "cli"}, func(event *Event) {
					event.EventID, event.Seq = "wrong-source", 10
				})
				var err error
				state, err = Apply(state, source)
				if err != nil {
					t.Fatal(err)
				}
				event := derivedCausationEvent(eventType, "wrong-source", 11)
				if _, err := Apply(state, event); !errors.Is(err, ErrInvalidEvent) {
					t.Fatalf("Apply error = %v, want ErrInvalidEvent", err)
				}
			})
		}
	})

	t.Run("accepts each exact source rule", func(t *testing.T) {
		for _, test := range []struct {
			name  string
			state func(*testing.T) State
			event Event
		}{
			{
				name:  "movement cancelled from run cancelled",
				state: func(t *testing.T) State { return cancelledSourceState(t, "cancel-source", 10) },
				event: derivedCausationEvent(EventMovementCancelled, "cancel-source", 11),
			},
			{
				name:  "attempt cancelled from run cancelled",
				state: func(t *testing.T) State { return cancelledSourceState(t, "cancel-source", 10) },
				event: derivedCausationEvent(EventAttemptCancelled, "cancel-source", 11),
			},
			{
				name:  "attempt superseded from amendment approved",
				state: func(t *testing.T) State { return supersessionSourceState(t, "approval-source", 10) },
				event: derivedCausationEvent(EventAttemptSuperseded, "approval-source", 11),
			},
			{
				name:  "decision obsoleted from amendment approved",
				state: func(t *testing.T) State { return supersessionSourceState(t, "approval-source", 10) },
				event: derivedCausationEvent(EventDecisionObsoleted, "approval-source", 11),
			},
			{
				name:  "decision obsoleted from final movement failed",
				state: func(t *testing.T) State { return terminalMovementFailureSourceState(t, "terminal-source", 10) },
				event: derivedCausationEvent(EventDecisionObsoleted, "terminal-source", 11),
			},
			{
				name:  "decision obsoleted from final movement succeeded",
				state: func(t *testing.T) State { return terminalMovementSuccessSourceState(t, "terminal-source", 10) },
				event: derivedCausationEvent(EventDecisionObsoleted, "terminal-source", 11),
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				if _, err := Apply(test.state(t), test.event); !errors.Is(err, ErrUnsupportedEventType) {
					t.Fatalf("Apply error = %v, want ErrUnsupportedEventType after causation validation", err)
				}
			})
		}
	})
}

func derivedCausationEvent(eventType EventType, causationID string, sequence uint64) Event {
	payload := map[string]any{}
	edit := func(event *Event) {
		event.CausationID, event.Seq = causationID, sequence
	}
	switch eventType {
	case EventMovementCancelled:
		edit = func(event *Event) {
			event.MovementID, event.CausationID, event.Seq = "m1", causationID, sequence
		}
	case EventAttemptCancelled, EventAttemptSuperseded:
		edit = func(event *Event) {
			event.AttemptID, event.CausationID, event.Seq = "a1", causationID, sequence
		}
	case EventDecisionObsoleted:
		payload = map[string]any{"decision_id": "decision-1"}
	}
	return fixtureEvent(eventType, payload, edit)
}

func cancelledSourceState(t *testing.T, eventID string, sequence uint64) State {
	t.Helper()
	source := fixtureEvent(EventRunCancelled, map[string]any{
		"cancelled_movement_ids": []any{"m1"},
		"cancelled_attempt_ids":  []any{"a1"},
		"obsoleted_decision_ids": []any{},
	}, func(event *Event) {
		event.EventID, event.Seq = eventID, sequence
	})
	state, err := Apply(runningAttemptState(t), source)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func supersessionSourceState(t *testing.T, eventID string, sequence uint64) State {
	t.Helper()
	state := runningAttemptState(t)
	prepared := fixtureEvent(EventAmendmentApprovalPrepared, autoPreparePayload(), func(event *Event) {
		event.EventID, event.Seq = "prepare-source", sequence-1
	})
	var err error
	state, err = Apply(state, prepared)
	if err != nil {
		t.Fatal(err)
	}
	approval := autoApprovalEvent()
	approval.EventID, approval.Seq = eventID, sequence
	state, err = Apply(state, approval)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func terminalMovementFailureSourceState(t *testing.T, eventID string, sequence uint64) State {
	t.Helper()
	source := fixtureEvent(EventMovementFailed, map[string]any{
		"reason": "human_gate_rejected", "decision_id": "decision-1", "subject_tree": "git-sha1:tree", "run_failed": true,
	}, func(event *Event) {
		event.EventID, event.Seq = eventID, sequence
		event.MovementID, event.AttemptID = "m1", "a1"
	})
	state, err := Apply(verifyingAttemptState(t), source)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func terminalMovementSuccessSourceState(t *testing.T, eventID string, sequence uint64) State {
	t.Helper()
	source := fixtureEvent(EventMovementSucceeded, movementSucceededPayload(true), func(event *Event) {
		event.EventID, event.Seq = eventID, sequence
		event.MovementID, event.AttemptID = "m1", "a1"
	})
	state, err := Apply(completedAttemptState(t), source)
	if err != nil {
		t.Fatal(err)
	}
	return state
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

func TestPendingPrepareRefusesOrdinaryLifecycleMutation(t *testing.T) {
	state := runningAttemptState(t)
	var err error
	state, err = Apply(state, fixtureEvent(EventAmendmentApprovalPrepared, autoPreparePayload(), nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(state, fixtureEvent(EventMovementReady, map[string]any{}, func(event *Event) { event.MovementID = "m1" }))
	if !errors.Is(err, ErrIllegalTransition) || !strings.Contains(err.Error(), "prepare_pending") {
		t.Fatalf("ordinary lifecycle mutation error = %v, want prepare_pending", err)
	}
	if _, err := Apply(state, fixtureEvent(EventCancelRequested, map[string]any{"requested_by": "cli"}, nil)); err != nil {
		t.Fatalf("cancellation remains permitted while prepare pending: %v", err)
	}
}

func TestPrepareRequiresCurrentObservedAuthorityAndMillisecondDeadline(t *testing.T) {
	for _, mutate := range []func(map[string]any){
		func(payload map[string]any) { payload["observed_authority_epoch"] = float64(1) },
		func(payload map[string]any) { payload["quiesce_deadline"] = "2026-07-26T00:00:00Z" },
	} {
		payload := autoPreparePayload()
		mutate(payload)
		if _, err := Apply(runningAttemptState(t), fixtureEvent(EventAmendmentApprovalPrepared, payload, nil)); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("prepared payload error = %v, want ErrInvalidEvent", err)
		}
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
		"head_movements":         headMovementsPayload("m1"),
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

func TestAmendmentApprovalModeFieldsAreConditional(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "auto requires envelope class", mutate: func(payload map[string]any) { delete(payload, "envelope_class") }},
		{name: "auto forbids decision id", mutate: func(payload map[string]any) { payload["decision_id"] = "decision-1" }},
		{name: "auto forbids envelope evaluation", mutate: func(payload map[string]any) { payload["envelope_evaluation"] = map[string]any{"guard_passed": true} }},
		{name: "auto rejects invalid envelope class", mutate: func(payload map[string]any) { payload["envelope_class"] = "WIDE" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := approvalPayload("auto")
			test.mutate(payload)
			if err := ValidateEvent(fixtureEvent(EventAmendmentApproved, payload, nil)); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("ValidateEvent() error = %v, want ErrInvalidEvent", err)
			}
		})
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "human requires decision id", mutate: func(payload map[string]any) { delete(payload, "decision_id") }},
		{name: "human forbids envelope class", mutate: func(payload map[string]any) { payload["envelope_class"] = "NARROW_PATHS" }},
		{name: "human requires envelope evaluation", mutate: func(payload map[string]any) { delete(payload, "envelope_evaluation") }},
		{name: "human finalization remains unsupported", mutate: func(payload map[string]any) { payload["finalization"] = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := approvalPayload("human")
			test.mutate(payload)
			if err := ValidateEvent(fixtureEvent(EventAmendmentApproved, payload, nil)); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("ValidateEvent() error = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestHumanApprovalBindsPreparedDecision(t *testing.T) {
	state := runningAttemptState(t)
	prepare := autoPreparePayload()
	prepare["mode"] = "human"
	delete(prepare, "envelope_class")
	prepare["decision_id"] = "decision-1"
	var err error
	state, err = Apply(state, fixtureEvent(EventAmendmentApprovalPrepared, prepare, nil))
	if err != nil {
		t.Fatal(err)
	}
	approval := fixtureEvent(EventAmendmentApproved, approvalPayload("human"), func(event *Event) {
		event.ScoreRevision = 2
	})
	if _, err := Apply(state, approval); err != nil {
		t.Fatal(err)
	}
	approval.Payload = mustPayload(t, approvalPayload("human"))
	var payload map[string]any
	if err := json.Unmarshal(approval.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload["decision_id"] = "decision-other"
	approval.Payload = mustPayload(t, payload)
	if _, err := Apply(state, approval); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Apply() error = %v, want ErrInvalidEvent", err)
	}
}

func TestApprovalReconcilesHeadMovements(t *testing.T) {
	state := runningAttemptState(t)
	state, err := Apply(state, fixtureEvent(EventAmendmentApprovalPrepared, autoPreparePayload(), nil))
	if err != nil {
		t.Fatal(err)
	}
	approval := approvalPayload("auto")
	approval["head_movements"] = []any{
		map[string]any{"id": "m1", "initial": "PENDING", "repo_write": false, "has_dependencies": false, "final": false},
		map[string]any{"id": "m2", "initial": "PENDING", "repo_write": true, "has_dependencies": true, "final": true},
	}
	next, err := Apply(state, fixtureEvent(EventAmendmentApproved, approval, func(event *Event) { event.ScoreRevision = 2 }))
	if err != nil {
		t.Fatal(err)
	}
	if next.Movements["m1"] != MovementRunning || next.Movements["m2"] != MovementPending ||
		!slices.Equal(next.MovementOrder, []MovementID{"m1", "m2"}) ||
		next.RepoWriteMovements["m1"] || !next.RepoWriteMovements["m2"] ||
		next.DependencyMovements["m1"] || !next.DependencyMovements["m2"] ||
		next.FinalMovements["m1"] || !next.FinalMovements["m2"] {
		t.Fatalf("reconciled head = %+v", next)
	}
}

func TestApprovalRejectsRemovingSucceededMovement(t *testing.T) {
	state := runningAttemptState(t)
	state.Movements["retired"] = MovementSucceeded
	state, err := Apply(state, fixtureEvent(EventAmendmentApprovalPrepared, autoPreparePayload(), nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(state, autoApprovalEvent()); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("Apply() error = %v, want ErrInvalidEvent", err)
	}
}

func TestApprovalHeadMovementsAreClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		head []any
	}{
		{name: "empty", head: []any{}},
		{name: "duplicate", head: append(headMovementsPayload("m1"), headMovementsPayload("m1")...)},
		{name: "invalid initial", head: []any{map[string]any{"id": "m1", "initial": "READY", "repo_write": false, "has_dependencies": false, "final": false}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := approvalPayload("auto")
			payload["head_movements"] = test.head
			if err := ValidateEvent(fixtureEvent(EventAmendmentApproved, payload, nil)); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("ValidateEvent() error = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestAmendmentApprovedFencedEpochMatchesPreparedObservation(t *testing.T) {
	tests := []struct {
		name       string
		stateEpoch uint64
		observed   uint64
		fenced     uint64
		wantErr    bool
	}{
		{
			name:       "observed_successor",
			stateEpoch: 1,
			observed:   1,
			fenced:     2,
		},
		{
			name:       "prior_epoch_plus_two",
			stateEpoch: 1,
			observed:   1,
			fenced:     3,
			wantErr:    true,
		},
		{
			name:       "fenced_epoch_before_observed_successor",
			stateEpoch: 1,
			observed:   1,
			fenced:     1,
			wantErr:    true,
		},
		{
			name:       "current_epoch_changed_since_prepare",
			stateEpoch: 2,
			observed:   1,
			fenced:     2,
			wantErr:    true,
		},
		{
			name:       "current_epoch_behind_prepared_observation",
			stateEpoch: 0,
			observed:   1,
			fenced:     2,
			wantErr:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := runningAttemptState(t)
			state.Authority.Epoch = test.observed
			preparePayload := autoPreparePayload()
			preparePayload["observed_authority_epoch"] = test.observed
			var err error
			state, err = Apply(state, fixtureEvent(EventAmendmentApprovalPrepared, preparePayload, nil))
			if err != nil {
				t.Fatal(err)
			}
			// The prepare itself must be valid at its observation epoch. These
			// cases exercise the approval-side revalidation against a changed
			// projected epoch without manufacturing an invalid prepare event.
			state.Authority.Epoch = test.stateEpoch

			approval := autoApprovalEvent()
			var approvalPayload map[string]any
			if err := json.Unmarshal(approval.Payload, &approvalPayload); err != nil {
				t.Fatal(err)
			}
			approvalPayload["fenced_epoch"] = test.fenced
			approval.Payload = mustPayload(t, approvalPayload)

			next, err := Apply(state, approval)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidEvent) {
					t.Fatalf("Apply() error = %v, want ErrInvalidEvent", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if next.Authority != (Authority{Epoch: test.fenced}) {
				t.Fatalf("authority = %+v, want epoch %d with no owner", next.Authority, test.fenced)
			}
		})
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

func TestCriterionCompletionValidatesTruncatedStreamsStrictly(t *testing.T) {
	base := verifiedAttemptState(t)
	base, err := Apply(base, fixtureEvent(EventAcceptanceStarted, map[string]any{
		"subject_tree": "git-sha1:tree", "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{"c1"}, "identity_versions": testIdentityVersions(),
	}, func(event *Event) { event.AttemptID = "a1" }))
	if err != nil {
		t.Fatal(err)
	}
	base, err = Apply(base, fixtureEvent(EventCriterionStarted, map[string]any{
		"criterion_id": "c1", "criterion_spec_hash": "sha256:criterion", "subject_tree": "git-sha1:tree", "spawn_failed": true, "identity_versions": testIdentityVersions(),
	}, func(event *Event) { event.AttemptID = "a1" }))
	if err != nil {
		t.Fatal(err)
	}
	for _, streams := range []any{[]any{}, []any{"stdout", "stdout"}, []any{"stdout", "stderr"}, []any{"other"}} {
		payload := map[string]any{"criterion_id": "c1", "criterion_spec_hash": "sha256:criterion", "subject_tree": "git-sha1:tree", "outcome": "ERROR", "error_detail": "spawn_failed", "truncated_streams": streams, "identity_versions": testIdentityVersions()}
		_, err := Apply(base, fixtureEvent(EventCriterionCompleted, payload, func(event *Event) { event.AttemptID = "a1" }))
		if err == nil {
			t.Fatalf("truncated_streams %v was accepted", streams)
		}
	}
	valid := map[string]any{"criterion_id": "c1", "criterion_spec_hash": "sha256:criterion", "subject_tree": "git-sha1:tree", "outcome": "ERROR", "error_detail": "spawn_failed", "truncated_streams": []any{"stderr", "stdout"}, "identity_versions": testIdentityVersions()}
	if _, err := Apply(base, fixtureEvent(EventCriterionCompleted, valid, func(event *Event) { event.AttemptID = "a1" })); err != nil {
		t.Fatalf("valid truncated streams rejected: %v", err)
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
	return runningAttemptStateWithFinality(t, true)
}

func attemptStartingState(t *testing.T, hasDependencies bool) State {
	t.Helper()
	state := NewState([]MovementSeed{{
		ID: "m1", Initial: MovementPending, HasDependencies: hasDependencies,
	}})
	var err error
	for _, event := range []Event{
		fixtureEvent(EventRunStarted, runStartedPayload(), nil),
		fixtureEvent(EventMovementReady, map[string]any{}, func(event *Event) { event.MovementID = "m1" }),
		fixtureEvent(EventMovementStarted, map[string]any{}, func(event *Event) { event.MovementID = "m1" }),
		fixtureEvent(EventPerformerSelected, performerSelectedPayload(), func(event *Event) {
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

func attemptStartedEvent(payload map[string]any) Event {
	return fixtureEvent(EventAttemptStarted, payload, func(event *Event) {
		event.MovementID = "m1"
		event.AttemptID = "a1"
	})
}

func runningAttemptStateWithFinality(t *testing.T, final bool) State {
	t.Helper()
	state := NewState([]MovementSeed{{ID: "m1", Initial: MovementPending, Final: final}})
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

func changeSetPayload() map[string]any {
	return map[string]any{
		"change_set_id":     "change-set-1",
		"base_tree":         "git-sha1:base",
		"result_tree":       "git-sha1:result",
		"commit":            "git-sha1:commit",
		"ref":               "refs/partitur/runs/run-1/attempts/a1/changeset",
		"identity_versions": testIdentityVersions(),
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
	return fixtureEvent(EventAmendmentApproved, approvalPayload("auto"), func(event *Event) {
		event.ScoreRevision = 2
	})
}

func approvalPayload(mode string) map[string]any {
	payload := map[string]any{
		"proposal_id": "proposal-1", "mode": mode, "base_revision": 1, "base_hash": "sha256:score-1", "classifier_version": 1,
		"new_revision": 2, "new_snapshot_hash": "sha256:score-2", "new_snapshot_file_hash": "sha256:file-2",
		"typed_delta": []any{}, "actual_impact": emptyActualImpact(), "head_movements": headMovementsPayload("m1"), "superseded_attempt_ids": []any{"a1"},
		"obsoleted_decision_ids": []any{}, "finalization": false, "identity_versions": testIdentityVersions(),
	}
	if mode == "auto" {
		payload["envelope_class"] = "NARROW_PATHS"
	} else {
		payload["decision_id"] = "decision-1"
		payload["envelope_evaluation"] = map[string]any{"guard_passed": true}
	}
	return payload
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

func headMovementsPayload(ids ...string) []any {
	values := make([]any, len(ids))
	for index, id := range ids {
		values[index] = map[string]any{
			"id": id, "initial": "PENDING", "repo_write": false, "has_dependencies": false, "final": false,
		}
	}
	return values
}

func amendmentRejectedPayload() map[string]any {
	return map[string]any{
		"proposal_id": "proposal-1", "reason": "patch_error", "base_revision": 1, "base_hash": "sha256:score-1",
		"classifier_version": 1, "patch_operations_hash": "sha256:patch", "error_location": "patch[0]",
		"identity_versions": testIdentityVersions(),
	}
}

func routedAmendmentPayload() map[string]any {
	return map[string]any{
		"proposal_id": "proposal-1", "reason": "auto_disabled", "decision_type": "amendment", "blocking": true,
		"proposal_record_hash": "sha256:proposal", "base_revision": 1, "base_hash": "sha256:score-1",
		"classifier_version": 1, "decision_id": "decision-1", "typed_delta": []any{},
		"actual_impact": emptyActualImpact(), "identity_versions": testIdentityVersions(),
	}
}

func compositionConflictedEvent() Event {
	return fixtureEvent(EventCompositionConflicted, map[string]any{
		"scope": "movement", "target_id": "m1", "composition_subject_hash": "sha256:subject", "contributors": []any{},
		"conflicted_paths": []any{"a"}, "composition_algorithm_version": "1", "identity_versions": testIdentityVersions(),
	}, func(event *Event) { event.MovementID = "m1" })
}

func compositionFailedEvent() Event {
	return fixtureEvent(EventCompositionFailed, map[string]any{
		"scope": "movement", "target_id": "m1", "composition_subject_hash": "sha256:subject", "cause": "git_exit", "git_exit_code": 2,
		"diagnostic": "exit 2", "contributors": []any{}, "composition_algorithm_version": "1", "identity_versions": testIdentityVersions(),
	}, func(event *Event) { event.MovementID = "m1" })
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
