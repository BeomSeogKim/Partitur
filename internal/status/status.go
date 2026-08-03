// Package status renders the read-only run projection used by partitur status.
package status

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

var (
	ErrNoActiveRun     = errors.New("no active run")
	ErrRunNotFound     = errors.New("run not found")
	ErrInvalidRunID    = errors.New("invalid run id")
	ErrSnapshot        = errors.New("run score snapshot is unavailable")
	ErrSnapshotInvalid = errors.New("run score snapshot is invalid")
	ErrRequiredInput   = errors.New("required run input is unreadable")
)

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// Report is the versioned, interface-neutral status projection.
type Report struct {
	Schema                string                `json:"schema"`
	Run                   Run                   `json:"run"`
	Application           Application           `json:"application"`
	Promotion             Promotion             `json:"promotion"`
	EnforcementAdvisories []EnforcementAdvisory `json:"enforcement_advisories"`
	Journal               Journal               `json:"journal"`
	Recovery              Recovery              `json:"recovery"`
}

type Run struct {
	ID               string            `json:"id"`
	Lifecycle        string            `json:"lifecycle"`
	Score            ScoreHead         `json:"score"`
	Movements        []Movement        `json:"movements"`
	PendingDecisions []PendingDecision `json:"pending_decisions"`
}

type PendingDecision struct {
	ID            string `json:"id"`
	Type          string `json:"type"`
	MovementID    string `json:"movement_id,omitempty"`
	AttemptID     string `json:"attempt_id,omitempty"`
	ScoreRevision uint64 `json:"score_revision"`
}

type ScoreHead struct {
	Revision     uint64 `json:"revision"`
	SemanticHash string `json:"semantic_hash"`
	FileHash     string `json:"file_hash"`
}

type Movement struct {
	ID       string    `json:"id"`
	State    string    `json:"state"`
	Attempts []Attempt `json:"attempts"`
	Marks    []Mark    `json:"marks"`
}

type Attempt struct {
	ID      string   `json:"id"`
	State   string   `json:"state"`
	Failure *Failure `json:"failure"`
}

type Failure struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
}

type Mark struct {
	Grade              string      `json:"grade"`
	AttemptID          string      `json:"attempt_id"`
	Criteria           []Criterion `json:"criteria"`
	SubjectTree        string      `json:"subject_tree"`
	ScoreRevision      uint64      `json:"score_revision"`
	FailedAttempts     int         `json:"failed_attempts"`
	FindingsInstanceID string      `json:"findings_instance_id,omitempty"`
	ReviewOutcome      string      `json:"review_outcome,omitempty"`
	GateDecisionID     string      `json:"gate_decision_id,omitempty"`
}

type Criterion struct {
	ID       string `json:"id"`
	SpecHash string `json:"spec_hash"`
}

type Application struct {
	State     string     `json:"state"`
	Candidate *Candidate `json:"candidate"`
}

type Candidate struct {
	ID                        string        `json:"id"`
	ScoreRevision             uint64        `json:"score_revision"`
	BaseTree                  string        `json:"base_tree"`
	ResultTree                string        `json:"result_tree"`
	OrderedChangeSets         []string      `json:"ordered_change_sets"`
	Contributors              []Contributor `json:"contributors"`
	CompositionDependencyHash string        `json:"composition_dependency_hash"`
}

type Contributor struct {
	MovementID  string `json:"movement_id"`
	ChangeSetID string `json:"change_set_id"`
}

type Promotion struct {
	State string `json:"state"`
}

type EnforcementAdvisory struct {
	AttemptID  string   `json:"attempt_id"`
	Dimensions []string `json:"dimensions"`
}

type Journal struct {
	Integrity      string `json:"integrity"`
	TruncatedSeq   uint64 `json:"truncated_seq,omitempty"`
	DiscardedBytes int    `json:"discarded_bytes,omitempty"`
}

type Recovery struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Read selects and projects a run from repositoryRoot without creating,
// repairing, locking, or leasing any repository state.
func Read(repositoryRoot, requestedID string) (Report, error) {
	store, err := runstore.New(repositoryRoot, faultpoint.Nop{})
	if err != nil {
		return Report{}, fmt.Errorf("%w: %v", ErrRequiredInput, err)
	}
	if requestedID != "" {
		if !runIDPattern.MatchString(requestedID) {
			return Report{}, ErrInvalidRunID
		}
		return readRun(store, runstate.RunID(requestedID))
	}

	ids, err := store.RunIDs()
	if err != nil {
		return Report{}, fmt.Errorf("%w: %v", ErrRequiredInput, err)
	}
	active := make([]Report, 0, 1)
	for _, id := range ids {
		report, err := readRun(store, id)
		if errors.Is(err, ErrRunNotFound) {
			continue
		}
		if err != nil {
			return Report{}, err
		}
		if !terminal(report.Run.Lifecycle) {
			active = append(active, report)
		}
	}
	if len(active) != 1 {
		return Report{}, fmt.Errorf("%w: found %d", ErrNoActiveRun, len(active))
	}
	return active[0], nil
}

func readRun(store *runstore.Store, runID runstate.RunID) (Report, error) {
	journal := filepath.Join(storeRoot(store), ".partitur", "runs", string(runID), "journal.jsonl")
	if _, err := os.Stat(journal); errors.Is(err, fs.ErrNotExist) {
		return Report{}, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	} else if err != nil {
		return Report{}, fmt.Errorf("%w: inspect run journal: %v", ErrRequiredInput, err)
	}
	compiled, err := loadInitialScore(store, runID)
	if err != nil {
		return Report{}, err
	}
	replay, err := store.ReadReplay(runID, movementSeeds(compiled))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Report{}, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
		}
		if errors.Is(err, fs.ErrPermission) {
			return Report{}, fmt.Errorf("%w: %v", ErrRequiredInput, err)
		}
		return Report{}, err
	}
	if replay.State.Run == runstate.RunNotStarted {
		return Report{}, fmt.Errorf("%w: %s", ErrRunNotFound, runID)
	}
	snapshots, err := loadAttemptSnapshots(store, runID, compiled, replay.State)
	if err != nil {
		return Report{}, err
	}
	if err := validateReviewArtifacts(storeRoot(store), runID, snapshots, replay.State); err != nil {
		return Report{}, err
	}
	return projectAt(runID, compiled, snapshots, replay, storeRoot(store)), nil
}

func loadInitialScore(store *runstore.Store, runID runstate.RunID) (*score.Score, error) {
	compiled, err := store.LoadInitialScore(runID)
	if err != nil {
		if errors.Is(err, runstore.ErrJournalCorrupt) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrSnapshot, err)
	}
	return compiled, nil
}

func loadAttemptSnapshots(store *runstore.Store, runID runstate.RunID, initial *score.Score, state runstate.State) (map[uint64]*score.Score, error) {
	snapshots := map[uint64]*score.Score{initial.Revision(): initial}
	for _, attempt := range state.Attempts {
		if attempt.ScoreRevision == 0 {
			return nil, fmt.Errorf("%w: attempt %q has no score revision", ErrSnapshot, attempt.MovementID)
		}
		if _, ok := snapshots[attempt.ScoreRevision]; ok {
			continue
		}
		compiled, err := store.LoadScoreSnapshot(runID, attempt.ScoreRevision)
		if err != nil {
			return nil, fmt.Errorf("%w: score snapshot revision %d: %v", ErrSnapshot, attempt.ScoreRevision, err)
		}
		snapshots[attempt.ScoreRevision] = compiled
	}
	return snapshots, nil
}

// storeRoot is intentionally kept here until runstore exposes a read-only
// root accessor; the status package only receives stores built from the
// invocation root and never searches beyond it.
func storeRoot(store *runstore.Store) string {
	return store.RepositoryRoot()
}

func movementSeeds(compiled *score.Score) []runstate.MovementSeed {
	movements := compiled.Movements()
	execution := compiled.Execution()
	seeds := make([]runstate.MovementSeed, len(movements))
	for index, movement := range movements {
		seeds[index] = runstate.MovementSeed{
			ID:              runstate.MovementID(movement.ID),
			Initial:         runstate.MovementPending,
			RepoWrite:       slices.Contains(movement.Grants, "repo_write"),
			HasDependencies: len(movement.Needs) != 0,
			Final:           movement.ID == execution.FinalMovementID,
		}
	}
	return seeds
}

func project(runID runstate.RunID, compiled *score.Score, replay runstore.ReadReplayResult) Report {
	return projectAt(runID, compiled, map[uint64]*score.Score{compiled.Revision(): compiled}, replay, "")
}

func projectAt(runID runstate.RunID, compiled *score.Score, snapshots map[uint64]*score.Score, replay runstore.ReadReplayResult, repositoryRoot string) Report {
	state := replay.State
	report := Report{
		Schema: "partitur/status+json;v=1",
		Run: Run{
			ID:        string(runID),
			Lifecycle: string(state.Run),
			Score: ScoreHead{
				Revision:     state.ScoreHead.Revision,
				SemanticHash: string(state.ScoreHead.SemanticHash),
				FileHash:     string(state.ScoreHead.FileHash),
			},
			Movements:        movementProjection(compiled, snapshots, state, repositoryRoot, runID),
			PendingDecisions: pendingDecisions(state),
		},
		Application:           Application{State: string(state.Application.State)},
		Promotion:             Promotion{State: string(state.Promotion.State)},
		EnforcementAdvisories: advisories(state),
		Journal:               Journal{Integrity: "INTACT"},
		Recovery:              Recovery{State: "NOT_REQUIRED"},
	}
	if state.ApplicationCandidate != nil {
		candidate := state.ApplicationCandidate
		contributors := make([]Contributor, len(candidate.Contributors))
		for index, contributor := range candidate.Contributors {
			contributors[index] = Contributor{
				MovementID:  string(contributor.MovementID),
				ChangeSetID: contributor.ChangeSetID,
			}
		}
		report.Application.Candidate = &Candidate{
			ID:                        candidate.ID,
			ScoreRevision:             candidate.Revision,
			BaseTree:                  candidate.BaseTree,
			ResultTree:                candidate.ResultTree,
			OrderedChangeSets:         slices.Clone(candidate.OrderedChangeSets),
			Contributors:              contributors,
			CompositionDependencyHash: string(candidate.CompositionDependencyHash),
		}
	}
	if state.Application.State == runstate.ApplicationRecoveryRequired {
		report.Recovery = Recovery{State: "RECOVERY_REQUIRED", Reason: state.Application.Reason}
	} else if state.Promotion.State == runstate.PromotionRecoveryRequired {
		report.Recovery = Recovery{State: "RECOVERY_REQUIRED", Reason: state.Promotion.Reason}
	}
	if replay.TailTruncated {
		report.Journal = Journal{
			Integrity:      "TAIL_UNPARSEABLE",
			TruncatedSeq:   replay.TruncatedSeq,
			DiscardedBytes: replay.DiscardedBytes,
		}
	}
	return report
}

func pendingDecisions(state runstate.State) []PendingDecision {
	ids := make([]string, 0, len(state.PendingDecisions))
	for id := range state.PendingDecisions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	decisions := make([]PendingDecision, 0, len(ids))
	for _, id := range ids {
		decision := state.PendingDecisions[id]
		decisions = append(decisions, PendingDecision{
			ID:            decision.ID,
			Type:          decision.Type,
			MovementID:    string(decision.MovementID),
			AttemptID:     string(decision.AttemptID),
			ScoreRevision: decision.ScoreRevision,
		})
	}
	return decisions
}

func movementProjection(compiled *score.Score, snapshots map[uint64]*score.Score, state runstate.State, repositoryRoot string, runID runstate.RunID) []Movement {
	views := compiled.Movements()
	result := make([]Movement, 0, len(views))
	for _, view := range views {
		id := runstate.MovementID(view.ID)
		movement := Movement{
			ID:       view.ID,
			State:    string(state.Movements[id]),
			Attempts: attemptsFor(state, id),
			Marks:    marksFor(state, id, snapshots, repositoryRoot, runID),
		}
		result = append(result, movement)
	}
	return result
}

func attemptsFor(state runstate.State, movementID runstate.MovementID) []Attempt {
	ids := make([]string, 0)
	for id, attempt := range state.Attempts {
		if attempt.MovementID == movementID {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	result := make([]Attempt, 0, len(ids))
	for _, id := range ids {
		attempt := state.Attempts[runstate.AttemptID(id)]
		view := Attempt{ID: id, State: string(attempt.State)}
		if attempt.Failure != nil {
			view.Failure = &Failure{Kind: attempt.Failure.Kind, Reason: attempt.Failure.Reason}
		}
		result = append(result, view)
	}
	return result
}

func marksFor(state runstate.State, movementID runstate.MovementID, snapshots map[uint64]*score.Score, repositoryRoot string, runID runstate.RunID) []Mark {
	failedAttempts := 0
	for _, attempt := range state.Attempts {
		if attempt.MovementID == movementID && attempt.State == runstate.AttemptFailed {
			failedAttempts++
		}
	}
	ids := make([]string, 0)
	for id, attempt := range state.Attempts {
		if attempt.MovementID == movementID && (attempt.State == runstate.AttemptCompleted || attempt.State == runstate.AttemptVerifying) {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	marks := make([]Mark, 0, len(ids))
	for _, id := range ids {
		attempt := state.Attempts[runstate.AttemptID(id)]
		compiled, ok := snapshots[attempt.ScoreRevision]
		if !ok {
			continue
		}
		view, ok := movementView(compiled, movementID)
		if !ok {
			continue
		}
		acceptance, ok := state.Acceptances[runstate.AttemptID(id)]
		if !ok || !acceptance.EvaluationCompleted {
			continue
		}
		criteria := make([]Criterion, 0, len(acceptance.PlannedCriterionIDs))
		complete := true
		for _, criterionID := range acceptance.PlannedCriterionIDs {
			record, ok := acceptance.Criteria[criterionID]
			if !ok || !record.Completed || record.Outcome != "PASS" {
				complete = false
				break
			}
			criteria = append(criteria, Criterion{ID: string(criterionID), SpecHash: string(record.SpecHash)})
		}
		if !complete {
			continue
		}
		if len(view.Acceptance.ArtifactCriteria)+len(view.Acceptance.RunCriteria) != 0 {
			marks = append(marks, Mark{Grade: "VERIFIED", AttemptID: id, Criteria: criteria, SubjectTree: acceptance.SubjectTree, ScoreRevision: attempt.ScoreRevision, FailedAttempts: failedAttempts})
		}
		if len(view.Acceptance.ReviewCriteria) == 1 {
			artifactID := view.Acceptance.ReviewCriteria[0].Findings
			instanceID := artifactID + "@" + id
			marks = append(marks, Mark{Grade: "REVIEWED", AttemptID: id, Criteria: criteria, SubjectTree: acceptance.SubjectTree, ScoreRevision: attempt.ScoreRevision, FailedAttempts: failedAttempts, FindingsInstanceID: instanceID, ReviewOutcome: acceptance.ReviewOutcome})
		}
		// The driver and recovery handlers bind the pending gate to this accepted
		// attempt, and Apply binds a resolution to that pending gate. Keep these
		// fail-closed checks for malformed projection input; they are not an
		// independently reachable journal path.
		if resolution := state.ResolvedHumanGates[runstate.AttemptID(id)]; resolution.Disposition == "approved" && resolution.Scope.SubjectTree == acceptance.SubjectTree &&
			resolution.ScoreRevision == attempt.ScoreRevision {
			marks = append(marks, Mark{Grade: "APPROVED", AttemptID: id, Criteria: criteria, SubjectTree: acceptance.SubjectTree, ScoreRevision: attempt.ScoreRevision, FailedAttempts: failedAttempts, GateDecisionID: resolution.DecisionID})
		}
	}
	return marks
}

func movementView(compiled *score.Score, movementID runstate.MovementID) (score.MovementView, bool) {
	for _, movement := range compiled.Movements() {
		if movement.ID == string(movementID) {
			return movement, true
		}
	}
	return score.MovementView{}, false
}

func validateReviewArtifacts(repositoryRoot string, runID runstate.RunID, snapshots map[uint64]*score.Score, state runstate.State) error {
	for attemptID, attempt := range state.Attempts {
		compiled, ok := snapshots[attempt.ScoreRevision]
		if !ok {
			return fmt.Errorf("%w: attempt %q has no score snapshot", ErrSnapshot, attemptID)
		}
		movement, ok := movementView(compiled, attempt.MovementID)
		if !ok || len(movement.Acceptance.ReviewCriteria) != 1 {
			continue
		}
		artifactID := movement.Acceptance.ReviewCriteria[0].Findings
		value, ok := state.Acceptances[attemptID]
		if !ok || !value.EvaluationCompleted {
			continue
		}
		instanceID := runstate.ArtifactInstanceID(artifactID + "@" + string(attemptID))
		artifact, ok := state.Artifacts[instanceID]
		if !ok {
			return fmt.Errorf("%w: findings artifact %q is absent", ErrRequiredInput, instanceID)
		}
		path := filepath.Join(repositoryRoot, ".partitur", "runs", string(runID), "artifacts", artifactID, string(attemptID))
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%w: read findings artifact %q: %v", ErrRequiredInput, instanceID, err)
		}
		digest := sha256.Sum256(contents)
		if fmt.Sprintf("sha256:%x", digest) != string(artifact.ContentHash) {
			return fmt.Errorf("%w: findings artifact %q content hash mismatch", ErrRequiredInput, instanceID)
		}
		outcome, blockers, reason := acceptance.ValidateFindings(contents, value.SubjectTree, movement.Acceptance.ReviewCriteria[0].Rubrics, func(path string, line *int64) error {
			return validateReviewEvidence(repositoryRoot, value.SubjectTree, path, line)
		})
		if reason != "" || outcome != value.ReviewOutcome || !sameFindings(blockers, value.BlockingFindings, string(instanceID)) {
			return fmt.Errorf("%w: findings artifact %q is not the validated review evidence", ErrRequiredInput, instanceID)
		}
	}
	return nil
}

func sameFindings(left []acceptance.FindingReference, right []runstate.FindingReference, instanceID string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].FindingID != right[index].FindingID || right[index].ArtifactInstanceID != instanceID {
			return false
		}
	}
	return true
}

func validateReviewEvidence(repositoryRoot, subjectTree, path string, line *int64) error {
	object := strings.TrimPrefix(strings.TrimPrefix(subjectTree, "git-sha1:"), "git-sha256:")
	listed, err := exec.Command("git", "-C", repositoryRoot, "ls-tree", object, "--", path).Output()
	if err != nil || !strings.Contains(string(listed), " blob ") {
		return errors.New("review evidence does not name a regular subject file")
	}
	if line == nil {
		return nil
	}
	contents, err := exec.Command("git", "-C", repositoryRoot, "show", object+":"+path).Output()
	if err != nil {
		return err
	}
	lineCount := int64(0)
	if len(contents) != 0 {
		lineCount = int64(strings.Count(string(contents), "\n")) + 1
		if contents[len(contents)-1] == '\n' {
			lineCount--
		}
	}
	if *line < 1 || *line > lineCount {
		return errors.New("review evidence line is outside subject file")
	}
	return nil
}

func advisories(state runstate.State) []EnforcementAdvisory {
	ids := make([]string, 0)
	for id, observation := range state.AdapterObservations {
		if len(observation.AdvisoryDimensions) != 0 {
			ids = append(ids, string(id))
		}
	}
	sort.Strings(ids)
	result := make([]EnforcementAdvisory, 0, len(ids))
	for _, id := range ids {
		result = append(result, EnforcementAdvisory{
			AttemptID:  id,
			Dimensions: slices.Clone(state.AdapterObservations[runstate.AttemptID(id)].AdvisoryDimensions),
		})
	}
	return result
}

func terminal(lifecycle string) bool {
	return lifecycle == string(runstate.RunSucceeded) ||
		lifecycle == string(runstate.RunFailed) ||
		lifecycle == string(runstate.RunCancelled)
}
