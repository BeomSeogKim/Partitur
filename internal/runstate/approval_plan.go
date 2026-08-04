package runstate

import (
	"encoding/json"
	"fmt"
	"slices"
)

const ApprovalPlanSchema = "partitur/approval-plan+json;v=1"

// ApprovalPlan is the invariant portion of an amendment.approved payload.
// fenced_epoch is deliberately absent because commit decides its sole allowed
// overlay from the observed authority epoch.
type ApprovalPlan struct {
	Schema               string         `json:"schema"`
	ProposalID           ProposalID     `json:"proposal_id"`
	EmittedID            *string        `json:"emitted_id,omitempty"`
	Mode                 string         `json:"mode"`
	DecisionID           *string        `json:"decision_id,omitempty"`
	EnvelopeClass        *string        `json:"envelope_class,omitempty"`
	BaseRevision         uint64         `json:"base_revision"`
	BaseHash             Hash           `json:"base_hash"`
	ClassifierVersion    uint64         `json:"classifier_version"`
	NewRevision          uint64         `json:"new_revision"`
	NewSnapshotHash      Hash           `json:"new_snapshot_hash"`
	NewSnapshotFileHash  Hash           `json:"new_snapshot_file_hash"`
	TypedDelta           []any          `json:"typed_delta"`
	ActualImpact         map[string]any `json:"actual_impact"`
	HeadMovements        []HeadMovement `json:"head_movements"`
	SupersededAttemptIDs []AttemptID    `json:"superseded_attempt_ids"`
	ObsoletedDecisionIDs []string       `json:"obsoleted_decision_ids"`
	CandidateID          *string        `json:"candidate_id,omitempty"`
	EnvelopeEvaluation   map[string]any `json:"envelope_evaluation,omitempty"`
	Finalization         bool           `json:"finalization"`
	IdentityVersions     map[string]any `json:"identity_versions"`
}

// EncodeApprovalPlan serializes only the versioned approval-plan format.
func EncodeApprovalPlan(plan ApprovalPlan) ([]byte, error) {
	if plan.Schema != ApprovalPlanSchema {
		return nil, fmt.Errorf("approval plan schema %q is not %q", plan.Schema, ApprovalPlanSchema)
	}
	return json.Marshal(plan)
}

// DecodeApprovalPlan decodes and validates the version discriminator before a
// caller binds the plan to its prepare.
func DecodeApprovalPlan(contents []byte) (ApprovalPlan, error) {
	var plan ApprovalPlan
	if err := json.Unmarshal(contents, &plan); err != nil {
		return ApprovalPlan{}, err
	}
	if plan.Schema != ApprovalPlanSchema {
		return ApprovalPlan{}, fmt.Errorf("approval plan schema %q is not %q", plan.Schema, ApprovalPlanSchema)
	}
	return plan, nil
}

// MatchesPrepare is the closed §6 predicate over fields shared by a plan and
// its pending prepare.
func (plan ApprovalPlan) MatchesPrepare(prepare PendingPrepare) bool {
	if plan.ProposalID != prepare.ProposalID ||
		plan.BaseRevision != prepare.BaseHead.Revision ||
		plan.BaseHash != prepare.BaseHead.SemanticHash ||
		plan.NewRevision != prepare.NewHead.Revision ||
		plan.NewSnapshotHash != prepare.NewHead.SemanticHash ||
		plan.NewSnapshotFileHash != prepare.NewHead.FileHash ||
		!slices.Equal(plan.SupersededAttemptIDs, prepare.TargetAttemptIDs) ||
		plan.Mode != prepare.Mode {
		return false
	}
	switch plan.Mode {
	case "human":
		return plan.DecisionID != nil && prepare.DecisionID != nil &&
			*plan.DecisionID == *prepare.DecisionID && plan.EnvelopeClass == nil
	case "auto":
		return plan.DecisionID == nil && prepare.DecisionID == nil &&
			plan.EnvelopeClass != nil && *plan.EnvelopeClass == prepare.EnvelopeClass
	default:
		return false
	}
}
