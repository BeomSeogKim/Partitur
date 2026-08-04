package runstate

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReadOnlySuccessPathProjectsAllSevenEvents(t *testing.T) {
	state := runningAttemptState(t)
	state = applyFixture(t, state, EventAdapterProbed, adapterProbedPayload(), attemptEnvelope)
	state = applyFixture(t, state, EventArtifactRecorded, artifactRecordedPayload(), attemptEnvelope)
	state = applyFixture(t, state, EventPerformerCompleted, map[string]any{
		"session_hint_stored": false,
	}, attemptEnvelope)
	state = applyFixture(t, state, EventVerificationPassed, map[string]any{}, attemptEnvelope)
	state = applyFixture(
		t,
		state,
		EventApplicationCandidateRecorded,
		applicationCandidatePayload(),
		nil,
	)
	state = applyFixture(t, state, EventAcceptanceStarted, acceptanceStartedPayload(), attemptEnvelope)
	state = applyFixture(t, state, EventCriterionStarted, criterionStartedPayload(), attemptEnvelope)
	state = applyFixture(t, state, EventCriterionCompleted, criterionCompletedPayload(), attemptEnvelope)
	state = applyFixture(
		t,
		state,
		EventAcceptanceEvaluationCompleted,
		acceptanceEvaluationCompletedPayload(),
		attemptEnvelope,
	)
	state = applyFixture(t, state, EventAttemptCompleted, map[string]any{}, attemptEnvelope)
	state = applyFixture(t, state, EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope)

	observation, ok := state.AdapterObservations["a1"]
	if !ok || observation.AdapterVersion != "1" ||
		observation.ExecutionDependencyHash != "sha256:dependency" {
		t.Fatalf("adapter observation = %+v, present=%v", observation, ok)
	}
	artifact, ok := state.Artifacts["report@a1"]
	if !ok || artifact.ContentHash != "sha256:artifact" || artifact.SizeBytes != 12 {
		t.Fatalf("artifact = %+v, present=%v", artifact, ok)
	}
	if !state.VerifiedAttempts["a1"] {
		t.Fatal("verification.passed was not projected")
	}
	if state.ApplicationCandidate == nil ||
		state.ApplicationCandidate.ID != "candidate-1" ||
		state.ApplicationCandidate.Revision != 1 {
		t.Fatalf("application candidate = %+v", state.ApplicationCandidate)
	}
	if !state.Acceptances["a1"].EvaluationCompleted {
		t.Fatal("acceptance evaluation was not projected")
	}
	if state.Attempts["a1"].State != AttemptCompleted ||
		state.Movements["m1"] != MovementSucceeded ||
		state.Run != RunSucceeded {
		t.Fatalf(
			"terminal projection: attempt=%s movement=%s run=%s",
			state.Attempts["a1"].State,
			state.Movements["m1"],
			state.Run,
		)
	}
	if got := state.MovementResults["m1"]; !reflect.DeepEqual(
		got.ApprovedArtifactInstanceIDs,
		[]ArtifactInstanceID{"report@a1"},
	) {
		t.Fatalf("movement result = %+v", got)
	}
}

func TestRunSuccessPathsUseMutuallyExclusiveEvents(t *testing.T) {
	// This subtest pins the projector's acceptance of the event shape, not a
	// waived runtime path: it pre-records a candidate, whereas §8 gives an
	// active waived run none until run.succeeded folds it in. This subtest does
	// not pin the live or recovery waived scheduler path.
	t.Run("projector accepts candidate-carrying run success after a non-final movement success", func(t *testing.T) {
		state := completedAttemptStateWithFinality(t, false)
		state = applyFixture(t, state, EventMovementSucceeded, movementSucceededPayload(false), attemptEnvelope)
		state = applyFixture(t, state, EventRunSucceeded, runSucceededPayload(), nil)
		if state.Run != RunSucceeded {
			t.Fatalf("run = %s, want %s", state.Run, RunSucceeded)
		}
	})

	t.Run("non-waived final movement carries the only run success transition", func(t *testing.T) {
		state := completedAttemptStateWithFinality(t, true)
		state = applyFixture(t, state, EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope)
		if state.Run != RunSucceeded {
			t.Fatalf("run = %s, want %s", state.Run, RunSucceeded)
		}
	})
}

func TestAdapterProbedGuards(t *testing.T) {
	event := fixtureEvent(EventAdapterProbed, adapterProbedPayload(), attemptEnvelope)

	t.Run("requires running attempt", func(t *testing.T) {
		state := runningAttemptState(t)
		attempt := state.Attempts["a1"]
		attempt.State = AttemptStarting
		state.Attempts["a1"] = attempt
		assertTransitionRejected(t, state, event)
	})

	t.Run("rejects prior observation", func(t *testing.T) {
		assertTransitionRejected(t, probedAttemptState(t), event)
	})
}

func TestPerformerCompletedRequiresAdapterProbed(t *testing.T) {
	event := fixtureEvent(EventPerformerCompleted, map[string]any{
		"session_hint_stored": false,
	}, attemptEnvelope)
	assertTransitionRejected(t, runningAttemptState(t), event)
}

func TestArtifactRecordedGuards(t *testing.T) {
	event := fixtureEvent(EventArtifactRecorded, artifactRecordedPayload(), attemptEnvelope)

	t.Run("running attempt requires probe", func(t *testing.T) {
		assertTransitionRejected(t, runningAttemptState(t), event)
	})

	t.Run("attempt must belong to movement", func(t *testing.T) {
		state := probedAttemptState(t)
		event := event
		event.MovementID = "other"
		assertInvalidRejected(t, state, event)
	})

	t.Run("rejects duplicate instance", func(t *testing.T) {
		state := applyFixture(
			t,
			probedAttemptState(t),
			EventArtifactRecorded,
			artifactRecordedPayload(),
			attemptEnvelope,
		)
		assertTransitionRejected(t, state, event)
	})

	t.Run("verifying attempt does not need a second probe check", func(t *testing.T) {
		state := verifyingAttemptState(t)
		delete(state.AdapterObservations, "a1")
		if _, err := Apply(state, event); err != nil {
			t.Fatalf("artifact from VERIFYING: %v", err)
		}
	})
}

func TestVerificationPassedGuards(t *testing.T) {
	event := fixtureEvent(EventVerificationPassed, map[string]any{}, attemptEnvelope)

	t.Run("requires verifying attempt", func(t *testing.T) {
		assertTransitionRejected(t, probedAttemptState(t), event)
	})

	t.Run("rejects prior verification", func(t *testing.T) {
		assertTransitionRejected(t, verifiedAttemptState(t), event)
	})
}

func TestApplicationCandidateRecordedGuards(t *testing.T) {
	event := fixtureEvent(
		EventApplicationCandidateRecorded,
		applicationCandidatePayload(),
		nil,
	)

	t.Run("requires running run", func(t *testing.T) {
		state := NewState(nil)
		assertTransitionRejected(t, state, event)
	})

	t.Run("requires every repo write movement succeeded", func(t *testing.T) {
		state := NewState([]MovementSeed{
			{ID: "writer", Initial: MovementPending, RepoWrite: true},
		})
		state.Run = RunRunning
		state.Movements["writer"] = MovementRunning
		assertTransitionRejected(t, state, event)
	})

	t.Run("inapplicable repo write movement is not instantiated", func(t *testing.T) {
		state := NewState([]MovementSeed{
			{ID: "writer", Initial: MovementInapplicable, RepoWrite: true},
		})
		state.Run = RunRunning
		if _, err := Apply(state, event); err != nil {
			t.Fatalf("candidate with inapplicable writer: %v", err)
		}
	})

	t.Run("rejects prior candidate", func(t *testing.T) {
		state := runningAttemptState(t)
		state = applyFixture(
			t,
			state,
			EventApplicationCandidateRecorded,
			applicationCandidatePayload(),
			nil,
		)
		assertTransitionRejected(t, state, event)
	})
}

func TestCandidateCompositionEnvironmentPayloadGuards(t *testing.T) {
	merged := func() map[string]any {
		payload := applicationCandidatePayload()
		payload["contributors"] = []any{map[string]any{"movement_id": "writer", "change_set_id": "sha256:change"}}
		payload["composition_environment_hash"] = "sha256:environment"
		return payload
	}
	assertInvalid := func(t *testing.T, eventType EventType, payload map[string]any, want string) {
		t.Helper()
		if _, err := validatePayload(fixtureEvent(eventType, payload, nil)); !errors.Is(err, ErrInvalidEvent) || !strings.Contains(err.Error(), want) {
			t.Fatalf("validate payload error = %v, want %q", err, want)
		}
	}

	t.Run("merged candidate requires environment hash", func(t *testing.T) {
		payload := merged()
		delete(payload, "composition_environment_hash")
		assertInvalid(t, EventApplicationCandidateRecorded, payload, "merged candidate requires composition_environment_hash")
	})

	t.Run("identity candidate forbids environment hash", func(t *testing.T) {
		payload := applicationCandidatePayload()
		payload["composition_environment_hash"] = "sha256:environment"
		assertInvalid(t, EventApplicationCandidateRecorded, payload, "identity candidate forbids composition_environment_hash")
	})

	t.Run("merged candidate rejects empty environment hash", func(t *testing.T) {
		payload := merged()
		payload["composition_environment_hash"] = ""
		assertInvalid(t, EventApplicationCandidateRecorded, payload, "composition_environment_hash must be non-empty")
	})

	t.Run("folded candidate has the same guard", func(t *testing.T) {
		payload := runSucceededPayload()
		candidate := payload["candidate"].(map[string]any)
		candidate["contributors"] = []any{map[string]any{"movement_id": "writer", "change_set_id": "sha256:change"}}
		assertInvalid(t, EventRunSucceeded, payload, "merged candidate requires composition_environment_hash")
	})
}

func TestRunSucceededProjectsRecordedCompositionEnvironment(t *testing.T) {
	payload := runSucceededPayload()
	candidate := payload["candidate"].(map[string]any)
	candidate["contributors"] = []any{map[string]any{"movement_id": "writer", "change_set_id": "sha256:change"}}
	candidate["composition_environment_hash"] = "sha256:environment"

	state := NewState(nil)
	state.Run = RunRunning
	next, err := Apply(state, fixtureEvent(EventRunSucceeded, payload, nil))
	if err != nil {
		t.Fatal(err)
	}
	if next.ApplicationCandidate == nil || next.ApplicationCandidate.CompositionEnvironmentHash != "sha256:environment" {
		t.Fatalf("recorded environment hash = %#v", next.ApplicationCandidate)
	}
}

func TestAcceptanceStartedRequiresVerificationPassed(t *testing.T) {
	event := fixtureEvent(EventAcceptanceStarted, acceptanceStartedPayload(), attemptEnvelope)
	assertTransitionRejected(t, verifyingAttemptState(t), event)
}

func TestAcceptanceEvaluationCompletedGuards(t *testing.T) {
	event := fixtureEvent(
		EventAcceptanceEvaluationCompleted,
		acceptanceEvaluationCompletedPayload(),
		attemptEnvelope,
	)

	t.Run("requires all planned criteria completed with pass", func(t *testing.T) {
		state := acceptanceStartedState(t)
		assertInvalidRejected(t, state, event)
	})

	t.Run("requires matching acceptance binding", func(t *testing.T) {
		state := completedCriteriaState(t)
		payload := acceptanceEvaluationCompletedPayload()
		payload["subject_tree"] = "git-sha1:other"
		assertInvalidRejected(
			t,
			state,
			fixtureEvent(EventAcceptanceEvaluationCompleted, payload, attemptEnvelope),
		)
	})

	t.Run("rejects prior evaluation", func(t *testing.T) {
		state := evaluatedAcceptanceState(t)
		assertTransitionRejected(t, state, event)
	})
}

func TestAttemptCompletedRequiresAcceptanceEvaluation(t *testing.T) {
	event := fixtureEvent(EventAttemptCompleted, map[string]any{}, attemptEnvelope)
	t.Run("requires verifying attempt", func(t *testing.T) {
		state := evaluatedAcceptanceState(t)
		attempt := state.Attempts["a1"]
		attempt.State = AttemptRunning
		state.Attempts["a1"] = attempt
		assertTransitionRejected(t, state, event)
	})
	t.Run("requires completed evaluation", func(t *testing.T) {
		assertTransitionRejected(t, acceptanceStartedState(t), event)
	})
}

func TestMovementSucceededGuards(t *testing.T) {
	t.Run("requires running run", func(t *testing.T) {
		state := completedAttemptState(t)
		state.Run = RunFailed
		event := fixtureEvent(EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope)
		assertTransitionRejected(t, state, event)
	})

	t.Run("requires running movement", func(t *testing.T) {
		state := completedAttemptState(t)
		state.Movements["m1"] = MovementReady
		event := fixtureEvent(EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope)
		assertTransitionRejected(t, state, event)
	})

	t.Run("requires completed attempt", func(t *testing.T) {
		state := completedAttemptState(t)
		attempt := state.Attempts["a1"]
		attempt.State = AttemptVerifying
		state.Attempts["a1"] = attempt
		event := fixtureEvent(EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope)
		assertTransitionRejected(t, state, event)
	})

	t.Run("attempt must belong to movement", func(t *testing.T) {
		state := completedAttemptState(t)
		attempt := state.Attempts["a1"]
		attempt.MovementID = "other"
		state.Attempts["a1"] = attempt
		event := fixtureEvent(EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope)
		assertInvalidRejected(t, state, event)
	})

	t.Run("approved artifacts must match attempt", func(t *testing.T) {
		state := completedAttemptState(t)
		payload := movementSucceededPayload(true)
		payload["approved_artifact_instance_ids"] = []any{}
		assertInvalidRejected(
			t,
			state,
			fixtureEvent(EventMovementSucceeded, payload, attemptEnvelope),
		)
	})

	t.Run("change set presence must match repo write", func(t *testing.T) {
		state := completedAttemptState(t)
		payload := movementSucceededPayload(true)
		payload["approved_change_set_id"] = "change-set-1"
		assertInvalidRejected(
			t,
			state,
			fixtureEvent(EventMovementSucceeded, payload, attemptEnvelope),
		)
	})

	t.Run("run succeeded must match final movement", func(t *testing.T) {
		state := completedAttemptState(t)
		event := fixtureEvent(EventMovementSucceeded, movementSucceededPayload(false), attemptEnvelope)
		assertInvalidRejected(t, state, event)
	})

	t.Run("waived score movement never carries the run transition", func(t *testing.T) {
		state := completedAttemptStateWithFinality(t, false)
		event := fixtureEvent(EventMovementSucceeded, movementSucceededPayload(false), attemptEnvelope)
		next, err := Apply(state, event)
		if err != nil {
			t.Fatalf("waived movement.succeeded: %v", err)
		}
		if next.Run != RunRunning {
			t.Fatalf("run = %s, want %s", next.Run, RunRunning)
		}
	})

	t.Run("non-final movement cannot carry the run transition", func(t *testing.T) {
		state := completedAttemptStateWithFinality(t, false)
		event := fixtureEvent(EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope)
		assertInvalidRejected(t, state, event)
	})

	t.Run("final movement requires candidate", func(t *testing.T) {
		state := completedAttemptState(t)
		state.ApplicationCandidate = nil
		event := fixtureEvent(EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope)
		assertTransitionRejected(t, state, event)
	})
}

func TestSuccessPathPayloadValidation(t *testing.T) {
	tests := []struct {
		name  string
		event Event
	}{
		{
			name: "adapter probed capabilities required",
			event: func() Event {
				payload := adapterProbedPayload()
				delete(payload, "capabilities")
				return fixtureEvent(EventAdapterProbed, payload, attemptEnvelope)
			}(),
		},
		{
			name: "adapter probed truncation is required",
			event: func() Event {
				payload := adapterProbedPayload()
				delete(payload, "truncated_resolutions")
				return fixtureEvent(EventAdapterProbed, payload, attemptEnvelope)
			}(),
		},
		{
			name: "adapter probed rejects retired withheld resolutions",
			event: func() Event {
				payload := adapterProbedPayload()
				payload["withheld_resolutions"] = []any{}
				return fixtureEvent(EventAdapterProbed, payload, attemptEnvelope)
			}(),
		},
		{
			name: "artifact size is integer",
			event: func() Event {
				payload := artifactRecordedPayload()
				payload["size_bytes"] = "12"
				return fixtureEvent(EventArtifactRecorded, payload, attemptEnvelope)
			}(),
		},
		{
			name: "verification passed is empty",
			event: fixtureEvent(
				EventVerificationPassed,
				map[string]any{"subject_tree": "git-sha1:tree"},
				attemptEnvelope,
			),
		},
		{
			name: "candidate composition projection is present",
			event: func() Event {
				payload := applicationCandidatePayload()
				delete(payload, "candidate_composition_dependency_hash")
				return fixtureEvent(EventApplicationCandidateRecorded, payload, nil)
			}(),
		},
		{
			name: "evaluation contains only pass",
			event: func() Event {
				payload := acceptanceEvaluationCompletedPayload()
				payload["criterion_outcomes"].([]any)[0].(map[string]any)["outcome"] = "FAIL"
				return fixtureEvent(EventAcceptanceEvaluationCompleted, payload, attemptEnvelope)
			}(),
		},
		{
			name: "attempt completed is empty",
			event: fixtureEvent(
				EventAttemptCompleted,
				map[string]any{"completed": true},
				attemptEnvelope,
			),
		},
		{
			name: "movement artifacts are sorted",
			event: fixtureEvent(EventMovementSucceeded, map[string]any{
				"approved_artifact_instance_ids": []any{"z@a1", "a@a1"},
				"identity_versions":              testIdentityVersions(),
				"run_succeeded":                  true,
			}, attemptEnvelope),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateEvent(test.event); !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("error = %v, want ErrInvalidEvent", err)
			}
		})
	}
}

func TestAdapterProbedProjectsDeliveredFeedback(t *testing.T) {
	state := runningAttemptState(t)
	payload := adapterProbedPayload()
	payload["delivered_feedback"] = []any{map[string]any{
		"previous_attempt_id":  "a0",
		"kind":                 "acceptance_report",
		"artifact_instance_id": "report@a0",
		"content_hash":         "sha256:report",
	}}
	state = applyFixture(t, state, EventAdapterProbed, payload, attemptEnvelope)
	if got := state.AdapterObservations["a1"].DeliveredFeedback; !reflect.DeepEqual(got, []DeliveredFeedback{{
		PreviousAttemptID: "a0", Kind: "acceptance_report", ArtifactInstanceID: "report@a0", ContentHash: "sha256:report",
	}}) {
		t.Fatalf("delivered feedback = %#v", got)
	}
}

func TestAdapterProbedProjectsDeliveredResolutions(t *testing.T) {
	state := runningAttemptState(t)
	payload := adapterProbedPayload()
	payload["delivered_resolutions"] = []any{map[string]any{
		"decision_id": "decision-1",
		"kind":        "answer",
		"digest":      "sha256:answer",
	}}
	state = applyFixture(t, state, EventAdapterProbed, payload, attemptEnvelope)
	if got := state.AdapterObservations["a1"].DeliveredResolutions; !reflect.DeepEqual(got, []DeliveredResolution{{
		DecisionID: "decision-1", Kind: "answer", Digest: "sha256:answer",
	}}) {
		t.Fatalf("delivered resolutions = %#v", got)
	}
}

func TestAdapterProbedDeliveredResolutionGuards(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "absent required selection",
			mutate: func(payload map[string]any) {
				delete(payload, "delivered_resolutions")
			},
			want: `required field "delivered_resolutions" is absent`,
		},
		{
			name: "missing tuple member",
			mutate: func(payload map[string]any) {
				payload["delivered_resolutions"] = []any{map[string]any{
					"decision_id": "decision-1",
					"kind":        "answer",
				}}
			},
			want: `delivered_resolutions: entry 0: required field "digest" is absent`,
		},
		{
			name: "malformed tuple member",
			mutate: func(payload map[string]any) {
				payload["delivered_resolutions"] = []any{map[string]any{
					"decision_id": "decision-1",
					"kind":        "answer",
					"digest":      12,
				}}
			},
			want: "delivered_resolutions: entry 0: digest must be a string",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := adapterProbedPayload()
			test.mutate(payload)
			err := ValidateEvent(fixtureEvent(EventAdapterProbed, payload, attemptEnvelope))
			if !errors.Is(err, ErrInvalidEvent) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want invalid event containing %q", err, test.want)
			}
		})
	}
}

func TestAdapterProbedDeliveredFeedbackGuards(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "absent required selection",
			mutate: func(payload map[string]any) {
				delete(payload, "delivered_feedback")
			},
			want: `required field "delivered_feedback" is absent`,
		},
		{
			name: "missing tuple member",
			mutate: func(payload map[string]any) {
				payload["delivered_feedback"] = []any{map[string]any{
					"previous_attempt_id":  "a0",
					"kind":                 "acceptance_report",
					"artifact_instance_id": "report@a0",
				}}
			},
			want: `delivered_feedback: entry 0: required field "content_hash" is absent`,
		},
		{
			name: "malformed tuple member",
			mutate: func(payload map[string]any) {
				payload["delivered_feedback"] = []any{map[string]any{
					"previous_attempt_id":  "a0",
					"kind":                 "acceptance_report",
					"artifact_instance_id": "report@a0",
					"content_hash":         12,
				}}
			},
			want: "delivered_feedback: entry 0: content_hash must be a string",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := adapterProbedPayload()
			test.mutate(payload)
			err := ValidateEvent(fixtureEvent(EventAdapterProbed, payload, attemptEnvelope))
			if !errors.Is(err, ErrInvalidEvent) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want invalid event containing %q", err, test.want)
			}
		})
	}
}

func TestAcceptanceEvaluationRequiresVerifyingAttemptAndOpenEvaluation(t *testing.T) {
	event := fixtureEvent(
		EventAcceptanceEvaluationCompleted,
		acceptanceEvaluationCompletedPayload(),
		attemptEnvelope,
	)
	t.Run("requires verifying attempt", func(t *testing.T) {
		state := completedCriteriaState(t)
		attempt := state.Attempts["a1"]
		attempt.State = AttemptCompleted
		state.Attempts["a1"] = attempt
		assertTransitionRejected(t, state, event)
	})
	t.Run("requires started acceptance", func(t *testing.T) {
		state := completedCriteriaState(t)
		acceptance := state.Acceptances["a1"]
		acceptance.Started = false
		state.Acceptances["a1"] = acceptance
		assertTransitionRejected(t, state, event)
	})
}

func TestArtifactRecordedRequiresLiveAttempt(t *testing.T) {
	state := probedAttemptState(t)
	attempt := state.Attempts["a1"]
	attempt.State = AttemptCompleted
	state.Attempts["a1"] = attempt
	event := fixtureEvent(EventArtifactRecorded, artifactRecordedPayload(), attemptEnvelope)
	assertTransitionRejected(t, state, event)
}

func TestSuccessPathProjectionDoesNotAliasInput(t *testing.T) {
	input := completedAttemptState(t)
	output, err := Apply(
		input,
		fixtureEvent(EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope),
	)
	if err != nil {
		t.Fatal(err)
	}

	output.RepoWriteMovements["new"] = true
	output.AdapterObservations["a1"].Capabilities["repo_write"] = true
	observation := output.AdapterObservations["a1"]
	observation.NegotiatedFeatures = append(observation.NegotiatedFeatures, "new")
	output.AdapterObservations["a1"] = observation
	output.VerifiedAttempts["new"] = true
	output.MovementResults["m1"] = MovementResult{
		ApprovedArtifactInstanceIDs: []ArtifactInstanceID{"changed"},
	}
	output.ApplicationCandidate.OrderedChangeSets = append(
		output.ApplicationCandidate.OrderedChangeSets,
		"changed",
	)

	if input.RepoWriteMovements["new"] ||
		input.AdapterObservations["a1"].Capabilities["repo_write"] ||
		len(input.AdapterObservations["a1"].NegotiatedFeatures) != 0 ||
		input.VerifiedAttempts["new"] ||
		reflect.DeepEqual(
			input.MovementResults["m1"].ApprovedArtifactInstanceIDs,
			[]ArtifactInstanceID{"changed"},
		) ||
		len(input.ApplicationCandidate.OrderedChangeSets) != 0 {
		t.Fatal("success-path projection aliases input")
	}
}

func TestSuccessPathIdempotencyKeys(t *testing.T) {
	tests := []struct {
		event Event
		want  string
	}{
		{fixtureEvent(EventAdapterProbed, adapterProbedPayload(), attemptEnvelope), "a1"},
		{fixtureEvent(EventArtifactRecorded, artifactRecordedPayload(), attemptEnvelope), "report\x00a1"},
		{
			fixtureEvent(
				EventApplicationCandidateRecorded,
				applicationCandidatePayload(),
				nil,
			),
			"candidate-1",
		},
		{fixtureEvent(EventVerificationPassed, map[string]any{}, attemptEnvelope), "a1"},
		{
			fixtureEvent(
				EventAcceptanceEvaluationCompleted,
				acceptanceEvaluationCompletedPayload(),
				attemptEnvelope,
			),
			"a1",
		},
		{fixtureEvent(EventAttemptCompleted, map[string]any{}, attemptEnvelope), "a1"},
		{
			fixtureEvent(EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope),
			"m1\x00a1",
		},
	}
	for _, test := range tests {
		key, err := IdempotencyKey(test.event)
		if err != nil {
			t.Fatalf("%s: %v", test.event.Type, err)
		}
		if key != test.want {
			t.Fatalf("%s key = %q, want %q", test.event.Type, key, test.want)
		}
	}
}

func TestProbeBoundaryPayloadsMovedFromSelectionAndAttemptStart(t *testing.T) {
	if err := ValidateEvent(fixtureEvent(
		EventPerformerSelected,
		performerSelectedPayload(),
		attemptEnvelope,
	)); err != nil {
		t.Fatalf("current performer.selected: %v", err)
	}
	legacySelection := performerSelectedPayload()
	legacySelection["adapter_version"] = "1"
	if err := ValidateEvent(fixtureEvent(
		EventPerformerSelected,
		legacySelection,
		attemptEnvelope,
	)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("legacy performer.selected error = %v", err)
	}

	if err := ValidateEvent(fixtureEvent(
		EventAttemptStarted,
		attemptStartedPayload(),
		attemptEnvelope,
	)); err != nil {
		t.Fatalf("current attempt.started: %v", err)
	}
	legacyAttemptStart := attemptStartedPayload()
	legacyAttemptStart["execution_dependency_hash"] = "sha256:dependency"
	if err := ValidateEvent(fixtureEvent(
		EventAttemptStarted,
		legacyAttemptStart,
		attemptEnvelope,
	)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("legacy attempt.started error = %v", err)
	}
}

func acceptanceStartedState(t *testing.T) State {
	t.Helper()
	return applyFixture(
		t,
		verifiedAttemptState(t),
		EventAcceptanceStarted,
		acceptanceStartedPayload(),
		attemptEnvelope,
	)
}

func completedCriteriaState(t *testing.T) State {
	t.Helper()
	state := acceptanceStartedState(t)
	state = applyFixture(t, state, EventCriterionStarted, criterionStartedPayload(), attemptEnvelope)
	return applyFixture(t, state, EventCriterionCompleted, criterionCompletedPayload(), attemptEnvelope)
}

func evaluatedAcceptanceState(t *testing.T) State {
	t.Helper()
	return applyFixture(
		t,
		completedCriteriaState(t),
		EventAcceptanceEvaluationCompleted,
		acceptanceEvaluationCompletedPayload(),
		attemptEnvelope,
	)
}

func completedAttemptState(t *testing.T) State {
	return completedAttemptStateWithFinality(t, true)
}

func completedAttemptStateWithFinality(t *testing.T, final bool) State {
	t.Helper()
	state := runningAttemptStateWithFinality(t, final)
	state = applyFixture(t, state, EventAdapterProbed, adapterProbedPayload(), attemptEnvelope)
	state = applyFixture(t, state, EventArtifactRecorded, artifactRecordedPayload(), attemptEnvelope)
	state = applyFixture(t, state, EventPerformerCompleted, map[string]any{
		"session_hint_stored": false,
	}, attemptEnvelope)
	state = applyFixture(t, state, EventVerificationPassed, map[string]any{}, attemptEnvelope)
	state = applyFixture(
		t,
		state,
		EventApplicationCandidateRecorded,
		applicationCandidatePayload(),
		nil,
	)
	state = applyFixture(t, state, EventAcceptanceStarted, acceptanceStartedPayload(), attemptEnvelope)
	state = applyFixture(t, state, EventCriterionStarted, criterionStartedPayload(), attemptEnvelope)
	state = applyFixture(t, state, EventCriterionCompleted, criterionCompletedPayload(), attemptEnvelope)
	state = applyFixture(
		t,
		state,
		EventAcceptanceEvaluationCompleted,
		acceptanceEvaluationCompletedPayload(),
		attemptEnvelope,
	)
	return applyFixture(t, state, EventAttemptCompleted, map[string]any{}, attemptEnvelope)
}

func applyFixture(
	t *testing.T,
	state State,
	eventType EventType,
	payload map[string]any,
	edit func(*Event),
) State {
	t.Helper()
	event := fixtureEvent(eventType, payload, edit)
	next, err := Apply(state, event)
	if err != nil {
		t.Fatalf("apply %s: %v", event.Type, err)
	}
	return next
}

func assertTransitionRejected(t *testing.T, state State, event Event) {
	t.Helper()
	if _, err := Apply(state, event); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("error = %v, want ErrIllegalTransition", err)
	}
}

func assertInvalidRejected(t *testing.T, state State, event Event) {
	t.Helper()
	if _, err := Apply(state, event); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("error = %v, want ErrInvalidEvent", err)
	}
}

func attemptEnvelope(event *Event) {
	event.MovementID = "m1"
	event.AttemptID = "a1"
}

func artifactRecordedPayload() map[string]any {
	return map[string]any{
		"logical_output_id": "report",
		"kind":              "artifact",
		"content_hash":      "sha256:artifact",
		"size_bytes":        12,
		"source_path":       "report.json",
	}
}

func applicationCandidatePayload() map[string]any {
	return map[string]any{
		"candidate_id":                          "candidate-1",
		"base_tree":                             "git-sha1:tree",
		"result_tree":                           "git-sha1:tree",
		"ordered_change_sets":                   []any{},
		"contributors":                          []any{},
		"candidate_composition_dependency_hash": "sha256:candidate-composition",
		"identity_versions":                     testIdentityVersions(),
	}
}

func acceptanceStartedPayload() map[string]any {
	return map[string]any{
		"subject_tree":          "git-sha1:tree",
		"acceptance_spec_hash":  "sha256:acceptance",
		"planned_criterion_ids": []any{"c1"},
		"identity_versions":     testIdentityVersions(),
	}
}

func criterionStartedPayload() map[string]any {
	return map[string]any{
		"criterion_id":        "c1",
		"criterion_spec_hash": "sha256:criterion",
		"subject_tree":        "git-sha1:tree",
		"identity_versions":   testIdentityVersions(),
	}
}

func criterionCompletedPayload() map[string]any {
	return map[string]any{
		"criterion_id":        "c1",
		"criterion_spec_hash": "sha256:criterion",
		"subject_tree":        "git-sha1:tree",
		"outcome":             "PASS",
		"duration_ms":         1,
		"identity_versions":   testIdentityVersions(),
	}
}

func acceptanceEvaluationCompletedPayload() map[string]any {
	return map[string]any{
		"subject_tree":         "git-sha1:tree",
		"acceptance_spec_hash": "sha256:acceptance",
		"criterion_outcomes": []any{
			map[string]any{
				"criterion_id":        "c1",
				"criterion_spec_hash": "sha256:criterion",
				"outcome":             "PASS",
			},
		},
		"identity_versions": testIdentityVersions(),
	}
}

func movementSucceededPayload(runSucceeded bool) map[string]any {
	return map[string]any{
		"approved_artifact_instance_ids": []any{"report@a1"},
		"identity_versions":              testIdentityVersions(),
		"run_succeeded":                  runSucceeded,
	}
}

func runSucceededPayload() map[string]any {
	return map[string]any{
		"candidate": map[string]any{
			"candidate_id":                          "candidate-1",
			"base_tree":                             "git-sha1:tree",
			"result_tree":                           "git-sha1:tree",
			"ordered_change_sets":                   []any{},
			"contributors":                          []any{},
			"candidate_composition_dependency_hash": "sha256:candidate-composition",
		},
		"waiver":            map[string]any{"reason": "fixture waiver"},
		"identity_versions": testIdentityVersions(),
	}
}
