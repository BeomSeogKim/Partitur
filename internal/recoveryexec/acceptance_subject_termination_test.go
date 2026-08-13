package recoveryexec

import (
	"context"
	"errors"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryconsequence"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// unverifiedSubjectRoutes is written out rather than derived because each route
// carries a criterion id that only the planner's own branch decides. The
// coverage assertion below keeps the written table from drifting behind the
// planner.
var unverifiedSubjectRoutes = []struct {
	caseID      recovery.CaseID
	kind        recovery.ActionKind
	criterionID string
}{
	{recovery.CaseIncompleteCriterion, recovery.ActionRecoverIncompleteCriterion, "criterion"},
	{recovery.CaseCriteriaPassed, recovery.ActionVerifyAcceptanceSubject, ""},
	{recovery.CaseHumanGateApproved, recovery.ActionVerifyAcceptanceSubject, ""},
	{recovery.CaseGateFreeCompletion, recovery.ActionVerifyAcceptanceSubject, ""},
	{recovery.CaseFirstCriterion, recovery.ActionVerifyAcceptanceSubject, ""},
	{recovery.CaseNextCriterion, recovery.ActionVerifyAcceptanceSubject, ""},
}

// TestUnverifiedAcceptanceSubjectRouteTableCoversPlanner fails when the planner
// gains a subject-verification route that the termination lock does not cover.
// Without it a new case would still be proved routable by the structural lock
// while escaping the execution error this file exists to pin.
func TestUnverifiedAcceptanceSubjectRouteTableCoversPlanner(t *testing.T) {
	type routeKey struct {
		caseID recovery.CaseID
		kind   recovery.ActionKind
	}
	covered := map[routeKey]bool{}
	for _, route := range unverifiedSubjectRoutes {
		covered[routeKey{caseID: route.caseID, kind: route.kind}] = true
	}
	derived := 0
	for _, route := range plannerStepRoutes(t) {
		if route.step != recovery.StepVerifyAcceptanceSubject {
			continue
		}
		derived++
		if !covered[routeKey{caseID: route.caseID, kind: route.kind}] {
			t.Errorf("planner emits %s but the termination table does not cover it", route)
		}
	}
	if derived != len(unverifiedSubjectRoutes) {
		t.Fatalf("derived %d planner verification routes, table has %d", derived, len(unverifiedSubjectRoutes))
	}
}

// These cases exercise the executor's unexported decision seam. They prove
// every catalogued (case, kind) dispatch rejects an unverified subject without
// replanning. They do not prove that the planner selects those decisions or
// that any preceding step and refresh run in production order.
func TestUnverifiedAcceptanceSubjectRoutesTerminate(t *testing.T) {
	for _, route := range unverifiedSubjectRoutes {
		t.Run(string(route.caseID), func(t *testing.T) {
			replan := errors.New("verification route replanned without changing its input")
			input := recovery.Input{Observations: recovery.Observations{AcceptanceSubject: recovery.SubjectUnverified}}
			executor := &Executor{Driver: &runstore.Driver{}, Load: func(context.Context) (recovery.Input, error) { return input, replan }}
			decision := recovery.Decision{CaseID: route.caseID, Action: &recovery.Action{
				Kind: route.kind, CriterionID: recoveryCriterionID(route.criterionID), Replan: true,
				Steps: []recovery.ActionStep{recovery.StepVerifyAcceptanceSubject},
			}}

			result, err := executor.execute(context.Background(), input, decision)
			if err == nil {
				t.Fatalf("error = nil, want unverified-subject execution error before replan (replans=%d)", result.Replans)
			}
			if errors.Is(err, replan) || result.Replans != 0 {
				t.Fatalf("route did not terminate at verification: error=%v replans=%d", err, result.Replans)
			}
			assertReachedVerification(t, err)
		})
	}
}

// assertReachedVerification separates the two ways a route can end in an error.
// Pinning termination alone is not enough: before this PR the shadowed routes
// also produced an error with zero replans, because the catalog refused to
// dispatch them at all. Requiring the error to come from verification rather
// than from routing is what makes these cases fail against the unfixed catalog.
// The refusal is identified by its sentinel rather than by its wording, since
// five of the six routes never sweep and the message says otherwise.
func assertReachedVerification(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, recoveryconsequence.ErrInvalidAction) || errors.Is(err, recoveryconsequence.ErrUnrecognizedCase) {
		t.Fatalf("route was refused by the catalog instead of reaching verification: %v", err)
	}
}

// These fixture-driven cases enter through Execute, so they prove the planner
// selects the expected route and that execution stops before a replan. RC-RESUME-024
// additionally proves sweep -> reload -> verify ordering. They do not cover the
// other four planner routes' production-state preconditions.
func TestUnverifiedAcceptanceSubjectPlannerRoutesTerminate(t *testing.T) {
	t.Run("RC-RESUME-024 sweep prefix", func(t *testing.T) {
		fixture := resumeCriterionFixture(t)
		defer fixture.driver.Release()
		startFixtureCriterion(t, fixture, "second")

		loads := 0
		executor := &Executor{
			Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID,
			Load: persistentUnverifiedFixtureLoader(t, fixture, &loads),
		}
		result, err := executor.Execute(context.Background())
		if err == nil {
			t.Fatal("error = nil, want unverified-subject execution error")
		}
		assertReachedVerification(t, err)
		if result.Decision.CaseID != recovery.CaseIncompleteCriterion || result.Decision.Action == nil ||
			result.Decision.Action.Kind != recovery.ActionRecoverIncompleteCriterion {
			t.Fatalf("decision = %+v, want RC-RESUME-024 recovery", result.Decision)
		}
		if loads != 2 || result.Replans != 0 || len(result.Steps) != 1 || result.Steps[0] != recovery.StepSweepCriterionSession {
			t.Fatalf("loads=%d steps=%v replans=%d, want sweep, reload, verification error, and zero replans", loads, result.Steps, result.Replans)
		}
	})

	t.Run("RC-RESUME-033 nil prefix", func(t *testing.T) {
		fixture := resumeCriterionFixture(t)
		defer fixture.driver.Release()

		loads := 0
		executor := &Executor{
			Store: fixture.store, Driver: fixture.driver, RunID: fixture.runID,
			Load: persistentUnverifiedFixtureLoader(t, fixture, &loads),
		}
		result, err := executor.Execute(context.Background())
		if err == nil {
			t.Fatal("error = nil, want unverified-subject execution error")
		}
		assertReachedVerification(t, err)
		if result.Decision.CaseID != recovery.CaseNextCriterion || result.Decision.Action == nil ||
			result.Decision.Action.Kind != recovery.ActionVerifyAcceptanceSubject {
			t.Fatalf("decision = %+v, want RC-RESUME-033 verification", result.Decision)
		}
		if loads != 1 || result.Replans != 0 || len(result.Steps) != 0 {
			t.Fatalf("loads=%d steps=%v replans=%d, want direct verification error and zero replans", loads, result.Steps, result.Replans)
		}
	})
}

func startFixtureCriterion(t *testing.T, fixture resumeCriterionFixtureState, criterionID runstate.CriterionID) {
	t.Helper()
	state, err := fixture.driver.State()
	if err != nil {
		t.Fatal(err)
	}
	acceptance := state.Acceptances[fixture.attemptID]
	_, err = fixture.driver.Append(runstate.Event{
		RunID: fixture.runID, ScoreRevision: 1, MovementID: "write", PartID: "writer", AttemptID: fixture.attemptID,
		Type: runstate.EventCriterionStarted,
		Payload: handlerPayload(t, map[string]any{
			"criterion_id": criterionID, "criterion_spec_hash": "sha256:criterion",
			"subject_tree":      acceptance.SubjectTree,
			"identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}},
		}),
	}, faultpoint.ReceiptAddress("test.unverified_subject.criterion_started"))
	if err != nil {
		t.Fatal(err)
	}
}

func persistentUnverifiedFixtureLoader(t *testing.T, fixture resumeCriterionFixtureState, loads *int) LoadInput {
	t.Helper()
	return func(context.Context) (recovery.Input, error) {
		*loads = *loads + 1
		loaded, err := fixture.store.LoadRunInput(fixture.runID)
		if err != nil {
			return recovery.Input{}, err
		}
		return recovery.Input{
			Projection: loaded.Projection,
			Observations: recovery.Observations{
				AcceptanceSubject: recovery.SubjectUnverified,
				Lease: recovery.LeaseObservation{
					Exists: true, Readable: true, Epoch: loaded.Projection.State.Authority.Epoch, Owner: recovery.OwnerCurrentDriver,
				},
			},
		}, nil
	}
}

func recoveryCriterionID(value string) runstate.CriterionID {
	return runstate.CriterionID(value)
}
