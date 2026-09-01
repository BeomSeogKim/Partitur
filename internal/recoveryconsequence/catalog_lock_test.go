package recoveryconsequence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

func TestDurableConsequenceCatalogMatchesDesignClosure(t *testing.T) {
	designPath := filepath.Join("..", "..", "docs", "DESIGN.md")
	design, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	const anchor = "Durable consequences close before a prepare can exist."
	if strings.Count(string(design), anchor) != 1 {
		t.Fatalf("%q occurrences = %d, want 1", anchor, strings.Count(string(design), anchor))
	}
	section := string(design[strings.Index(string(design), anchor):])
	next := strings.Index(section, "This is narrower than quiescing")
	if next < 0 {
		t.Fatal("durable-consequence closure has no following paragraph boundary")
	}
	section = section[:next]
	listStart := strings.Index(section, "selected by ")
	listEnd := strings.Index(section, ". These are")
	if listStart < 0 || listEnd < listStart {
		t.Fatal("durable-consequence closure has no parseable selected-case list")
	}
	list := section[listStart+len("selected by ") : listEnd]
	list = strings.ReplaceAll(list, "`", "")
	re := regexp.MustCompile(`(?:RC-RESUME-)?([0-9]{3})(?:–([0-9]{3}))?`)
	matches := re.FindAllStringSubmatch(list, -1)
	if len(matches) == 0 {
		t.Fatal("durable-consequence closure parsed no recovery cases")
	}
	want := make([]recovery.CaseID, 0, len(matches))
	for _, match := range matches {
		if match[2] == "" {
			want = append(want, recovery.CaseID("RC-RESUME-"+match[1]))
			continue
		}
		for value := match[1]; ; value = nextID(value) {
			want = append(want, recovery.CaseID("RC-RESUME-"+value))
			if value == match[2] {
				break
			}
			if nextID(value) == "" {
				t.Fatalf("unparseable recovery-case range %q", match[0])
			}
		}
	}
	slices.Sort(want)
	if slices.Contains(want, recovery.CaseID("RC-RESUME-047")) {
		want = slices.DeleteFunc(want, func(caseID recovery.CaseID) bool { return caseID == "RC-RESUME-047" })
	} else {
		t.Fatal("closure did not contain mechanically exempt RC-RESUME-047")
	}
	got := Cases()
	if !slices.Equal(got, want) {
		t.Fatalf("durable-consequence catalog = %v, want exact DESIGN §6 closure %v", got, want)
	}
}

func TestCatalogAndRecoveryDispatchFailClosed(t *testing.T) {
	if err := Apply(context.Background(), HandlerContext{}, "RC-RESUME-unknown", recovery.Action{}); !errors.Is(err, ErrUnrecognizedCase) {
		t.Fatalf("unknown catalog case error = %v, want ErrUnrecognizedCase", err)
	}
	executor, err := os.ReadFile(filepath.Join("..", "recoveryexec", "executor.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, caller := range []string{"recoveryconsequence.ApplyStep", "recoveryconsequence.Apply"} {
		if !strings.Contains(string(executor), caller) {
			t.Fatalf("recovery executor no longer delegates %s to the consequence catalog", caller)
		}
	}
}

// This mismatched kind is not planner-reachable. It directly pins the
// defensive boundary that prevents one catalogued route from claiming another
// route's step handler.
func TestHandlesStepRejectsCataloguedCaseWithMismatchedKind(t *testing.T) {
	if HandlesStep(recovery.CaseHumanGateApproved, recovery.ActionProceedAcceptance, recovery.StepAppendAttemptCompleted) {
		t.Fatal("catalogued step was claimed under a mismatched action kind")
	}
}

// TestCatalogWitnessesArePlannerSelected makes the catalog lock prove more
// than registration: every catalogued consequence is selected from replayed
// planner input, with its action (and, where applicable, step order) intact.
func TestCatalogWitnessesArePlannerSelected(t *testing.T) {
	for _, witness := range catalogWitnesses() {
		t.Run(string(witness.caseID), func(t *testing.T) {
			decision := witness.plan(witness.input)
			if decision.CaseID != witness.caseID || decision.Action == nil {
				t.Fatalf("planner decision = %+v, want action for %s", decision, witness.caseID)
			}
			if decision.Action.Kind != witness.kind {
				t.Fatalf("planner action kind = %s, want %s", decision.Action.Kind, witness.kind)
			}
			if !slices.Equal(decision.Action.Steps, witness.steps) {
				t.Fatalf("planner steps = %v, want %v", decision.Action.Steps, witness.steps)
			}
		})
	}
}

// TestEveryCataloguedConsequenceHasPlannerWitness is deliberately separate
// from the selection assertions: a new handler key must add a concrete
// planner witness before the catalog lock can pass.
func TestEveryCataloguedConsequenceHasPlannerWitness(t *testing.T) {
	witnessed := make([]recovery.CaseID, 0, len(catalogWitnesses()))
	for _, witness := range catalogWitnesses() {
		witnessed = append(witnessed, witness.caseID)
	}
	slices.Sort(witnessed)
	if !slices.Equal(witnessed, Cases()) {
		t.Fatalf("planner witness cases = %v, want catalog cases %v", witnessed, Cases())
	}
}

// TestEveryCataloguedConsequenceHasJournalEffectWitness locks the lower
// contract layer separately from planner selection. Each witness names the
// constructed state, the exact appended event sequence, the design-fixed
// payload fields, and the test that applies and asserts that effect. Keeping
// references to established effect tests avoids duplicating their fixtures.
func TestEveryCataloguedConsequenceHasJournalEffectWitness(t *testing.T) {
	witnessed := make([]recovery.CaseID, 0, len(journalEffectWitnesses()))
	for _, witness := range journalEffectWitnesses() {
		if witness.state == "" || len(witness.events) == 0 || len(witness.fields) == 0 {
			t.Fatalf("journal effect witness for %s is incomplete: %+v", witness.caseID, witness)
		}
		contents, err := os.ReadFile(filepath.Join(witness.testFile))
		if err != nil {
			t.Fatalf("read journal effect witness %s for %s: %v", witness.testFile, witness.caseID, err)
		}
		if !strings.Contains(string(contents), "func "+witness.testName+"(") {
			t.Fatalf("journal effect witness for %s references missing %s in %s", witness.caseID, witness.testName, witness.testFile)
		}
		witnessed = append(witnessed, witness.caseID)
	}
	slices.Sort(witnessed)
	if !slices.Equal(witnessed, Cases()) {
		t.Fatalf("journal effect witness cases = %v, want catalog cases %v", witnessed, Cases())
	}
}

type catalogWitness struct {
	caseID recovery.CaseID
	kind   recovery.ActionKind
	steps  []recovery.ActionStep
	input  recovery.Input
	plan   func(recovery.Input) recovery.Decision
}

type journalEffectWitness struct {
	caseID   recovery.CaseID
	state    string
	events   []runstate.EventType
	fields   []string
	testFile string
	testName string
}

func journalEffectWitnesses() []journalEffectWitness {
	const (
		amendmentTests = "../amendmentexec/dispositioner_test.go"
		recoveryTests  = "../recoveryexec/executor_test.go"
	)
	return []journalEffectWitness{
		{recovery.CaseBlockedProposalRoute, "attempt.blocked with a frozen blocking proposal route", []runstate.EventType{runstate.EventAmendmentRoutedHuman}, []string{"proposal_record_hash", "decision_id", "blocking"}, amendmentTests, "TestRecoveryCompletesFrozenBlockingProposalRouteThenRequest"},
		{recovery.CaseRoutedAmendment, "durable amendment.routed_human without its decision.requested", []runstate.EventType{runstate.EventDecisionRequested}, []string{"decision_id", "decision_type", "proposal_id", "routed_reason", "blocking", "emitted_id"}, amendmentTests, "TestRecoveryCompletesFrozenBlockingProposalRouteThenRequest"},
		{recovery.CaseCompositionTerminal, "durable movement composition failure without its terminal", []runstate.EventType{runstate.EventMovementFailed}, []string{"reason", "run_failed", "causation_id"}, recoveryTests, "TestAppendCompositionTerminalKeepsEvidenceStateNeutralUntilTerminal"},
		{recovery.CaseRealizeDisposition, "attempt.failed with recorded terminal disposition", []runstate.EventType{runstate.EventMovementFailed}, []string{"reason", "run_failed", "causation_id"}, recoveryTests, "TestIncompleteAttemptBudgetExhaustionRealizesRecordedDisposition"},
		{recovery.CaseAppendQuestionRequest, "attempt.blocked with an unrequested raised question", []runstate.EventType{runstate.EventDecisionRequested}, []string{"decision_id", "decision_type", "emitted_id", "question"}, recoveryTests, "TestAppendQuestionRequestDerivesOnlyFromBlockedSource"},
		{recovery.CaseMovementSucceeded, "attempt.completed without movement.succeeded", []runstate.EventType{runstate.EventMovementSucceeded}, []string{"approved_artifact_instance_ids", "approved_change_set_id", "identity_versions", "run_succeeded"}, recoveryTests, "TestRCResume019MatchesLiveMovementSuccessPayload"},
		{recovery.CaseRunFailed, "movement.failed while the run is nonterminal", []runstate.EventType{runstate.EventRunFailed}, []string{"reason", "causation_id"}, recoveryTests, "TestDefaultDirectKindsAppendToRealStore"},
		{recovery.CaseFinalGateRejected, "resolved rejected final human gate", []runstate.EventType{runstate.EventMovementFailed}, []string{"reason", "decision_id", "subject_tree", "run_failed"}, recoveryTests, "TestRecoveryFinalGateRejectionEndsAtomically"},
		{recovery.CaseAcceptanceFailed, "durable acceptance.failed with an unrealized disposition", []runstate.EventType{runstate.EventMovementFailed}, []string{"reason", "run_failed", "causation_id"}, recoveryTests, "TestStagedAcceptanceFailureRealizesRecordedDisposition"},
		{recovery.CaseCriterionFailed, "completed failing criterion without acceptance.failed", []runstate.EventType{runstate.EventAcceptanceFailed}, []string{"reason", "subject_tree", "failed_criterion_id", "disposition"}, recoveryTests, "TestExecutorRecoversIncompleteCriterionAfterSweep"},
		{recovery.CaseCriteriaPassed, "all planned criteria passed without acceptance.evaluation_completed", []runstate.EventType{runstate.EventAcceptanceEvaluationCompleted}, []string{"subject_tree", "acceptance_spec_hash", "criterion_outcomes", "identity_versions"}, recoveryTests, "TestRCResume033ReachesAcceptanceEvaluationCompletedAfterSelectedCriterion"},
		{recovery.CaseRequestHumanGate, "evaluated acceptance with a missing required human-gate request", []runstate.EventType{runstate.EventDecisionRequested}, []string{"decision_id", "decision_type", "gate_id", "gate_mode", "subject_tree", "blocking_findings"}, recoveryTests, "TestRecoveryFinalGateRejectionEndsAtomically"},
		{recovery.CaseHumanGateApproved, "approved required human gate after evaluation", []runstate.EventType{runstate.EventAttemptCompleted, runstate.EventMovementSucceeded}, []string{"approved_artifact_instance_ids", "identity_versions", "run_succeeded"}, recoveryTests, "TestDefaultAcceptanceHandlersAppendToRealStore"},
		{recovery.CaseHumanGateRejected, "resolved rejected non-final human gate", []runstate.EventType{runstate.EventMovementFailed}, []string{"reason", "decision_id", "subject_tree", "run_failed"}, recoveryTests, "TestRecoveryNonFinalGateRejectionCascades"},
		{recovery.CaseGateFreeCompletion, "evaluated acceptance with no human gate", []runstate.EventType{runstate.EventAttemptCompleted, runstate.EventMovementSucceeded}, []string{"approved_artifact_instance_ids", "identity_versions", "run_succeeded"}, recoveryTests, "TestDefaultAcceptanceHandlersAppendToRealStore"},
	}
}

func catalogWitnesses() []catalogWitness {
	return []catalogWitness{
		{recovery.CaseBlockedProposalRoute, recovery.ActionAppendBlockedProposalRoute, nil, plannerInput(recovery.Projection{BlockedProposalRoutes: []recovery.BlockedProposalRoute{{ProposalID: "proposal-1", AttemptID: "attempt-1", ScoreRevision: 1}}}), recovery.Plan},
		{recovery.CaseRoutedAmendment, recovery.ActionAppendRoutedRequest, nil, plannerInput(recovery.Projection{State: stateWithRoutedAmendment()}), recovery.Plan},
		{recovery.CaseCompositionTerminal, recovery.ActionAppendCompositionTerminal, nil, plannerInput(recovery.Projection{CompositionTerminals: []recovery.CompositionTerminal{{Scope: "movement", TargetID: "write", Reason: "composition_conflicted", EvidenceEventID: "event-1", ScoreRevision: 1}}}), recovery.Plan},
		{recovery.CaseRealizeDisposition, recovery.ActionRealizeRecordedDisposition, nil, attemptInput(runstate.AttemptFailed, func(attempt *recovery.AttemptRecovery) {
			attempt.RecordedDisposition = &runstate.Disposition{Charged: "none", MovementTerminal: true, TerminalReason: "task_failed"}
		}), recovery.PlanAttempt},
		{recovery.CaseAppendQuestionRequest, recovery.ActionAppendQuestionRequest, nil, attemptInput(runstate.AttemptBlocked, func(attempt *recovery.AttemptRecovery) {
			attempt.QuestionRequests = []recovery.QuestionRequest{{DecisionID: "question-1"}}
		}), recovery.PlanAttempt},
		{recovery.CaseMovementSucceeded, recovery.ActionAppendMovementSucceeded, nil, attemptInput(runstate.AttemptCompleted, nil), recovery.PlanAttempt},
		{recovery.CaseRunFailed, recovery.ActionAppendRunFailed, nil, attemptInput(runstate.AttemptFailed, func(attempt *recovery.AttemptRecovery) {
			attempt.MovementFailed = true
			attempt.FailureDispositionRealized = true
		}), recovery.PlanAttempt},
		{recovery.CaseFinalGateRejected, recovery.ActionAppendFinalGateFailure, nil, attemptInput(runstate.AttemptFailed, func(attempt *recovery.AttemptRecovery) {
			attempt.FinalGateRejected = true
			attempt.FailureDispositionRealized = true
		}), recovery.PlanAttempt},
		{recovery.CaseAcceptanceFailed, recovery.ActionRealizeRecordedDisposition, nil, acceptanceInput(func(projection *recovery.Projection) {
			projection.Acceptance = &recovery.AcceptanceRecovery{Failed: true}
			projection.CurrentHeadAttempt.RecordedDisposition = &runstate.Disposition{Charged: "none", MovementTerminal: true, TerminalReason: "task_failed"}
		}), recovery.PlanAcceptance},
		{recovery.CaseCriterionFailed, recovery.ActionAppendAcceptanceFailure, []recovery.ActionStep{recovery.StepClassifyAcceptanceFailure}, acceptanceInput(func(projection *recovery.Projection) {
			acceptance := projection.State.Acceptances["attempt-1"]
			acceptance.PlannedCriterionIDs = []runstate.CriterionID{"criterion-1"}
			acceptance.Criteria = map[runstate.CriterionID]runstate.CriterionRecord{"criterion-1": {Completed: true, Outcome: "FAIL"}}
			projection.State.Acceptances["attempt-1"] = acceptance
		}), recovery.PlanAcceptance},
		{recovery.CaseCriteriaPassed, recovery.ActionAppendEvaluationCompleted, nil, acceptanceInput(func(projection *recovery.Projection) {
			acceptance := projection.State.Acceptances["attempt-1"]
			acceptance.PlannedCriterionIDs = []runstate.CriterionID{"criterion-1"}
			acceptance.Criteria = map[runstate.CriterionID]runstate.CriterionRecord{"criterion-1": {Completed: true, Outcome: "PASS"}}
			projection.State.Acceptances["attempt-1"] = acceptance
		}), recovery.PlanAcceptance},
		{recovery.CaseRequestHumanGate, recovery.ActionAppendHumanGateRequest, nil, evaluatedAcceptanceInput(recovery.GateRecovery{Required: true, DecisionID: "gate-1"}), recovery.PlanAcceptance},
		{recovery.CaseHumanGateApproved, recovery.ActionAppendAcceptanceSuccess, []recovery.ActionStep{recovery.StepAppendAttemptCompleted, recovery.StepAppendMovementSucceeded}, evaluatedAcceptanceInput(recovery.GateRecovery{Required: true, Requested: true, Resolved: true, Approved: true, DecisionID: "gate-1"}), recovery.PlanAcceptance},
		{recovery.CaseHumanGateRejected, recovery.ActionAppendGateRejectedFailure, nil, evaluatedAcceptanceInput(recovery.GateRecovery{Required: true, Requested: true, Resolved: true, DecisionID: "gate-1"}), recovery.PlanAcceptance},
		{recovery.CaseGateFreeCompletion, recovery.ActionAppendAcceptanceSuccess, []recovery.ActionStep{recovery.StepAppendAttemptCompleted, recovery.StepAppendMovementSucceeded}, evaluatedAcceptanceInput(recovery.GateRecovery{}), recovery.PlanAcceptance},
	}
}

func plannerInput(projection recovery.Projection) recovery.Input {
	if projection.State.Movements == nil {
		projection.State = basePlannerState()
	}
	return recovery.Input{Projection: projection}
}

func attemptInput(state runstate.AttemptState, configure func(*recovery.AttemptRecovery)) recovery.Input {
	projection := recovery.Projection{State: basePlannerState(), CurrentHeadAttempt: &recovery.AttemptRecovery{AttemptID: "attempt-1", MovementID: "write", ScoreRevision: 1, State: state}}
	if configure != nil {
		configure(projection.CurrentHeadAttempt)
	}
	return recovery.Input{Projection: projection}
}

func acceptanceInput(configure func(*recovery.Projection)) recovery.Input {
	projection := recovery.Projection{State: basePlannerState(), CurrentHeadAttempt: &recovery.AttemptRecovery{AttemptID: "attempt-1", MovementID: "write", ScoreRevision: 1, State: runstate.AttemptVerifying, AcceptanceStarted: true}}
	projection.State.Acceptances["attempt-1"] = runstate.Acceptance{Started: true, SubjectTree: "git-sha1:subject", Criteria: map[runstate.CriterionID]runstate.CriterionRecord{}}
	if configure != nil {
		configure(&projection)
	}
	return recovery.Input{Projection: projection, Observations: recovery.Observations{AcceptanceSubject: recovery.SubjectMatched}}
}

func evaluatedAcceptanceInput(gate recovery.GateRecovery) recovery.Input {
	input := acceptanceInput(nil)
	acceptance := input.Projection.State.Acceptances["attempt-1"]
	acceptance.EvaluationCompleted = true
	input.Projection.State.Acceptances["attempt-1"] = acceptance
	input.Projection.Acceptance = &recovery.AcceptanceRecovery{Gate: gate}
	return input
}

func basePlannerState() runstate.State {
	state := runstate.NewState([]runstate.MovementSeed{{ID: "write", Initial: runstate.MovementPending}})
	state.Run = runstate.RunRunning
	state.ScoreHead.Revision = 1
	return state
}

func stateWithRoutedAmendment() runstate.State {
	state := basePlannerState()
	state.RoutedAmendments["proposal-1"] = runstate.RoutedAmendment{ProposalID: "proposal-1", DecisionID: "decision-1"}
	return state
}

func TestFrozenRoutePayloadRejectsRecordHashMismatch(t *testing.T) {
	root := t.TempDir()
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	const runID = runstate.RunID("run-1")
	record := []byte(`{"proposal_id":"proposal-1","attempt_id":"attempt-1","emitted_id":"emitted-1","requires_decision":true}`)
	path := filepath.Join(root, ".partitur", "runs", string(runID), "proposals")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "proposal-1.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	source := runstate.Event{AttemptID: "attempt-1"}
	raised := map[string]any{"proposal_id": "proposal-1", "decision_id": "decision-1", "blocking": true}
	descriptor := map[string]any{"proposal_record_hash": rawHash(record), "reason": "requires_decision"}
	payload, err := frozenRoutePayload(HandlerContext{Store: store, RunID: runID}, source, raised, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if payload["emitted_id"] != "emitted-1" || payload["decision_id"] != "decision-1" || payload["blocking"] != true {
		t.Fatalf("frozen route payload = %#v", payload)
	}
	if err := os.WriteFile(filepath.Join(path, "proposal-1.json"), append(append([]byte(nil), record...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := frozenRoutePayload(HandlerContext{Store: store, RunID: runID}, source, raised, descriptor); !errors.Is(err, ErrMissingProposalRecord) {
		t.Fatalf("tampered record error = %v, want ErrMissingProposalRecord", err)
	}
}

func nextID(value string) string {
	number, err := strconv.Atoi(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%03d", number+1)
}
