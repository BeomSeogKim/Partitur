package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type gitResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type gitCommand interface {
	Run(repositoryRoot string, stdin []byte, args ...string) (gitResult, error)
	RunWithEnvironment(repositoryRoot string, stdin []byte, environment []string, args ...string) (gitResult, error)
}

// compositionGitCommand adds the exact merge execution path required by the
// composition contract without changing the environment or hardening applied
// to the repository's other Git operations.
type compositionGitCommand interface {
	gitCommand
	RunComposition(workingDirectory string, stdin []byte, environment []string, args ...string) (gitResult, error)
}

type systemGit struct {
	path string
	env  []string
}

func newSystemGit() (compositionGitCommand, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("find git: %w", err)
	}
	return systemGit{path: path, env: gitEnvironment()}, nil
}

func gitEnvironment() []string {
	return []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"LANG=C",
		"LC_ALL=C",
		"PATH=" + os.Getenv("PATH"),
	}
}

func (g systemGit) Run(
	repositoryRoot string,
	stdin []byte,
	args ...string,
) (gitResult, error) {
	return g.RunWithEnvironment(repositoryRoot, stdin, nil, args...)
}

func (g systemGit) RunWithEnvironment(
	repositoryRoot string,
	stdin []byte,
	environment []string,
	args ...string,
) (gitResult, error) {
	commandArgs := make([]string, 0, len(args)+6)
	commandArgs = append(
		commandArgs,
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fileMode=true",
	)
	if repositoryRoot != "" {
		commandArgs = append(commandArgs, "-C", repositoryRoot)
	}
	commandArgs = append(commandArgs, args...)
	command := exec.Command(g.path, commandArgs...)
	command.Env = append(append([]string(nil), g.env...), environment...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := gitResult{
		stdout:   stdout.Bytes(),
		stderr:   stderr.Bytes(),
		exitCode: 0,
	}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.exitCode = exitError.ExitCode()
		return result, nil
	}
	return gitResult{}, err
}

// RunComposition executes the §5 merge invocation exactly as supplied. It is
// deliberately separate from RunWithEnvironment: composition must neither add
// shared hardening flags nor inherit the shared Git environment. Its temporary
// repository is initialized from a core-created empty template, so no project
// template hook or configuration can enter that repository.
func (g systemGit) RunComposition(
	workingDirectory string,
	stdin []byte,
	environment []string,
	args ...string,
) (gitResult, error) {
	return runGitCommandIn(workingDirectory, g.path, args, environment, stdin)
}

func runGitCommandIn(workingDirectory, path string, args, environment []string, stdin []byte) (gitResult, error) {
	command := exec.Command(path, args...)
	command.Dir = workingDirectory
	command.Env = append([]string(nil), environment...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := gitResult{
		stdout:   stdout.Bytes(),
		stderr:   stderr.Bytes(),
		exitCode: 0,
	}
	if err == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.exitCode = exitError.ExitCode()
		return result, nil
	}
	return gitResult{}, err
}

type repositoryFacts struct {
	commit          string
	commitQualified string
	tree            string
	treeQualified   string
	objectFormat    string
}

func inspectRepository(
	git gitCommand,
	root string,
) (repositoryFacts, error) {
	versionResult, err := git.Run("", nil, "--version")
	if err != nil {
		return repositoryFacts{}, fmt.Errorf("run git --version: %w", err)
	}
	if versionResult.exitCode != 0 {
		return repositoryFacts{}, gitFailure("git --version", versionResult)
	}
	version, err := parseGitVersion(string(versionResult.stdout))
	if err != nil {
		return repositoryFacts{}, err
	}
	if version.lessThan(2, 47) {
		return repositoryFacts{}, fmt.Errorf("%w: %s", ErrGitTooOld, version)
	}

	bare, err := gitOutput(git, root, nil, "rev-parse", "--is-bare-repository")
	if err != nil {
		return repositoryFacts{}, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	if strings.TrimSpace(string(bare)) != "false" {
		return repositoryFacts{}, ErrBareRepository
	}
	top, err := gitOutput(git, root, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return repositoryFacts{}, fmt.Errorf("%w: %v", ErrNotRepository, err)
	}
	anchored, err := samePath(root, strings.TrimSpace(string(top)))
	if err != nil || !anchored {
		return repositoryFacts{}, ErrNotRepository
	}
	status, err := gitOutput(
		git,
		root,
		nil,
		"status",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
		"--ignored=no",
	)
	if err != nil {
		return repositoryFacts{}, err
	}
	if len(status) != 0 {
		return repositoryFacts{}, ErrDirtySource
	}

	commit, err := gitOutput(git, root, nil, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return repositoryFacts{}, err
	}
	tree, err := gitOutput(git, root, nil, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return repositoryFacts{}, err
	}
	format, err := gitOutput(git, root, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return repositoryFacts{}, err
	}
	facts := repositoryFacts{
		commit:       strings.TrimSpace(string(commit)),
		tree:         strings.TrimSpace(string(tree)),
		objectFormat: strings.TrimSpace(string(format)),
	}
	if facts.objectFormat != "sha1" && facts.objectFormat != "sha256" {
		return repositoryFacts{}, fmt.Errorf(
			"unsupported Git object format %q",
			facts.objectFormat,
		)
	}
	facts.commitQualified = qualifyGitObject(facts.objectFormat, facts.commit)
	facts.treeQualified = qualifyGitObject(facts.objectFormat, facts.tree)
	if err := rejectExternalMergeDrivers(git, root, facts.commit); err != nil {
		return repositoryFacts{}, err
	}
	return facts, nil
}

type gitVersion struct {
	major int
	minor int
	patch int
}

func (v gitVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

func (v gitVersion) lessThan(major, minor int) bool {
	return v.major < major || v.major == major && v.minor < minor
}

func parseGitVersion(output string) (gitVersion, error) {
	fields := strings.Fields(strings.TrimSpace(output))
	if len(fields) < 3 || fields[0] != "git" || fields[1] != "version" {
		return gitVersion{}, fmt.Errorf("unrecognized git version %q", output)
	}
	parts := strings.Split(fields[2], ".")
	if len(parts) < 2 {
		return gitVersion{}, fmt.Errorf("unrecognized git version %q", output)
	}
	values := [3]int{}
	for index := 0; index < len(values) && index < len(parts); index++ {
		digits := leadingDigits(parts[index])
		if digits == "" {
			return gitVersion{}, fmt.Errorf("unrecognized git version %q", output)
		}
		value, err := strconv.Atoi(digits)
		if err != nil {
			return gitVersion{}, fmt.Errorf("unrecognized git version %q", output)
		}
		values[index] = value
	}
	return gitVersion{major: values[0], minor: values[1], patch: values[2]}, nil
}

func leadingDigits(value string) string {
	index := 0
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	return value[:index]
}

func samePath(left, right string) (bool, error) {
	left, err := filepath.Abs(left)
	if err != nil {
		return false, err
	}
	right, err = filepath.Abs(right)
	if err != nil {
		return false, err
	}
	leftResolved, err := filepath.EvalSymlinks(left)
	if err != nil {
		return false, err
	}
	rightResolved, err := filepath.EvalSymlinks(right)
	if err != nil {
		return false, err
	}
	return leftResolved == rightResolved, nil
}

func rejectExternalMergeDrivers(
	git gitCommand,
	root, source string,
) error {
	configured, err := git.Run(
		root,
		nil,
		"config",
		"--local",
		"--get-regexp",
		`^merge\..*\.driver$`,
	)
	if err != nil {
		return err
	}
	if configured.exitCode != 0 && configured.exitCode != 1 {
		return gitFailure("inspect merge drivers", configured)
	}
	if len(bytes.TrimSpace(configured.stdout)) != 0 {
		return fmt.Errorf(
			"%w: repository config",
			ErrExternalMergeDriver,
		)
	}
	defaults, err := git.Run(root, nil, "config", "--local", "--get-all", "merge.default")
	if err != nil {
		return err
	}
	if defaults.exitCode != 0 && defaults.exitCode != 1 {
		return gitFailure("inspect merge.default", defaults)
	}
	for _, value := range strings.Fields(string(defaults.stdout)) {
		if !allowedMergeDriver(value) {
			return fmt.Errorf(
				"%w: merge.default=%s",
				ErrExternalMergeDriver,
				value,
			)
		}
	}

	paths, err := gitOutput(
		git,
		root,
		nil,
		"ls-tree",
		"-r",
		"-z",
		"--name-only",
		source,
	)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	attributes, err := gitOutput(
		git,
		root,
		paths,
		"check-attr",
		"--source="+source,
		"--stdin",
		"-z",
		"merge",
	)
	if err != nil {
		return err
	}
	fields := splitNUL(attributes)
	if len(fields)%3 != 0 {
		return errors.New("git check-attr returned an invalid record")
	}
	for index := 0; index < len(fields); index += 3 {
		if !allowedMergeDriver(fields[index+2]) {
			return fmt.Errorf(
				"%w: path=%s merge=%s",
				ErrExternalMergeDriver,
				fields[index],
				fields[index+2],
			)
		}
	}
	return nil
}

func allowedMergeDriver(value string) bool {
	switch value {
	case "unspecified", "set", "unset", "text", "binary", "union":
		return true
	default:
		return false
	}
}

func gitOutput(
	git gitCommand,
	root string,
	stdin []byte,
	args ...string,
) ([]byte, error) {
	result, err := git.Run(root, stdin, args...)
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, gitFailure(strings.Join(args, " "), result)
	}
	return result.stdout, nil
}

func gitFailure(operation string, result gitResult) error {
	detail := strings.TrimSpace(string(result.stderr))
	if detail == "" {
		detail = strings.TrimSpace(string(result.stdout))
	}
	return fmt.Errorf(
		"%s: git exit %d: %s",
		operation,
		result.exitCode,
		detail,
	)
}

func qualifyGitObject(format, object string) string {
	return "git-" + format + ":" + object
}

func splitNUL(value []byte) []string {
	if len(value) == 0 {
		return nil
	}
	parts := bytes.Split(value, []byte{0})
	if len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	result := make([]string, len(parts))
	for index, part := range parts {
		result[index] = string(part)
	}
	return result
}
