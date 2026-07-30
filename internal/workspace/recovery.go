package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

// AppendWaivedRunSucceeded materializes the identity candidate and waiver in
// the waived terminal event. A waived run never records a candidate first.
func AppendWaivedRunSucceeded(
	driver *runstore.Driver,
	input runstore.RunInput,
	address faultpoint.ReceiptAddress,
) error {
	if driver == nil {
		return errors.New("workspace: waived completion requires driver")
	}
	if input.Score == nil {
		return errors.New("workspace: waived completion requires pinned score")
	}
	if input.BaseTree == "" {
		return errors.New("workspace: waived completion requires base tree")
	}
	execution := input.Score.Execution()
	if !execution.GateWaived {
		return errors.New("workspace: waived completion requires waived gate")
	}
	if execution.WaiverReason == "" {
		return errors.New("workspace: waived completion requires waiver reason")
	}
	for _, movement := range input.Score.Movements() {
		if hasGrant(movement.Grants, "repo_write") {
			return fmt.Errorf("workspace: waived completion cannot compose writer movement %q", movement.ID)
		}
		state := input.Projection.State.Movements[runstate.MovementID(movement.ID)]
		if state == runstate.MovementSucceeded {
			continue
		}
		if state == runstate.MovementInapplicable {
			continue
		}
		return fmt.Errorf("workspace: waived completion movement %q is incomplete", movement.ID)
	}
	candidateID, err := canonical.Hash(canonical.DomainCandidate, map[string]any{
		"base_tree": input.BaseTree, "result_tree": input.BaseTree, "ordered_change_sets": []any{},
	})
	if err != nil {
		return err
	}
	compositionHash, err := canonical.Hash(canonical.DomainCandidateComposition, map[string]any{
		"composition_mode": "identity", "base_tree": input.BaseTree, "contributors": []any{},
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
	})
	if err != nil {
		return err
	}
	versions, err := identityVersions(canonical.DomainCandidate, canonical.DomainCandidateComposition)
	if err != nil {
		return err
	}
	versions["composition"] = canonical.CompositionAlgorithmVersion
	payload, err := json.Marshal(map[string]any{
		"candidate": map[string]any{
			"candidate_id": candidateID, "base_tree": input.BaseTree, "result_tree": input.BaseTree,
			"ordered_change_sets": []any{}, "contributors": []any{},
			"candidate_composition_dependency_hash": compositionHash,
		},
		"waiver":            map[string]any{"reason": execution.WaiverReason},
		"identity_versions": versions,
	})
	if err != nil {
		return err
	}
	_, err = driver.Append(runstate.Event{
		RunID: driver.RunID(), ScoreRevision: input.Projection.State.ScoreHead.Revision,
		Type: runstate.EventRunSucceeded, Payload: payload,
	}, address)
	return err
}

// RecordRecoveredZeroWriterCandidate records the same identity candidate as
// the live run path from run-owned recovery inputs.
func RecordRecoveredZeroWriterCandidate(
	store *runstore.Store,
	driver *runstore.Driver,
	input runstore.RunInput,
) (Candidate, error) {
	if store == nil || driver == nil || input.Score == nil || input.BaseCommit == "" || input.BaseTree == "" {
		return Candidate{}, errors.New("workspace: incomplete zero-writer recovery input")
	}
	git, err := newSystemGit()
	if err != nil {
		return Candidate{}, err
	}
	run := &Run{
		id:                driver.RunID(),
		repositoryRoot:    store.RepositoryRoot(),
		scoreRevision:     input.Projection.State.ScoreHead.Revision,
		baseCommit:        recoveryGitObject(input.BaseCommit),
		baseTreeQualified: input.BaseTree,
		movements:         input.Score.Movements(),
		store:             store,
		git:               git,
	}
	if err := run.BindDriver(driver); err != nil {
		return Candidate{}, err
	}
	return run.RecordZeroWriterCandidate()
}

// CreateRecoveredAttempt uses the same attempt-workspace construction as the
// live driver, with the selected run's pinned recovery input.
func CreateRecoveredAttempt(
	store *runstore.Store,
	driver *runstore.Driver,
	input runstore.RunInput,
	movementID string,
) (*AttemptWorkspace, error) {
	return CreateRecoveredAttemptAtBase(store, driver, input, movementID, "")
}

// CreateRecoveredAttemptAtBase uses a base commit freshly selected by the
// recovery composition caller. An empty value retains the run base.
func CreateRecoveredAttemptAtBase(
	store *runstore.Store,
	driver *runstore.Driver,
	input runstore.RunInput,
	movementID, baseCommit string,
) (*AttemptWorkspace, error) {
	if store == nil || driver == nil || input.Score == nil || input.BaseCommit == "" {
		return nil, errors.New("workspace: incomplete recovered attempt input")
	}
	git, err := newSystemGit()
	if err != nil {
		return nil, err
	}
	run := &Run{
		id:             driver.RunID(),
		repositoryRoot: store.RepositoryRoot(),
		scoreRevision:  input.Projection.State.ScoreHead.Revision,
		baseCommit:     recoveryGitObject(input.BaseCommit),
		movements:      input.Score.Movements(),
		store:          store,
		git:            git,
		newID:          newUUIDv7,
	}
	if err := run.BindDriver(driver); err != nil {
		return nil, err
	}
	if baseCommit == "" {
		baseCommit = run.baseCommit
	}
	return run.CreateAttemptAtBase(movementID, baseCommit)
}

// CaptureRecoveredChangeSet captures the authoritative existing worktree for
// RC-RESUME-016. The caller remains responsible for appending its event.
func CaptureRecoveredChangeSet(
	store *runstore.Store,
	driver *runstore.Driver,
	input runstore.RunInput,
	attemptID runstate.AttemptID,
) (ChangeSet, error) {
	if store == nil || driver == nil || input.Score == nil || input.BaseCommit == "" || input.BaseTree == "" || attemptID == "" {
		return ChangeSet{}, errors.New("workspace: incomplete change set recovery input")
	}
	state, err := driver.State()
	if err != nil {
		return ChangeSet{}, err
	}
	attemptState, ok := state.Attempts[attemptID]
	if !ok {
		return ChangeSet{}, fmt.Errorf("workspace: recovery attempt %q is absent", attemptID)
	}
	var movement score.MovementView
	found := false
	for _, candidate := range input.Score.Movements() {
		if candidate.ID == string(attemptState.MovementID) {
			movement, found = candidate, true
			break
		}
	}
	if !found {
		return ChangeSet{}, fmt.Errorf("%w: %s", ErrMovementNotFound, attemptState.MovementID)
	}
	if !hasGrant(movement.Grants, "repo_write") {
		return ChangeSet{}, ErrReadOnlyRequired
	}
	git, err := newSystemGit()
	if err != nil {
		return ChangeSet{}, err
	}
	run := &Run{
		id:                driver.RunID(),
		repositoryRoot:    store.RepositoryRoot(),
		scoreRevision:     state.ScoreHead.Revision,
		baseCommit:        recoveryGitObject(input.BaseCommit),
		baseTreeQualified: input.BaseTree,
		movements:         input.Score.Movements(),
		store:             store,
		git:               git,
	}
	if err := run.BindDriver(driver); err != nil {
		return ChangeSet{}, err
	}
	attempt := &AttemptWorkspace{
		RunID:      driver.RunID(),
		AttemptID:  attemptID,
		MovementID: attemptState.MovementID,
		PartID:     movement.PartID,
		Worktree:   filepath.Join(store.RepositoryRoot(), ".partitur", "work", string(driver.RunID()), string(attemptID), "worktree"),
		run:        run,
	}
	return attempt.CaptureChangeSet()
}

// InitialPerformerSelectedEvent builds the shared durable initial-selection
// event used by live execution and recovery.
func InitialPerformerSelectedEvent(
	attempt *AttemptWorkspace,
	scoreRevision uint64,
	performerID, adapterID, model string,
) (runstate.Event, error) {
	return PerformerSelectedEvent(
		attempt, scoreRevision, performerID, adapterID, model, "initial", "",
	)
}

// PerformerSelectedEvent builds the durable selection event for a fresh
// attempt. Recovery supplies the already-recorded successor reason and
// causation instead of recomputing either from current configuration.
func PerformerSelectedEvent(
	attempt *AttemptWorkspace,
	scoreRevision uint64,
	performerID, adapterID, model, reason, causationID string,
) (runstate.Event, error) {
	if attempt == nil || attempt.RunID == "" || attempt.AttemptID == "" || performerID == "" || adapterID == "" || model == "" {
		return runstate.Event{}, errors.New("workspace: incomplete initial performer selection")
	}
	if reason == "" {
		return runstate.Event{}, errors.New("workspace: performer selection reason is absent")
	}
	payload, err := json.Marshal(map[string]any{
		"reason": reason, "performer_id": performerID,
		"adapter_id": adapterID, "model": model,
	})
	if err != nil {
		return runstate.Event{}, err
	}
	return runstate.Event{
		RunID: attempt.RunID, ScoreRevision: scoreRevision,
		MovementID: attempt.MovementID, PartID: attempt.PartID, AttemptID: attempt.AttemptID,
		Type: runstate.EventPerformerSelected, CausationID: causationID, Payload: payload,
	}, nil
}

// VerifyRecoveredPostHoc performs the §5 check that was interrupted before
// verification.passed crossed its durable boundary.
func VerifyRecoveredPostHoc(
	store *runstore.Store,
	driver *runstore.Driver,
	input runstore.RunInput,
	attemptID runstate.AttemptID,
) error {
	if store == nil || driver == nil || input.Score == nil || attemptID == "" {
		return errors.New("workspace: incomplete post-hoc recovery input")
	}
	state, err := driver.State()
	if err != nil {
		return err
	}
	attempt, ok := state.Attempts[attemptID]
	if !ok {
		return fmt.Errorf("workspace: recovery attempt %q is absent", attemptID)
	}
	var movementID string
	var partID string
	var repoWrite bool
	for _, movement := range input.Score.Movements() {
		if movement.ID != string(attempt.MovementID) {
			continue
		}
		movementID = movement.ID
		partID = movement.PartID
		repoWrite = hasGrant(movement.Grants, "repo_write")
		break
	}
	if movementID == "" {
		return fmt.Errorf("%w: %s", ErrMovementNotFound, attempt.MovementID)
	}
	worktree := filepath.Join(store.RepositoryRoot(), ".partitur", "work", string(driver.RunID()), string(attemptID), "worktree")
	expectedTree := input.BaseTree
	for _, scheduled := range input.Projection.Scheduler.Movements {
		if scheduled.ID == attempt.MovementID && scheduled.Final && state.ApplicationCandidate != nil {
			expectedTree = state.ApplicationCandidate.ResultTree
		}
	}
	if expectedTree == "" {
		return errors.New("workspace: recovery verification tree is absent")
	}
	if repoWrite {
		matched, err := verifyRecoveryProtectedPaths(store.RepositoryRoot(), worktree, input.BaseTree)
		if err != nil {
			return err
		}
		if !matched {
			return &VerificationError{Reason: "protected_path_violation", Cause: ErrProtectedPathChanged}
		}
	} else {
		matched, err := VerifyRecoverySubject(store.RepositoryRoot(), worktree, expectedTree)
		if err != nil {
			return err
		}
		if !matched {
			reason := "read_only_violation"
			if state.ApplicationCandidate != nil && expectedTree == state.ApplicationCandidate.ResultTree {
				reason = "candidate_mismatch"
			}
			return &VerificationError{Reason: reason, Cause: ErrReadOnlyChanged}
		}
	}
	_, err = driver.Append(runstate.Event{
		RunID:         driver.RunID(),
		ScoreRevision: state.ScoreHead.Revision,
		MovementID:    runstate.MovementID(movementID),
		PartID:        partID,
		AttemptID:     attemptID,
		Type:          runstate.EventVerificationPassed,
		Payload:       []byte(`{}`),
	}, faultpoint.ReceiptAddress("recovery.post_hoc_verification"))
	return err
}

func verifyRecoveryProtectedPaths(repositoryRoot, worktree, baseTree string) (bool, error) {
	if err := verifyRecoveryGitDir(repositoryRoot, worktree); err != nil {
		return false, err
	}
	git, err := newSystemGit()
	if err != nil {
		return false, err
	}
	baseTree = recoveryGitObject(baseTree)
	for _, path := range []string{"partitur.yaml", ".partitur"} {
		if err := verifyRecoveryProtectedPath(git, worktree, baseTree, path); err != nil {
			if errors.Is(err, errRecoveryProtectedMismatch) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

// VerifyRecoverySubject checks the non-mutating recovery form of the workspace
// invariant. repositoryRoot identifies the repository that owns the worktree;
// subjectTree is the tree recorded by acceptance.started.
//
// Git's tree comparison covers tracked content, modes, and symlink targets.
// Non-ignored untracked files are checked separately. The protected paths are
// also required to be represented by the recorded tree rather than being
// tolerated merely because Git ignores them.
func VerifyRecoverySubject(repositoryRoot, worktree, subjectTree string) (bool, error) {
	if repositoryRoot == "" || worktree == "" || subjectTree == "" {
		return false, errors.New("workspace: recovery subject is incomplete")
	}
	if err := verifyRecoveryGitDir(repositoryRoot, worktree); err != nil {
		return false, err
	}
	git, err := newSystemGit()
	if err != nil {
		return false, err
	}
	gitTree := recoveryGitObject(subjectTree)
	changed, err := git.Run(worktree, nil, "diff-index", "--quiet", gitTree, "--")
	if err != nil {
		return false, fmt.Errorf("compare recovery subject tree: %w", err)
	}
	if changed.exitCode == 1 {
		return false, nil
	}
	if changed.exitCode != 0 {
		return false, gitFailure("compare recovery subject tree", changed)
	}
	untracked, err := gitOutput(git, worktree, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return false, err
	}
	if len(splitNUL(untracked)) != 0 {
		return false, nil
	}
	for _, path := range []string{"partitur.yaml", ".partitur"} {
		if err := verifyRecoveryProtectedPath(git, worktree, gitTree, path); err != nil {
			if errors.Is(err, errRecoveryProtectedMismatch) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

func recoveryGitObject(value string) string {
	for _, prefix := range []string{"git-sha1:", "git-sha256:"} {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return value
}

var (
	errRecoveryProtectedMismatch = errors.New("recovery protected path mismatch")
	errRecoveryGitDirUnverified  = errors.New("recovery gitdir relationship is unverified")
)

func verifyRecoveryProtectedPath(git gitCommand, worktree, subjectTree, path string) error {
	tracked, err := gitOutput(git, worktree, nil, "ls-tree", "-z", subjectTree, "--", path)
	if err != nil {
		return err
	}
	_, err = os.Lstat(worktree + "/" + path)
	if len(tracked) == 0 {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return errRecoveryProtectedMismatch
	}
	if err != nil {
		return err
	}
	return nil
}

func verifyRecoveryGitDir(repositoryRoot, worktree string) error {
	expectedCommonDir, err := commonGitDir(repositoryRoot, false)
	if err != nil {
		return err
	}
	gitDir, err := linkedGitDir(worktree)
	if err != nil {
		return err
	}
	backlink, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return fmt.Errorf("%w: read gitdir backlink: %v", errRecoveryGitDirUnverified, err)
	}
	expected, ok := gitDirBacklinkPath(gitDir, string(backlink))
	matches, err := samePath(expected, filepath.Join(worktree, ".git"))
	if !ok || err != nil || !matches {
		return errRecoveryGitDirUnverified
	}
	actualCommonDir, err := commonGitDir(gitDir, true)
	if err != nil {
		return err
	}
	matches, err = samePath(expectedCommonDir, actualCommonDir)
	if err != nil || !matches {
		return errRecoveryGitDirUnverified
	}
	return nil
}

func linkedGitDir(worktree string) (string, error) {
	gitFile := filepath.Join(worktree, ".git")
	info, err := os.Lstat(gitFile)
	if err != nil {
		return "", fmt.Errorf("%w: inspect .git: %v", errRecoveryGitDirUnverified, err)
	}
	if !info.Mode().IsRegular() {
		return "", errRecoveryGitDirUnverified
	}
	contents, err := os.ReadFile(gitFile)
	if err != nil {
		return "", fmt.Errorf("%w: read .git: %v", errRecoveryGitDirUnverified, err)
	}
	gitDir, ok := gitDirPath(filepath.Dir(gitFile), string(contents))
	if !ok {
		return "", errRecoveryGitDirUnverified
	}
	return gitDir, nil
}

func commonGitDir(root string, linked bool) (string, error) {
	gitDir := filepath.Join(root, ".git")
	if linked {
		gitDir = root
	} else if info, err := os.Lstat(gitDir); err != nil {
		return "", fmt.Errorf("%w: inspect repository .git: %v", errRecoveryGitDirUnverified, err)
	} else if info.Mode().IsRegular() {
		var err error
		gitDir, err = linkedGitDir(root)
		if err != nil {
			return "", err
		}
	} else if !info.IsDir() {
		return "", errRecoveryGitDirUnverified
	}
	contents, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if errors.Is(err, fs.ErrNotExist) {
		return gitDir, nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: read common gitdir: %v", errRecoveryGitDirUnverified, err)
	}
	commonDir, ok := gitDirBacklinkPath(gitDir, string(contents))
	if !ok {
		return "", errRecoveryGitDirUnverified
	}
	return commonDir, nil
}

func gitDirBacklinkPath(base, contents string) (string, bool) {
	path := strings.TrimSuffix(contents, "\n")
	if path == "" || strings.Contains(path, "\n") {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path), true
}

func gitDirPath(base, contents string) (string, bool) {
	value := strings.TrimSuffix(contents, "\n")
	if !strings.HasPrefix(value, "gitdir: ") {
		return "", false
	}
	path := strings.TrimPrefix(value, "gitdir: ")
	if path == "" || strings.Contains(path, "\n") {
		return "", false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path), true
}
