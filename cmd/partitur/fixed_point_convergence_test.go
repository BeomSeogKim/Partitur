package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestFixedPointRecoveryClassificationAcceptsDeclaredBranches(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture fixedPointFixture
		state   runstate.State
	}{
		{
			name:    "waiting_human",
			fixture: fixedPointNoneFixture,
			state:   fixedPointProjectionState(runstate.RunWaitingHuman, runstate.ApplicationApplied, runstate.PromotionPromoted),
		},
		{
			name:    "ordinary_convergence",
			fixture: fixedPointNoneFixture,
			state:   fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplied, runstate.PromotionPromoted),
		},
		{
			name:    "declared_application_recovery",
			fixture: fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryApplication},
			state:   fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationRecoveryRequired, runstate.PromotionPromoted),
		},
		{
			name:    "declared_promotion_recovery",
			fixture: fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryPromotion},
			state:   fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplied, runstate.PromotionRecoveryRequired),
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if err := fixedPointRecoveryBranchError(test.fixture, test.state); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestFixedPointRecoveryClassificationRejectsUnsettledProjections enumerates
// every state newly admitted by declaring command-specific recovery. These are
// direct negative controls for the classified form: the metadata is injected
// separately from the recovered projection, so no projection selects its own
// exception.
func TestFixedPointRecoveryClassificationRejectsUnsettledProjections(t *testing.T) {
	for _, test := range []struct {
		name      string
		fixture   fixedPointFixture
		state     runstate.State
		signature string
	}{
		{
			name:      "waiting_human_declares_command_specific_recovery",
			fixture:   fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryApplication},
			state:     fixedPointProjectionState(runstate.RunWaitingHuman, runstate.ApplicationApplied, runstate.PromotionPromoted),
			signature: "WAITING_HUMAN fixed point declares",
		},
		{
			name:      "nonterminal_run_is_not_a_fixed_point",
			fixture:   fixedPointNoneFixture,
			state:     fixedPointProjectionState(runstate.RunRunning, runstate.ApplicationApplied, runstate.PromotionPromoted),
			signature: "non-halted fixed point lifecycle",
		},
		{
			name:      "undeclared_application_applying",
			fixture:   fixedPointNoneFixture,
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplying, runstate.PromotionPromoted),
			signature: "unsettled application projection",
		},
		{
			name:      "undeclared_application_recovery_required",
			fixture:   fixedPointNoneFixture,
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationRecoveryRequired, runstate.PromotionPromoted),
			signature: "unsettled application projection",
		},
		{
			name:      "undeclared_promotion_recovery_required",
			fixture:   fixedPointNoneFixture,
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplied, runstate.PromotionRecoveryRequired),
			signature: "unsettled promotion projection",
		},
		{
			name:      "undeclared_promotion_promoting",
			fixture:   fixedPointNoneFixture,
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplied, runstate.PromotionPromoting),
			signature: "unsettled promotion projection",
		},
		{
			name:      "declared_application_with_settled_application",
			fixture:   fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryApplication},
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplied, runstate.PromotionPromoted),
			signature: "application recovery declaration requires",
		},
		{
			name:      "declared_application_with_promotion_unsettled",
			fixture:   fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryApplication},
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationRecoveryRequired, runstate.PromotionPromoting),
			signature: "application recovery declaration retains unsettled promotion",
		},
		{
			name:      "declared_application_with_promotion_recovery_required",
			fixture:   fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryApplication},
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationRecoveryRequired, runstate.PromotionRecoveryRequired),
			signature: "application recovery declaration retains unsettled promotion",
		},
		{
			name:      "declared_promotion_with_settled_promotion",
			fixture:   fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryPromotion},
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplied, runstate.PromotionPromoted),
			signature: "promotion recovery declaration requires",
		},
		{
			name:      "declared_promotion_with_application_unsettled",
			fixture:   fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryPromotion},
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplying, runstate.PromotionRecoveryRequired),
			signature: "promotion recovery declaration retains unsettled application",
		},
		{
			name:      "declared_promotion_with_application_recovery_required",
			fixture:   fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryPromotion},
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationRecoveryRequired, runstate.PromotionRecoveryRequired),
			signature: "promotion recovery declaration retains unsettled application",
		},
		{
			name:      "declaration_projection_mismatch",
			fixture:   fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryApplication},
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplied, runstate.PromotionRecoveryRequired),
			signature: "application recovery declaration requires",
		},
		{
			name:      "both_declarations_and_both_projections_unsettled",
			fixture:   fixedPointFixture{commandSpecificRecovery: "application+promotion"},
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationRecoveryRequired, runstate.PromotionRecoveryRequired),
			signature: "unknown command-specific recovery declaration",
		},
		{
			name:      "applying_is_not_recovery_required",
			fixture:   fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryApplication},
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplying, runstate.PromotionPromoted),
			signature: "application recovery declaration requires",
		},
		{
			name:      "promoting_is_not_recovery_required",
			fixture:   fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryPromotion},
			state:     fixedPointProjectionState(runstate.RunSucceeded, runstate.ApplicationApplied, runstate.PromotionPromoting),
			signature: "promotion recovery declaration requires",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := fixedPointRecoveryBranchError(test.fixture, test.state)
			if err == nil || !strings.Contains(err.Error(), test.signature) {
				t.Fatalf("negative control error=%v, want signature %q", err, test.signature)
			}
			t.Logf("negative control measured signature=%q decoded=%v", test.signature, err)
		})
	}
}

func fixedPointProjectionState(run runstate.RunLifecycle, application runstate.ApplicationState, promotion runstate.PromotionState) runstate.State {
	return runstate.State{
		Run:         run,
		Application: runstate.ApplicationProjection{State: application},
		Promotion:   runstate.PromotionProjection{State: promotion},
	}
}

func TestRecoveryOutcomeImplicationsRequireDurableEffects(t *testing.T) {
	for _, test := range []struct {
		name      string
		events    []runstate.Event
		gateModes map[runstate.MovementID]string
		state     runstate.State
		signature string
	}{
		{
			name:      "completed_attempt_requires_movement_success",
			events:    []runstate.Event{{Type: runstate.EventAttemptCompleted, MovementID: "move", AttemptID: "attempt"}},
			state:     fixedPointAttemptProjection(runstate.MovementSucceeded),
			signature: "completed attempt",
		},
		{
			name: "completed_attempt_requires_projected_movement_success",
			events: []runstate.Event{
				{Type: runstate.EventAttemptCompleted, MovementID: "move", AttemptID: "attempt"},
				{Type: runstate.EventMovementSucceeded, MovementID: "move", AttemptID: "attempt"},
			},
			state:     fixedPointAttemptProjection(runstate.MovementFailed),
			signature: "completed attempt",
		},
		{
			name:      "failed_movement_requires_run_failure",
			events:    []runstate.Event{{Type: runstate.EventMovementFailed, MovementID: "move", AttemptID: "attempt"}},
			state:     fixedPointOutcomeState(runstate.RunSucceeded),
			signature: "failed movement",
		},
		{
			name: "failed_movement_requires_projected_run_failure",
			events: []runstate.Event{
				{Type: runstate.EventMovementFailed, MovementID: "move", AttemptID: "attempt"},
				{Type: runstate.EventRunFailed},
			},
			state:     fixedPointOutcomeState(runstate.RunSucceeded),
			signature: "failed movement",
		},
		{
			name:      "failed_movement_requires_durable_run_failure",
			events:    []runstate.Event{{Type: runstate.EventMovementFailed, MovementID: "move", AttemptID: "attempt"}},
			state:     fixedPointOutcomeState(runstate.RunFailed),
			signature: "failed movement",
		},
		{
			name:      "criterion_error_requires_acceptance_failure",
			events:    []runstate.Event{fixedPointEvent(runstate.EventCriterionCompleted, "move", "attempt", map[string]any{"outcome": "ERROR"})},
			state:     fixedPointOutcomeState(runstate.RunFailed),
			signature: "criterion ERROR",
		},
		{
			name:   "criterion_error_requires_durable_acceptance_failure",
			events: []runstate.Event{fixedPointEvent(runstate.EventCriterionCompleted, "move", "attempt", map[string]any{"outcome": "ERROR"})},
			state: func() runstate.State {
				state := fixedPointOutcomeState(runstate.RunFailed)
				state.Attempts["attempt"] = runstate.Attempt{State: runstate.AttemptFailed, MovementID: "move"}
				return state
			}(),
			signature: "criterion ERROR",
		},
		{
			name: "criterion_error_requires_projected_failed_attempt",
			events: []runstate.Event{
				fixedPointEvent(runstate.EventCriterionCompleted, "move", "attempt", map[string]any{"outcome": "ERROR"}),
				{Type: runstate.EventAcceptanceFailed, MovementID: "move", AttemptID: "attempt"},
			},
			state: func() runstate.State {
				state := fixedPointOutcomeState(runstate.RunFailed)
				state.Attempts["attempt"] = runstate.Attempt{State: runstate.AttemptVerifying, MovementID: "move"}
				return state
			}(),
			signature: "criterion ERROR",
		},
		{
			name: "always_gate_requires_human_gate_request",
			events: []runstate.Event{fixedPointEvent(runstate.EventAcceptanceEvaluationCompleted, "move", "attempt", map[string]any{
				"review_outcome": "",
			})},
			gateModes: map[runstate.MovementID]string{"move": "always"},
			state:     fixedPointOutcomeState(runstate.RunWaitingHuman),
			signature: "completed evaluation",
		},
		{
			name: "completed_evaluation_requires_durable_human_gate_request",
			events: []runstate.Event{fixedPointEvent(runstate.EventAcceptanceEvaluationCompleted, "move", "attempt", map[string]any{
				"review_outcome": "CONTESTED",
			})},
			gateModes: map[runstate.MovementID]string{"move": "on_contested"},
			state: func() runstate.State {
				state := fixedPointOutcomeState(runstate.RunWaitingHuman)
				state.PendingDecisions["decision"] = runstate.PendingDecision{Type: "human_gate", MovementID: "move", AttemptID: "attempt", Blocking: true}
				return state
			}(),
			signature: "completed evaluation",
		},
		{
			name: "completed_evaluation_requires_projected_human_gate",
			events: []runstate.Event{fixedPointEvent(runstate.EventAcceptanceEvaluationCompleted, "move", "attempt", map[string]any{
				"review_outcome": "CONTESTED",
			}), fixedPointEvent(runstate.EventDecisionRequested, "move", "attempt", map[string]any{"decision_type": "human_gate"})},
			gateModes: map[runstate.MovementID]string{"move": "on_contested"},
			state:     fixedPointOutcomeState(runstate.RunWaitingHuman),
			signature: "completed evaluation",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := recoveryOutcomeImplicationsError(test.events, test.gateModes, test.state)
			if err == nil || !strings.Contains(err.Error(), test.signature) {
				t.Fatalf("negative control error=%v, want signature %q", err, test.signature)
			}
			t.Logf("negative control measured signature=%q decoded=%v", test.signature, err)
		})
	}
}

func TestRecoveryOutcomeImplicationsAcceptDurableEffects(t *testing.T) {
	t.Run("completed_attempt_has_movement_success", func(t *testing.T) {
		state := fixedPointOutcomeState(runstate.RunSucceeded)
		state.Attempts["attempt"] = runstate.Attempt{State: runstate.AttemptCompleted, MovementID: "move"}
		state.Movements["move"] = runstate.MovementSucceeded
		events := []runstate.Event{
			{Type: runstate.EventAttemptCompleted, MovementID: "move", AttemptID: "attempt"},
			{Type: runstate.EventMovementSucceeded, MovementID: "move", AttemptID: "attempt"},
		}
		assertNoOutcomeImplicationError(t, events, nil, state)
	})

	t.Run("failed_movement_has_run_failure", func(t *testing.T) {
		state := fixedPointOutcomeState(runstate.RunFailed)
		events := []runstate.Event{
			{Type: runstate.EventMovementFailed, MovementID: "move", AttemptID: "attempt"},
			{Type: runstate.EventRunFailed},
		}
		assertNoOutcomeImplicationError(t, events, nil, state)
	})

	t.Run("criterion_error_has_acceptance_failure", func(t *testing.T) {
		state := fixedPointOutcomeState(runstate.RunFailed)
		state.Attempts["attempt"] = runstate.Attempt{State: runstate.AttemptFailed, MovementID: "move"}
		events := []runstate.Event{
			fixedPointEvent(runstate.EventCriterionCompleted, "move", "attempt", map[string]any{"outcome": "ERROR"}),
			{Type: runstate.EventAcceptanceFailed, MovementID: "move", AttemptID: "attempt"},
		}
		assertNoOutcomeImplicationError(t, events, nil, state)
	})

	t.Run("completed_evaluation_has_human_gate_request", func(t *testing.T) {
		state := fixedPointOutcomeState(runstate.RunWaitingHuman)
		state.PendingDecisions["decision"] = runstate.PendingDecision{Type: "human_gate", MovementID: "move", AttemptID: "attempt", Blocking: true}
		events := []runstate.Event{
			fixedPointEvent(runstate.EventAcceptanceEvaluationCompleted, "move", "attempt", map[string]any{"review_outcome": "CONTESTED"}),
			fixedPointEvent(runstate.EventDecisionRequested, "move", "attempt", map[string]any{"decision_type": "human_gate"}),
		}
		assertNoOutcomeImplicationError(t, events, map[runstate.MovementID]string{"move": "on_contested"}, state)
	})
}

func fixedPointOutcomeState(run runstate.RunLifecycle) runstate.State {
	return runstate.State{
		Run:                run,
		Movements:          make(map[runstate.MovementID]runstate.MovementState),
		Attempts:           make(map[runstate.AttemptID]runstate.Attempt),
		PendingDecisions:   make(map[string]runstate.PendingDecision),
		ResolvedHumanGates: make(map[runstate.AttemptID]runstate.HumanGateResolution),
	}
}

func fixedPointAttemptProjection(movement runstate.MovementState) runstate.State {
	state := fixedPointOutcomeState(runstate.RunSucceeded)
	state.Attempts["attempt"] = runstate.Attempt{State: runstate.AttemptCompleted, MovementID: "move"}
	state.Movements["move"] = movement
	return state
}

func fixedPointEvent(eventType runstate.EventType, movementID runstate.MovementID, attemptID runstate.AttemptID, payload map[string]any) runstate.Event {
	contents, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return runstate.Event{Type: eventType, MovementID: movementID, AttemptID: attemptID, Payload: contents}
}

func assertNoOutcomeImplicationError(t *testing.T, events []runstate.Event, gateModes map[runstate.MovementID]string, state runstate.State) {
	t.Helper()
	if err := recoveryOutcomeImplicationsError(events, gateModes, state); err != nil {
		t.Fatal(err)
	}
}

func TestResumeOwnedResidueEnumeration(t *testing.T) {
	repository := t.TempDir()
	runRoot := filepath.Join(repository, ".partitur", "runs", "run")
	if err := os.MkdirAll(filepath.Join(runRoot, "prepares"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository, ".partitur", "work", "run", "attempt", "criterion-launch"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path := range map[string]string{
		filepath.Join(runRoot, "driver.lease"):               "lease",
		filepath.Join(runRoot, "driver.quiesced.prepare-1"):  "sidecar",
		filepath.Join(runRoot, "prepares", "prepare-1.json"): "prepare",
	} {
		if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	residues, err := resumeOwnedResiduals(repository, "run")
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, residue := range residues {
		got[residue.family] = true
	}
	for _, family := range []string{"lease", "sidecar", "prepare staging", "attempt staging"} {
		if !got[family] {
			t.Fatalf("resume-owned residue family %q was not enumerated: %+v", family, residues)
		}
	}

	// These similarly named paths belong to command-specific recovery or are
	// deliberately malformed/non-resume forms, so the resume enumerator must
	// not make claims about them.
	for _, path := range []string{
		filepath.Join(runRoot, "driver.quiesced."),
		filepath.Join(runRoot, "prepares", "note.txt"),
		filepath.Join(repository, ".partitur.yaml.promote-fixture"),
	} {
		if err := os.WriteFile(path, []byte("out of scope\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	residues, err = resumeOwnedResiduals(repository, "run")
	if err != nil {
		t.Fatal(err)
	}
	for _, residue := range residues {
		if strings.HasSuffix(residue.path, "driver.quiesced.") || strings.HasSuffix(residue.path, "note.txt") || strings.HasSuffix(residue.path, ".partitur.yaml.promote-fixture") {
			t.Fatalf("out-of-scope residue was enumerated: %+v", residue)
		}
	}
}
