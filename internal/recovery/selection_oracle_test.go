package recovery

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

type appendixActionRow struct {
	id      string
	caseID  CaseID
	surface string
	guards  map[string]string
}

// TestAppendixC41SelectsThisPlanner makes the finite selection table an
// executable oracle for the pure planner. C.4.1's own runstate test expands
// wildcard guards over reachable cuts. Here a representative is sufficient
// because wildcards name axes which are either outside this planner's input or
// have already been made irrelevant by the row's higher-precedence cut; every
// non-wildcard discriminant is materialized by the case fixture below.
func TestAppendixC41SelectsThisPlanner(t *testing.T) {
	rows := appendixC41ActionRows(t)
	const declaredActiveRows = 57
	if len(rows) != declaredActiveRows {
		t.Fatalf("C.4.1 active action rows = %d, want declared active set %d", len(rows), declaredActiveRows)
	}

	outOfScope := 0
	checked := 0
	for _, row := range rows {
		row := row
		t.Run(row.id, func(t *testing.T) {
			if row.surface != "resume" || preprocessingCase(row.caseID) {
				outOfScope++
				return
			}
			input, planner := appendixC41Cut(t, row)
			assertAppendixC41CutMatches(t, row)
			if got := planner(input); got.CaseID != row.caseID {
				t.Fatalf("%s selected %s, want %s", row.id, got.CaseID, row.caseID)
			} else if row.caseID == CaseIncompleteCriterion && got.Action != nil {
				want := []ActionStep{StepSweepCriterionSession, StepVerifyAcceptanceSubject}
				if !slices.Equal(got.Action.Steps, want) {
					t.Fatalf("%s steps = %v, want %v", row.id, got.Action.Steps, want)
				}
			}
			checked++
		})
	}
	if checked == 0 || outOfScope == 0 {
		t.Fatalf("C.4.1 oracle checked=%d out_of_scope=%d; both sets must be visible", checked, outOfScope)
	}
	t.Logf("C.4.1 planner oracle: %d resume rows checked, %d explicit out-of-scope rows", checked, outOfScope)
}

func appendixC41ActionRows(t *testing.T) []appendixActionRow {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")
	const header = "| Action row | Precedence | Recovery case | surface | run | integrity | owner | control | consequence | unit | phase | decision | budget | observation | Selected result |"
	const separator = "|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|"
	index := -1
	for line, value := range lines {
		if value == header {
			if index != -1 {
				t.Fatalf("duplicate C.4.1 action-table header")
			}
			index = line
		}
	}
	if index == -1 || index+1 >= len(lines) || lines[index+1] != separator {
		t.Fatalf("C.4.1 action table has missing or malformed separator")
	}
	rowID := regexp.MustCompile("^`(RA-[0-9]{3})`$")
	caseID := regexp.MustCompile("^`(RC-[A-Z]+-[0-9]{3})`$")
	seen := map[string]bool{}
	var rows []appendixActionRow
	for index++; index < len(lines) && strings.HasPrefix(lines[index], "|"); index++ {
		if index == 0 || lines[index] == separator {
			continue
		}
		cells := strings.Split(strings.Trim(lines[index], "|"), "|")
		if len(cells) != 15 {
			t.Fatalf("unparseable C.4.1 action row %q", lines[index])
		}
		for cell := range cells {
			cells[cell] = strings.TrimSpace(cells[cell])
		}
		id := rowID.FindStringSubmatch(cells[0])
		caseMatch := caseID.FindStringSubmatch(cells[2])
		if id == nil || caseMatch == nil || cells[3] == "" || cells[14] == "" {
			t.Fatalf("unparseable C.4.1 action row %q", lines[index])
		}
		if _, err := strconv.Atoi(cells[1]); err != nil {
			t.Fatalf("C.4.1 action row %s has invalid precedence %q", id[1], cells[1])
		}
		if seen[id[1]] {
			t.Fatalf("duplicate C.4.1 action row %s", id[1])
		}
		seen[id[1]] = true
		guards := make(map[string]string, 11)
		for offset, axis := range []string{"surface", "run", "integrity", "owner", "control", "consequence", "unit", "phase", "decision", "budget", "observation"} {
			if cells[offset+3] == "" {
				t.Fatalf("C.4.1 action row %s has an empty %s guard", id[1], axis)
			}
			guards[axis] = cells[offset+3]
		}
		rows = append(rows, appendixActionRow{id: id[1], caseID: CaseID(caseMatch[1]), surface: cells[3], guards: guards})
	}
	if len(rows) == 0 {
		t.Fatal("C.4.1 action-row extraction produced zero rows")
	}
	return rows
}

func preprocessingCase(caseID CaseID) bool {
	switch caseID {
	case "RC-RESUME-001", "RC-RESUME-034", "RC-RESUME-035", "RC-RESUME-036", "RC-RESUME-038":
		return true
	default:
		return false
	}
}

func appendixC41Cut(t *testing.T, row appendixActionRow) (Input, func(Input) Decision) {
	t.Helper()
	switch row.id {
	case "RA-053":
		input := c2Input(runstate.AttemptStarting)
		input.Observations.Handoff = HandoffUnverifiable
		return input, PlanAttempt
	case "RA-054":
		return withUnjournaledLaunch(c3Input("c1"), UnjournaledLaunchHandoffUnverifiable), PlanAcceptance
	case "RA-055":
		input := c2Input(runstate.AttemptStarting)
		input.Observations.Handoff = HandoffSweepFailed
		return input, PlanAttempt
	case "RA-056":
		input := c2Input(runstate.AttemptRunning)
		input.Observations.AdapterSweep = SweepUnverifiable
		return input, PlanAttempt
	case "RA-057":
		input := withAdapterProbe(c2Input(runstate.AttemptRunning))
		input.Observations.AdapterSweep = SweepUnverifiable
		return input, PlanAttempt
	case "RA-058":
		return withUnjournaledLaunch(c3Input("c1"), UnjournaledLaunchSweepUnverifiable), PlanAcceptance
	case "RA-059":
		input := withCriterion(c3Input("c1"), "c1", false, "")
		input.Observations.CriterionSweep = SweepUnverifiable
		return input, PlanAcceptance
	}
	switch row.caseID {
	case CaseTerminal:
		return withTerminal(baseInput()), Plan
	case CaseStaleLease:
		return withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1}, 2), Plan
	case CaseOrphanLease:
		return withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 2}, 1), Plan
	case CaseOwnerUnverifiable:
		return withLease(baseInput(), LeaseObservation{Exists: true, Epoch: 1}, 1), Plan
	case CaseLiveOwner:
		return withLease(baseInput(), LeaseObservation{Exists: true, Readable: true, Epoch: 1, Owner: OwnerLive}, 1), Plan
	case CaseCancellation:
		return withCancel(baseInput()), Plan
	case CasePendingPrepare:
		return withPrepare(baseInput()), Plan
	case CaseReclaimAuthority:
		return withAuthority(baseInput(), 1), Plan
	case CaseRootSnapshotDivergence:
		return withRootDivergence(baseInput()), Plan
	case CaseMissingReference:
		return withMissingReference(baseInput(), ReferenceArtifact), Plan
	case CaseRoutedAmendment:
		return withMissingRoutedRequest(baseInput()), Plan
	case CaseRevisionRestart:
		return withRevisionRestart(baseInput()), Plan
	case CaseCompositionTerminal:
		return withCompositionTerminal(baseInput()), Plan

	case CaseRealizeDisposition:
		return withFailedAttempt(c2Input(runstate.AttemptFailed), false), PlanAttempt
	case CaseAppendQuestionRequest:
		return withQuestionRequests(c2Input(runstate.AttemptBlocked), false, true), PlanAttempt
	case CaseWaitingHuman:
		return withQuestionRequests(c2Input(runstate.AttemptBlocked), true, true), PlanAttempt
	case CaseDecisionResume:
		return withQuestionRequests(c2Input(runstate.AttemptBlocked), true, false), PlanAttempt
	case CaseUnstartedAttempt:
		return c2Input(runstate.AttemptStarting), PlanAttempt
	case CaseUnprobedAttempt:
		return c2Input(runstate.AttemptRunning), PlanAttempt
	case CaseIncompleteAttempt:
		return withAdapterProbe(c2Input(runstate.AttemptRunning)), PlanAttempt
	case CaseCaptureChangeSet:
		return withRepoWrite(c2Input(runstate.AttemptVerifying)), PlanAttempt
	case CasePostHocVerification:
		return withChangeSet(c2Input(runstate.AttemptVerifying)), PlanAttempt
	case CaseStartAcceptance:
		return withVerificationPassed(withChangeSet(c2Input(runstate.AttemptVerifying))), PlanAttempt
	case CaseMovementSucceeded:
		return c2Input(runstate.AttemptCompleted), PlanAttempt
	case CaseRunFailed:
		return withMovementFailed(withFailedAttempt(c2Input(runstate.AttemptFailed), true)), PlanAttempt
	case CaseFinalGateRejected:
		return withFinalGateRejected(withFailedAttempt(c2Input(runstate.AttemptFailed), true)), PlanAttempt

	case CaseAcceptanceFailed:
		return withAcceptanceFailure(c3Input("c1")), PlanAcceptance
	case CaseCriterionFailed:
		return withCriterion(c3Input("c1"), "c1", true, "FAIL"), PlanAcceptance
	case CaseIncompleteCriterion:
		return withSubject(withCriterion(c3Input("c1"), "c1", false, ""), SubjectMatched), PlanAcceptance
	case CaseCriteriaPassed:
		return withSubject(withCriterion(c3Input("c1"), "c1", true, "PASS"), SubjectMatched), PlanAcceptance
	case CaseRequestHumanGate:
		return withRequiredGate(withEvaluationCompleted(c3Input("c1")), false, false, false), PlanAcceptance
	case CaseHumanGateWaiting:
		return withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, false, false), PlanAcceptance
	case CaseHumanGateApproved:
		return withSubject(withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, true, true), SubjectMatched), PlanAcceptance
	case CaseHumanGateRejected:
		return withRequiredGate(withEvaluationCompleted(c3Input("c1")), true, true, false), PlanAcceptance
	case CaseGateFreeCompletion:
		return withSubject(withEvaluationCompleted(c3Input("c1")), SubjectMatched), PlanAcceptance
	case CaseUnjournaledLaunch:
		return withUnjournaledLaunch(c3Input("c1"), UnjournaledLaunchMarkerFree), PlanAcceptance
	case CaseFirstCriterion:
		return withSubject(c3Input("c1"), SubjectMatched), PlanAcceptance
	case CaseNextCriterion:
		return withSubject(withCriterion(withAdditionalCriterion(c3Input("c1"), "c2"), "c1", true, "PASS"), SubjectMatched), PlanAcceptance

	case CaseBudgetExhausted:
		input := c4Cut(runstate.MovementRunning)
		input.Projection.Scheduler.RemainingTime = 0
		return input, PlanScheduler
	case CaseRecoveredComposition:
		input := c4Cut(runstate.MovementRunning)
		input.Projection.CompositionRecovery = &CompositionRecovery{Scope: "movement", MovementID: "write", Recovered: true}
		return input, PlanScheduler
	case CaseScheduler:
		return c4Cut(runstate.MovementPending), PlanScheduler
	default:
		t.Fatalf("no C.4.1 planner fixture for %s", row.caseID)
		return Input{}, nil
	}
}

func assertAppendixC41CutMatches(t *testing.T, row appendixActionRow) {
	t.Helper()
	actual := map[string]string{
		"surface": "resume", "run": "active", "integrity": "valid", "owner": "clear",
		"control": "none", "consequence": "none", "unit": "none", "phase": "idle",
		"decision": "none", "budget": "available", "observation": "safe",
	}
	switch row.caseID {
	case CaseTerminal:
		actual["run"] = "terminal"
	case CaseStaleLease:
		actual["owner"] = "stale"
	case CaseOrphanLease:
		actual["owner"] = "orphan"
	case CaseOwnerUnverifiable:
		actual["owner"], actual["observation"] = "unverifiable", "owner_unverifiable"
	case CaseLiveOwner:
		actual["owner"] = "live"
	case CaseCancellation:
		actual["control"] = "cancel"
	case CasePendingPrepare:
		actual["control"] = "prepare"
	case CaseReclaimAuthority:
		actual["owner"] = "unowned"
	case CaseRootSnapshotDivergence:
		actual["integrity"], actual["phase"] = "halt", "root_divergence"
	case CaseMissingReference:
		actual["integrity"], actual["phase"] = "halt", "missing_reference"
	case CaseRoutedAmendment:
		actual["consequence"], actual["phase"] = "request", "blocked"
	case CaseRevisionRestart:
		actual["consequence"], actual["phase"], actual["decision"] = "revision", "revision_changed", "released"
	case CaseCompositionTerminal:
		actual["consequence"], actual["unit"], actual["phase"] = "composition_terminal", "movement_composition", "completed"
	case CaseRealizeDisposition:
		actual["consequence"], actual["unit"], actual["phase"] = "disposition", "attempt", "failed"
	case CaseAcceptanceFailed:
		actual["consequence"], actual["unit"], actual["phase"] = "disposition", "acceptance", "failed"
	case CaseAppendQuestionRequest:
		actual["consequence"], actual["unit"], actual["phase"] = "request", "attempt", "blocked"
	case CaseWaitingHuman:
		actual["unit"], actual["phase"], actual["decision"] = "attempt", "blocked", "unresolved"
	case CaseDecisionResume:
		actual["unit"], actual["phase"], actual["decision"] = "attempt", "blocked", "released"
	case CaseUnstartedAttempt:
		actual["unit"], actual["phase"] = "attempt", "selected"
	case CaseUnprobedAttempt:
		actual["unit"], actual["phase"] = "attempt", "started"
	case CaseIncompleteAttempt:
		actual["unit"], actual["phase"] = "attempt", "probed"
	case CaseCaptureChangeSet:
		actual["unit"], actual["phase"] = "attempt", "performed"
	case CasePostHocVerification:
		actual["unit"], actual["phase"] = "attempt", "verified"
	case CaseStartAcceptance:
		actual["unit"], actual["phase"] = "attempt", "acceptance_ready"
	case CaseMovementSucceeded:
		actual["consequence"], actual["unit"], actual["phase"] = "lifecycle_terminal", "attempt", "completed"
	case CaseRunFailed:
		actual["consequence"], actual["unit"], actual["phase"] = "lifecycle_terminal", "attempt", "failed"
	case CaseFinalGateRejected:
		actual["consequence"], actual["unit"], actual["phase"] = "lifecycle_terminal", "attempt", "gate_rejected"
	case CaseCriterionFailed:
		actual["unit"], actual["phase"] = "acceptance", "criterion_failed"
	case CaseIncompleteCriterion:
		actual["unit"], actual["phase"] = "acceptance", "criterion_running"
	case CaseCriteriaPassed:
		actual["unit"], actual["phase"] = "acceptance", "criteria_passed"
	case CaseRequestHumanGate:
		actual["unit"], actual["phase"] = "acceptance", "evaluated"
	case CaseHumanGateWaiting:
		actual["unit"], actual["phase"], actual["decision"] = "acceptance", "gate_open", "unresolved"
	case CaseHumanGateApproved:
		actual["unit"], actual["phase"], actual["decision"] = "acceptance", "gate_approved", "released"
	case CaseHumanGateRejected:
		actual["unit"], actual["phase"], actual["decision"] = "acceptance", "gate_rejected", "released"
	case CaseGateFreeCompletion:
		actual["unit"], actual["phase"] = "acceptance", "gate_free"
	case CaseUnjournaledLaunch:
		actual["unit"], actual["phase"] = "acceptance", "criterion_pending"
	case CaseFirstCriterion:
		actual["unit"], actual["phase"] = "acceptance", "acceptance_empty"
	case CaseNextCriterion:
		actual["unit"], actual["phase"] = "acceptance", "criterion_next"
	case CaseBudgetExhausted:
		actual["budget"] = "exhausted"
	case CaseRecoveredComposition:
		actual["unit"], actual["phase"] = "movement_composition", "interrupted"
	case CaseScheduler:
		actual["unit"], actual["phase"] = "none", "idle"
	}
	switch row.id {
	case "RA-053", "RA-054":
		actual["observation"] = "handoff_unverifiable"
	case "RA-055", "RA-056", "RA-057", "RA-058", "RA-059":
		actual["observation"] = "sweep_unverifiable"
	}
	for axis, want := range actual {
		guard := row.guards[axis]
		if guard == "*" || containsSelectionValue(guard, want) {
			continue
		}
		t.Fatalf("fixture for %s has %s=%s, which does not match C.4.1 guard %q", row.id, axis, want, guard)
	}
}

func containsSelectionValue(cell, want string) bool {
	for _, value := range strings.Split(cell, ",") {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func c4Cut(write runstate.MovementState) Input {
	input := baseInput()
	input.Projection.Scheduler = Scheduler{RemainingTime: 1, Movements: []ScheduledMovement{{ID: "write", RepoWrite: true}, {ID: "check", Needs: []runstate.MovementID{"write"}, Final: true}}}
	input.Projection.State.Movements = map[runstate.MovementID]runstate.MovementState{"write": write, "check": runstate.MovementPending}
	return input
}
