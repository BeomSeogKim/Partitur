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

	for _, gate := range []string{"always", "on_contested"} {
		withGate := movementFixture()
		withGate.Acceptance.HumanGate = gate
		if _, err := Compile(withGate); !errors.Is(err, ErrUnsupportedCriteria) ||
			!strings.Contains(err.Error(), "unit 4.1") {
			t.Fatalf("human gate %q error = %v, want unsupported criteria naming unit 4.1", gate, err)
		}
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
		name   string
		mutate func(*score.MovementView)
	}{
		{
			name: "run",
			mutate: func(movement *score.MovementView) {
				movement.Acceptance.HasRunCriteria = true
			},
		},
		{
			name: "review",
			mutate: func(movement *score.MovementView) {
				movement.Acceptance.HasReviewCriteria = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			movement := movementFixture()
			test.mutate(&movement)
			if _, err := Compile(movement); !errors.Is(err, ErrUnsupportedCriteria) {
				t.Fatalf("error = %v, want ErrUnsupportedCriteria", err)
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
	current := time.Unix(0, 0)
	return evaluationDependencies{now: func() time.Time {
		value := current
		current = current.Add(time.Millisecond)
		return value
	}}
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
