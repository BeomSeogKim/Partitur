package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	if err := verifyRecoveryGitDir(worktree); err != nil {
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
	if path == ".git" {
		return verifyRecoveryGitDir(worktree)
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

func verifyRecoveryGitDir(worktree string) error {
	gitFile := filepath.Join(worktree, ".git")
	info, err := os.Lstat(gitFile)
	if err != nil {
		return fmt.Errorf("%w: inspect .git: %v", errRecoveryGitDirUnverified, err)
	}
	if !info.Mode().IsRegular() {
		return errRecoveryGitDirUnverified
	}
	contents, err := os.ReadFile(gitFile)
	if err != nil {
		return fmt.Errorf("%w: read .git: %v", errRecoveryGitDirUnverified, err)
	}
	gitDir, ok := gitDirPath(filepath.Dir(gitFile), string(contents))
	if !ok {
		return errRecoveryGitDirUnverified
	}
	backlink, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return fmt.Errorf("%w: read gitdir backlink: %v", errRecoveryGitDirUnverified, err)
	}
	expected, ok := gitDirBacklinkPath(gitDir, string(backlink))
	matches, err := samePath(expected, gitFile)
	if !ok || err != nil || !matches {
		return errRecoveryGitDirUnverified
	}
	return nil
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
