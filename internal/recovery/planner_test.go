package recovery

import (
	"slices"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestPlanC4RowsAndSchedulerOrder(t *testing.T) {
	base := func() Input {
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
		return input
	}

	cases := []struct {
		name   string
		input  Input
		caseID CaseID
		kind   ActionKind
	}{
		{
			name: "exhaustion while a movement awaits fan-in fails the movement before the run",
			input: func() Input {
				input := base()
				input.Projection.Scheduler.RemainingTime = 0
				input.Projection.State.Movements["write"] = runstate.MovementRunning
				return input
			}(),
			caseID: CaseBudgetExhausted, kind: ActionAppendBudgetFailure,
		},
		{
			name: "exhaustion between movements fails the run directly",
			input: func() Input {
				input := base()
				input.Projection.Scheduler.RemainingTime = 0
				return input
			}(),
			caseID: CaseBudgetExhausted, kind: ActionAppendRunFailed,
		},
		{
			name: "recovered movement composition is rerun rather than failed",
			input: func() Input {
				input := base()
				input.Projection.State.Movements["write"] = runstate.MovementRunning
				input.Projection.CompositionRecovery = &CompositionRecovery{Scope: "movement", MovementID: "write", Recovered: true}
				return input
			}(),
			caseID: CaseRecoveredComposition, kind: ActionRerunComposition,
		},
		{
			name: "already selected successor materializes before compiled lifecycle",
			input: func() Input {
				input := base()
				input.Projection.Scheduler.PendingSuccessor = &PendingSuccessor{MovementID: "write", Reason: "quality_retry"}
				return input
			}(),
			caseID: CaseScheduler, kind: ActionMaterializeSuccessor,
		},
		{
			name:   "first dependency-satisfied pending movement becomes ready",
			input:  base(),
			caseID: CaseScheduler, kind: ActionAppendMovementReady,
		},
		{
			name: "first ready movement starts",
			input: func() Input {
				input := base()
				input.Projection.State.Movements["write"] = runstate.MovementReady
				return input
			}(),
			caseID: CaseScheduler, kind: ActionAppendMovementStarted,
		},
		{
			name: "running movement with no attempt selects its initial performer",
			input: func() Input {
				input := base()
				input.Projection.State.Movements["write"] = runstate.MovementRunning
				return input
			}(),
			caseID: CaseScheduler, kind: ActionSelectInitialPerformer,
		},
		{
			name: "candidate is recorded before final movement can become ready",
			input: func() Input {
				input := base()
				input.Projection.State.Movements["write"] = runstate.MovementSucceeded
				return input
			}(),
			caseID: CaseScheduler, kind: ActionComposeCandidate,
		},
		{
			name: "final movement becomes ready only after a candidate exists",
			input: func() Input {
				input := base()
				input.Projection.State.Movements["write"] = runstate.MovementSucceeded
				input.Projection.State.ApplicationCandidate = &runstate.ApplicationCandidate{}
				return input
			}(),
			caseID: CaseScheduler, kind: ActionAppendMovementReady,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := PlanScheduler(test.input)
			assertDecision(t, got, test.caseID, test.kind, "", true)
		})
	}
}

func TestPlanBetweenUnitAppliesLifecyclePrecedenceWithoutRecoveryObservations(t *testing.T) {
	base := func() Projection {
		input := baseInput()
		input.Projection.Scheduler = Scheduler{RemainingTime: 1, Movements: []ScheduledMovement{
			{ID: "first"}, {ID: "second"},
		}}
		input.Projection.State.Movements = map[runstate.MovementID]runstate.MovementState{
			"first": runstate.MovementPending, "second": runstate.MovementPending,
		}
		return input.Projection
	}
	tests := []struct {
		name       string
		projection Projection
		caseID     CaseID
		kind       ActionKind
		movement   runstate.MovementID
	}{
		{
			name: "zero budget wins over pending successor and readiness",
			projection: func() Projection {
				projection := base()
				projection.Scheduler.RemainingTime = 0
				projection.Scheduler.PendingSuccessor = &PendingSuccessor{MovementID: "first", AttemptID: "old", Performer: "worker", Reason: "quality_retry"}
				return projection
			}(),
			caseID: CaseBudgetExhausted, kind: ActionAppendRunFailed,
		},
		{
			name: "pending successor wins over ordinary scheduling",
			projection: func() Projection {
				projection := base()
				projection.Scheduler.PendingSuccessor = &PendingSuccessor{MovementID: "second", AttemptID: "old", Performer: "worker", Reason: "quality_retry"}
				return projection
			}(),
			caseID: CaseScheduler, kind: ActionMaterializeSuccessor, movement: "second",
		},
		{
			name:       "declaration order chooses the first eligible movement",
			projection: base(),
			caseID:     CaseScheduler, kind: ActionAppendMovementReady, movement: "first",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := PlanBetweenUnit(test.projection)
			if got.CaseID != test.caseID || got.Action == nil || got.Action.Kind != test.kind || got.Action.MovementID != test.movement {
				t.Fatalf("PlanBetweenUnit() = %+v, want case=%s kind=%s movement=%s", got, test.caseID, test.kind, test.movement)
			}
		})
	}

	if got := PlanBetweenUnit(Projection{
		State:     runstate.State{ApplicationCandidate: &runstate.ApplicationCandidate{}},
		Scheduler: Scheduler{RemainingTime: 1},
	}); got.Valid() || got.Action != nil || got.CaseID != CaseScheduler {
		t.Fatalf("compiled-lifecycle error sentinel = %+v", got)
	}
}

func TestPlanBetweenUnitResumesTheSameDependencySatisfiedMovement(t *testing.T) {
	projection := baseInput().Projection
	projection.Scheduler = Scheduler{RemainingTime: 1, Movements: []ScheduledMovement{
		{ID: "blocked", Needs: []runstate.MovementID{"dependency"}},
		{ID: "eligible"},
		{ID: "dependency"},
	}}
	projection.State.Movements = map[runstate.MovementID]runstate.MovementState{
		"blocked":    runstate.MovementPending,
		"eligible":   runstate.MovementPending,
		"dependency": runstate.MovementSucceeded,
	}

	decision := PlanBetweenUnit(projection)
	if decision.CaseID != CaseScheduler {
		t.Fatalf("case = %s, want %s", decision.CaseID, CaseScheduler)
	}
	if decision.Action == nil {
		t.Fatal("resume decision has no action")
	}
	if decision.Action.Kind != ActionAppendMovementReady {
		t.Fatalf("action = %s, want %s", decision.Action.Kind, ActionAppendMovementReady)
	}
	if decision.Action.MovementID != "blocked" {
		t.Fatalf("movement = %s, want blocked", decision.Action.MovementID)
	}
}

func TestPlanBetweenUnitDoesNotReadyAnUnsucceededDependency(t *testing.T) {
	projection := baseInput().Projection
	projection.Scheduler = Scheduler{RemainingTime: 1, Movements: []ScheduledMovement{
		{ID: "blocked", Needs: []runstate.MovementID{"dependency"}},
		{ID: "eligible"},
		{ID: "dependency"},
	}}
	projection.State.Movements = map[runstate.MovementID]runstate.MovementState{
		"blocked":    runstate.MovementPending,
		"eligible":   runstate.MovementPending,
		"dependency": runstate.MovementPending,
	}

	decision := PlanBetweenUnit(projection)
	if decision.CaseID != CaseScheduler {
		t.Fatalf("case = %s, want %s", decision.CaseID, CaseScheduler)
	}
	if decision.Action == nil {
		t.Fatal("scheduler decision has no action")
	}
	if decision.Action.Kind != ActionAppendMovementReady {
		t.Fatalf("action = %s, want %s", decision.Action.Kind, ActionAppendMovementReady)
	}
	if decision.Action.MovementID != "eligible" {
		t.Fatalf("movement = %s, want eligible", decision.Action.MovementID)
	}
	projection.State.Movements["dependency"] = runstate.MovementWaitingHuman
	decision = PlanBetweenUnit(projection)
	if decision.Action == nil {
		t.Fatal("waiting dependency decision has no action")
	}
	if decision.Action.MovementID != "eligible" {
		t.Fatalf("waiting dependency movement = %s, want eligible", decision.Action.MovementID)
	}
}

func TestPlanC4RecoveredCloseNeverSynthesizesCompositionFailure(t *testing.T) {
	input := baseInput()
	input.Projection.Scheduler = Scheduler{
		RemainingTime: 1,
		Movements:     []ScheduledMovement{{ID: "write", RepoWrite: true}},
	}
	input.Projection.State.Movements = map[runstate.MovementID]runstate.MovementState{"write": runstate.MovementRunning}
	input.Projection.CompositionRecovery = &CompositionRecovery{Scope: "movement", MovementID: "write", Recovered: true}

	got := PlanScheduler(input)
	assertDecision(t, got, CaseRecoveredComposition, ActionRerunComposition, "", true)
	if got.Action.FailureReason != "" || got.Action.Kind == ActionAppendCompositionTerminal {
		t.Fatalf("recovered close synthesized composition failure: %+v", got.Action)
	}
}

func TestPlanC4WaivedCompletionAndC48Boundary(t *testing.T) {
	input := baseInput()
	input.Projection.Scheduler = Scheduler{
		GateWaived: true, RemainingTime: 1,
		Movements: []ScheduledMovement{{ID: "work"}},
	}
	input.Projection.State.Movements = map[runstate.MovementID]runstate.MovementState{"work": runstate.MovementSucceeded}
	got := PlanScheduler(input)
	assertDecision(t, got, CaseScheduler, ActionComposeCandidate, "", true)
	if !got.Action.CandidateCarrying {
		t.Fatal("waived completion must compose the candidate-carrying run.succeeded")
	}

	blocked := c2Input(runstate.AttemptBlocked)
	blocked.Projection.State.PendingDecisions["blocking"] = runstate.PendingDecision{
		ID: "blocking", AttemptID: "attempt", Blocking: true,
	}
	assertDecision(t, PlanAttempt(blocked), CaseWaitingHuman, ActionReturnWaitingHuman, "", false)
}

func TestPlanC1RowsAndAdjacentStates(t *testing.T) {
	cases := []struct {
		name     string
		input    Input
		wantCase CaseID
		wantKind ActionKind
		wantHalt HaltReason
		replan   bool
		adjacent Input
	}{
		{
			name:     "terminal cleanup outranks every other condition",
			input:    withTerminal(baseInput()),
			wantCase: CaseTerminal, wantKind: ActionTerminalCleanup,
			adjacent: baseInput(),
		},
		{
			name:     "stale readable lease",
			input:    withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerLive}, 2),
			wantCase: CaseStaleLease, wantKind: ActionRemoveStaleLease, replan: true,
			adjacent: withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 2, Owner: OwnerLive}, 2),
		},
		{
			name:     "orphan lease",
			input:    withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 2, Owner: OwnerLive}, 1),
			wantCase: CaseOrphanLease, wantKind: ActionQuarantineOrphanLease, replan: true,
			adjacent: withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerLive}, 1),
		},
		{
			name:     "unverifiable current owner",
			input:    withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerUnverifiable}, 1),
			wantCase: CaseOwnerUnverifiable, wantHalt: HaltOwnerUnverifiable,
			adjacent: withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerLive}, 1),
		},
		{
			name:     "current live owner refuses this invocation",
			input:    withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerLive}, 1),
			wantCase: CaseLiveOwner, wantKind: ActionRefuseResume,
			adjacent: withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerDead}, 1),
		},
		{
			name:     "cancellation",
			input:    withCancel(baseInput()),
			wantCase: CaseCancellation, wantKind: ActionExecuteCancellation,
			adjacent: baseInput(),
		},
		{
			name:     "pending prepare",
			input:    withPrepare(baseInput()),
			wantCase: CasePendingPrepare, wantKind: ActionCompleteOrAbandonPrepare,
			adjacent: baseInput(),
		},
		{
			name:     "authority reclaim",
			input:    withAuthority(baseInput(), 1),
			wantCase: CaseReclaimAuthority, wantKind: ActionReclaimAuthority,
			adjacent: baseInput(),
		},
		{
			name:     "open non-adapter interval closes before any consequence",
			input:    withOpenExecution(baseInput(), "acceptance"),
			wantCase: CaseOpenExecution, wantKind: ActionCloseOpenExecutionInterval, replan: true,
			adjacent: withOpenExecution(baseInput(), "adapter"),
		},
		{
			name:     "root snapshot divergence",
			input:    withRootDivergence(baseInput()),
			wantCase: CaseRootSnapshotDivergence, wantHalt: HaltRootSnapshotDivergence,
			adjacent: baseInput(),
		},
		{
			name:     "missing event-named reference",
			input:    withMissingReference(baseInput(), ReferenceArtifact),
			wantCase: CaseMissingReference, wantHalt: HaltMissingArtifactFile,
			adjacent: baseInput(),
		},
		{
			name:     "routed amendment without its request",
			input:    withMissingRoutedRequest(baseInput()),
			wantCase: CaseRoutedAmendment, wantKind: ActionAppendRoutedRequest, replan: true,
			adjacent: withRequestedRoutedAmendment(baseInput()),
		},
		{
			name:     "revision restart",
			input:    withRevisionRestart(baseInput()),
			wantCase: CaseRevisionRestart, wantKind: ActionSelectRevisionRestart,
			adjacent: baseInput(),
		},
		{
			name:     "composition evidence awaiting terminal",
			input:    withCompositionTerminal(baseInput()),
			wantCase: CaseCompositionTerminal, wantKind: ActionAppendCompositionTerminal, replan: true,
			adjacent: baseInput(),
		},
		{
			name:     "otherwise dispatches to C2",
			input:    withCurrentHeadAttempt(baseInput()),
			wantCase: CaseContinue, wantKind: ActionProceedAttempt,
			adjacent: baseInput(),
		},
		{
			name:     "otherwise dispatches to C4",
			input:    baseInput(),
			wantCase: CaseContinue, wantKind: ActionProceedScheduler,
			adjacent: withCurrentHeadAttempt(baseInput()),
		},
	}

	seen := map[CaseID]bool{}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := Plan(test.input)
			assertDecision(t, got, test.wantCase, test.wantKind, test.wantHalt, test.replan)
			if adjacent := Plan(test.adjacent); adjacent.CaseID == test.wantCase &&
				adjacent.Action != nil && adjacent.Action.Kind == test.wantKind && adjacent.Halt == test.wantHalt {
				t.Fatalf("adjacent state selected the same result: %+v", adjacent)
			}
		})
		seen[test.wantCase] = true
	}

	for _, caseID := range []CaseID{
		CaseOpenExecution, CaseTerminal, CaseStaleLease, CaseOrphanLease, CaseOwnerUnverifiable, CaseLiveOwner,
		CaseCancellation, CasePendingPrepare, CaseReclaimAuthority, CaseRootSnapshotDivergence,
		CaseMissingReference, CaseRoutedAmendment, CaseRevisionRestart, CaseCompositionTerminal,
		CaseContinue,
	} {
		if !seen[caseID] {
			t.Fatalf("C.1 case %s has no direct selection test", caseID)
		}
	}
}

func TestPlanC1HaltsUseAppendixDReasons(t *testing.T) {
	cases := []struct {
		name   string
		input  Input
		caseID CaseID
		halt   HaltReason
	}{
		{
			name:   "unreadable lease is never treated as absent",
			input:  withLease(baseInput(), LeaseObservation{Exists: true, Epoch: 1}, 1),
			caseID: CaseOwnerUnverifiable, halt: HaltOwnerUnverifiable,
		},
		{
			name:   "prepare plan takes its specific halt before generic references",
			input:  withMissingReference(withPreparePlanMissing(baseInput()), ReferenceArtifact),
			caseID: CasePendingPrepare, halt: HaltMissingPreparePlan,
		},
		{
			name:   "prepare snapshot has its specific halt",
			input:  withPrepareSnapshotMissing(baseInput()),
			caseID: CasePendingPrepare, halt: HaltMissingSnapshotFile,
		},
		{
			name:   "artifact hash mismatch uses missing artifact halt",
			input:  withMissingReference(baseInput(), ReferenceArtifact),
			caseID: CaseMissingReference, halt: HaltMissingArtifactFile,
		},
		{
			name:   "snapshot hash mismatch uses missing snapshot halt",
			input:  withMissingReference(baseInput(), ReferenceSnapshot),
			caseID: CaseMissingReference, halt: HaltMissingSnapshotFile,
		},
		{
			name:   "change set ref mismatch uses missing ref halt",
			input:  withMissingReference(baseInput(), ReferenceChangeSetRef),
			caseID: CaseMissingReference, halt: HaltMissingChangeSetRef,
		},
		{
			name:   "proposal record mismatch uses proposal halt",
			input:  withMissingReference(baseInput(), ReferenceProposalRecord),
			caseID: CaseMissingReference, halt: HaltMissingProposalRecord,
		},
		{
			name:   "resolved cast mismatch uses resolved cast halt",
			input:  withMissingReference(baseInput(), ReferenceResolvedCast),
			caseID: CaseMissingReference, halt: HaltMissingResolvedCast,
		},
		{
			name:   "root divergence uses its closed halt",
			input:  withRootDivergence(baseInput()),
			caseID: CaseRootSnapshotDivergence, halt: HaltRootSnapshotDivergence,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := Plan(test.input)
			assertDecision(t, got, test.caseID, "", test.halt, false)
		})
	}
}

func TestPlanC1Precedence(t *testing.T) {
	cases := []struct {
		name  string
		input Input
		want  CaseID
	}{
		{
			name:  "terminal outranks stale lease and cancellation",
			input: withCancel(withTerminal(withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1}, 2))),
			want:  CaseTerminal,
		},
		{
			name:  "stale lease outranks cancellation",
			input: withCancel(withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1}, 2)),
			want:  CaseStaleLease,
		},
		{
			name:  "orphan lease outranks cancellation",
			input: withCancel(withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 2}, 1)),
			want:  CaseOrphanLease,
		},
		{
			name:  "unverifiable owner outranks cancellation",
			input: withCancel(withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerUnverifiable}, 1)),
			want:  CaseOwnerUnverifiable,
		},
		{
			name:  "live owner refuses before cancellation",
			input: withCancel(withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerLive}, 1)),
			want:  CaseLiveOwner,
		},
		{
			name:  "cancellation outranks pending prepare",
			input: withCancel(withPrepare(baseInput())),
			want:  CaseCancellation,
		},
		{
			name:  "pending prepare outranks root divergence and generic missing reference",
			input: withRootDivergence(withMissingReference(withPrepare(baseInput()), ReferenceArtifact)),
			want:  CasePendingPrepare,
		},
		{
			name:  "generic recovered close precedes integrity checks outside the control rows",
			input: withRootDivergence(withOpenExecution(baseInput(), "acceptance")),
			want:  CaseOpenExecution,
		},
		{
			name:  "root divergence outranks generic reference corruption",
			input: withRootDivergence(withMissingReference(baseInput(), ReferenceArtifact)),
			want:  CaseRootSnapshotDivergence,
		},
		{
			name:  "missing reference outranks routed amendment",
			input: withMissingReference(withMissingRoutedRequest(baseInput()), ReferenceArtifact),
			want:  CaseMissingReference,
		},
		{
			name:  "revision restart outranks composition terminal",
			input: withCompositionTerminal(withRevisionRestart(baseInput())),
			want:  CaseRevisionRestart,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := Plan(test.input); got.CaseID != test.want {
				t.Fatalf("Plan() selected %s, want first matching %s", got.CaseID, test.want)
			}
		})
	}
}

func TestPlanC1ReplanActionsClearTheirOwnSelectionCut(t *testing.T) {
	cases := []struct {
		name   string
		input  Input
		next   Input
		caseID CaseID
	}{
		{
			name:   "stale lease removal",
			input:  withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1}, 2),
			next:   withAuthority(baseInput(), 2),
			caseID: CaseStaleLease,
		},
		{
			name:   "orphan lease quarantine",
			input:  withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 2}, 1),
			next:   withAuthority(baseInput(), 1),
			caseID: CaseOrphanLease,
		},
		{
			name:   "routed request append",
			input:  withMissingRoutedRequest(baseInput()),
			next:   withRequestedRoutedAmendment(baseInput()),
			caseID: CaseRoutedAmendment,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			first := Plan(test.input)
			if first.CaseID != test.caseID || first.Action == nil || !first.Action.Replan {
				t.Fatalf("first plan = %+v, want %s replan action", first, test.caseID)
			}
			if next := Plan(test.next); next.CaseID == test.caseID {
				t.Fatalf("re-plan repeated %s after its action changed the selection cut: %+v", test.caseID, next)
			}
		})
	}
}

func TestPlanC2RowsAndAdjacentStates(t *testing.T) {
	cases := []struct {
		name     string
		input    Input
		wantCase CaseID
		wantKind ActionKind
		wantHalt HaltReason
		replan   bool
		steps    []ActionStep
		adjacent Input
	}{
		{
			name:     "recorded failed disposition is realized without reclassification",
			input:    withFailedAttempt(c2Input(runstate.AttemptFailed), false),
			wantCase: CaseRealizeDisposition, wantKind: ActionRealizeRecordedDisposition, replan: true,
			adjacent: withFailedAttempt(c2Input(runstate.AttemptFailed), true),
		},
		{
			name:     "first missing raised question request",
			input:    withQuestionRequests(c2Input(runstate.AttemptBlocked), false, true),
			wantCase: CaseAppendQuestionRequest, wantKind: ActionAppendQuestionRequest, replan: true,
			adjacent: withQuestionRequests(c2Input(runstate.AttemptBlocked), true, true),
		},
		{
			name:     "released blocking decision selects decision resume",
			input:    withQuestionRequests(c2Input(runstate.AttemptBlocked), true, false),
			wantCase: CaseDecisionResume, wantKind: ActionSelectDecisionResume,
			adjacent: withQuestionRequests(c2Input(runstate.AttemptBlocked), true, true),
		},
		{
			name:     "unresolved blocking decision remains quiescent",
			input:    withQuestionRequests(c2Input(runstate.AttemptBlocked), true, true),
			wantCase: CaseWaitingHuman, wantKind: ActionReturnWaitingHuman,
			adjacent: withQuestionRequests(c2Input(runstate.AttemptBlocked), true, false),
		},
		{
			name:     "selected attempt stabilizes before recovered close and failure",
			input:    c2Input(runstate.AttemptStarting),
			wantCase: CaseUnstartedAttempt, wantKind: ActionRecoverUnstartedAttempt, replan: true,
			steps:    []ActionStep{StepStabilizeHandoff, StepCloseAdapterInterval, StepClassifyAndAppendFailure},
			adjacent: c2Input(runstate.AttemptRunning),
		},
		{
			name:     "started unprobed attempt sweeps before close and infrastructure failure",
			input:    c2Input(runstate.AttemptRunning),
			wantCase: CaseUnprobedAttempt, wantKind: ActionRecoverUnprobedAttempt, replan: true,
			steps:    []ActionStep{StepSweepRecordedSession, StepCloseAdapterInterval, StepClassifyAndAppendFailure},
			adjacent: withAdapterProbe(c2Input(runstate.AttemptRunning)),
		},
		{
			name:     "probed incomplete attempt sweeps before quality failure",
			input:    withAdapterProbe(c2Input(runstate.AttemptRunning)),
			wantCase: CaseIncompleteAttempt, wantKind: ActionRecoverIncompleteAttempt, replan: true,
			steps:    []ActionStep{StepSweepRecordedSession, StepCloseAdapterInterval, StepClassifyAndAppendFailure},
			adjacent: withChangeSet(c2Input(runstate.AttemptVerifying)),
		},
		{
			name:     "repo write completion captures its missing change set first",
			input:    withRepoWrite(c2Input(runstate.AttemptVerifying)),
			wantCase: CaseCaptureChangeSet, wantKind: ActionCaptureChangeSet, replan: true,
			adjacent: withChangeSet(withRepoWrite(c2Input(runstate.AttemptVerifying))),
		},
		{
			name:     "completed adapter work is fully verified before acceptance",
			input:    withChangeSet(c2Input(runstate.AttemptVerifying)),
			wantCase: CasePostHocVerification, wantKind: ActionRerunPostHocVerification, replan: true,
			adjacent: withVerificationPassed(withChangeSet(c2Input(runstate.AttemptVerifying))),
		},
		{
			name:     "verified attempt enters acceptance",
			input:    withVerificationPassed(withChangeSet(c2Input(runstate.AttemptVerifying))),
			wantCase: CaseStartAcceptance, wantKind: ActionAppendAcceptanceStarted,
			adjacent: withAcceptanceStarted(withVerificationPassed(withChangeSet(c2Input(runstate.AttemptVerifying)))),
		},
		{
			name:     "completed attempt finalizes its movement",
			input:    c2Input(runstate.AttemptCompleted),
			wantCase: CaseMovementSucceeded, wantKind: ActionAppendMovementSucceeded, replan: true,
			adjacent: withMovementSucceeded(c2Input(runstate.AttemptCompleted)),
		},
		{
			name:     "failed movement finalizes its nonterminal run",
			input:    withMovementFailed(withFailedAttempt(c2Input(runstate.AttemptFailed), true)),
			wantCase: CaseRunFailed, wantKind: ActionAppendRunFailed, replan: true,
			adjacent: withTerminal(withMovementFailed(withFailedAttempt(c2Input(runstate.AttemptFailed), true))),
		},
		{
			name:     "final gate rejection is an atomic movement failure",
			input:    withFinalGateRejected(withFailedAttempt(c2Input(runstate.AttemptFailed), true)),
			wantCase: CaseFinalGateRejected, wantKind: ActionAppendFinalGateFailure, replan: true,
			adjacent: withMovementFailed(withFinalGateRejected(withFailedAttempt(c2Input(runstate.AttemptFailed), true))),
		},
	}

	seen := map[CaseID]bool{}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := PlanAttempt(test.input)
			assertDecision(t, got, test.wantCase, test.wantKind, test.wantHalt, test.replan)
			if !slices.Equal(got.Action.Steps, test.steps) {
				t.Fatalf("steps = %v, want %v", got.Action.Steps, test.steps)
			}
			adjacent := PlanAttempt(test.adjacent)
			if adjacent.CaseID == test.wantCase && adjacent.Action != nil && adjacent.Action.Kind == test.wantKind && adjacent.Halt == test.wantHalt {
				t.Fatalf("adjacent state selected the same result: %+v", adjacent)
			}
		})
		seen[test.wantCase] = true
	}
	for _, caseID := range []CaseID{
		CaseRealizeDisposition, CaseAppendQuestionRequest, CaseDecisionResume, CaseWaitingHuman,
		CaseUnstartedAttempt, CaseUnprobedAttempt, CaseIncompleteAttempt, CaseCaptureChangeSet,
		CasePostHocVerification, CaseStartAcceptance, CaseMovementSucceeded, CaseRunFailed,
		CaseFinalGateRejected,
	} {
		if !seen[caseID] {
			t.Fatalf("C.2 case %s has no direct selection test", caseID)
		}
	}
}

func TestPlanC2ScopeHaltsAndPrecedence(t *testing.T) {
	t.Run("historical attempt cannot enter C2", func(t *testing.T) {
		input := c2Input(runstate.AttemptRunning)
		input.Projection.CurrentHeadAttempt.ScoreRevision = input.Projection.State.ScoreHead.Revision + 1
		assertDecision(t, Plan(input), CaseContinue, ActionProceedScheduler, "", false)
		assertDecision(t, PlanAttempt(input), CaseContinue, ActionProceedScheduler, "", false)
	})
	t.Run("superseded attempt cannot enter C2", func(t *testing.T) {
		input := c2Input(runstate.AttemptSuperseded)
		assertDecision(t, Plan(input), CaseContinue, ActionProceedScheduler, "", false)
		assertDecision(t, PlanAttempt(input), CaseContinue, ActionProceedScheduler, "", false)
	})
	t.Run("missing request precedes waiting human", func(t *testing.T) {
		input := withQuestionRequests(c2Input(runstate.AttemptBlocked), false, true)
		assertDecision(t, PlanAttempt(input), CaseAppendQuestionRequest, ActionAppendQuestionRequest, "", true)
	})
	t.Run("recorded disposition is carried unchanged to Arm 2", func(t *testing.T) {
		input := withFailedAttempt(c2Input(runstate.AttemptFailed), false)
		got := PlanAttempt(input)
		assertDecision(t, got, CaseRealizeDisposition, ActionRealizeRecordedDisposition, "", true)
		if got.Action.RecordedDisposition == nil || *got.Action.RecordedDisposition != *input.Projection.CurrentHeadAttempt.RecordedDisposition {
			t.Fatalf("recorded disposition = %+v, want %+v", got.Action.RecordedDisposition, input.Projection.CurrentHeadAttempt.RecordedDisposition)
		}
	})
	t.Run("recorded retry selects C4 and a materialized successor prevents RC39 from refiring", func(t *testing.T) {
		input := withFailedAttempt(c2Input(runstate.AttemptFailed), false)
		input.Projection.Scheduler.PendingSuccessor = &PendingSuccessor{
			MovementID: "movement", AttemptID: "attempt", Performer: "writer", Reason: "quality_retry",
		}
		input.Projection.Scheduler.RemainingTime = 1
		arm2 := PlanAttempt(input)
		assertDecision(t, arm2, CaseRealizeDisposition, ActionRealizeRecordedDisposition, "", false)
		if arm2.Action.Continuation != ContinuationC4 || arm2.Action.PendingSuccessor == nil {
			t.Fatalf("Arm 2 = %+v, want pending C4 continuation", arm2)
		}
		input.Projection.Scheduler.PendingSuccessor = arm2.Action.PendingSuccessor
		assertDecision(t, PlanScheduler(input), CaseScheduler, ActionMaterializeSuccessor, "", true)

		input.Projection.Scheduler.PendingSuccessor = nil
		input.Projection.CurrentHeadAttempt = &AttemptRecovery{
			AttemptID: "retry-attempt", MovementID: "movement", ScoreRevision: input.Projection.State.ScoreHead.Revision,
			State: runstate.AttemptStarting,
		}
		if next := PlanAttempt(input); next.CaseID == CaseRealizeDisposition {
			t.Fatalf("materialized successor reselected RC39: %+v", next)
		}
	})
	t.Run("C2 continuation is explicit at table boundaries", func(t *testing.T) {
		if got := PlanAttempt(withQuestionRequests(c2Input(runstate.AttemptBlocked), true, false)); got.Action.Continuation != ContinuationC4 {
			t.Fatalf("decision resume continuation = %q, want %q", got.Action.Continuation, ContinuationC4)
		}
		if got := PlanAttempt(withVerificationPassed(withChangeSet(c2Input(runstate.AttemptVerifying)))); got.Action.Continuation != ContinuationC3 {
			t.Fatalf("acceptance continuation = %q, want %q", got.Action.Continuation, ContinuationC3)
		}
	})
	t.Run("change set capture precedes post hoc verification", func(t *testing.T) {
		input := withRepoWrite(c2Input(runstate.AttemptVerifying))
		assertDecision(t, PlanAttempt(input), CaseCaptureChangeSet, ActionCaptureChangeSet, "", true)
	})
	t.Run("performer completed excludes incomplete execution recovery", func(t *testing.T) {
		input := withAdapterProbe(withChangeSet(c2Input(runstate.AttemptVerifying)))
		assertDecision(t, PlanAttempt(input), CasePostHocVerification, ActionRerunPostHocVerification, "", true)
	})
	t.Run("unverifiable sweep halts before interval close", func(t *testing.T) {
		input := withAdapterProbe(c2Input(runstate.AttemptRunning))
		input.Observations.AdapterSweep = SweepUnverifiable
		assertDecision(t, PlanAttempt(input), CaseIncompleteAttempt, "", HaltSweepUnverifiable, false)
	})
	t.Run("unverifiable handoff halts", func(t *testing.T) {
		input := c2Input(runstate.AttemptStarting)
		input.Observations.Handoff = HandoffUnverifiable
		assertDecision(t, PlanAttempt(input), CaseUnstartedAttempt, "", HaltSpawnHandoffUnverifiable, false)
	})
	t.Run("attempt terminated incomplete is a quality failure instruction", func(t *testing.T) {
		got := PlanAttempt(withAdapterProbe(c2Input(runstate.AttemptRunning)))
		assertDecision(t, got, CaseIncompleteAttempt, ActionRecoverIncompleteAttempt, "", true)
		if got.Action.FailureKind != "task_failed" || got.Action.FailureReason != "attempt_terminated_incomplete" {
			t.Fatalf("failure = %q/%q, want task_failed/attempt_terminated_incomplete", got.Action.FailureKind, got.Action.FailureReason)
		}
	})
}

func TestPlanC3RowsAndAdjacentStates(t *testing.T) {
	cases := []struct {
		name     string
		input    Input
		wantCase CaseID
		wantKind ActionKind
		wantHalt HaltReason
		replan   bool
		adjacent Input
	}{
		{
			name:     "recorded acceptance failure realizes its recorded disposition",
			input:    withAcceptanceFailure(c3Input("c1")),
			wantCase: CaseAcceptanceFailed, wantKind: ActionRealizeRecordedDisposition, replan: true,
			adjacent: c3Input("c1"),
		},
		{
			name:     "completed failed criterion closes acceptance",
			input:    withCriterion(c3Input("c1"), "c1", true, "FAIL"),
			wantCase: CaseCriterionFailed, wantKind: ActionAppendAcceptanceFailure, replan: true,
			adjacent: withCriterion(c3Input("c1"), "c1", true, "PASS"),
		},
		{
			name:     "in flight criterion always sweeps then verifies the post-sweep subject",
			input:    withSubject(withCriterion(c3Input("c1"), "c1", false, ""), SubjectMatched),
			wantCase: CaseIncompleteCriterion, wantKind: ActionVerifyAcceptanceSubject, replan: true,
			adjacent: withSubject(withCriterion(c3Input("c1"), "c1", true, "PASS"), SubjectMatched),
		},
		{
			name:     "all passing criteria append evaluation completion",
			input:    withSubject(withCriterion(c3Input("c1"), "c1", true, "PASS"), SubjectMatched),
			wantCase: CaseCriteriaPassed, wantKind: ActionAppendEvaluationCompleted, replan: true,
			adjacent: withSubject(withEvaluationCompleted(withCriterion(c3Input("c1"), "c1", true, "PASS")), SubjectMatched),
		},
		{
			name:     "evaluated acceptance requests its missing required gate",
			input:    withRequiredGate(withEvaluationCompleted(c3Input("c1")), false, false, false),
			wantCase: CaseRequestHumanGate, wantKind: ActionAppendHumanGateRequest, replan: true,
			adjacent: withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, false, false),
		},
		{
			name:     "unresolved required gate is already waiting human",
			input:    withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, false, false),
			wantCase: CaseHumanGateWaiting, wantKind: ActionReturnWaitingHuman,
			adjacent: withSubject(withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, true, true), SubjectMatched),
		},
		{
			name:     "approved gate completes attempt before movement",
			input:    withSubject(withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, true, true), SubjectMatched),
			wantCase: CaseHumanGateApproved, wantKind: ActionAppendAcceptanceSuccess, replan: true,
			adjacent: withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, true, false),
		},
		{
			name:     "rejected gate appends its movement terminal",
			input:    withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, true, false),
			wantCase: CaseHumanGateRejected, wantKind: ActionAppendGateRejectedFailure, replan: true,
			adjacent: withSubject(withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, true, true), SubjectMatched),
		},
		{
			name:     "gate free acceptance completes attempt before movement",
			input:    withSubject(withEvaluationCompleted(c3Input("c1")), SubjectMatched),
			wantCase: CaseGateFreeCompletion, wantKind: ActionAppendAcceptanceSuccess, replan: true,
			adjacent: withSubject(withEvaluationCompleted(c3Input("c1")), SubjectMismatched),
		},
		{
			name:     "safe unjournaled launch is removed before criteria resume",
			input:    withUnjournaledLaunch(c3Input("c1"), UnjournaledLaunchSessionEmpty),
			wantCase: CaseUnjournaledLaunch, wantKind: ActionRemoveUnjournaledLaunch, replan: true,
			adjacent: withSubject(c3Input("c1"), SubjectMatched),
		},
		{
			name:  "empty acceptance resumes its first criterion",
			input: withSubject(c3Input("c1"), SubjectMatched),
			// The evaluator appends durable criterion facts, so the executor must
			// re-enter C.1 rather than continue from a stale C.3 projection.
			wantCase: CaseFirstCriterion, wantKind: ActionResumeCriterion, replan: true,
			adjacent: withSubject(withCriterion(c3Input("c1"), "c1", false, ""), SubjectMatched),
		},
		{
			name:  "passing completed criteria resume the next unstarted criterion",
			input: withSubject(withCriterion(c3Input("c1", "c2"), "c1", true, "PASS"), SubjectMatched),
			// See the first-criterion row: a resumed evaluator mutates durable
			// state, making a fresh C.1 dispatch semantically required.
			wantCase: CaseNextCriterion, wantKind: ActionResumeCriterion, replan: true,
			adjacent: withSubject(withCriterion(withCriterion(c3Input("c1", "c2"), "c1", true, "PASS"), "c2", true, "PASS"), SubjectMatched),
		},
	}

	seen := map[CaseID]bool{}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := PlanAcceptance(test.input)
			assertDecision(t, got, test.wantCase, test.wantKind, test.wantHalt, test.replan)
			adjacent := PlanAcceptance(test.adjacent)
			if adjacent.CaseID == test.wantCase && adjacent.Action != nil && adjacent.Action.Kind == test.wantKind && adjacent.Halt == test.wantHalt {
				t.Fatalf("adjacent state selected the same result: %+v", adjacent)
			}
		})
		seen[test.wantCase] = true
	}
	for _, caseID := range []CaseID{
		CaseAcceptanceFailed, CaseCriterionFailed, CaseIncompleteCriterion, CaseCriteriaPassed,
		CaseRequestHumanGate, CaseHumanGateWaiting, CaseHumanGateApproved, CaseHumanGateRejected,
		CaseGateFreeCompletion, CaseUnjournaledLaunch, CaseFirstCriterion, CaseNextCriterion,
	} {
		if !seen[caseID] {
			t.Fatalf("C.3 case %s has no direct selection test", caseID)
		}
	}
}

func TestPlanC3PrecedenceAndHalts(t *testing.T) {
	t.Run("recorded acceptance failure precedes criterion evidence", func(t *testing.T) {
		input := withCriterion(withAcceptanceFailure(c3Input("c1")), "c1", true, "FAIL")
		assertDecision(t, PlanAcceptance(input), CaseAcceptanceFailed, ActionRealizeRecordedDisposition, "", true)
	})
	t.Run("completed failure precedes an in flight criterion", func(t *testing.T) {
		input := withSubject(withCriterion(withCriterion(c3Input("c1", "c2"), "c1", true, "ERROR"), "c2", false, ""), SubjectMatched)
		assertDecision(t, PlanAcceptance(input), CaseCriterionFailed, ActionAppendAcceptanceFailure, "", true)
	})
	t.Run("criteria completion precedes unjournaled launch cleanup", func(t *testing.T) {
		input := withUnjournaledLaunch(withSubject(withCriterion(c3Input("c1"), "c1", true, "PASS"), SubjectMatched), UnjournaledLaunchSessionEmpty)
		assertDecision(t, PlanAcceptance(input), CaseCriteriaPassed, ActionAppendEvaluationCompleted, "", true)
	})
	t.Run("criterion sweep halt is an Appendix D halt", func(t *testing.T) {
		input := withCriterion(c3Input("c1"), "c1", false, "")
		input.Observations.CriterionSweep = SweepUnverifiable
		assertDecision(t, PlanAcceptance(input), CaseIncompleteCriterion, "", HaltSweepUnverifiable, false)
	})
	t.Run("unjournaled launch handoff halt is an Appendix D halt", func(t *testing.T) {
		input := withUnjournaledLaunch(c3Input("c1"), UnjournaledLaunchHandoffUnverifiable)
		assertDecision(t, PlanAcceptance(input), CaseUnjournaledLaunch, "", HaltSpawnHandoffUnverifiable, false)
	})
	t.Run("C2 dispatches a started acceptance to C3", func(t *testing.T) {
		input := withVerificationPassed(c2Input(runstate.AttemptVerifying))
		input.Projection.CurrentHeadAttempt.AcceptanceStarted = true
		got := PlanAttempt(input)
		assertDecision(t, got, CaseContinue, ActionProceedAcceptance, "", false)
		if got.Action.Continuation != ContinuationC3 {
			t.Fatalf("continuation = %q, want %q", got.Action.Continuation, ContinuationC3)
		}
	})
}

func TestPlanC3FullInvariantGate(t *testing.T) {
	for _, test := range []struct {
		name  string
		input Input
	}{
		{name: "first criterion", input: c3Input("c1")},
		{name: "next criterion", input: withCriterion(c3Input("c1", "c2"), "c1", true, "PASS")},
		{name: "evaluation", input: withCriterion(c3Input("c1"), "c1", true, "PASS")},
		{name: "approved completion", input: withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, true, true)},
		{name: "gate free completion", input: withEvaluationCompleted(c3Input("c1"))},
	} {
		t.Run(test.name+" is unreachable without matching observation", func(t *testing.T) {
			for _, observation := range []SubjectVerification{SubjectUnverified, SubjectMismatched} {
				input := withSubject(test.input, observation)
				got := PlanAcceptance(input)
				if got.Action != nil {
					switch got.Action.Kind {
					case ActionResumeCriterion, ActionAppendEvaluationCompleted, ActionAppendAcceptanceSuccess:
						t.Fatalf("success-claiming action %q reachable with %q observation", got.Action.Kind, observation)
					}
				}
				if observation == SubjectMismatched {
					assertDecision(t, got, got.CaseID, ActionAppendAcceptanceFailure, "", true)
					if got.Action.FailureReason != "recovery_subject_mismatch" {
						t.Fatalf("failure reason = %q, want recovery_subject_mismatch", got.Action.FailureReason)
					}
				}
			}
		})
	}
	t.Run("every synthesized acceptance failure invokes Arm 1 classification", func(t *testing.T) {
		for _, input := range []Input{withCriterion(c3Input("c1"), "c1", true, "FAIL")} {
			got := PlanAcceptance(input)
			if got.Action == nil || got.Action.Kind != ActionAppendAcceptanceFailure || !slices.Contains(got.Action.Steps, StepClassifyAcceptanceFailure) {
				t.Fatalf("failure action = %+v, want Arm 1 classification before append", got.Action)
			}
		}
	})
	t.Run("in-flight criterion defers every subject verdict until after its sweep", func(t *testing.T) {
		for _, observation := range []SubjectVerification{SubjectUnverified, SubjectMatched, SubjectMismatched} {
			got := PlanAcceptance(withSubject(withCriterion(c3Input("c1"), "c1", false, ""), observation))
			assertDecision(t, got, CaseIncompleteCriterion, ActionVerifyAcceptanceSubject, "", true)
			want := []ActionStep{StepSweepCriterionSession, StepVerifyAcceptanceSubject}
			if !slices.Equal(got.Action.Steps, want) {
				t.Fatalf("observation %q steps = %v, want %v", observation, got.Action.Steps, want)
			}
		}
	})
}
func assertDecision(t *testing.T, got Decision, wantCase CaseID, wantKind ActionKind, wantHalt HaltReason, wantReplan bool) {
	t.Helper()
	if got.CaseID != wantCase {
		t.Fatalf("case = %s, want %s", got.CaseID, wantCase)
	}
	if got.Halt != wantHalt {
		t.Fatalf("halt = %s, want %s", got.Halt, wantHalt)
	}
	if wantKind == "" {
		if got.Action != nil {
			t.Fatalf("action = %+v, want halt", got.Action)
		}
		return
	}
	if got.Action == nil {
		t.Fatalf("action = nil, want %s", wantKind)
	}
	if got.Action.Kind != wantKind || got.Action.Replan != wantReplan {
		t.Fatalf("action = %+v, want kind %s replan %t", got.Action, wantKind, wantReplan)
	}
}

func baseInput() Input {
	return Input{Projection: Projection{State: runstate.State{
		Run:              runstate.RunRunning,
		RoutedAmendments: map[runstate.ProposalID]runstate.RoutedAmendment{},
		PendingDecisions: map[string]runstate.PendingDecision{},
	}}}
}

func withTerminal(input Input) Input {
	input.Projection.State.Run = runstate.RunSucceeded
	return input
}

func withAuthority(input Input, epoch uint64) Input {
	input.Projection.State.Authority = runstate.Authority{
		Epoch: epoch,
		Owner: &runstate.AuthorityOwner{PID: 1},
	}
	return input
}

func withOpenExecution(input Input, phase string) Input {
	input.Projection.State.OpenExecution = &runstate.ExecutionInterval{
		ID: "interval", Phase: phase, WallStart: "2026-07-28T00:00:00Z", RemainingAtStart: 1,
	}
	return input
}

func withLease(input Input, lease LeaseObservation, authorityEpoch uint64) Input {
	input = withAuthority(input, authorityEpoch)
	input.Observations.Lease = lease
	return input
}

func withCancel(input Input) Input {
	input.Projection.State.CancelRequested = true
	return input
}

func withPrepare(input Input) Input {
	input.Projection.State.PendingPrepare = &runstate.PendingPrepare{}
	input.Observations.Prepare = PrepareObservation{PlanPresent: true, SnapshotPresent: true}
	return input
}

func withPrepareSnapshotMissing(input Input) Input {
	input = withPrepare(input)
	input.Observations.Prepare.SnapshotPresent = false
	return input
}

func withPreparePlanMissing(input Input) Input {
	input = withPrepare(input)
	input.Observations.Prepare.PlanPresent = false
	return input
}

func withRootDivergence(input Input) Input {
	input.Observations.RootSnapshotDivergence = true
	return input
}

func withMissingReference(input Input, kind ReferenceKind) Input {
	input.Observations.References = []ReferenceObservation{{Kind: kind}}
	return input
}

func withMissingRoutedRequest(input Input) Input {
	input.Projection.State.RoutedAmendments = map[runstate.ProposalID]runstate.RoutedAmendment{
		"proposal": {ProposalID: "proposal", DecisionID: "decision"},
	}
	input.Projection.State.PendingDecisions = map[string]runstate.PendingDecision{}
	return input
}

func withRequestedRoutedAmendment(input Input) Input {
	input = withMissingRoutedRequest(input)
	input.Projection.State.PendingDecisions["decision"] = runstate.PendingDecision{ID: "decision"}
	return input
}

func withRevisionRestart(input Input) Input {
	input.Projection.RevisionRestarts = []RevisionRestart{{MovementID: "movement"}}
	return input
}

func withCompositionTerminal(input Input) Input {
	input.Projection.CompositionTerminals = []CompositionTerminal{{
		Scope: "movement", TargetID: "movement", Reason: "composition_failed",
		EvidenceEventID: "composition-evidence", ScoreRevision: input.Projection.State.ScoreHead.Revision,
	}}
	return input
}

func withCurrentHeadAttempt(input Input) Input {
	input.Projection.CurrentHeadAttempt = &AttemptRecovery{
		AttemptID:     "attempt",
		MovementID:    "movement",
		ScoreRevision: input.Projection.State.ScoreHead.Revision,
		State:         runstate.AttemptStarting,
	}
	return input
}

func c2Input(state runstate.AttemptState) Input {
	input := baseInput()
	input.Projection.State.Attempts = map[runstate.AttemptID]runstate.Attempt{}
	input.Projection.State.AdapterObservations = map[runstate.AttemptID]runstate.AdapterObservation{}
	input.Projection.State.VerifiedAttempts = map[runstate.AttemptID]bool{}
	input.Projection.State.RepoWriteMovements = map[runstate.MovementID]bool{}
	input.Projection.CurrentHeadAttempt = &AttemptRecovery{
		AttemptID:     "attempt",
		MovementID:    "movement",
		ScoreRevision: input.Projection.State.ScoreHead.Revision,
		State:         state,
	}
	return input
}

func c3Input(criteria ...runstate.CriterionID) Input {
	input := c2Input(runstate.AttemptVerifying)
	attempt := input.Projection.CurrentHeadAttempt
	attempt.AcceptanceStarted = true
	input.Projection.State.Acceptances = map[runstate.AttemptID]runstate.Acceptance{
		attempt.AttemptID: {
			Started:             true,
			SubjectTree:         "tree",
			PlannedCriterionIDs: criteria,
			Criteria:            map[runstate.CriterionID]runstate.CriterionRecord{},
		},
	}
	input.Projection.Acceptance = &AcceptanceRecovery{}
	return input
}

func withAcceptanceFailure(input Input) Input {
	attempt := input.Projection.CurrentHeadAttempt
	attempt.State = runstate.AttemptFailed
	disposition := runstate.Disposition{Charged: "quality_retry"}
	attempt.RecordedDisposition = &disposition
	input.Projection.Acceptance.Failed = true
	return input
}

func withCriterion(input Input, criterionID runstate.CriterionID, completed bool, outcome string) Input {
	attempt := input.Projection.CurrentHeadAttempt
	acceptance := input.Projection.State.Acceptances[attempt.AttemptID]
	acceptance.Criteria[criterionID] = runstate.CriterionRecord{
		Started: true, Completed: completed, Outcome: outcome,
	}
	input.Projection.State.Acceptances[attempt.AttemptID] = acceptance
	return input
}

func withAdditionalCriterion(input Input, criterionID runstate.CriterionID) Input {
	attempt := input.Projection.CurrentHeadAttempt
	acceptance := input.Projection.State.Acceptances[attempt.AttemptID]
	acceptance.PlannedCriterionIDs = append(acceptance.PlannedCriterionIDs, criterionID)
	input.Projection.State.Acceptances[attempt.AttemptID] = acceptance
	return input
}

func withEvaluationCompleted(input Input) Input {
	attempt := input.Projection.CurrentHeadAttempt
	acceptance := input.Projection.State.Acceptances[attempt.AttemptID]
	acceptance.EvaluationCompleted = true
	input.Projection.State.Acceptances[attempt.AttemptID] = acceptance
	return input
}

func withRequiredGate(input Input, requested, resolved, approved bool) Input {
	input.Projection.Acceptance.Gate = GateRecovery{
		Required: true, Requested: requested, Resolved: resolved, Approved: approved,
		DecisionID: "gate-decision", GateID: "gate",
	}
	return input
}

func withSubject(input Input, verification SubjectVerification) Input {
	input.Observations.AcceptanceSubject = verification
	return input
}

func withUnjournaledLaunch(input Input, state UnjournaledLaunchState) Input {
	input.Observations.UnjournaledLaunch = state
	return input
}

func withFailedAttempt(input Input, realized bool) Input {
	attempt := input.Projection.CurrentHeadAttempt
	attempt.State = runstate.AttemptFailed
	attempt.FailureDispositionRealized = realized
	disposition := runstate.Disposition{Charged: "quality_retry"}
	attempt.RecordedDisposition = &disposition
	input.Projection.State.Attempts[attempt.AttemptID] = runstate.Attempt{
		MovementID: attempt.MovementID,
		State:      runstate.AttemptFailed,
		Failure: &runstate.AttemptFailure{
			Kind:        "task_failed",
			Reason:      "attempt_terminated_incomplete",
			Disposition: disposition,
		},
	}
	return input
}

func withQuestionRequests(input Input, durable, unresolved bool) Input {
	attempt := input.Projection.CurrentHeadAttempt
	attempt.QuestionRequests = []QuestionRequest{{DecisionID: "question-1", Durable: durable}}
	if unresolved {
		input.Projection.State.PendingDecisions["question-1"] = runstate.PendingDecision{
			ID: "question-1", AttemptID: attempt.AttemptID, Blocking: true,
		}
	}
	return input
}

func withAdapterProbe(input Input) Input {
	attempt := input.Projection.CurrentHeadAttempt
	input.Projection.State.AdapterObservations[attempt.AttemptID] = runstate.AdapterObservation{}
	return input
}

func withRepoWrite(input Input) Input {
	attempt := input.Projection.CurrentHeadAttempt
	input.Projection.State.RepoWriteMovements[attempt.MovementID] = true
	return input
}

func withChangeSet(input Input) Input {
	input.Projection.CurrentHeadAttempt.ChangeSetRecorded = true
	return input
}

func withVerificationPassed(input Input) Input {
	attempt := input.Projection.CurrentHeadAttempt
	input.Projection.State.VerifiedAttempts[attempt.AttemptID] = true
	return input
}

func withAcceptanceStarted(input Input) Input {
	input.Projection.CurrentHeadAttempt.AcceptanceStarted = true
	return input
}

func withMovementSucceeded(input Input) Input {
	input.Projection.CurrentHeadAttempt.MovementSucceeded = true
	return input
}

func withMovementFailed(input Input) Input {
	input.Projection.CurrentHeadAttempt.MovementFailed = true
	return input
}

func withFinalGateRejected(input Input) Input {
	input.Projection.CurrentHeadAttempt.FinalGateRejected = true
	return input
}
