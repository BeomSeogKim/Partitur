package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/protectedpath"
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

// AcceptanceSubject is the writer worktree tree that acceptance evaluates.
// Its ref is an attempt-scoped durable wrapper, distinct from the shippable
// change set.
type AcceptanceSubject struct {
	Tree    string
	Commit  string
	Ref     string
	Receipt faultpoint.DurabilityReceipt
}

// CaptureAcceptanceSubject captures the complete writer worktree after
// post-hoc verification. Unlike a change set, it force-adds protected paths
// so ignored authoritative content remains part of the acceptance subject.
func (attempt *AttemptWorkspace) CaptureAcceptanceSubject() (AcceptanceSubject, error) {
	if attempt == nil || attempt.run == nil {
		return AcceptanceSubject{}, errors.New("workspace: incomplete acceptance subject")
	}
	if attempt.readOnly {
		return AcceptanceSubject{}, ErrReadOnlyRequired
	}
	if _, err := gitOutput(attempt.run.git, attempt.Worktree, nil, "read-tree", "HEAD"); err != nil {
		return AcceptanceSubject{}, fmt.Errorf("reset acceptance subject index: %w", err)
	}
	if _, err := gitOutput(attempt.run.git, attempt.Worktree, nil, "add", "--all", "--", "."); err != nil {
		return AcceptanceSubject{}, fmt.Errorf("stage acceptance subject worktree: %w", err)
	}
	protected, err := protectedPathsPresent(attempt.Worktree)
	if err != nil {
		return AcceptanceSubject{}, err
	}
	if len(protected) != 0 {
		args := append([]string{"add", "--force", "--"}, protected...)
		if _, err := gitOutput(attempt.run.git, attempt.Worktree, nil, args...); err != nil {
			return AcceptanceSubject{}, fmt.Errorf("force stage protected acceptance subject paths: %w", err)
		}
	}
	tree, err := writeTree(attempt.run.git, attempt.Worktree)
	if err != nil {
		return AcceptanceSubject{}, err
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
		"commit-tree", recoveryGitObject(tree), "-m", "partitur: acceptance subject",
	)
	if err != nil {
		return AcceptanceSubject{}, fmt.Errorf("create acceptance subject wrapper: %w", err)
	}
	commitID := strings.TrimSpace(string(commit))
	ref := subjectRef(attempt.RunID, attempt.AttemptID)
	receipt, err := ensureRef(
		attempt.run.git,
		attempt.run.repositoryRoot,
		ref,
		commitID,
		attempt.RunID,
		faultpoint.ReceiptAddress("attempt."+string(attempt.AttemptID)+".subject.ref"),
		refExistingMustMatchTree,
	)
	if err != nil {
		return AcceptanceSubject{}, err
	}
	return AcceptanceSubject{Tree: tree, Commit: commitID, Ref: ref, Receipt: receipt}, nil
}

func protectedPathsPresent(worktree string) ([]string, error) {
	paths := make([]string, 0, 2)
	for _, path := range protectedpath.WorktreeNames() {
		_, err := os.Lstat(worktree + "/" + path)
		switch {
		case err == nil:
			paths = append(paths, path)
		case errors.Is(err, fs.ErrNotExist):
		default:
			return nil, err
		}
	}
	return paths, nil
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
	args := append([]string{"add", "--all", "--", "."}, protectedpath.CaptureExclusions()...)
	if _, err := gitOutput(attempt.run.git, attempt.Worktree, nil, args...); err != nil {
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

func subjectRef(runID runstate.RunID, attemptID runstate.AttemptID) string {
	return "refs/partitur/runs/" + string(runID) + "/attempts/" + string(attemptID) + "/subject"
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
