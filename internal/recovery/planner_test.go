package recovery

import (
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

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
			wantCase: CaseCompositionTerminal, wantKind: ActionAppendCompositionTerminal,
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
		CaseTerminal, CaseStaleLease, CaseOrphanLease, CaseOwnerUnverifiable, CaseLiveOwner,
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

func TestPlanC1TotalOverDeclaredAxes(t *testing.T) {
	type axis struct {
		name   string
		values []func(Input) Input
	}
	noChange := func(input Input) Input { return input }
	axes := []axis{
		{
			name:   "run",
			values: []func(Input) Input{noChange, withTerminal},
		},
		{
			name: "lease",
			values: []func(Input) Input{
				noChange,
				func(input Input) Input {
					return withLease(input, LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerLive}, 2)
				},
				func(input Input) Input {
					return withLease(input, LeaseObservation{Exists: true, Readable: true, Epoch: 2}, 1)
				},
				func(input Input) Input {
					return withLease(input, LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerDead}, 1)
				},
				func(input Input) Input {
					return withLease(input, LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerUnverifiable}, 1)
				},
				func(input Input) Input {
					return withLease(input, LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerLive}, 1)
				},
				func(input Input) Input { return withLease(input, LeaseObservation{Exists: true, Epoch: 1}, 1) },
			},
		},
		{
			name:   "control",
			values: []func(Input) Input{noChange, withCancel, withPrepare},
		},
		{
			name:   "root snapshot",
			values: []func(Input) Input{noChange, withRootDivergence},
		},
		{
			name:   "event-named reference",
			values: []func(Input) Input{noChange, func(input Input) Input { return withMissingReference(input, ReferenceArtifact) }},
		},
		{
			name:   "routed request",
			values: []func(Input) Input{noChange, withMissingRoutedRequest},
		},
		{
			name:   "revision restart",
			values: []func(Input) Input{noChange, withRevisionRestart},
		},
		{
			name:   "composition terminal",
			values: []func(Input) Input{noChange, withCompositionTerminal},
		},
		{
			name:   "current head attempt",
			values: []func(Input) Input{noChange, withCurrentHeadAttempt},
		},
	}

	count := 0
	var expand func(int, Input)
	expand = func(index int, input Input) {
		if index == len(axes) {
			count++
			decision := Plan(input)
			if !decision.Valid() {
				t.Fatalf("invalid decision for declared input %d: %+v", count, decision)
			}
			return
		}
		for _, set := range axes[index].values {
			expand(index+1, set(input))
		}
	}
	expand(0, baseInput())
	if count == 0 {
		t.Fatal("declared recovery axes produced no inputs")
	}
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
	input.Projection.CompositionTerminals = []CompositionTerminal{{Scope: "movement", TargetID: "movement", Reason: "composition_failed"}}
	return input
}

func withCurrentHeadAttempt(input Input) Input {
	input.Projection.HasCurrentHeadAttempt = true
	return input
}
