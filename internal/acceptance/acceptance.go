// Package acceptance compiles and evaluates the in-process acceptance slice.
package acceptance

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

var (
	ErrUnsupportedCriteria = errors.New("acceptance contains unsupported criteria")
	ErrInvalidEvaluation   = errors.New("acceptance evaluation is incomplete")
	ErrInvalidReceipt      = errors.New("invalid acceptance durability receipt")
)

const generatedArtifactPrefix = "partitur.artifact."

// ArtifactLookup reads the projected immutable instance for one attempt.
type ArtifactLookup func(
	runstate.ArtifactInstanceID,
) (runstate.ArtifactRecord, bool, error)

// AppendEvent durably appends one runner-owned event.
type AppendEvent func(runstate.Event) (faultpoint.DurabilityReceipt, error)

// Evaluation binds one compiled plan to one verified attempt and subject.
type Evaluation struct {
	RunID              runstate.RunID
	ScoreRevision      uint64
	MovementID         runstate.MovementID
	PartID             string
	AttemptID          runstate.AttemptID
	SubjectTree        string
	FailureDisposition runstate.Disposition
	LookupArtifact     ArtifactLookup
	Append             AppendEvent
}

// Result reports the durable evaluation boundary and the derived VERIFIED
// mark. Generated criteria can complete evaluation but cannot set Verified.
type Result struct {
	EvaluationCompleted bool
	Verified            bool
	AcceptanceSpecHash  runstate.Hash
	FailedCriterionID   string
	FailureReason       string
	Receipts            []faultpoint.DurabilityReceipt
}

// Plan is an immutable effective acceptance plan.
type Plan struct {
	criteria           []criterion
	specHash           runstate.Hash
	declaredHard       int
	acceptanceVersions map[string]any
	criterionVersions  map[string]any
}

type criterion struct {
	id           string
	artifactID   string
	outputKind   string
	expectedHash string
	specHash     runstate.Hash
	generated    bool
}

// Hash returns the effective acceptance-spec identity.
func (plan *Plan) Hash() runstate.Hash {
	if plan == nil {
		return ""
	}
	return plan.specHash
}

// Compile builds the effective artifact-only plan. Declared criteria retain
// declaration order; generated checks retain output declaration order.
func Compile(movement score.MovementView) (*Plan, error) {
	if movement.Acceptance.HasRunCriteria ||
		movement.Acceptance.HasReviewCriteria {
		return nil, ErrUnsupportedCriteria
	}

	outputKinds := make(map[string]string, len(movement.Outputs))
	for _, output := range movement.Outputs {
		outputKinds[output.ArtifactID] = output.Kind
	}
	replaced := make(map[string]bool, len(movement.Acceptance.ArtifactCriteria))
	criteria := make([]criterion, 0, len(movement.Acceptance.ArtifactCriteria)+len(movement.Outputs))
	for _, declared := range movement.Acceptance.ArtifactCriteria {
		compiled, err := compileCriterion(
			declared.ID,
			declared.ArtifactID,
			outputKinds[declared.ArtifactID],
			declared.ExpectedHash,
			false,
		)
		if err != nil {
			return nil, err
		}
		criteria = append(criteria, compiled)
		replaced[declared.ArtifactID] = true
	}
	for _, output := range movement.Outputs {
		if output.Kind == "change_set" || replaced[output.ArtifactID] {
			continue
		}
		compiled, err := compileCriterion(
			generatedArtifactPrefix+output.ArtifactID,
			output.ArtifactID,
			output.Kind,
			"",
			true,
		)
		if err != nil {
			return nil, err
		}
		criteria = append(criteria, compiled)
	}

	hard := make([]any, len(criteria))
	for index, criterion := range criteria {
		hard[index] = string(criterion.specHash)
	}
	specHash, err := canonical.Hash(
		canonical.DomainAcceptanceSpec,
		map[string]any{
			"hard":       hard,
			"review":     []any{},
			"human_gate": movement.Acceptance.HumanGate,
		},
	)
	if err != nil {
		return nil, err
	}
	acceptanceVersions, err := identityVersions(
		canonical.DomainAcceptanceSpec,
		canonical.DomainCriterionSpec,
	)
	if err != nil {
		return nil, err
	}
	criterionVersions, err := identityVersions(canonical.DomainCriterionSpec)
	if err != nil {
		return nil, err
	}
	return &Plan{
		criteria:           criteria,
		specHash:           runstate.Hash(specHash),
		declaredHard:       len(movement.Acceptance.ArtifactCriteria),
		acceptanceVersions: acceptanceVersions,
		criterionVersions:  criterionVersions,
	}, nil
}

func compileCriterion(
	id, artifactID, outputKind, expectedHash string,
	generated bool,
) (criterion, error) {
	projection := map[string]any{
		"kind":     "hard.artifact",
		"id":       id,
		"artifact": artifactID,
	}
	if expectedHash != "" {
		projection["expected_hash"] = expectedHash
	}
	hash, err := canonical.Hash(canonical.DomainCriterionSpec, projection)
	if err != nil {
		return criterion{}, err
	}
	return criterion{
		id:           id,
		artifactID:   artifactID,
		outputKind:   outputKind,
		expectedHash: expectedHash,
		specHash:     runstate.Hash(hash),
		generated:    generated,
	}, nil
}

// Evaluate runs every in-process artifact criterion in plan order.
func Evaluate(plan *Plan, evaluation Evaluation) (Result, error) {
	return evaluate(plan, evaluation, evaluationDependencies{now: time.Now})
}

type evaluationDependencies struct {
	now func() time.Time
}

func evaluate(
	plan *Plan,
	evaluation Evaluation,
	dependencies evaluationDependencies,
) (Result, error) {
	result := Result{}
	if plan == nil || plan.specHash == "" ||
		evaluation.RunID == "" || evaluation.ScoreRevision == 0 ||
		evaluation.MovementID == "" || evaluation.PartID == "" ||
		evaluation.AttemptID == "" || evaluation.SubjectTree == "" ||
		evaluation.LookupArtifact == nil || evaluation.Append == nil {
		return result, ErrInvalidEvaluation
	}
	result.AcceptanceSpecHash = plan.specHash
	base := runstate.Event{
		RunID:         evaluation.RunID,
		ScoreRevision: evaluation.ScoreRevision,
		MovementID:    evaluation.MovementID,
		PartID:        evaluation.PartID,
		AttemptID:     evaluation.AttemptID,
	}
	criterionIDs := make([]any, len(plan.criteria))
	for index, criterion := range plan.criteria {
		criterionIDs[index] = criterion.id
	}
	started, err := eventWithPayload(base, runstate.EventAcceptanceStarted, map[string]any{
		"subject_tree":          evaluation.SubjectTree,
		"acceptance_spec_hash":  plan.specHash,
		"planned_criterion_ids": criterionIDs,
		"identity_versions":     plan.acceptanceVersions,
	})
	if err != nil {
		return result, err
	}
	if err := appendEvent(evaluation.Append, started, &result); err != nil {
		return result, err
	}

	outcomes := make([]any, 0, len(plan.criteria))
	for _, criterion := range plan.criteria {
		startedAt := dependencies.now()
		criterionStarted, err := eventWithPayload(base, runstate.EventCriterionStarted, map[string]any{
			"criterion_id":        criterion.id,
			"criterion_spec_hash": criterion.specHash,
			"subject_tree":        evaluation.SubjectTree,
			"identity_versions":   plan.criterionVersions,
		})
		if err != nil {
			return result, err
		}
		if err := appendEvent(evaluation.Append, criterionStarted, &result); err != nil {
			return result, err
		}

		outcome, reason, detail := evaluateCriterion(criterion, evaluation)
		duration := dependencies.now().Sub(startedAt).Milliseconds()
		if duration < 0 {
			duration = 0
		}
		completedPayload := map[string]any{
			"criterion_id":        criterion.id,
			"criterion_spec_hash": criterion.specHash,
			"subject_tree":        evaluation.SubjectTree,
			"outcome":             outcome,
			"duration_ms":         duration,
			"identity_versions":   plan.criterionVersions,
		}
		if outcome == "ERROR" {
			completedPayload["error_detail"] = detail
		}
		criterionCompleted, err := eventWithPayload(
			base,
			runstate.EventCriterionCompleted,
			completedPayload,
		)
		if err != nil {
			return result, err
		}
		if err := appendEvent(evaluation.Append, criterionCompleted, &result); err != nil {
			return result, err
		}
		outcomes = append(outcomes, map[string]any{
			"criterion_id":        criterion.id,
			"criterion_spec_hash": criterion.specHash,
			"outcome":             outcome,
		})
		if outcome == "PASS" {
			continue
		}

		failed, err := eventWithPayload(base, runstate.EventAcceptanceFailed, map[string]any{
			"reason":              reason,
			"failed_criterion_id": criterion.id,
			"subject_tree":        evaluation.SubjectTree,
			"disposition":         evaluation.FailureDisposition,
		})
		if err != nil {
			return result, err
		}
		if err := appendEvent(evaluation.Append, failed, &result); err != nil {
			return result, err
		}
		result.FailedCriterionID = criterion.id
		result.FailureReason = reason
		return result, nil
	}

	completed, err := eventWithPayload(
		base,
		runstate.EventAcceptanceEvaluationCompleted,
		map[string]any{
			"subject_tree":         evaluation.SubjectTree,
			"acceptance_spec_hash": plan.specHash,
			"criterion_outcomes":   outcomes,
			"identity_versions":    plan.acceptanceVersions,
		},
	)
	if err != nil {
		return result, err
	}
	if err := appendEvent(evaluation.Append, completed, &result); err != nil {
		return result, err
	}
	result.EvaluationCompleted = true
	result.Verified = plan.declaredHard > 0
	return result, nil
}

func evaluateCriterion(
	criterion criterion,
	evaluation Evaluation,
) (outcome, reason, detail string) {
	instanceID := runstate.ArtifactInstanceID(
		criterion.artifactID + "@" + string(evaluation.AttemptID),
	)
	artifact, present, err := evaluation.LookupArtifact(instanceID)
	if err != nil {
		return "ERROR", "criterion_errored", "artifact_lookup_failed"
	}
	if !present {
		return "FAIL", "artifact_missing", ""
	}
	if artifact.AttemptID != evaluation.AttemptID ||
		artifact.LogicalOutputID != criterion.artifactID {
		return "ERROR", "criterion_errored", "artifact_record_mismatch"
	}
	if artifact.Kind != criterion.outputKind {
		return "FAIL", "artifact_kind_mismatch", ""
	}
	if criterion.expectedHash != "" &&
		!strings.EqualFold(string(artifact.ContentHash), criterion.expectedHash) {
		return "FAIL", "artifact_hash_mismatch", ""
	}
	return "PASS", "", ""
}

func eventWithPayload(
	base runstate.Event,
	eventType runstate.EventType,
	payload map[string]any,
) (runstate.Event, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return runstate.Event{}, err
	}
	base.Type = eventType
	base.Payload = encoded
	if err := runstate.ValidateEvent(base); err != nil {
		return runstate.Event{}, err
	}
	return base, nil
}

func appendEvent(
	appendFn AppendEvent,
	event runstate.Event,
	result *Result,
) error {
	receipt, err := appendFn(event)
	if err != nil {
		return err
	}
	mutation := receipt.Mutation
	if mutation.Kind != faultpoint.JournalAppend ||
		mutation.EventType != string(event.Type) ||
		mutation.EventID == "" || mutation.Sequence == 0 ||
		mutation.Timestamp == "" || mutation.Path == "" {
		return fmt.Errorf("%w: %s", ErrInvalidReceipt, event.Type)
	}
	result.Receipts = append(result.Receipts, receipt)
	return nil
}

func identityVersions(domains ...canonical.Domain) (map[string]any, error) {
	projections := make(map[string]any, len(domains))
	for _, domain := range domains {
		versions, err := canonical.CurrentVersions(domain)
		if err != nil {
			return nil, err
		}
		projections[string(domain)] = versions.Projection
	}
	return map[string]any{
		"canonical_encoding": canonical.CanonicalEncodingVersion,
		"projections":        projections,
	}, nil
}
