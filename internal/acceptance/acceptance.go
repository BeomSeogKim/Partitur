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

// RunCriterionExecutor performs the external, process-owning portion of one
// hard.run criterion. Acceptance retains the durable lifecycle and verdict.
type RunCriterionExecutor func(RunCriterionRequest) RunCriterionResult

type RunCriterionRequest struct {
	ID            string
	Argv          []string
	TimeoutMin    int64
	RecordStarted func(runstate.ProcessIdentity) (faultpoint.DurabilityReceipt, error)
}

type RunCriterionResult struct {
	Err         error
	SpawnFailed bool
	// Cancelled reports that the owning driver observed durable control while
	// this criterion was in flight and swept its recorded session. Cancellation
	// owns the attempt terminal projection, so evaluation records no criterion
	// outcome after this boundary.
	Cancelled bool
	// BudgetExhausted reports that the run budget, rather than the
	// criterion timeout, ended this command. The driver owns the resulting
	// attempt and run terminal sequence.
	BudgetExhausted bool
	// DeadlineTied reports that the criterion timeout and remaining run budget
	// were equal. It remains a criterion timeout, but no retry is fundable.
	DeadlineTied     bool
	Outcome          string
	Reason           string
	ErrorDetail      string
	ExitCode         *int64
	DurationMS       int64
	OutputRef        string
	TruncatedStreams []string
}

// Evaluation binds one compiled plan to one verified attempt and subject.
type Evaluation struct {
	RunID                 runstate.RunID
	ScoreRevision         uint64
	MovementID            runstate.MovementID
	PartID                string
	AttemptID             runstate.AttemptID
	SubjectTree           string
	FailureDisposition    runstate.Disposition
	FailureDispositionFor func(RunCriterionResult) (runstate.Disposition, error)
	LookupArtifact        ArtifactLookup
	ReadArtifact          func(runstate.ArtifactRecord) ([]byte, error)
	ValidateEvidence      func(path string, line *int64) error
	RunCriterion          RunCriterionExecutor
	Append                AppendEvent
	ReviewOutcome         string
	BlockingFindings      []FindingReference
}

// Result reports the durable evaluation boundary and the derived VERIFIED
// mark. Generated criteria can complete evaluation but cannot set Verified.
type Result struct {
	EvaluationCompleted bool
	Verified            bool
	BudgetExhausted     bool
	Cancelled           bool
	AcceptanceSpecHash  runstate.Hash
	FailedCriterionID   string
	FailureReason       string
	FindingsInstanceID  string
	ReviewOutcome       string
	BlockingFindings    []FindingReference
	Receipts            []faultpoint.DurabilityReceipt
}

// CriterionOutcome is one PASS result used to close an already-started
// acceptance after recovery has replayed every planned criterion.
type CriterionOutcome struct {
	CriterionID string
	Outcome     string
}

type FindingReference struct {
	ArtifactInstanceID string
	FindingID          string
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
	run          []string
	timeoutMin   int64
	artifactID   string
	outputKind   string
	expectedHash string
	specHash     runstate.Hash
	generated    bool
	review       bool
	rubrics      []string
}

// Hash returns the effective acceptance-spec identity.
func (plan *Plan) Hash() runstate.Hash {
	if plan == nil {
		return ""
	}
	return plan.specHash
}

// DeclaresHardCriteria reports whether this plan can earn VERIFIED. Generated
// criteria close integrity holes but cannot earn the mark on their own.
func (plan *Plan) DeclaresHardCriteria() bool {
	return plan != nil && plan.declaredHard > 0
}

// HasReviewCriterion reports whether this plan can earn REVIEWED.
func (plan *Plan) HasReviewCriterion() bool {
	if plan == nil {
		return false
	}
	for _, criterion := range plan.criteria {
		if criterion.review {
			return true
		}
	}
	return false
}

// SatisfiesAcceptance reports whether recorded acceptance evidence is exactly
// the compiled plan and every planned criterion completed with PASS.
func (plan *Plan) SatisfiesAcceptance(recorded runstate.Acceptance) bool {
	if plan == nil || recorded.SpecHash != plan.specHash {
		return false
	}
	return plan.matchesCriteria(recorded.PlannedCriterionIDs, func(_ int, id runstate.CriterionID) bool {
		record, ok := recorded.Criteria[id]
		return ok && record.Completed && record.Outcome == "PASS"
	})
}

// Compile builds the effective acceptance plan. Declared criteria retain
// declaration order; generated checks retain output declaration order.
func Compile(movement score.MovementView) (*Plan, error) {
	if contains(movement.Grants, "repo_write") && len(movement.Acceptance.ReviewCriteria) != 0 {
		return nil, fmt.Errorf("%w: review criterion requires a read-only movement", ErrUnsupportedCriteria)
	}
	if len(movement.Acceptance.ReviewCriteria) > 1 {
		return nil, fmt.Errorf("%w: more than one review criterion", ErrUnsupportedCriteria)
	}

	outputKinds := make(map[string]string, len(movement.Outputs))
	for _, output := range movement.Outputs {
		outputKinds[output.ArtifactID] = output.Kind
	}
	replaced := make(map[string]bool, len(movement.Acceptance.ArtifactCriteria))
	criteria := make([]criterion, 0, len(movement.Acceptance.ArtifactCriteria)+len(movement.Acceptance.RunCriteria)+len(movement.Acceptance.ReviewCriteria)+len(movement.Outputs))
	type declaredCriterion struct {
		source   int
		artifact *score.ArtifactCriterionView
		run      *score.RunCriterionView
		review   *score.ReviewCriterionView
	}
	declared := make([]declaredCriterion, 0, len(movement.Acceptance.ArtifactCriteria)+len(movement.Acceptance.RunCriteria)+len(movement.Acceptance.ReviewCriteria))
	for index := range movement.Acceptance.ArtifactCriteria {
		declared = append(declared, declaredCriterion{source: movement.Acceptance.ArtifactCriteria[index].SourceIndex, artifact: &movement.Acceptance.ArtifactCriteria[index]})
	}
	for index := range movement.Acceptance.RunCriteria {
		declared = append(declared, declaredCriterion{source: movement.Acceptance.RunCriteria[index].SourceIndex, run: &movement.Acceptance.RunCriteria[index]})
	}
	for index := range movement.Acceptance.ReviewCriteria {
		declared = append(declared, declaredCriterion{source: movement.Acceptance.ReviewCriteria[index].SourceIndex, review: &movement.Acceptance.ReviewCriteria[index]})
	}
	for index := 1; index < len(declared); index++ {
		for previous := index; previous > 0 && declared[previous].source < declared[previous-1].source; previous-- {
			declared[previous], declared[previous-1] = declared[previous-1], declared[previous]
		}
	}
	for _, item := range declared {
		var compiled criterion
		var err error
		if item.artifact != nil {
			compiled, err = compileArtifactCriterion(item.artifact.ID, item.artifact.ArtifactID, outputKinds[item.artifact.ArtifactID], item.artifact.ExpectedHash, false)
			replaced[item.artifact.ArtifactID] = true
		} else if item.run != nil {
			compiled, err = compileRunCriterion(item.run.ID, item.run.Argv, item.run.TimeoutMin)
		} else {
			compiled, err = compileReviewCriterion(item.review.ID, item.review.Findings, outputKinds[item.review.Findings], item.review.Rubrics)
		}
		if err != nil {
			return nil, err
		}
		criteria = append(criteria, compiled)
	}
	for _, output := range movement.Outputs {
		if output.Kind == "change_set" || replaced[output.ArtifactID] {
			continue
		}
		compiled, err := compileArtifactCriterion(
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

	hard := make([]any, 0, len(criteria))
	review := make([]any, 0, 1)
	for _, criterion := range criteria {
		if criterion.review {
			review = append(review, string(criterion.specHash))
		} else {
			hard = append(hard, string(criterion.specHash))
		}
	}
	specHash, err := canonical.Hash(
		canonical.DomainAcceptanceSpec,
		map[string]any{
			"hard":       hard,
			"review":     review,
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
		declaredHard:       len(movement.Acceptance.ArtifactCriteria) + len(movement.Acceptance.RunCriteria),
		acceptanceVersions: acceptanceVersions,
		criterionVersions:  criterionVersions,
	}, nil
}

func compileReviewCriterion(id, artifactID, outputKind string, rubrics []string) (criterion, error) {
	projection := map[string]any{"kind": "review.findings", "id": id, "findings": artifactID, "rubric": stringsToAny(rubrics)}
	hash, err := canonical.Hash(canonical.DomainCriterionSpec, projection)
	if err != nil {
		return criterion{}, err
	}
	return criterion{id: id, artifactID: artifactID, outputKind: outputKind, specHash: runstate.Hash(hash), review: true, rubrics: append([]string(nil), rubrics...)}, nil
}

func compileArtifactCriterion(
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

func compileRunCriterion(id string, argv []string, timeoutMin int64) (criterion, error) {
	projection := map[string]any{"kind": "hard.run", "id": id, "run": stringsToAny(argv)}
	if timeoutMin != 0 {
		projection["timeout_min"] = float64(timeoutMin)
	}
	hash, err := canonical.Hash(canonical.DomainCriterionSpec, projection)
	if err != nil {
		return criterion{}, err
	}
	return criterion{id: id, run: append([]string(nil), argv...), timeoutMin: timeoutMin, specHash: runstate.Hash(hash)}, nil
}

// Evaluate runs every in-process artifact criterion in plan order.
func Evaluate(plan *Plan, evaluation Evaluation) (Result, error) {
	return evaluate(plan, evaluation, evaluationDependencies{now: time.Now})
}

// EvaluateStarted runs the live evaluator after acceptance.started is already
// durable for evaluation's exact pinned plan and subject.
func EvaluateStarted(plan *Plan, evaluation Evaluation) (Result, error) {
	return evaluateStarted(plan, evaluation, evaluationDependencies{now: time.Now})
}

// EvaluateStartedCriterion runs one named criterion after acceptance.started
// is already durable. It does not declare the whole acceptance complete.
func EvaluateStartedCriterion(plan *Plan, evaluation Evaluation, criterionID string) (Result, error) {
	return evaluateStartedCriterion(plan, evaluation, criterionID, evaluationDependencies{now: time.Now})
}

// CompleteStarted records completion for an already-started acceptance whose
// full planned criterion sequence is durably PASS.
func CompleteStarted(plan *Plan, evaluation Evaluation, outcomes []CriterionOutcome) (Result, error) {
	return completeStarted(plan, evaluation, outcomes)
}

type evaluationDependencies struct {
	now func() time.Time
}

func evaluate(
	plan *Plan,
	evaluation Evaluation,
	dependencies evaluationDependencies,
) (Result, error) {
	if plan == nil || !validCriteriaEvaluation(plan, evaluation, plan.criteria) {
		return Result{}, ErrInvalidEvaluation
	}
	base := runstate.Event{
		RunID: evaluation.RunID, ScoreRevision: evaluation.ScoreRevision,
		MovementID: evaluation.MovementID, PartID: evaluation.PartID, AttemptID: evaluation.AttemptID,
	}
	started, err := plan.StartEvent(base, evaluation.SubjectTree)
	if err != nil {
		return Result{}, err
	}
	var prefix Result
	prefix.AcceptanceSpecHash = plan.specHash
	if err := appendEvent(evaluation.Append, started, &prefix); err != nil {
		return Result{}, err
	}
	result, err := evaluateStarted(plan, evaluation, dependencies)
	result.Receipts = append(prefix.Receipts, result.Receipts...)
	return result, err
}

func evaluateStarted(plan *Plan, evaluation Evaluation, dependencies evaluationDependencies) (Result, error) {
	if plan == nil {
		return Result{}, ErrInvalidEvaluation
	}
	return evaluateStartedCriteria(plan, evaluation, plan.criteria, true, dependencies)
}

func evaluateStartedCriterion(
	plan *Plan,
	evaluation Evaluation,
	criterionID string,
	dependencies evaluationDependencies,
) (Result, error) {
	if !validEvaluation(plan, evaluation) || criterionID == "" {
		return Result{}, ErrInvalidEvaluation
	}
	for _, selected := range plan.criteria {
		if selected.id == criterionID {
			if !validCriteriaEvaluation(plan, evaluation, []criterion{selected}) {
				return Result{}, ErrInvalidEvaluation
			}
			return evaluateStartedCriteria(plan, evaluation, []criterion{selected}, false, dependencies)
		}
	}
	return Result{}, ErrInvalidEvaluation
}

func evaluateStartedCriteria(
	plan *Plan,
	evaluation Evaluation,
	criteria []criterion,
	completeEvaluation bool,
	dependencies evaluationDependencies,
) (Result, error) {
	result := Result{}
	if !validCriteriaEvaluation(plan, evaluation, criteria) {
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
	outcomes := make([]CriterionOutcome, 0, len(criteria))
	for _, criterion := range criteria {
		startedAt := dependencies.now()
		appendStarted := func(identity *runstate.ProcessIdentity, spawnFailed bool) error {
			payload := map[string]any{
				"criterion_id":        criterion.id,
				"criterion_spec_hash": criterion.specHash,
				"subject_tree":        evaluation.SubjectTree,
				"identity_versions":   plan.criterionVersions,
			}
			if identity != nil {
				payload["criterion_process"] = processPayload(*identity)
			}
			if spawnFailed {
				payload["spawn_failed"] = true
			}
			criterionStarted, err := eventWithPayload(base, runstate.EventCriterionStarted, payload)
			if err != nil {
				return err
			}
			return appendEvent(evaluation.Append, criterionStarted, &result)
		}
		var outcome, reason, detail string
		var runResult RunCriterionResult
		if len(criterion.run) != 0 {
			runResult = evaluation.RunCriterion(RunCriterionRequest{
				ID: criterion.id, Argv: append([]string(nil), criterion.run...), TimeoutMin: criterion.timeoutMin,
				RecordStarted: func(identity runstate.ProcessIdentity) (faultpoint.DurabilityReceipt, error) {
					before := len(result.Receipts)
					if err := appendStarted(&identity, false); err != nil {
						return faultpoint.DurabilityReceipt{}, err
					}
					return result.Receipts[before], nil
				},
			})
			if runResult.Err != nil {
				return result, runResult.Err
			}
			if runResult.SpawnFailed {
				if err := appendStarted(nil, true); err != nil {
					return result, err
				}
			}
			if runResult.Cancelled {
				result.Cancelled = true
				return result, nil
			}
			outcome, reason, detail = runResult.Outcome, runResult.Reason, runResult.ErrorDetail
		} else {
			if err := appendStarted(nil, false); err != nil {
				return result, err
			}
			if criterion.review {
				outcome, reason, detail = evaluateReviewCriterion(criterion, evaluation, &result)
			} else {
				outcome, reason, detail = evaluateCriterion(criterion, evaluation)
			}
		}
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
		if len(criterion.run) != 0 {
			completedPayload["duration_ms"] = runResult.DurationMS
			if runResult.ExitCode != nil {
				completedPayload["exit_code"] = *runResult.ExitCode
			}
			if runResult.OutputRef != "" {
				completedPayload["output_ref"] = runResult.OutputRef
			}
			if len(runResult.TruncatedStreams) != 0 {
				completedPayload["truncated_streams"] = stringsToAny(runResult.TruncatedStreams)
			}
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
		outcomes = append(outcomes, CriterionOutcome{CriterionID: criterion.id, Outcome: outcome})
		if runResult.BudgetExhausted {
			result.BudgetExhausted = true
			return result, nil
		}
		if outcome == "PASS" {
			continue
		}

		disposition := evaluation.FailureDisposition
		if len(criterion.run) != 0 && evaluation.FailureDispositionFor != nil {
			disposition, err = evaluation.FailureDispositionFor(runResult)
			if err != nil {
				return result, err
			}
		}
		failed, err := eventWithPayload(base, runstate.EventAcceptanceFailed, map[string]any{
			"reason":              reason,
			"failed_criterion_id": criterion.id,
			"subject_tree":        evaluation.SubjectTree,
			"disposition":         disposition,
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
	if !completeEvaluation {
		return result, nil
	}

	evaluation.ReviewOutcome = result.ReviewOutcome
	evaluation.BlockingFindings = result.BlockingFindings
	completed, err := completeStarted(plan, evaluation, outcomes)
	result.Receipts = append(result.Receipts, completed.Receipts...)
	result.EvaluationCompleted = completed.EvaluationCompleted
	result.Verified = completed.Verified
	return result, err
}

func completeStarted(plan *Plan, evaluation Evaluation, outcomes []CriterionOutcome) (Result, error) {
	result := Result{}
	if !validEvaluation(plan, evaluation) || !allCriteriaPass(plan, outcomes) {
		return result, ErrInvalidEvaluation
	}
	result.AcceptanceSpecHash = plan.specHash
	criterionOutcomes := make([]any, len(outcomes))
	for index, outcome := range outcomes {
		criterionOutcomes[index] = map[string]any{
			"criterion_id":        outcome.CriterionID,
			"criterion_spec_hash": plan.criteria[index].specHash,
			"outcome":             outcome.Outcome,
		}
	}
	base := runstate.Event{
		RunID: evaluation.RunID, ScoreRevision: evaluation.ScoreRevision,
		MovementID: evaluation.MovementID, PartID: evaluation.PartID, AttemptID: evaluation.AttemptID,
	}
	payload := map[string]any{
		"subject_tree":         evaluation.SubjectTree,
		"acceptance_spec_hash": plan.specHash,
		"criterion_outcomes":   criterionOutcomes,
		"identity_versions":    plan.acceptanceVersions,
	}
	if evaluation.ReviewOutcome != "" {
		blockers := make([]any, len(evaluation.BlockingFindings))
		for index, finding := range evaluation.BlockingFindings {
			blockers[index] = map[string]any{"artifact_instance_id": finding.ArtifactInstanceID, "finding_id": finding.FindingID}
		}
		payload["review_outcome"] = evaluation.ReviewOutcome
		payload["blocking_findings"] = blockers
	}
	completed, err := eventWithPayload(base, runstate.EventAcceptanceEvaluationCompleted, payload)
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

func allCriteriaPass(plan *Plan, outcomes []CriterionOutcome) bool {
	if plan == nil {
		return false
	}
	ids := make([]runstate.CriterionID, len(outcomes))
	for index, outcome := range outcomes {
		ids[index] = runstate.CriterionID(outcome.CriterionID)
	}
	return plan.matchesCriteria(ids, func(index int, _ runstate.CriterionID) bool {
		return outcomes[index].Outcome == "PASS"
	})
}

func (plan *Plan) matchesCriteria(ids []runstate.CriterionID, passed func(int, runstate.CriterionID) bool) bool {
	if len(ids) != len(plan.criteria) {
		return false
	}
	for index, id := range ids {
		if id != runstate.CriterionID(plan.criteria[index].id) || !passed(index, id) {
			return false
		}
	}
	return true
}

func validEvaluation(plan *Plan, evaluation Evaluation) bool {
	return plan != nil && plan.specHash != "" &&
		evaluation.RunID != "" && evaluation.ScoreRevision != 0 &&
		evaluation.MovementID != "" && evaluation.PartID != "" &&
		evaluation.AttemptID != "" && evaluation.SubjectTree != "" &&
		evaluation.Append != nil
}

func validCriteriaEvaluation(plan *Plan, evaluation Evaluation, criteria []criterion) bool {
	if !validEvaluation(plan, evaluation) {
		return false
	}
	for _, value := range criteria {
		if value.artifactID != "" && evaluation.LookupArtifact == nil {
			return false
		}
		if value.review && (evaluation.ReadArtifact == nil || evaluation.ValidateEvidence == nil) {
			return false
		}
		if len(value.run) != 0 && evaluation.RunCriterion == nil {
			return false
		}
	}
	return true
}

// StartEvent builds the durable acceptance boundary from the compiled plan.
func (plan *Plan) StartEvent(base runstate.Event, subjectTree string) (runstate.Event, error) {
	if plan == nil || plan.specHash == "" || subjectTree == "" {
		return runstate.Event{}, ErrInvalidEvaluation
	}
	criterionIDs := make([]any, len(plan.criteria))
	for index, criterion := range plan.criteria {
		criterionIDs[index] = criterion.id
	}
	return eventWithPayload(base, runstate.EventAcceptanceStarted, map[string]any{
		"subject_tree":          subjectTree,
		"acceptance_spec_hash":  plan.specHash,
		"planned_criterion_ids": criterionIDs,
		"identity_versions":     plan.acceptanceVersions,
	})
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

type findingsDocument struct {
	Schema      string             `json:"schema"`
	SubjectTree string             `json:"subject_tree"`
	Coverage    []findingsCoverage `json:"coverage"`
	Findings    []finding          `json:"findings"`
	Provenance  *provenance        `json:"provenance,omitempty"`
}

type findingsCoverage struct {
	Rubric     string  `json:"rubric"`
	Conclusion string  `json:"conclusion"`
	Note       *string `json:"note,omitempty"`
}

type finding struct {
	ID       string     `json:"id"`
	Rubric   string     `json:"rubric"`
	Summary  string     `json:"summary"`
	Evidence []evidence `json:"evidence"`
	Blocking bool       `json:"blocking"`
}

type evidence struct {
	Path string `json:"path"`
	Line *int64 `json:"line,omitempty"`
}

type provenance struct {
	Reviewer *string `json:"reviewer,omitempty"`
	Model    *string `json:"model,omitempty"`
	Adapter  *string `json:"adapter,omitempty"`
}

func evaluateReviewCriterion(criterion criterion, evaluation Evaluation, result *Result) (string, string, string) {
	instanceID := runstate.ArtifactInstanceID(criterion.artifactID + "@" + string(evaluation.AttemptID))
	artifact, present, err := evaluation.LookupArtifact(instanceID)
	if err != nil {
		return "ERROR", "criterion_errored", "artifact_lookup_failed"
	}
	if !present {
		return "FAIL", "artifact_missing", ""
	}
	if artifact.AttemptID != evaluation.AttemptID || artifact.LogicalOutputID != criterion.artifactID || artifact.Kind != "findings" {
		return "FAIL", "findings_malformed", ""
	}
	contents, err := evaluation.ReadArtifact(artifact)
	if err != nil {
		return "FAIL", "findings_malformed", ""
	}
	outcome, blocking, reason := ValidateFindings(contents, evaluation.SubjectTree, criterion.rubrics, evaluation.ValidateEvidence)
	if reason != "" {
		return "FAIL", reason, ""
	}
	result.FindingsInstanceID = string(instanceID)
	for index := range blocking {
		blocking[index].ArtifactInstanceID = string(instanceID)
	}
	result.BlockingFindings = blocking
	result.ReviewOutcome = outcome
	return "PASS", "", ""
}

// ValidateFindings applies the strict findings artifact schema and returns its
// validated verdict. validateEvidence binds each cited source location.
func ValidateFindings(contents []byte, subjectTree string, rubrics []string, validateEvidence func(string, *int64) error) (string, []FindingReference, string) {
	var document findingsDocument
	if err := decodeFindings(contents, &document); err != nil {
		return "", nil, "findings_malformed"
	}
	if document.Schema != "partitur/findings+json;v=1" {
		return "", nil, "findings_malformed"
	}
	if document.SubjectTree != subjectTree {
		return "", nil, "findings_subject_mismatch"
	}
	if !validCoverage(document, rubrics) {
		return "", nil, "findings_rubric_incomplete"
	}
	seen := make(map[string]bool, len(document.Findings))
	blocking := make([]FindingReference, 0)
	for _, value := range document.Findings {
		if value.ID == "" || value.Summary == "" || seen[value.ID] || !contains(rubrics, value.Rubric) || len(value.Evidence) == 0 {
			return "", nil, "findings_malformed"
		}
		seen[value.ID] = true
		for _, location := range value.Evidence {
			if !validPath(location.Path) || location.Line != nil && *location.Line < 1 || validateEvidence(location.Path, location.Line) != nil {
				return "", nil, "findings_malformed"
			}
		}
		if value.Blocking {
			blocking = append(blocking, FindingReference{FindingID: value.ID})
		}
	}
	if !validProvenance(document.Provenance) {
		return "", nil, "findings_malformed"
	}
	if len(blocking) == 0 {
		return "CLEAN", blocking, ""
	}
	return "CONTESTED", blocking, ""
}

func decodeFindings(contents []byte, value *findingsDocument) error {
	if _, err := canonical.ParseJSON(contents); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func validCoverage(document findingsDocument, rubrics []string) bool {
	if len(document.Coverage) != len(rubrics) {
		return false
	}
	seen := make(map[string]bool, len(rubrics))
	for _, value := range document.Coverage {
		if !contains(rubrics, value.Rubric) || seen[value.Rubric] || (value.Conclusion != "examined_none_found" && value.Conclusion != "findings_raised") || value.Note != nil && *value.Note == "" {
			return false
		}
		seen[value.Rubric] = true
	}
	return true
}

func validProvenance(value *provenance) bool {
	if value == nil {
		return true
	}
	for _, field := range []*string{value.Reviewer, value.Model, value.Adapter} {
		if field != nil && *field == "" {
			return false
		}
	}
	return true
}

func validPath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index := range values {
		result[index] = values[index]
	}
	return result
}

func processPayload(identity runstate.ProcessIdentity) map[string]any {
	start := map[string]any{}
	switch value := identity.Start.(type) {
	case runstate.LinuxStartIdentity:
		start = map[string]any{"platform": "linux", "boot_id": value.BootID, "start_ticks": value.StartTicks}
	case runstate.DarwinStartIdentity:
		start = map[string]any{"platform": "darwin", "start_tvsec": value.StartTVSec, "start_tvusec": value.StartTVUsec}
	}
	return map[string]any{"pid": identity.PID, "session_id": identity.SessionID, "start_identity": start}
}
