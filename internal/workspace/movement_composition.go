package workspace

import (
	"errors"
	"fmt"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// PinMovementBase wraps a composed tree in a commit and pins it for this run
// and movement. Existing wrappers for the same tree are accepted; a different
// tree is a collision rather than a movable ref.
func PinMovementBase(store *runstore.Store, driver *runstore.Driver, input runstore.RunInput, movementID runstate.MovementID, tree string) (string, error) {
	if store == nil || driver == nil || input.BaseCommit == "" || movementID == "" || tree == "" {
		return "", errors.New("workspace: incomplete movement base pin")
	}
	git, err := newSystemGit()
	if err != nil {
		return "", err
	}
	commit, err := gitOutputWithEnvironment(git, store.RepositoryRoot(), []string{
		"GIT_AUTHOR_NAME=Partitur", "GIT_AUTHOR_EMAIL=partitur@invalid", "GIT_AUTHOR_DATE=1970-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Partitur", "GIT_COMMITTER_EMAIL=partitur@invalid", "GIT_COMMITTER_DATE=1970-01-01T00:00:00Z",
	}, "commit-tree", recoveryGitObject(tree), "-p", recoveryGitObject(input.BaseCommit), "-m", "partitur: movement base")
	if err != nil {
		return "", fmt.Errorf("create movement base wrapper: %w", err)
	}
	commitID := string(bytesTrimSpace(commit))
	ref := movementBaseRef(driver.RunID(), movementID)
	err = driver.Mutate(func(transaction *runstore.Txn, _ runstate.State) error {
		_, err := ensureRef(git, store.RepositoryRoot(), ref, commitID, driver.RunID(), faultpoint.ReceiptAddress("movement."+string(movementID)+".base.ref"), refExistingMustMatchTree)
		return err
	})
	if err != nil {
		return "", err
	}
	return commitID, nil
}

func movementBaseCommit(git gitCommand, root string, runID runstate.RunID, movementID runstate.MovementID) (string, error) {
	value, err := gitOutput(git, root, nil, "rev-parse", movementBaseRef(runID, movementID)+"^{commit}")
	if err != nil {
		return "", err
	}
	return string(bytesTrimSpace(value)), nil
}

func movementBaseRef(runID runstate.RunID, movementID runstate.MovementID) string {
	return "refs/partitur/runs/" + string(runID) + "/movements/" + string(movementID) + "/base"
}
