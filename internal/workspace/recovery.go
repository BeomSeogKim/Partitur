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
