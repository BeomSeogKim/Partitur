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
	RunCriterion          RunCriterionExecutor
	Append                AppendEvent
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
	Receipts            []faultpoint.DurabilityReceipt
}

// CriterionOutcome is one PASS result used to close an already-started
// acceptance after recovery has replayed every planned criterion.
type CriterionOutcome struct {
	CriterionID string
	Outcome     string
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
}

// Hash returns the effective acceptance-spec identity.
func (plan *Plan) Hash() runstate.Hash {
	if plan == nil {
		return ""
	}
	return plan.specHash
}

// Compile builds the effective acceptance plan. Declared criteria retain
// declaration order; generated checks retain output declaration order.
func Compile(movement score.MovementView) (*Plan, error) {
	if movement.Acceptance.HasReviewCriteria {
		return nil, fmt.Errorf(
			"%w: review criteria require unit 4.1",
			ErrUnsupportedCriteria,
		)
	}
	if movement.Acceptance.HumanGate == "on_contested" {
		return nil, fmt.Errorf(
			"%w: human_gate %q requires unit 4.1",
			ErrUnsupportedCriteria,
			movement.Acceptance.HumanGate,
		)
	}

	outputKinds := make(map[string]string, len(movement.Outputs))
	for _, output := range movement.Outputs {
		outputKinds[output.ArtifactID] = output.Kind
	}
	replaced := make(map[string]bool, len(movement.Acceptance.ArtifactCriteria))
	criteria := make([]criterion, 0, len(movement.Acceptance.ArtifactCriteria)+len(movement.Acceptance.RunCriteria)+len(movement.Outputs))
	type declaredCriterion struct {
		source   int
		artifact *score.ArtifactCriterionView
		run      *score.RunCriterionView
	}
	declared := make([]declaredCriterion, 0, len(movement.Acceptance.ArtifactCriteria)+len(movement.Acceptance.RunCriteria))
	for index := range movement.Acceptance.ArtifactCriteria {
		declared = append(declared, declaredCriterion{source: movement.Acceptance.ArtifactCriteria[index].SourceIndex, artifact: &movement.Acceptance.ArtifactCriteria[index]})
	}
	for index := range movement.Acceptance.RunCriteria {
		declared = append(declared, declaredCriterion{source: movement.Acceptance.RunCriteria[index].SourceIndex, run: &movement.Acceptance.RunCriteria[index]})
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
		} else {
			compiled, err = compileRunCriterion(item.run.ID, item.run.Argv, item.run.TimeoutMin)
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
		declaredHard:       len(movement.Acceptance.ArtifactCriteria) + len(movement.Acceptance.RunCriteria),
		acceptanceVersions: acceptanceVersions,
		criterionVersions:  criterionVersions,
	}, nil
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
			outcome, reason, detail = evaluateCriterion(criterion, evaluation)
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
	completed, err := eventWithPayload(base, runstate.EventAcceptanceEvaluationCompleted, map[string]any{
		"subject_tree":         evaluation.SubjectTree,
		"acceptance_spec_hash": plan.specHash,
		"criterion_outcomes":   criterionOutcomes,
		"identity_versions":    plan.acceptanceVersions,
	})
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
	if len(outcomes) != len(plan.criteria) {
		return false
	}
	for index, outcome := range outcomes {
		if outcome.CriterionID != plan.criteria[index].id || outcome.Outcome != "PASS" {
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
