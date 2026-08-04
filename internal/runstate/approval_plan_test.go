package runstate

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestApprovalPlanCodecCarriesEveryInvariantField(t *testing.T) {
	emittedID := "emitted-1"
	decisionID := "decision-1"
	candidateID := "candidate-1"
	plan := ApprovalPlan{
		Schema:              ApprovalPlanSchema,
		ProposalID:          "proposal-1",
		EmittedID:           &emittedID,
		Mode:                "human",
		DecisionID:          &decisionID,
		BaseRevision:        1,
		BaseHash:            "sha256:base",
		ClassifierVersion:   1,
		NewRevision:         2,
		NewSnapshotHash:     "sha256:new",
		NewSnapshotFileHash: "sha256:file",
		TypedDelta:          []any{map[string]any{"selector": "movements[0]", "operation": "replace"}},
		ActualImpact:        map[string]any{"budget": map[string]any{}},
		HeadMovements: []HeadMovement{{
			ID: "write", Initial: MovementPending, RepoWrite: true, HasDependencies: true, Final: true,
		}},
		SupersededAttemptIDs: []AttemptID{"attempt-1"},
		ObsoletedDecisionIDs: []string{"decision-old"},
		CandidateID:          &candidateID,
		EnvelopeEvaluation:   map[string]any{"guard_passed": true},
		Finalization:         true,
		IdentityVersions:     map[string]any{"partitur/score": float64(1)},
	}
	contents, err := EncodeApprovalPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(contents, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 20 {
		t.Fatalf("human approval plan fields=%d, want 20 invariant fields applicable to human mode", len(fields))
	}
	decoded, err := DecodeApprovalPlan(contents)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, plan) {
		t.Fatalf("decoded plan = %#v, want %#v", decoded, plan)
	}
}

func TestApprovalPlanSchemaIsRequiredAndExact(t *testing.T) {
	for _, test := range []struct {
		name     string
		contents []byte
	}{
		{name: "missing", contents: []byte(`{"proposal_id":"proposal-1"}`)},
		{name: "unexpected", contents: []byte(`{"schema":"partitur/approval-plan+json;v=2"}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeApprovalPlan(test.contents); err == nil {
				t.Fatal("DecodeApprovalPlan accepted an invalid schema")
			}
		})
	}
}
