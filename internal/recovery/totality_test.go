package recovery

import (
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestPlanTotalOverDeclaredAxes(t *testing.T) {
	type surface uint8
	const (
		surfaceC1 surface = iota + 1
		surfaceC2
		surfaceC3
		surfaceC4
	)
	type spec struct {
		surface surface

		terminal, staleLease, cancelled, openExecution, rootDivergence, missingReference, missingRouted, revisionRestart, compositionTerminal bool

		phase                                       runstate.AttemptState
		probe, failureRealized, blocking, staleHead bool
		question                                    int
		completion                                  int
		handoff                                     HandoffState
		adapterSweep                                SweepState
		worktree                                    WorktreeState

		acceptance          int
		gate                int
		additionalCriterion bool
		subject             SubjectVerification
		criterionSweep      SweepState
		unjournaled         UnjournaledLaunchState

		schedulerState                                                   int
		compositionRecovered, schedulerBudgetExhausted, pendingSuccessor bool
	}
	type axis struct {
		name   string
		values func(spec) []spec
	}
	forSurface := func(want surface, changes ...func(spec) spec) func(spec) []spec {
		return func(input spec) []spec {
			if input.surface != want {
				return []spec{input}
			}
			result := make([]spec, 0, len(changes)+1)
			result = append(result, input)
			for _, change := range changes {
				result = append(result, change(input))
			}
			return result
		}
	}
	axes := []axis{
		{name: "planner surface", values: func(spec) []spec {
			return []spec{
				{surface: surfaceC1},
				{surface: surfaceC2, phase: runstate.AttemptStarting},
				{surface: surfaceC3, phase: runstate.AttemptVerifying},
				{surface: surfaceC4},
			}
		}},
		{name: "run", values: forSurface(surfaceC1, func(input spec) spec { input.terminal = true; return input })},
		{name: "lease", values: forSurface(surfaceC1, func(input spec) spec { input.staleLease = true; return input })},
		{name: "control", values: forSurface(surfaceC1, func(input spec) spec { input.cancelled = true; return input })},
		{name: "open execution interval", values: forSurface(surfaceC1, func(input spec) spec { input.openExecution = true; return input })},
		{name: "root snapshot", values: forSurface(surfaceC1, func(input spec) spec { input.rootDivergence = true; return input })},
		{name: "event-named reference", values: forSurface(surfaceC1, func(input spec) spec { input.missingReference = true; return input })},
		{name: "routed request", values: forSurface(surfaceC1, func(input spec) spec { input.missingRouted = true; return input })},
		{name: "revision restart", values: forSurface(surfaceC1, func(input spec) spec { input.revisionRestart = true; return input })},
		{name: "composition terminal", values: forSurface(surfaceC1, func(input spec) spec { input.compositionTerminal = true; return input })},

		{name: "attempt lifecycle position", values: forSurface(surfaceC2,
			func(input spec) spec { input.phase = runstate.AttemptRunning; return input },
			func(input spec) spec { input.phase = runstate.AttemptVerifying; return input },
			func(input spec) spec { input.phase = runstate.AttemptCompleted; return input },
			func(input spec) spec { input.phase = runstate.AttemptFailed; return input },
			func(input spec) spec { input.phase = runstate.AttemptBlocked; return input },
			func(input spec) spec { input.phase = runstate.AttemptSuperseded; return input },
		)},
		{name: "probe outcome", values: forSurface(surfaceC2, func(input spec) spec { input.probe = true; return input })},
		{name: "failure realization state", values: forSurface(surfaceC2, func(input spec) spec { input.failureRealized = true; return input })},
		{name: "question request presence", values: forSurface(surfaceC2,
			func(input spec) spec { input.question = 1; return input },
			func(input spec) spec { input.question = 2; return input },
		)},
		{name: "blocking-decision state", values: forSurface(surfaceC2, func(input spec) spec { input.blocking = true; return input })},
		{name: "completion boundary", values: forSurface(surfaceC2,
			func(input spec) spec { input.completion = 1; return input },
			func(input spec) spec { input.completion = 2; return input },
			func(input spec) spec { input.completion = 3; return input },
			func(input spec) spec { input.completion = 4; return input },
			func(input spec) spec { input.completion = 5; return input },
		)},
		{name: "handoff observation", values: forSurface(surfaceC2,
			func(input spec) spec { input.handoff = HandoffUnverifiable; return input },
			func(input spec) spec { input.handoff = HandoffSweepFailed; return input },
		)},
		{name: "adapter sweep observation", values: forSurface(surfaceC2, func(input spec) spec { input.adapterSweep = SweepUnverifiable; return input })},
		{name: "worktree observation", values: forSurface(surfaceC2, func(input spec) spec { input.worktree = WorktreeMissing; return input })},

		{name: "acceptance state", values: forSurface(surfaceC3,
			func(input spec) spec { input.acceptance = 1; return input },
			func(input spec) spec { input.acceptance = 2; return input },
			func(input spec) spec { input.acceptance = 3; return input },
			func(input spec) spec { input.acceptance = 4; return input },
			func(input spec) spec { input.acceptance = 5; return input },
		)},
		{name: "human gate state", values: forSurface(surfaceC3,
			func(input spec) spec { input.gate = 1; return input },
			func(input spec) spec { input.gate = 2; return input },
			func(input spec) spec { input.gate = 3; return input },
			func(input spec) spec { input.gate = 4; return input },
		)},
		{name: "criteria plan state", values: forSurface(surfaceC3, func(input spec) spec { input.additionalCriterion = true; return input })},
		{name: "subject-tree verification observation", values: forSurface(surfaceC3,
			func(input spec) spec { input.subject = SubjectMatched; return input },
			func(input spec) spec { input.subject = SubjectMismatched; return input },
		)},
		{name: "criterion sweep observation", values: forSurface(surfaceC3, func(input spec) spec { input.criterionSweep = SweepUnverifiable; return input })},
		{name: "unjournaled launch observation", values: forSurface(surfaceC3,
			func(input spec) spec { input.unjournaled = UnjournaledLaunchUnstabilized; return input },
			func(input spec) spec { input.unjournaled = UnjournaledLaunchMarkerFree; return input },
			func(input spec) spec { input.unjournaled = UnjournaledLaunchHandoffUnverifiable; return input },
			func(input spec) spec { input.unjournaled = UnjournaledLaunchSweepUnverifiable; return input },
		)},
		{name: "current-head scope", values: func(input spec) []spec {
			if input.surface == surfaceC1 || input.surface == surfaceC4 {
				return []spec{input}
			}
			stale := input
			stale.staleHead = true
			return []spec{input, stale}
		}},
		{name: "C.4 compiled lifecycle position", values: forSurface(surfaceC4,
			func(input spec) spec { input.schedulerState = 1; return input },
			func(input spec) spec { input.schedulerState = 2; return input },
			func(input spec) spec { input.schedulerState = 3; return input },
			func(input spec) spec { input.schedulerState = 4; return input },
		)},
		{name: "C.4 recovered composition close", values: forSurface(surfaceC4, func(input spec) spec { input.compositionRecovered = true; return input })},
		{name: "C.4 budget boundary", values: forSurface(surfaceC4, func(input spec) spec { input.schedulerBudgetExhausted = true; return input })},
		{name: "C.4 pending successor", values: forSurface(surfaceC4, func(input spec) spec { input.pendingSuccessor = true; return input })},
	}
	if len(axes) < 29 {
		t.Fatalf("declared recovery axes = %d, want at least 29", len(axes))
	}

	materialize := func(spec spec) Input {
		if spec.surface == surfaceC1 {
			input := baseInput()
			if spec.terminal {
				input = withTerminal(input)
			}
			if spec.staleLease {
				input = withLease(input, LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerLive}, 2)
			}
			if spec.cancelled {
				input = withCancel(input)
			}
			if spec.openExecution {
				input = withOpenExecution(input, "acceptance")
			}
			if spec.rootDivergence {
				input = withRootDivergence(input)
			}
			if spec.missingReference {
				input = withMissingReference(input, ReferenceArtifact)
			}
			if spec.missingRouted {
				input = withMissingRoutedRequest(input)
			}
			if spec.revisionRestart {
				input = withRevisionRestart(input)
			}
			if spec.compositionTerminal {
				input = withCompositionTerminal(input)
			}
			return input
		}
		if spec.surface == surfaceC3 {
			input := c3Input("c1")
			switch spec.acceptance {
			case 1:
				input = withAcceptanceFailure(input)
			case 2:
				input = withCriterion(input, "c1", true, "FAIL")
			case 3:
				input = withCriterion(input, "c1", false, "")
			case 4:
				input = withCriterion(input, "c1", true, "PASS")
			case 5:
				input = withEvaluationCompleted(input)
			}
			if spec.additionalCriterion {
				input = withAdditionalCriterion(input, "c2")
			}
			switch spec.gate {
			case 1:
				input = withRequiredGate(input, false, false, false)
			case 2:
				input = withRequiredGate(input, true, false, false)
			case 3:
				input = withRequiredGate(input, true, true, true)
			case 4:
				input = withRequiredGate(input, true, true, false)
			}
			input = withSubject(input, spec.subject)
			input.Observations.CriterionSweep = spec.criterionSweep
			input = withUnjournaledLaunch(input, spec.unjournaled)
			if spec.staleHead {
				input.Projection.CurrentHeadAttempt.ScoreRevision++
			}
			return input
		}
		if spec.surface == surfaceC4 {
			input := baseInput()
			input.Projection.Scheduler = Scheduler{
				RemainingTime: 1,
				Movements: []ScheduledMovement{
					{ID: "write", RepoWrite: true},
					{ID: "check", Needs: []runstate.MovementID{"write"}, Final: true},
				},
			}
			input.Projection.State.Movements = map[runstate.MovementID]runstate.MovementState{
				"write": runstate.MovementPending,
				"check": runstate.MovementPending,
			}
			switch spec.schedulerState {
			case 1:
				input.Projection.State.Movements["write"] = runstate.MovementReady
			case 2:
				input.Projection.State.Movements["write"] = runstate.MovementRunning
			case 3:
				input.Projection.State.Movements["write"] = runstate.MovementSucceeded
			case 4:
				input.Projection.Scheduler.GateWaived = true
				input.Projection.Scheduler.Movements = []ScheduledMovement{{ID: "write"}}
				input.Projection.State.Movements = map[runstate.MovementID]runstate.MovementState{"write": runstate.MovementSucceeded}
			}
			if spec.compositionRecovered {
				input.Projection.CompositionRecovery = &CompositionRecovery{Scope: "movement", MovementID: "write", Recovered: true}
			}
			if spec.schedulerBudgetExhausted {
				input.Projection.Scheduler.RemainingTime = 0
			}
			if spec.pendingSuccessor {
				input.Projection.Scheduler.PendingSuccessor = &PendingSuccessor{MovementID: "write", Reason: "quality_retry"}
			}
			return input
		}
		input := c2Input(spec.phase)
		attempt := input.Projection.CurrentHeadAttempt
		if spec.phase == runstate.AttemptFailed {
			input = withFailedAttempt(input, spec.failureRealized)
		} else {
			attempt.FailureDispositionRealized = spec.failureRealized
		}
		if spec.probe {
			input = withAdapterProbe(input)
		}
		switch spec.question {
		case 1:
			input = withQuestionRequests(input, false, false)
		case 2:
			input = withQuestionRequests(input, true, false)
		}
		if spec.blocking {
			input.Projection.State.PendingDecisions["blocking"] = runstate.PendingDecision{ID: "blocking", AttemptID: attempt.AttemptID, Blocking: true}
		}
		switch spec.completion {
		case 1:
			input = withRepoWrite(input)
		case 2:
			input = withChangeSet(input)
		case 3:
			input = withVerificationPassed(input)
		case 4:
			input = withMovementSucceeded(input)
		case 5:
			input = withMovementFailed(input)
		}
		input.Observations.Handoff = spec.handoff
		input.Observations.AdapterSweep = spec.adapterSweep
		input.Observations.Worktree = spec.worktree
		if spec.staleHead {
			input.Projection.CurrentHeadAttempt.ScoreRevision++
		}
		return input
	}

	count := 0
	seen := map[CaseID]bool{}
	var expand func(int, spec)
	expand = func(index int, input spec) {
		if index == len(axes) {
			count++
			materialized := materialize(input)
			var decision Decision
			switch input.surface {
			case surfaceC1:
				decision = Plan(materialized)
			case surfaceC2:
				decision = PlanAttempt(materialized)
			case surfaceC3:
				decision = PlanAcceptance(materialized)
			case surfaceC4:
				decision = PlanScheduler(materialized)
			default:
				t.Fatalf("unknown planner surface %d", input.surface)
			}
			if !decision.Valid() {
				t.Fatalf("invalid decision for declared input %d: %+v", count, decision)
			}
			seen[decision.CaseID] = true
			return
		}
		for _, next := range axes[index].values(input) {
			expand(index+1, next)
		}
	}
	expand(0, spec{})
	if count < 28000 {
		t.Fatalf("declared recovery combinations = %d, want at least 28000", count)
	}
	for _, caseID := range []CaseID{
		CaseOpenExecution, CaseTerminal, CaseStaleLease,
		CaseUnstartedAttempt, CaseIncompleteAttempt, CasePostHocVerification,
		CaseFirstCriterion, CaseCriteriaPassed, CaseHumanGateApproved, CaseUnjournaledLaunch,
		CaseBudgetExhausted, CaseRecoveredComposition, CaseScheduler,
	} {
		if !seen[caseID] {
			t.Fatalf("declared recovery axes did not select %s", caseID)
		}
	}
}
