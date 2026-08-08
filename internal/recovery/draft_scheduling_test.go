package recovery

import (
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestPlanBetweenUnitDraftRefusesNonInterviewSelection(t *testing.T) {
	projection := Projection{
		State: runstate.NewState([]runstate.MovementSeed{
			{ID: "ordinary", Initial: runstate.MovementPending},
			{ID: "interview", Initial: runstate.MovementPending},
		}),
		Scheduler: Scheduler{
			Status: "draft", DraftInterviewMovement: "interview", RemainingTime: 1,
			Movements: []ScheduledMovement{{ID: "ordinary"}, {ID: "interview"}},
		},
	}

	decision := PlanBetweenUnit(projection)
	assertDecision(t, decision, CaseScheduler, ActionAppendMovementReady, "", true)
	if decision.Action.MovementID != "interview" {
		t.Fatalf("ready movement = %q, want interview", decision.Action.MovementID)
	}

	projection.State.Movements["ordinary"] = runstate.MovementReady
	projection.State.Movements["interview"] = runstate.MovementReady
	decision = PlanBetweenUnit(projection)
	assertDecision(t, decision, CaseScheduler, ActionAppendMovementStarted, "", true)
	if decision.Action.MovementID != "interview" {
		t.Fatalf("started movement = %q, want interview", decision.Action.MovementID)
	}
}
