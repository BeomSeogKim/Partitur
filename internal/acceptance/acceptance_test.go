package acceptance

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

const (
	testRunID     = runstate.RunID("018f05c8-1b4a-7abc-8def-0123456789ab")
	testAttemptID = runstate.AttemptID("018f05c8-1b4b-7abc-8def-0123456789ab")
)

func TestGeneratedCheckCompletesButCannotEarnVerified(t *testing.T) {
	generatedPlan := compileFixture(t, movementFixture())
	generatedRecorder := newAcceptanceRecorder()
	generated, err := evaluate(
		generatedPlan,
		evaluationFixture(generatedRecorder, artifactLookup(artifactFixture())),
		clockFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !generated.EvaluationCompleted || generated.Verified {
		t.Fatalf("generated-only result = %#v", generated)
	}
	if !generatedRecorder.state.Acceptances[testAttemptID].EvaluationCompleted {
		t.Fatal("generated-only evaluation did not durably complete")
	}

	declaredMovement := movementFixture()
	declaredMovement.Acceptance.ArtifactCriteria = []score.ArtifactCriterionView{{
		ID: "report-present", ArtifactID: "report",
	}}
	declaredPlan := compileFixture(t, declaredMovement)
	declaredRecorder := newAcceptanceRecorder()
	declared, err := evaluate(
		declaredPlan,
		evaluationFixture(declaredRecorder, artifactLookup(artifactFixture())),
		clockFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !declared.EvaluationCompleted || !declared.Verified {
		t.Fatalf("declared result = %#v", declared)
	}
}

func TestRunCriterionCompilesInDeclarationOrderAndEarnsVerified(t *testing.T) {
	movement := movementFixture()
	movement.Outputs = nil
	movement.Acceptance.RunCriteria = []score.RunCriterionView{{
		SourceIndex: 0, ID: "argv", Argv: []string{"criterion-helper", "pass"}, TimeoutMin: 2,
	}}
	plan := compileFixture(t, movement)
	if len(plan.criteria) != 1 || plan.criteria[0].id != "argv" || len(plan.criteria[0].run) != 2 {
		t.Fatalf("compiled run plan = %#v", plan.criteria)
	}
	recorder := newAcceptanceRecorder()
	evaluation := evaluationFixture(recorder, nil)
	evaluation.RunCriterion = func(request RunCriterionRequest) RunCriterionResult {
		if request.ID != "argv" || request.TimeoutMin != 2 || len(request.Argv) != 2 {
			t.Fatalf("run request = %#v", request)
		}
		if _, err := request.RecordStarted(runstate.ProcessIdentity{
			PID: 123, SessionID: 123,
			Start: runstate.LinuxStartIdentity{BootID: "boot", StartTicks: "1"},
		}); err != nil {
			t.Fatal(err)
		}
		code := int64(0)
		return RunCriterionResult{Outcome: "PASS", ExitCode: &code, DurationMS: 4, OutputRef: "attempts/test/criteria/argv"}
	}
	result, err := evaluate(plan, evaluation, clockFixture())
	if err != nil {
		t.Fatal(err)
	}
	if !result.EvaluationCompleted || !result.Verified {
		t.Fatalf("run result = %#v", result)
	}
	if len(recorder.events) != 4 || recorder.events[1].Type != runstate.EventCriterionStarted || recorder.events[2].Type != runstate.EventCriterionCompleted {
		t.Fatalf("run lifecycle = %#v", recorder.events)
	}
}

func TestRunCriterionBudgetExhaustionReportsForDriverTerminalization(t *testing.T) {
	movement := movementFixture()
	movement.Outputs = nil
	movement.Acceptance.RunCriteria = []score.RunCriterionView{{
		SourceIndex: 0, ID: "argv", Argv: []string{"criterion-helper"},
	}}
	plan := compileFixture(t, movement)
	recorder := newAcceptanceRecorder()
	evaluation := evaluationFixture(recorder, nil)
	evaluation.RunCriterion = func(request RunCriterionRequest) RunCriterionResult {
		if _, err := request.RecordStarted(runstate.ProcessIdentity{
			PID: 123, SessionID: 123,
			Start: runstate.LinuxStartIdentity{BootID: "boot", StartTicks: "1"},
		}); err != nil {
			t.Fatal(err)
		}
		return RunCriterionResult{
			BudgetExhausted: true,
			Outcome:         "ERROR",
			Reason:          "criterion_errored",
			ErrorDetail:     "acceptance_budget_exhausted",
			OutputRef:       "attempts/test/criteria/argv",
		}
	}
	result, err := evaluate(plan, evaluation, clockFixture())
	if err != nil {
		t.Fatal(err)
	}
	if !result.BudgetExhausted || result.EvaluationCompleted || result.FailedCriterionID != "" {
		t.Fatalf("budget result = %#v", result)
	}
	if len(recorder.events) != 3 || recorder.events[2].Type != runstate.EventCriterionCompleted {
		t.Fatalf("budget lifecycle = %#v, want criterion completion without acceptance.failed", recorder.events)
	}
}

func TestCompleteStartedWithRunCriterionNeedsNoRunExecutor(t *testing.T) {
	movement := movementFixture()
	movement.Outputs = nil
	movement.Acceptance.RunCriteria = []score.RunCriterionView{{
		SourceIndex: 0, ID: "argv", Argv: []string{"criterion-helper", "pass"},
	}}
	plan := compileFixture(t, movement)
	recorder := newAcceptanceRecorder()
	evaluation := evaluationFixture(recorder, nil)
	started, err := plan.StartEvent(runstate.Event{
		RunID: evaluation.RunID, ScoreRevision: evaluation.ScoreRevision,
		MovementID: evaluation.MovementID, PartID: evaluation.PartID, AttemptID: evaluation.AttemptID,
	}, evaluation.SubjectTree)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.append(started); err != nil {
		t.Fatal(err)
	}
	criterion := plan.criteria[0]
	criterionStarted, err := eventWithPayload(runstate.Event{
		RunID: evaluation.RunID, ScoreRevision: evaluation.ScoreRevision,
		MovementID: evaluation.MovementID, PartID: evaluation.PartID, AttemptID: evaluation.AttemptID,
	}, runstate.EventCriterionStarted, map[string]any{
		"criterion_id":        criterion.id,
		"criterion_spec_hash": criterion.specHash,
		"subject_tree":        evaluation.SubjectTree,
		"identity_versions":   plan.criterionVersions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.append(criterionStarted); err != nil {
		t.Fatal(err)
	}
	criterionCompleted, err := eventWithPayload(runstate.Event{
		RunID: evaluation.RunID, ScoreRevision: evaluation.ScoreRevision,
		MovementID: evaluation.MovementID, PartID: evaluation.PartID, AttemptID: evaluation.AttemptID,
	}, runstate.EventCriterionCompleted, map[string]any{
		"criterion_id":        criterion.id,
		"criterion_spec_hash": criterion.specHash,
		"subject_tree":        evaluation.SubjectTree,
		"outcome":             "PASS",
		"duration_ms":         int64(0),
		"identity_versions":   plan.criterionVersions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.append(criterionCompleted); err != nil {
		t.Fatal(err)
	}
	result, err := CompleteStarted(plan, evaluation, []CriterionOutcome{{
		CriterionID: "argv", Outcome: "PASS",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.EvaluationCompleted || !result.Verified {
		t.Fatalf("completion result = %#v", result)
	}
	if len(recorder.events) != 4 || recorder.events[3].Type != runstate.EventAcceptanceEvaluationCompleted {
		t.Fatalf("completion lifecycle = %#v", recorder.events)
	}
}

func TestRunCriterionHashCoversIDArgvAndTimeoutWithoutChangingArtifactHash(t *testing.T) {
	artifact := movementFixture()
	artifact.Acceptance.ArtifactCriteria = []score.ArtifactCriterionView{{ID: "artifact", ArtifactID: "report"}}
	artifactBefore := compileFixture(t, artifact).criteria[0].specHash
	artifactAfter := compileFixture(t, artifact).criteria[0].specHash
	if artifactBefore != artifactAfter {
		t.Fatalf("artifact hash changed: %s != %s", artifactBefore, artifactAfter)
	}
	newRun := func(id string, argv []string, timeout int64) score.MovementView {
		value := movementFixture()
		value.Outputs = nil
		value.Acceptance.RunCriteria = []score.RunCriterionView{{ID: id, Argv: argv, TimeoutMin: timeout}}
		return value
	}
	base := newRun("run", []string{"tool", "one"}, 1)
	hash := compileFixture(t, base).criteria[0].specHash
	for _, value := range []score.MovementView{newRun("other", []string{"tool", "one"}, 1), newRun("run", []string{"tool", "two"}, 1), newRun("run", []string{"tool", "one"}, 2)} {
		if got := compileFixture(t, value).criteria[0].specHash; got == hash {
			t.Fatalf("run hash did not cover mutation: %s", got)
		}
	}
}

func TestCompileOrdersDeclaredThenGeneratedAndSuppressesByOutput(t *testing.T) {
	movement := movementFixture()
	movement.Outputs = []score.OutputView{
		{ArtifactID: "declared-second", Kind: "artifact"},
		{ArtifactID: "generated-second", Kind: "artifact"},
		{ArtifactID: "declared-first", Kind: "findings"},
		{ArtifactID: "generated-first", Kind: "artifact"},
	}
	movement.Acceptance.ArtifactCriteria = []score.ArtifactCriterionView{
		{ID: "criterion-first", ArtifactID: "declared-first"},
		{ID: "criterion-second", ArtifactID: "declared-second"},
	}
	plan := compileFixture(t, movement)

	var ids []string
	var generated []bool
	for _, criterion := range plan.criteria {
		ids = append(ids, criterion.id)
		generated = append(generated, criterion.generated)
	}
	wantIDs := []string{
		"criterion-first",
		"criterion-second",
		"partitur.artifact.generated-second",
		"partitur.artifact.generated-first",
	}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Fatalf("criterion order = %#v, want %#v", ids, wantIDs)
	}
	if !reflect.DeepEqual(generated, []bool{false, false, true, true}) {
		t.Fatalf("generated flags = %#v", generated)
	}
	for _, criterion := range plan.criteria {
		if criterion.id == "partitur.artifact.declared-first" ||
			criterion.id == "partitur.artifact.declared-second" {
			t.Fatalf("referenced output retained generated check: %#v", criterion)
		}
	}
}

func TestGeneratedChecksParticipateInAcceptanceSpecHash(t *testing.T) {
	one := movementFixture()
	two := movementFixture()
	two.Outputs = append(two.Outputs, score.OutputView{
		ArtifactID: "notes", Kind: "artifact",
	})
	removed := movementFixture()
	removed.Outputs = nil

	onePlan := compileFixture(t, one)
	twoPlan := compileFixture(t, two)
	removedPlan := compileFixture(t, removed)
	if onePlan.Hash() == twoPlan.Hash() ||
		onePlan.Hash() == removedPlan.Hash() ||
		twoPlan.Hash() == removedPlan.Hash() {
		t.Fatalf(
			"output changes did not change plan hashes: one=%s two=%s removed=%s",
			onePlan.Hash(),
			twoPlan.Hash(),
			removedPlan.Hash(),
		)
	}

	withChangeSet := movementFixture()
	withChangeSet.Outputs = append(withChangeSet.Outputs, score.OutputView{
		ArtifactID: "changes", Kind: "change_set",
	})
	if got := compileFixture(t, withChangeSet).Hash(); got != onePlan.Hash() {
		t.Fatalf("change_set generated an artifact check: got=%s want=%s", got, onePlan.Hash())
	}

	hard := make([]any, len(onePlan.criteria))
	for index, criterion := range onePlan.criteria {
		hard[index] = string(criterion.specHash)
	}
	want, err := canonical.Hash(canonical.DomainAcceptanceSpec, map[string]any{
		"hard":       hard,
		"review":     []any{},
		"human_gate": "never",
	})
	if err != nil {
		t.Fatal(err)
	}
	if onePlan.Hash() != runstate.Hash(want) {
		t.Fatalf("acceptance hash = %s, want %s", onePlan.Hash(), want)
	}

	withAlwaysGate := movementFixture()
	withAlwaysGate.Acceptance.HumanGate = "always"
	if _, err := Compile(withAlwaysGate); err != nil {
		t.Fatalf("human_gate always compile error = %v", err)
	}
	withContestedGate := movementFixture()
	withContestedGate.Acceptance.HumanGate = "on_contested"
	if _, err := Compile(withContestedGate); !errors.Is(err, ErrUnsupportedCriteria) ||
		!strings.Contains(err.Error(), "unit 4.1") {
		t.Fatalf("human_gate on_contested error = %v, want unsupported criteria naming unit 4.1", err)
	}
}

func TestEvaluateRecordsFixedOrderAndRepeatsSubjectBinding(t *testing.T) {
	movement := movementFixture()
	movement.Outputs = append(movement.Outputs, score.OutputView{
		ArtifactID: "notes", Kind: "findings",
	})
	plan := compileFixture(t, movement)
	recorder := newAcceptanceRecorder()
	artifacts := map[runstate.ArtifactInstanceID]runstate.ArtifactRecord{
		"report@" + runstate.ArtifactInstanceID(testAttemptID): artifactFixture(),
		"notes@" + runstate.ArtifactInstanceID(testAttemptID): {
			AttemptID:       testAttemptID,
			LogicalOutputID: "notes",
			Kind:            "findings",
			ContentHash:     "sha256:notes",
		},
	}
	result, err := evaluate(
		plan,
		evaluationFixture(recorder, artifactLookupMap(artifacts)),
		clockFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.EvaluationCompleted {
		t.Fatalf("result = %#v", result)
	}
	gotTypes := eventTypes(recorder.events)
	wantTypes := []runstate.EventType{
		runstate.EventAcceptanceStarted,
		runstate.EventCriterionStarted,
		runstate.EventCriterionCompleted,
		runstate.EventCriterionStarted,
		runstate.EventCriterionCompleted,
		runstate.EventAcceptanceEvaluationCompleted,
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("event order = %#v, want %#v", gotTypes, wantTypes)
	}
	for _, event := range recorder.events {
		if event.Type != runstate.EventCriterionStarted &&
			event.Type != runstate.EventCriterionCompleted {
			continue
		}
		if payloadString(t, event, "subject_tree") != "git-sha1:subject" {
			t.Fatalf("%s subject = %s", event.Type, event.Payload)
		}
	}
	started := payloadObject(t, recorder.events[0])
	wantIDs := []any{"partitur.artifact.report", "partitur.artifact.notes"}
	if !reflect.DeepEqual(started["planned_criterion_ids"], wantIDs) {
		t.Fatalf("planned ids = %#v, want %#v", started["planned_criterion_ids"], wantIDs)
	}
	versions := started["identity_versions"].(map[string]any)
	projections := versions["projections"].(map[string]any)
	if projections[string(canonical.DomainAcceptanceSpec)] != float64(1) ||
		projections[string(canonical.DomainCriterionSpec)] != float64(1) {
		t.Fatalf("acceptance identity versions = %#v", versions)
	}
}

func TestEvaluateShortCircuitsOnFirstFailOrError(t *testing.T) {
	tests := []struct {
		name        string
		criterion   score.ArtifactCriterionView
		lookup      ArtifactLookup
		wantReason  string
		wantOutcome string
	}{
		{
			name: "FAIL",
			criterion: score.ArtifactCriterionView{
				ID: "declared-first", ArtifactID: "report",
				ExpectedHash: "sha256:deadbeef",
			},
			lookup:      artifactLookup(artifactFixture()),
			wantReason:  "artifact_hash_mismatch",
			wantOutcome: "FAIL",
		},
		{
			name: "ERROR",
			criterion: score.ArtifactCriterionView{
				ID: "declared-first", ArtifactID: "report",
			},
			lookup: func(runstate.ArtifactInstanceID) (runstate.ArtifactRecord, bool, error) {
				return artifactFixture(), true, errors.New("injected lookup failure")
			},
			wantReason:  "criterion_errored",
			wantOutcome: "ERROR",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			movement := movementFixture()
			movement.Outputs = append(movement.Outputs, score.OutputView{
				ArtifactID: "must-not-run", Kind: "artifact",
			})
			movement.Acceptance.ArtifactCriteria = []score.ArtifactCriterionView{
				test.criterion,
			}
			plan := compileFixture(t, movement)
			recorder := newAcceptanceRecorder()
			result, err := evaluate(
				plan,
				evaluationFixture(recorder, test.lookup),
				clockFixture(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.EvaluationCompleted || result.Verified ||
				result.FailedCriterionID != "declared-first" ||
				result.FailureReason != test.wantReason {
				t.Fatalf("result = %#v", result)
			}
			wantTypes := []runstate.EventType{
				runstate.EventAcceptanceStarted,
				runstate.EventCriterionStarted,
				runstate.EventCriterionCompleted,
				runstate.EventAcceptanceFailed,
			}
			if got := eventTypes(recorder.events); !reflect.DeepEqual(got, wantTypes) {
				t.Fatalf("event order = %#v, want %#v", got, wantTypes)
			}
			if got := payloadString(t, recorder.events[2], "outcome"); got != test.wantOutcome {
				t.Fatalf("outcome = %q, want %q", got, test.wantOutcome)
			}
			for _, event := range recorder.events {
				if payloadStringOrEmpty(t, event, "criterion_id") == "partitur.artifact.must-not-run" {
					t.Fatalf("runner did not short-circuit: %#v", recorder.events)
				}
			}
			if recorder.state.Attempts[testAttemptID].State != runstate.AttemptFailed {
				t.Fatalf("attempt state = %s", recorder.state.Attempts[testAttemptID].State)
			}
		})
	}
}

func TestEvaluateStartedCriterionEmitsFirstTimeCriterionEventsWithoutCompletingAcceptance(t *testing.T) {
	movement := movementFixture()
	movement.Outputs = append(movement.Outputs, score.OutputView{ArtifactID: "second", Kind: "artifact"})
	movement.Acceptance.ArtifactCriteria = []score.ArtifactCriterionView{
		{ID: "first", ArtifactID: "report"},
		{ID: "second", ArtifactID: "second"},
	}
	plan := compileFixture(t, movement)
	artifacts := map[runstate.ArtifactInstanceID]runstate.ArtifactRecord{
		"report@" + runstate.ArtifactInstanceID(testAttemptID): artifactFixture(),
		"second@" + runstate.ArtifactInstanceID(testAttemptID): {
			AttemptID: testAttemptID, LogicalOutputID: "second", Kind: "artifact", ContentHash: "sha256:second",
		},
	}

	wholeRecorder := newAcceptanceRecorder()
	if _, err := evaluate(plan, evaluationFixture(wholeRecorder, artifactLookupMap(artifacts)), clockFixture()); err != nil {
		t.Fatal(err)
	}

	selectedRecorder := newAcceptanceRecorder()
	started, err := plan.StartEvent(runstate.Event{
		RunID: testRunID, ScoreRevision: 1, MovementID: "inspect", PartID: "reader", AttemptID: testAttemptID,
	}, "git-sha1:subject")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selectedRecorder.append(started); err != nil {
		t.Fatal(err)
	}
	selectedRecorder.events = nil
	selected, err := evaluateStartedCriterion(
		plan,
		evaluationFixture(selectedRecorder, artifactLookupMap(artifacts)),
		"second",
		evaluationDependencies{now: advancingClock(time.Unix(0, 2*int64(time.Millisecond)), time.Millisecond)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected.EvaluationCompleted || selected.Verified {
		t.Fatalf("selected result = %#v", selected)
	}
	if !reflect.DeepEqual(selectedRecorder.events, wholeRecorder.events[3:5]) {
		t.Fatalf("selected events = %#v, want first-time events %#v", selectedRecorder.events, wholeRecorder.events[3:5])
	}
}

func TestEvaluateStartedCriterionShortCircuitsWithAcceptanceFailure(t *testing.T) {
	movement := movementFixture()
	movement.Acceptance.ArtifactCriteria = []score.ArtifactCriterionView{{
		ID: "report", ArtifactID: "report", ExpectedHash: "sha256:wrong",
	}}
	plan := compileFixture(t, movement)
	recorder := newAcceptanceRecorder()
	started, err := plan.StartEvent(runstate.Event{
		RunID: testRunID, ScoreRevision: 1, MovementID: "inspect", PartID: "reader", AttemptID: testAttemptID,
	}, "git-sha1:subject")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.append(started); err != nil {
		t.Fatal(err)
	}
	recorder.events = nil
	result, err := evaluateStartedCriterion(
		plan, evaluationFixture(recorder, artifactLookup(artifactFixture())), "report", clockFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.EvaluationCompleted || result.FailedCriterionID != "report" ||
		result.FailureReason != "artifact_hash_mismatch" {
		t.Fatalf("result = %#v", result)
	}
	if got, want := eventTypes(recorder.events), []runstate.EventType{
		runstate.EventCriterionStarted,
		runstate.EventCriterionCompleted,
		runstate.EventAcceptanceFailed,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("event order = %#v, want %#v", got, want)
	}
}

func TestArtifactCriterionRejectsEachEvidenceMismatch(t *testing.T) {
	tests := []struct {
		name       string
		criterion  score.ArtifactCriterionView
		lookup     ArtifactLookup
		wantReason string
	}{
		{
			name: "missing",
			criterion: score.ArtifactCriterionView{
				ID: "check", ArtifactID: "report",
			},
			lookup: func(runstate.ArtifactInstanceID) (runstate.ArtifactRecord, bool, error) {
				return artifactFixture(), false, nil
			},
			wantReason: "artifact_missing",
		},
		{
			name: "kind mismatch",
			criterion: score.ArtifactCriterionView{
				ID: "check", ArtifactID: "report",
			},
			lookup: artifactLookup(func() runstate.ArtifactRecord {
				record := artifactFixture()
				record.Kind = "findings"
				return record
			}()),
			wantReason: "artifact_kind_mismatch",
		},
		{
			name: "hash mismatch",
			criterion: score.ArtifactCriterionView{
				ID: "check", ArtifactID: "report",
				ExpectedHash: "sha256:deadbeef",
			},
			lookup:     artifactLookup(artifactFixture()),
			wantReason: "artifact_hash_mismatch",
		},
		{
			name: "record identity mismatch",
			criterion: score.ArtifactCriterionView{
				ID: "check", ArtifactID: "report",
			},
			lookup: artifactLookup(func() runstate.ArtifactRecord {
				record := artifactFixture()
				record.AttemptID = "other"
				return record
			}()),
			wantReason: "criterion_errored",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			movement := movementFixture()
			movement.Acceptance.ArtifactCriteria = []score.ArtifactCriterionView{
				test.criterion,
			}
			recorder := newAcceptanceRecorder()
			result, err := evaluate(
				compileFixture(t, movement),
				evaluationFixture(recorder, test.lookup),
				clockFixture(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.FailureReason != test.wantReason {
				t.Fatalf("result = %#v, want reason %q", result, test.wantReason)
			}
		})
	}
}

func TestArtifactExpectedHashUsesHexadecimalCaseEquivalence(t *testing.T) {
	movement := movementFixture()
	movement.Acceptance.ArtifactCriteria = []score.ArtifactCriterionView{{
		ID: "check", ArtifactID: "report", ExpectedHash: "sha256:AABB",
	}}
	recorder := newAcceptanceRecorder()
	result, err := evaluate(
		compileFixture(t, movement),
		evaluationFixture(recorder, artifactLookup(artifactFixture())),
		clockFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.EvaluationCompleted || !result.Verified {
		t.Fatalf("uppercase expected hash result = %#v", result)
	}
}

func TestCompileRejectsUnsupportedAcceptanceShapes(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*score.MovementView)
		wantMessage string
		absentUnit  string
	}{
		{
			name: "review",
			mutate: func(movement *score.MovementView) {
				movement.Acceptance.HasReviewCriteria = true
			},
			wantMessage: "unit 4.1",
			absentUnit:  "unit 3.2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			movement := movementFixture()
			test.mutate(&movement)
			if _, err := Compile(movement); !errors.Is(err, ErrUnsupportedCriteria) ||
				!strings.Contains(err.Error(), test.wantMessage) ||
				strings.Contains(err.Error(), test.absentUnit) {
				t.Fatalf("error = %v, want unsupported criteria naming %q but not %q", err, test.wantMessage, test.absentUnit)
			}
		})
	}
}

func TestEvaluateRejectsIncompleteInputAndNonDurableReceipt(t *testing.T) {
	plan := compileFixture(t, movementFixture())
	tests := []struct {
		name   string
		plan   *Plan
		mutate func(*Evaluation)
	}{
		{name: "nil plan", plan: nil, mutate: func(*Evaluation) {}},
		{name: "run id", plan: plan, mutate: func(value *Evaluation) { value.RunID = "" }},
		{name: "score revision", plan: plan, mutate: func(value *Evaluation) { value.ScoreRevision = 0 }},
		{name: "movement id", plan: plan, mutate: func(value *Evaluation) { value.MovementID = "" }},
		{name: "part id", plan: plan, mutate: func(value *Evaluation) { value.PartID = "" }},
		{name: "attempt id", plan: plan, mutate: func(value *Evaluation) { value.AttemptID = "" }},
		{name: "subject tree", plan: plan, mutate: func(value *Evaluation) { value.SubjectTree = "" }},
		{name: "artifact lookup", plan: plan, mutate: func(value *Evaluation) { value.LookupArtifact = nil }},
		{name: "append", plan: plan, mutate: func(value *Evaluation) { value.Append = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := newAcceptanceRecorder()
			evaluation := evaluationFixture(
				recorder,
				artifactLookup(artifactFixture()),
			)
			test.mutate(&evaluation)
			if _, err := Evaluate(test.plan, evaluation); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("error = %v, want ErrInvalidEvaluation", err)
			}
			if len(recorder.events) != 0 {
				t.Fatalf("events appended for incomplete input: %#v", recorder.events)
			}
		})
	}

	receiptMutations := []struct {
		name   string
		mutate func(*faultpoint.DurabilityReceipt)
	}{
		{name: "kind", mutate: func(value *faultpoint.DurabilityReceipt) {
			value.Mutation.Kind = faultpoint.FilePublication
		}},
		{name: "event type", mutate: func(value *faultpoint.DurabilityReceipt) {
			value.Mutation.EventType = "wrong"
		}},
		{name: "event id", mutate: func(value *faultpoint.DurabilityReceipt) {
			value.Mutation.EventID = ""
		}},
		{name: "sequence", mutate: func(value *faultpoint.DurabilityReceipt) {
			value.Mutation.Sequence = 0
		}},
		{name: "timestamp", mutate: func(value *faultpoint.DurabilityReceipt) {
			value.Mutation.Timestamp = ""
		}},
		{name: "path", mutate: func(value *faultpoint.DurabilityReceipt) {
			value.Mutation.Path = ""
		}},
	}
	for _, test := range receiptMutations {
		t.Run("receipt "+test.name, func(t *testing.T) {
			evaluation := evaluationFixture(
				newAcceptanceRecorder(),
				artifactLookup(artifactFixture()),
			)
			evaluation.Append = func(event runstate.Event) (faultpoint.DurabilityReceipt, error) {
				receipt := validReceipt(event, 1)
				test.mutate(&receipt)
				return receipt, nil
			}
			if _, err := Evaluate(plan, evaluation); !errors.Is(err, ErrInvalidReceipt) {
				t.Fatalf("error = %v, want ErrInvalidReceipt", err)
			}
		})
	}
}

func movementFixture() score.MovementView {
	return score.MovementView{
		ID:     "inspect",
		PartID: "reader",
		Outputs: []score.OutputView{{
			ArtifactID: "report",
			Kind:       "artifact",
		}},
		Acceptance: score.AcceptanceView{HumanGate: "never"},
	}
}

func compileFixture(t *testing.T, movement score.MovementView) *Plan {
	t.Helper()
	plan, err := Compile(movement)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func artifactFixture() runstate.ArtifactRecord {
	return runstate.ArtifactRecord{
		AttemptID:       testAttemptID,
		LogicalOutputID: "report",
		Kind:            "artifact",
		ContentHash:     "sha256:aabb",
		SizeBytes:       6,
		Source:          "report.txt",
	}
}

func artifactLookup(record runstate.ArtifactRecord) ArtifactLookup {
	return func(runstate.ArtifactInstanceID) (runstate.ArtifactRecord, bool, error) {
		return record, true, nil
	}
}

func artifactLookupMap(
	records map[runstate.ArtifactInstanceID]runstate.ArtifactRecord,
) ArtifactLookup {
	return func(id runstate.ArtifactInstanceID) (runstate.ArtifactRecord, bool, error) {
		record, ok := records[id]
		return record, ok, nil
	}
}

func evaluationFixture(
	recorder *acceptanceRecorder,
	lookup ArtifactLookup,
) Evaluation {
	return Evaluation{
		RunID:              testRunID,
		ScoreRevision:      1,
		MovementID:         "inspect",
		PartID:             "reader",
		AttemptID:          testAttemptID,
		SubjectTree:        "git-sha1:subject",
		FailureDisposition: runstate.Disposition{Charged: "none", MovementTerminal: true},
		LookupArtifact:     lookup,
		Append:             recorder.append,
	}
}

type acceptanceRecorder struct {
	state  runstate.State
	events []runstate.Event
}

func newAcceptanceRecorder() *acceptanceRecorder {
	state := runstate.NewState([]runstate.MovementSeed{{
		ID: "inspect", Initial: runstate.MovementPending,
	}})
	state.Run = runstate.RunRunning
	state.Movements["inspect"] = runstate.MovementRunning
	state.Attempts[testAttemptID] = runstate.Attempt{
		MovementID: "inspect",
		State:      runstate.AttemptVerifying,
	}
	state.VerifiedAttempts[testAttemptID] = true
	return &acceptanceRecorder{state: state}
}

func (recorder *acceptanceRecorder) append(
	event runstate.Event,
) (faultpoint.DurabilityReceipt, error) {
	next, err := runstate.Apply(recorder.state, event)
	if err != nil {
		return faultpoint.DurabilityReceipt{}, err
	}
	recorder.state = next
	recorder.events = append(recorder.events, event)
	sequence := uint64(len(recorder.events))
	return validReceipt(event, sequence), nil
}

func validReceipt(
	event runstate.Event,
	sequence uint64,
) faultpoint.DurabilityReceipt {
	return faultpoint.DurabilityReceipt{
		Address: faultpoint.ReceiptAddress("acceptance.test"),
		Mutation: faultpoint.Mutation{
			Kind:      faultpoint.JournalAppend,
			RunID:     string(event.RunID),
			EventID:   "event",
			EventType: string(event.Type),
			Sequence:  sequence,
			Timestamp: "2026-07-27T00:00:00.000Z",
			Path:      ".partitur/runs/test/journal.jsonl",
		},
	}
}

func clockFixture() evaluationDependencies {
	return evaluationDependencies{now: advancingClock(time.Unix(0, 0), time.Millisecond)}
}

func advancingClock(current time.Time, step time.Duration) func() time.Time {
	return evaluationDependencies{now: func() time.Time {
		value := current
		current = current.Add(step)
		return value
	}}.now
}

func eventTypes(events []runstate.Event) []runstate.EventType {
	result := make([]runstate.EventType, len(events))
	for index, event := range events {
		result[index] = event.Type
	}
	return result
}

func payloadObject(t *testing.T, event runstate.Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func payloadString(t *testing.T, event runstate.Event, name string) string {
	t.Helper()
	value, ok := payloadObject(t, event)[name].(string)
	if !ok {
		t.Fatalf("%s payload %q is not a string: %s", event.Type, name, event.Payload)
	}
	return value
}

func payloadStringOrEmpty(t *testing.T, event runstate.Event, name string) string {
	t.Helper()
	value, _ := payloadObject(t, event)[name].(string)
	return value
}
