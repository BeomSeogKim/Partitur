package runstate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

type runTerminalSource struct {
	eventType string
	condition string
}

type derivedSourceTransition struct {
	derivedEvent string
	sourceEvent  string
	condition    string
}

func TestDerivedEventSourceProjectionLock(t *testing.T) {
	lines := recoveryDesignLines(t)

	derived := appendixBDerivedEventTypes(t, lines)
	goDerived := goDerivedEventTypes(t)
	requireSameUniqueStrings(t, "Appendix B derived events", derived, "Go derived events", goDerived)

	markedSources := appendixBRunTerminalSources(t, lines)
	marked := make([]string, 0, len(markedSources))
	for _, source := range markedSources {
		marked = append(marked, source.eventType)
	}
	requireSameUniqueStrings(t, "Appendix B run-terminal sources", marked, "Go terminal run projections", goRunTerminalProjectionEventTypes(t))

	transitions := appendixBDerivedSourceTransitions(t, lines, markedSources)
	fixtures := derivedSourceProjectionFixtures()
	transitionKeys := make([]string, 0, len(transitions))
	fixtureKeys := make([]string, 0, len(fixtures))
	for _, transition := range transitions {
		transitionKeys = append(transitionKeys, transition.key())
		if !hasNonTestAppendSite(t, transition.sourceEvent) {
			t.Fatalf("derived source %q has no non-test append site", transition.sourceEvent)
		}
	}
	for key := range fixtures {
		fixtureKeys = append(fixtureKeys, key)
	}
	requireSameUniqueStrings(t, "parsed derived source transitions", transitionKeys, "executed derived source fixtures", fixtureKeys)

	for _, transition := range transitions {
		transition := transition
		t.Run(transition.key(), func(t *testing.T) {
			fixture := fixtures[transition.key()]
			if fixture.sourceEvent != EventType(transition.sourceEvent) {
				t.Fatalf("fixture source = %q, want %q", fixture.sourceEvent, transition.sourceEvent)
			}
			state := applyDerivedFixtureSource(t, fixture)
			assertDerivedStateEffect(t, transition, state)

			replayed := replayDerivedFixtureJournal(t, fixture)
			assertDerivedStateEffect(t, transition, replayed)
		})
	}
}

func goDerivedEventTypes(t *testing.T) []string {
	t.Helper()

	var derived []string
	for _, eventType := range goEventTypes(t) {
		if isDerivedEvent(EventType(eventType)) {
			derived = append(derived, eventType)
		}
	}
	if len(derived) == 0 {
		t.Fatal("Go derived-event classification produced no values")
	}
	return derived
}

func appendixBRunTerminalSources(t *testing.T, lines []string) []runTerminalSource {
	t.Helper()

	const marker = "**Run-terminal source:** `"
	marking := regexp.MustCompile("\\*\\*Run-terminal source:\\*\\* `([a-z_]+)`$")
	allowed := map[string]bool{
		"always":                             true,
		"final_movement":                     true,
		"final_human_gate_rejected_movement": true,
	}
	var sources []runTerminalSource
	for _, row := range appendixBEventRows(t, lines) {
		count := strings.Count(row.effect, marker)
		if count == 0 {
			if !row.derived && strings.Contains(row.effect, "Run-terminal source:") {
				t.Fatalf("Appendix B event %q has malformed run-terminal marking in %q", row.eventType, row.effect)
			}
			continue
		}
		if count != 1 {
			t.Fatalf("Appendix B event %q has %d run-terminal markings", row.eventType, count)
		}
		match := marking.FindStringSubmatch(row.effect)
		if match == nil || !allowed[match[1]] {
			t.Fatalf("Appendix B event %q has malformed or invalid run-terminal marking in %q", row.eventType, row.effect)
		}
		sources = append(sources, runTerminalSource{eventType: row.eventType, condition: match[1]})
	}
	if len(sources) == 0 {
		t.Fatal("Appendix B run-terminal source extraction produced no rows")
	}
	return sources
}

func appendixBDerivedSourceTransitions(t *testing.T, lines []string, markedSources []runTerminalSource) []derivedSourceTransition {
	t.Helper()

	rows := appendixBEventRows(t, lines)
	known := map[string]appendixBEventRow{}
	for _, row := range rows {
		known[row.eventType] = row
	}
	directSource := regexp.MustCompile("from `([a-z][a-z0-9_.]*)`")
	const markedSourcesClause = "every Appendix B row carrying a **Run-terminal source:** marking"
	var transitions []derivedSourceTransition
	for _, row := range rows {
		if !row.derived {
			continue
		}
		matches := directSource.FindAllStringSubmatch(row.effect, -1)
		if len(matches) == 0 {
			t.Fatalf("Appendix B derived event %q names no direct source in %q", row.eventType, row.effect)
		}
		for _, match := range matches {
			transitions = append(transitions, derivedSourceTransition{derivedEvent: row.eventType, sourceEvent: match[1]})
		}
		if strings.Contains(row.effect, markedSourcesClause) {
			for _, source := range markedSources {
				transitions = append(transitions, derivedSourceTransition{
					derivedEvent: row.eventType,
					sourceEvent:  source.eventType,
					condition:    source.condition,
				})
			}
		}
	}
	if len(transitions) == 0 {
		t.Fatal("Appendix B derived source extraction produced no transitions")
	}
	keys := make([]string, 0, len(transitions))
	for _, transition := range transitions {
		source, exists := known[transition.sourceEvent]
		if !exists || source.derived {
			t.Fatalf("derived transition %s names non-authoritative source %q", transition.key(), transition.sourceEvent)
		}
		keys = append(keys, transition.key())
	}
	uniqueStrings(t, "Appendix B derived source transitions", keys)
	return transitions
}

func (transition derivedSourceTransition) key() string {
	return transition.derivedEvent + " <- " + transition.sourceEvent + " [" + transition.condition + "]"
}

func goRunTerminalProjectionEventTypes(t *testing.T) []string {
	t.Helper()

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "apply.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declarations := goEventTypeDeclarations(t)
	var terminal []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "Apply" || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			caseClause, ok := node.(*ast.CaseClause)
			if !ok || !caseSetsTerminalRunLifecycle(caseClause) {
				return true
			}
			for _, expression := range caseClause.List {
				identifier, ok := expression.(*ast.Ident)
				if !ok {
					t.Fatalf("Apply terminal projection case has unparseable event expression %T", expression)
				}
				eventType, exists := declarations[identifier.Name]
				if !exists {
					t.Fatalf("Apply terminal projection case names non-EventType %q", identifier.Name)
				}
				terminal = append(terminal, eventType)
			}
			return true
		})
	}
	if len(terminal) == 0 {
		t.Fatal("Go terminal run projection extraction produced no event types")
	}
	return terminal
}

func caseSetsTerminalRunLifecycle(caseClause *ast.CaseClause) bool {
	terminal := false
	ast.Inspect(caseClause, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
			return true
		}
		left, ok := assignment.Lhs[0].(*ast.SelectorExpr)
		if !ok || left.Sel.Name != "Run" {
			return true
		}
		state, ok := left.X.(*ast.Ident)
		if !ok || state.Name != "state" {
			return true
		}
		right, ok := assignment.Rhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		terminal = right.Name == "RunSucceeded" || right.Name == "RunFailed" || right.Name == "RunCancelled"
		return !terminal
	})
	return terminal
}

type derivedSourceProjectionFixture struct {
	build       func(*testing.T) State
	source      Event
	sourceEvent EventType
}

func derivedSourceProjectionFixtures() map[string]derivedSourceProjectionFixture {
	fixtures := map[string]derivedSourceProjectionFixture{}
	add := func(derived, source, condition string, fixture derivedSourceProjectionFixture) {
		fixtures[(derivedSourceTransition{derivedEvent: derived, sourceEvent: source, condition: condition}).key()] = fixture
	}

	add("movement.cancelled", "run.cancelled", "", derivedRunCancelledFixture())
	add("attempt.cancelled", "run.cancelled", "", derivedRunCancelledFixture())
	add("attempt.superseded", "amendment.approved", "", derivedAmendmentApprovedFixture())
	add("decision.obsoleted", "amendment.approved", "", derivedAmendmentApprovedFixture())
	add("decision.obsoleted", "run.succeeded", "always", derivedRunSucceededFixture())
	add("decision.obsoleted", "run.failed", "always", derivedRunFailedFixture())
	add("decision.obsoleted", "run.cancelled", "always", derivedRunCancelledFixture())
	add("decision.obsoleted", "movement.succeeded", "final_movement", derivedMovementSucceededFixture())
	add("decision.obsoleted", "movement.failed", "final_human_gate_rejected_movement", derivedMovementFailedFixture())
	return fixtures
}

func derivedRunCancelledFixture() derivedSourceProjectionFixture {
	journal := newDerivedFixtureJournal([]MovementSeed{{ID: "m1", Initial: MovementPending, Final: true}})
	journal.runningAttempt()
	journal.questionDecision()
	journal.add(fixtureEvent(EventRunCancelled, map[string]any{
		"cancelled_movement_ids": []any{"m1"},
		"cancelled_attempt_ids":  []any{"a1"},
		"obsoleted_decision_ids": []any{"decision-1"},
	}, nil))
	return journal.fixture(EventRunCancelled)
}

func derivedAmendmentApprovedFixture() derivedSourceProjectionFixture {
	journal := newDerivedFixtureJournal([]MovementSeed{{ID: "m1", Initial: MovementPending, Final: true}})
	journal.runningAttempt()
	journal.questionDecision()
	journal.add(fixtureEvent(EventAmendmentApprovalPrepared, autoPreparePayload(), nil))
	payload := approvalPayload("auto")
	payload["obsoleted_decision_ids"] = []any{"decision-1"}
	approval := fixtureEvent(EventAmendmentApproved, payload, func(event *Event) {
		event.ScoreRevision = 2
	})
	journal.add(approval)
	return journal.fixture(EventAmendmentApproved)
}

func derivedRunSucceededFixture() derivedSourceProjectionFixture {
	journal := newDerivedFixtureJournal(nil)
	journal.add(fixtureEvent(EventRunStarted, runStartedPayload(), nil))
	journal.add(fixtureEvent(EventRunSucceeded, runSucceededPayload(), nil))
	return journal.fixture(EventRunSucceeded, pendingDecisionFixture)
}

func derivedRunFailedFixture() derivedSourceProjectionFixture {
	journal := newDerivedFixtureJournal(nil)
	journal.add(fixtureEvent(EventRunStarted, runStartedPayload(), nil))
	journal.add(fixtureEvent(EventRunFailed, map[string]any{"reason": "movement_failed"}, nil))
	return journal.fixture(EventRunFailed, pendingDecisionFixture)
}

func derivedMovementSucceededFixture() derivedSourceProjectionFixture {
	journal := newDerivedFixtureJournal([]MovementSeed{{ID: "m1", Initial: MovementPending, Final: true}})
	journal.completedAttempt()
	journal.add(fixtureEvent(EventMovementSucceeded, movementSucceededPayload(true), attemptEnvelope))
	return journal.fixture(EventMovementSucceeded, pendingDecisionFixture)
}

func derivedMovementFailedFixture() derivedSourceProjectionFixture {
	journal := newDerivedFixtureJournal([]MovementSeed{{ID: "m1", Initial: MovementPending, Final: true}})
	journal.runningAttempt()
	journal.add(fixtureEvent(EventAdapterProbed, adapterProbedPayload(), attemptEnvelope))
	journal.add(fixtureEvent(EventPerformerCompleted, map[string]any{"session_hint_stored": false}, attemptEnvelope))
	journal.questionDecision()
	journal.add(fixtureEvent(EventMovementFailed, map[string]any{
		"reason": "human_gate_rejected", "decision_id": "gate-1", "subject_tree": "git-sha1:tree", "run_failed": true,
	}, attemptEnvelope))
	return journal.fixture(EventMovementFailed)
}

type derivedFixtureJournal struct {
	seed   []MovementSeed
	events []Event
}

func newDerivedFixtureJournal(seed []MovementSeed) *derivedFixtureJournal {
	return &derivedFixtureJournal{seed: seed}
}

func (journal *derivedFixtureJournal) add(event Event) {
	sequence := len(journal.events) + 1
	event.EventID = fmt.Sprintf("derived-fixture-%02d", sequence)
	event.Seq = uint64(sequence)
	journal.events = append(journal.events, event)
}

func (journal *derivedFixtureJournal) fixture(source EventType, prepare ...func(State) State) derivedSourceProjectionFixture {
	if len(journal.events) == 0 || journal.events[len(journal.events)-1].Type != source {
		panic(fmt.Sprintf("derived fixture source %q is not the final journal event", source))
	}
	history := append([]Event(nil), journal.events[:len(journal.events)-1]...)
	sourceEvent := journal.events[len(journal.events)-1]
	return derivedSourceProjectionFixture{
		build: func(t *testing.T) State {
			state := NewState(journal.seed)
			for index, event := range history {
				var err error
				state, err = Apply(state, event)
				if err != nil {
					t.Fatalf("build fixture event %d (%s): %v", index+1, event.Type, err)
				}
			}
			if len(prepare) == 1 {
				state = prepare[0](state)
			}
			return state
		},
		source:      sourceEvent,
		sourceEvent: source,
	}
}

func (journal *derivedFixtureJournal) runningAttempt() {
	journal.add(fixtureEvent(EventRunStarted, runStartedPayload(), nil))
	journal.add(fixtureEvent(EventMovementReady, map[string]any{}, func(event *Event) { event.MovementID = "m1" }))
	journal.add(fixtureEvent(EventMovementStarted, map[string]any{}, func(event *Event) { event.MovementID = "m1" }))
	journal.add(fixtureEvent(EventPerformerSelected, performerSelectedPayload(), attemptEnvelope))
	journal.add(fixtureEvent(EventAttemptStarted, attemptStartedPayload(), attemptEnvelope))
}

func (journal *derivedFixtureJournal) questionDecision() {
	journal.add(fixtureEvent(EventDecisionRequested, map[string]any{
		"decision_id": "decision-1", "decision_type": "question", "emitted_id": "question-1", "question": "Continue?",
	}, attemptEnvelope))
}

func (journal *derivedFixtureJournal) completedAttempt() {
	journal.runningAttempt()
	journal.add(fixtureEvent(EventAdapterProbed, adapterProbedPayload(), attemptEnvelope))
	journal.add(fixtureEvent(EventArtifactRecorded, artifactRecordedPayload(), attemptEnvelope))
	journal.add(fixtureEvent(EventPerformerCompleted, map[string]any{"session_hint_stored": false}, attemptEnvelope))
	journal.add(fixtureEvent(EventVerificationPassed, map[string]any{}, attemptEnvelope))
	journal.add(fixtureEvent(EventApplicationCandidateRecorded, applicationCandidatePayload(), nil))
	journal.add(fixtureEvent(EventAcceptanceStarted, acceptanceStartedPayload(), attemptEnvelope))
	journal.add(fixtureEvent(EventCriterionStarted, criterionStartedPayload(), attemptEnvelope))
	journal.add(fixtureEvent(EventCriterionCompleted, criterionCompletedPayload(), attemptEnvelope))
	journal.add(fixtureEvent(EventAcceptanceEvaluationCompleted, acceptanceEvaluationCompletedPayload(), attemptEnvelope))
	journal.add(fixtureEvent(EventAttemptCompleted, map[string]any{}, attemptEnvelope))
}

func pendingDecisionFixture(state State) State {
	state.PendingDecisions["decision-1"] = PendingDecision{ID: "decision-1", Type: "question"}
	return state
}

func applyDerivedFixtureSource(t *testing.T, fixture derivedSourceProjectionFixture) State {
	t.Helper()

	state := fixture.build(t)
	next, err := Apply(state, fixture.source)
	if err != nil {
		t.Fatalf("apply source %s: %v", fixture.source.Type, err)
	}
	return next
}

func replayDerivedFixtureJournal(t *testing.T, fixture derivedSourceProjectionFixture) State {
	t.Helper()

	state := fixture.build(t)
	next, err := Apply(state, fixture.source)
	if err != nil {
		t.Fatalf("replay source %s: %v", fixture.source.Type, err)
	}
	return next
}

func assertDerivedStateEffect(t *testing.T, transition derivedSourceTransition, state State) {
	t.Helper()

	switch transition.derivedEvent {
	case "movement.cancelled":
		if state.Movements["m1"] != MovementCancelled {
			t.Fatalf("movement.cancelled projection = %q, want %q", state.Movements["m1"], MovementCancelled)
		}
	case "attempt.cancelled":
		if state.Attempts["a1"].State != AttemptCancelled {
			t.Fatalf("attempt.cancelled projection = %q, want %q", state.Attempts["a1"].State, AttemptCancelled)
		}
	case "attempt.superseded":
		if state.Attempts["a1"].State != AttemptSuperseded {
			t.Fatalf("attempt.superseded projection = %q, want %q", state.Attempts["a1"].State, AttemptSuperseded)
		}
	case "decision.obsoleted":
		if _, pending := state.PendingDecisions["decision-1"]; pending {
			t.Fatalf("decision.obsoleted projection left decision-1 pending")
		}
	default:
		t.Fatalf("fixture has no state-effect assertion for derived event %q", transition.derivedEvent)
	}
}
