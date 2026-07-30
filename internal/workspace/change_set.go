package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const (
	changeSetAuthorName  = "Partitur"
	changeSetAuthorEmail = "partitur@invalid"
	changeSetDate        = "1970-01-01T00:00:00Z"
	changeSetMessage     = "partitur: change set"
)

// ChangeSet is the core-created checkpoint and its content identity.
type ChangeSet struct {
	ID               string
	BaseTree         string
	ResultTree       string
	Commit           string
	Ref              string
	IdentityVersions map[string]any
	Receipt          faultpoint.DurabilityReceipt
}

// CaptureChangeSet stages the complete non-ignored worktree content, writes a
// checkpoint commit, and durably pins it before its event may be appended.
func (attempt *AttemptWorkspace) CaptureChangeSet() (ChangeSet, error) {
	if attempt == nil || attempt.run == nil {
		return ChangeSet{}, errors.New("workspace: incomplete attempt")
	}
	if attempt.readOnly {
		return ChangeSet{}, ErrReadOnlyRequired
	}
	baseTree, err := qualifiedTree(attempt.run.git, attempt.Worktree, "HEAD^{tree}")
	if err != nil {
		return ChangeSet{}, err
	}
	ref := changesetRef(attempt.RunID, attempt.AttemptID)

	if _, err := gitOutput(attempt.run.git, attempt.Worktree, nil, "read-tree", "HEAD"); err != nil {
		return ChangeSet{}, fmt.Errorf("reset attempt index: %w", err)
	}
	if _, err := gitOutput(
		attempt.run.git,
		attempt.Worktree,
		nil,
		"add",
		"--all",
		"--",
		".",
		":(exclude).partitur/**",
		":(exclude)partitur.yaml",
	); err != nil {
		return ChangeSet{}, fmt.Errorf("stage attempt worktree: %w", err)
	}
	resultTree, err := writeTree(attempt.run.git, attempt.Worktree)
	if err != nil {
		return ChangeSet{}, err
	}
	commit, err := gitOutputWithEnvironment(
		attempt.run.git,
		attempt.Worktree,
		[]string{
			"GIT_AUTHOR_NAME=" + changeSetAuthorName,
			"GIT_AUTHOR_EMAIL=" + changeSetAuthorEmail,
			"GIT_AUTHOR_DATE=" + changeSetDate,
			"GIT_COMMITTER_NAME=" + changeSetAuthorName,
			"GIT_COMMITTER_EMAIL=" + changeSetAuthorEmail,
			"GIT_COMMITTER_DATE=" + changeSetDate,
		},
		"commit-tree",
		recoveryGitObject(resultTree),
		"-m",
		changeSetMessage,
	)
	if err != nil {
		return ChangeSet{}, fmt.Errorf("create change set checkpoint: %w", err)
	}
	commitID := strings.TrimSpace(string(commit))
	receipt, err := ensureRef(
		attempt.run.git,
		attempt.run.repositoryRoot,
		ref,
		commitID,
		attempt.RunID,
		faultpoint.ReceiptAddress("attempt."+string(attempt.AttemptID)+".changeset.ref"),
		refExistingMayMove,
	)
	if err != nil {
		return ChangeSet{}, err
	}
	changeSet, err := capturedChangeSet(attempt.run.git, attempt.Worktree, baseTree, commitID, ref)
	if err != nil {
		return ChangeSet{}, err
	}
	changeSet.Receipt = receipt
	return changeSet, nil
}

// ChangeSetRecordedEvent builds the event whose append belongs to the driver.
func (attempt *AttemptWorkspace) ChangeSetRecordedEvent(changeSet ChangeSet) (runstate.Event, error) {
	if attempt == nil || attempt.run == nil {
		return runstate.Event{}, errors.New("workspace: incomplete change set")
	}
	return ChangeSetRecordedEvent(
		attempt.RunID,
		attempt.run.scoreRevision,
		attempt.MovementID,
		attempt.PartID,
		attempt.AttemptID,
		changeSet,
	)
}

// ChangeSetRecordedEvent builds the driver-owned journal event for a captured
// checkpoint.
func ChangeSetRecordedEvent(
	runID runstate.RunID,
	scoreRevision uint64,
	movementID runstate.MovementID,
	partID string,
	attemptID runstate.AttemptID,
	changeSet ChangeSet,
) (runstate.Event, error) {
	if runID == "" || movementID == "" || partID == "" || attemptID == "" || changeSet.ID == "" ||
		changeSet.BaseTree == "" || changeSet.ResultTree == "" ||
		changeSet.Commit == "" || changeSet.Ref == "" ||
		changeSet.IdentityVersions == nil {
		return runstate.Event{}, errors.New("workspace: incomplete change set")
	}
	payload, err := json.Marshal(map[string]any{
		"change_set_id":     changeSet.ID,
		"base_tree":         changeSet.BaseTree,
		"result_tree":       changeSet.ResultTree,
		"commit":            changeSet.Commit,
		"ref":               changeSet.Ref,
		"identity_versions": changeSet.IdentityVersions,
	})
	if err != nil {
		return runstate.Event{}, err
	}
	return runstate.Event{
		RunID:         runID,
		ScoreRevision: scoreRevision,
		MovementID:    movementID,
		PartID:        partID,
		AttemptID:     attemptID,
		Type:          runstate.EventChangeSetRecorded,
		Payload:       payload,
	}, nil
}

func capturedChangeSet(
	git gitCommand,
	worktree, baseTree, commit, ref string,
) (ChangeSet, error) {
	resultTree, err := qualifiedTree(git, worktree, commit+"^{tree}")
	if err != nil {
		return ChangeSet{}, err
	}
	changeSetID, err := canonical.Hash(canonical.DomainChangeSet, map[string]any{
		"base_tree": baseTree, "result_tree": resultTree,
	})
	if err != nil {
		return ChangeSet{}, err
	}
	versions, err := identityVersions(canonical.DomainChangeSet)
	if err != nil {
		return ChangeSet{}, err
	}
	return ChangeSet{
		ID: changeSetID, BaseTree: baseTree, ResultTree: resultTree,
		Commit: commit, Ref: ref, IdentityVersions: versions,
	}, nil
}

func qualifiedTree(git gitCommand, root, revision string) (string, error) {
	tree, err := gitOutput(git, root, nil, "rev-parse", revision)
	if err != nil {
		return "", err
	}
	format, err := gitOutput(git, root, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	return qualifyGitObject(strings.TrimSpace(string(format)), strings.TrimSpace(string(tree))), nil
}

func writeTree(git gitCommand, root string) (string, error) {
	tree, err := gitOutput(git, root, nil, "write-tree")
	if err != nil {
		return "", err
	}
	format, err := gitOutput(git, root, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return "", err
	}
	return qualifyGitObject(strings.TrimSpace(string(format)), strings.TrimSpace(string(tree))), nil
}

func changesetRef(runID runstate.RunID, attemptID runstate.AttemptID) string {
	return "refs/partitur/runs/" + string(runID) + "/attempts/" + string(attemptID) + "/changeset"
}

func gitOutputWithEnvironment(
	git gitCommand,
	root string,
	environment []string,
	args ...string,
) ([]byte, error) {
	result, err := git.RunWithEnvironment(root, nil, environment, args...)
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, gitFailure(strings.Join(args, " "), result)
	}
	return result.stdout, nil
}
