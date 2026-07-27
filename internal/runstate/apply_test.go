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
	_, err := Apply(state, fixtureEvent("movement.failed", map[string]any{
		"reason":     "retries_exhausted",
		"run_failed": false,
	}, nil))
	if !errors.Is(err, ErrUnsupportedEventType) {
		t.Fatalf("error = %v, want ErrUnsupportedEventType", err)
	}
	if errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("unsupported valid registry type was classified as invalid: %v", err)
	}
}

func TestEScopedSupportedEventSetHasTwentyNineTypes(t *testing.T) {
	var count int
	for eventType := range registryEvents {
		if isSupportedEvent(eventType) {
			count++
		}
	}
	if count != 29 {
		t.Fatalf("supported event count = %d, want 29", count)
	}
	if isSupportedEvent("movement.cancelled") {
		t.Fatal("derived movement.cancelled must not be accepted as an authoritative event")
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
		"withheld_resolutions":      []any{},
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

func testIdentityVersions() map[string]any {
	return map[string]any{
		"canonical_encoding": 1,
		"projections":        map[string]any{},
	}
}
