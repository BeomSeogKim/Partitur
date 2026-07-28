package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// VerifyRecoverySubject checks the non-mutating recovery form of the workspace
// invariant. subjectTree is the tree recorded by acceptance.started.
//
// Git's tree comparison covers tracked content, modes, and symlink targets.
// Non-ignored untracked files are checked separately. The protected paths are
// also required to be represented by the recorded tree rather than being
// tolerated merely because Git ignores them.
func VerifyRecoverySubject(worktree, subjectTree string) (bool, error) {
	if worktree == "" || subjectTree == "" {
		return false, errors.New("workspace: recovery subject is incomplete")
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
	for _, path := range []string{".git", "partitur.yaml", ".partitur"} {
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

var errRecoveryProtectedMismatch = errors.New("recovery protected path mismatch")

func verifyRecoveryProtectedPath(git gitCommand, worktree, subjectTree, path string) error {
	if path == ".git" {
		info, err := os.Lstat(worktree + "/.git")
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errRecoveryProtectedMismatch
		}
		return nil
	}
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
