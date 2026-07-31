package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// CompositionContributor is one already ordered dependency change set. The
// caller supplies topological order with score declaration order as its tie
// breaker; this package preserves that order for both merging and identity.
type CompositionContributor struct {
	MovementID  runstate.MovementID
	ChangeSetID string
	BaseTree    string
	ResultTree  string
}

// CompositionInput describes one movement's fan-in before any lifecycle
// evidence is emitted. BaseTree and every contributor tree are qualified Git
// object identifiers.
type CompositionInput struct {
	RepositoryRoot string
	BaseTree       string
	Contributors   []CompositionContributor
}

// CompositionPair is a sorted key/value pair in the composition environment.
type CompositionPair struct {
	Key   string
	Value string
}

// CompositionEnvironment identifies the Git configuration and invocation that
// produced a merge verdict. Argv contains each full merge-tree argv in order;
// a fan-in can execute more than one normative merge invocation. Env[i] is
// A.4's env for Argv[i], because a multi-merge composition has a sequence of
// merge subprocesses rather than a different environment field.
type CompositionEnvironment struct {
	GitVersionString string
	ObjectFormat     string
	Argv             [][]string
	Env              [][]CompositionPair
	MergeRenormalize bool
	MergeConfig      []CompositionPair
}

// CompositionConflict is a merge verdict. Paths retain Git's NUL-delimited
// spelling and are not normalized.
type CompositionConflict struct {
	Tree  string
	Paths []string
}

// CompositionFailure means that no merge verdict was obtained. ExitStatus is
// present only when merge-tree itself exited.
type CompositionFailure struct {
	ExitStatus *int
	Diagnostic string
}

// CompositionResult has exactly one outcome: ResultTree, Conflict, or
// Failure. Environment and EnvironmentHash are present once the isolated
// composition repository is ready to describe an attempted merge.
type CompositionResult struct {
	ResultTree      string
	Conflict        *CompositionConflict
	Failure         *CompositionFailure
	Environment     *CompositionEnvironment
	EnvironmentHash string
}

// Compose merges contributors in their supplied deterministic order. It never
// executes a merge in the source repository and does not emit lifecycle
// evidence or mutate Partitur refs. A successful result tree is imported into
// the source object database so the caller can wrap and pin it.
func Compose(input CompositionInput) CompositionResult {
	git, err := newSystemGit()
	if err != nil {
		return compositionFailureResult("find git", nil, err)
	}
	return compose(git, input)
}

func compose(git compositionGitCommand, input CompositionInput) CompositionResult {
	if git == nil || input.RepositoryRoot == "" || input.BaseTree == "" {
		return compositionFailureResult("prepare composition", nil, errors.New("incomplete input"))
	}
	if len(input.Contributors) == 0 {
		return CompositionResult{ResultTree: input.BaseTree}
	}

	version, err := compositionGitVersion(git)
	if err != nil {
		return compositionFailureResult("read git version", nil, err)
	}
	format, err := compositionObjectFormat(input.BaseTree)
	if err != nil {
		return compositionFailureResult("read object format", nil, err)
	}
	sourceObjects, err := compositionObjectDirectory(input.RepositoryRoot)
	if err != nil {
		return compositionFailureResult("read source objects", nil, err)
	}
	objects, err := collectCompositionObjects(format, input)
	if err != nil {
		return compositionFailureResult("validate composition trees", nil, err)
	}

	temporary, err := newCompositionRepository(git, format)
	if err != nil {
		return compositionFailureResult("create composition repository", nil, err)
	}
	defer os.RemoveAll(temporary.root)
	if err := populateCompositionRepository(git, sourceObjects, temporary, objects); err != nil {
		return compositionFailureResult("populate composition repository", nil, err)
	}

	environment, err := temporary.environment(git, version, format)
	if err != nil {
		return compositionFailureResult("read composition environment", nil, err)
	}
	result := CompositionResult{Environment: &environment}

	current := objects.base
	seen := make(map[string]struct{}, len(input.Contributors))
	for _, contributor := range input.Contributors {
		if contributor.ChangeSetID == "" {
			return compositionFailureWithEnvironment(result, "validate contributor", nil, errors.New("change set id is absent"))
		}
		if _, duplicate := seen[contributor.ChangeSetID]; duplicate {
			continue
		}
		seen[contributor.ChangeSetID] = struct{}{}
		base, err := compositionTreeObject(format, contributor.BaseTree)
		if err != nil {
			return compositionFailureWithEnvironment(result, "validate contributor base", nil, err)
		}
		theirs, err := compositionTreeObject(format, contributor.ResultTree)
		if err != nil {
			return compositionFailureWithEnvironment(result, "validate contributor result", nil, err)
		}
		argv := temporary.mergeArgv(base, current, theirs)
		env := compositionMergeEnvironment(current)
		if err := rejectCompositionMergeDrivers(git, temporary, base, current, theirs); err != nil {
			return compositionFailureWithEnvironment(result, "reject merge driver", nil, err)
		}
		merge, err := git.RunComposition(
			filepath.Dir(temporary.gitDir),
			nil,
			env,
			argv...,
		)
		if err != nil {
			return compositionFailureWithEnvironment(result, "run merge-tree", nil, err)
		}
		result.Environment.Argv = append(result.Environment.Argv, append([]string(nil), argv...))
		result.Environment.Env = append(result.Environment.Env, compositionEnvironmentPairs(env))
		result.EnvironmentHash, err = compositionEnvironmentHash(*result.Environment)
		if err != nil {
			return compositionFailureWithEnvironment(result, "hash composition environment", nil, err)
		}
		switch merge.exitCode {
		case 0:
			fields := splitNUL(merge.stdout)
			if len(fields) == 0 || fields[0] == "" {
				return compositionFailureWithEnvironment(result, "parse merge-tree", nil, errors.New("merge-tree returned no result tree"))
			}
			current = fields[0]
		case 1:
			fields := splitNUL(merge.stdout)
			if len(fields) == 0 || fields[0] == "" {
				return compositionFailureWithEnvironment(result, "parse merge-tree", intPointer(1), errors.New("merge-tree returned no conflict tree"))
			}
			return CompositionResult{
				Conflict:    &CompositionConflict{Tree: fields[0], Paths: append([]string(nil), fields[1:]...)},
				Environment: result.Environment, EnvironmentHash: result.EnvironmentHash,
			}
		default:
			return compositionFailureWithEnvironment(result, "merge-tree", intPointer(merge.exitCode), gitFailure("merge-tree", merge))
		}
	}
	if err := persistCompositionResult(git, input.RepositoryRoot, temporary, current); err != nil {
		return compositionFailureWithEnvironment(result, "persist composition result", nil, err)
	}
	result.ResultTree = qualifyGitObject(format, current)
	return result
}

func persistCompositionResult(git gitCommand, sourceRoot string, temporary compositionRepository, tree string) error {
	packed, err := temporary.run(git, []byte(tree+"\n"), "pack-objects", "--stdout", "--revs")
	if err != nil {
		return err
	}
	if packed.exitCode != 0 {
		return gitFailure("pack composed result", packed)
	}
	indexed, err := git.Run(sourceRoot, packed.stdout, "index-pack", "--stdin", "--fix-thin")
	if err != nil {
		return err
	}
	if indexed.exitCode != 0 {
		return gitFailure("import composed result", indexed)
	}
	return nil
}

// MovementCompositionHash returns the A.4 movement-composition identity. The
// contributor sequence is deliberately pre-deduplication: deduplication only
// controls merge application, while the identity records every declared writer.
func MovementCompositionHash(
	movementID runstate.MovementID,
	baseTree string,
	contributors []CompositionContributor,
	environmentHash string,
) (string, error) {
	if movementID == "" || baseTree == "" {
		return "", errors.New("movement composition identity is incomplete")
	}
	if len(contributors) == 0 {
		if environmentHash != "" {
			return "", errors.New("identity composition forbids an environment hash")
		}
		return canonical.Hash(canonical.DomainMovementComposition, map[string]any{
			"composition_mode":              "identity",
			"movement_id":                   string(movementID),
			"base_tree":                     baseTree,
			"contributors":                  []any{},
			"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
		})
	}
	if environmentHash == "" {
		return "", errors.New("merge composition requires an environment hash")
	}
	value := make([]any, len(contributors))
	for index, contributor := range contributors {
		if contributor.MovementID == "" || contributor.ChangeSetID == "" {
			return "", errors.New("merge composition contributor is incomplete")
		}
		value[index] = map[string]any{
			"movement_id":   string(contributor.MovementID),
			"change_set_id": contributor.ChangeSetID,
		}
	}
	return canonical.Hash(canonical.DomainMovementComposition, map[string]any{
		"composition_mode":              "merge",
		"movement_id":                   string(movementID),
		"base_tree":                     baseTree,
		"contributors":                  value,
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
		"composition_environment_hash":  environmentHash,
	})
}

// CandidateCompositionHash returns the A.4 candidate-composition identity.
// Its merge input deliberately retains every writer in stable topological
// order; Compose performs the separate content deduplication that selects the applied
// change-set sequence for the candidate identity.
func CandidateCompositionHash(
	baseTree string,
	contributors []CompositionContributor,
	environmentHash string,
) (string, error) {
	if baseTree == "" {
		return "", errors.New("candidate composition identity is incomplete")
	}
	if len(contributors) == 0 {
		if environmentHash != "" {
			return "", errors.New("identity composition forbids an environment hash")
		}
		return canonical.Hash(canonical.DomainCandidateComposition, map[string]any{
			"composition_mode":              "identity",
			"base_tree":                     baseTree,
			"contributors":                  []any{},
			"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
		})
	}
	if environmentHash == "" {
		return "", errors.New("merge composition requires an environment hash")
	}
	value := make([]any, len(contributors))
	for index, contributor := range contributors {
		if contributor.MovementID == "" || contributor.ChangeSetID == "" {
			return "", errors.New("merge composition contributor is incomplete")
		}
		value[index] = map[string]any{
			"movement_id":   string(contributor.MovementID),
			"change_set_id": contributor.ChangeSetID,
		}
	}
	return canonical.Hash(canonical.DomainCandidateComposition, map[string]any{
		"composition_mode":              "merge",
		"base_tree":                     baseTree,
		"contributors":                  value,
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
		"composition_environment_hash":  environmentHash,
	})
}

func compositionEnvironmentHash(environment CompositionEnvironment) (string, error) {
	argv := make([]any, len(environment.Argv))
	for index, invocation := range environment.Argv {
		argv[index] = stringsToCompositionAny(invocation)
	}
	return canonical.Hash(canonical.DomainCompositionEnvironment, map[string]any{
		"git_version_string": environment.GitVersionString,
		"object_format":      environment.ObjectFormat,
		"argv":               argv,
		"env":                compositionEnvironmentsAny(environment.Env),
		"merge_renormalize":  environment.MergeRenormalize,
		"merge_config":       compositionPairsAny(environment.MergeConfig),
	})
}

func stringsToCompositionAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func compositionPairsAny(values []CompositionPair) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = map[string]any{"key": value.Key, "value": value.Value}
	}
	return result
}

func compositionEnvironmentsAny(environments [][]CompositionPair) []any {
	result := make([]any, len(environments))
	for index, environment := range environments {
		result[index] = compositionPairsAny(environment)
	}
	return result
}

type compositionObjects struct {
	base string
	all  []string
}

func collectCompositionObjects(format string, input CompositionInput) (compositionObjects, error) {
	base, err := compositionTreeObject(format, input.BaseTree)
	if err != nil {
		return compositionObjects{}, err
	}
	objects := compositionObjects{base: base, all: []string{base}}
	for _, contributor := range input.Contributors {
		if contributor.ChangeSetID == "" {
			return compositionObjects{}, errors.New("change set id is absent")
		}
		contributorBase, err := compositionTreeObject(format, contributor.BaseTree)
		if err != nil {
			return compositionObjects{}, err
		}
		result, err := compositionTreeObject(format, contributor.ResultTree)
		if err != nil {
			return compositionObjects{}, err
		}
		objects.all = append(objects.all, contributorBase, result)
	}
	return objects, nil
}

func compositionTreeObject(format, value string) (string, error) {
	prefix := "git-" + format + ":"
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return "", fmt.Errorf("tree %q is not a %s object", value, format)
	}
	return strings.TrimPrefix(value, prefix), nil
}

func compositionGitVersion(git gitCommand) (string, error) {
	result, err := git.Run("", nil, "--version")
	if err != nil {
		return "", err
	}
	if result.exitCode != 0 {
		return "", gitFailure("git --version", result)
	}
	version, err := parseGitVersion(string(result.stdout))
	if err != nil {
		return "", err
	}
	if version.lessThan(2, 47) {
		return "", fmt.Errorf("%w: %s", ErrGitTooOld, version)
	}
	return string(result.stdout), nil
}

func compositionObjectFormat(tree string) (string, error) {
	switch {
	case strings.HasPrefix(tree, "git-sha1:"):
		return "sha1", nil
	case strings.HasPrefix(tree, "git-sha256:"):
		return "sha256", nil
	default:
		return "", fmt.Errorf("tree %q has no supported object format", tree)
	}
}

type compositionRepository struct {
	root     string
	gitDir   string
	workTree string
}

func newCompositionRepository(git gitCommand, format string) (compositionRepository, error) {
	root, err := os.MkdirTemp("", "partitur-composition-")
	if err != nil {
		return compositionRepository{}, err
	}
	fail := func(err error) (compositionRepository, error) {
		_ = os.RemoveAll(root)
		return compositionRepository{}, err
	}
	repository := filepath.Join(root, "repository")
	template := filepath.Join(root, "template")
	if err := os.Mkdir(template, 0o700); err != nil {
		return fail(err)
	}
	initialized, err := git.Run("", nil, "init", "--quiet", "--object-format="+format, "--template="+template, repository)
	if err != nil {
		return fail(err)
	}
	if initialized.exitCode != 0 {
		return fail(gitFailure("initialize composition repository", initialized))
	}
	workTree := filepath.Join(root, "worktree")
	if err := os.Mkdir(workTree, 0o700); err != nil {
		return fail(err)
	}
	composition := compositionRepository{root: root, gitDir: filepath.Join(repository, ".git"), workTree: workTree}
	configured, err := composition.run(git, nil, "config", "--local", "merge.renormalize", "false")
	if err != nil {
		return fail(err)
	}
	if configured.exitCode != 0 {
		return fail(gitFailure("configure composition repository", configured))
	}
	return composition, nil
}

func (repository compositionRepository) run(git gitCommand, stdin []byte, args ...string) (gitResult, error) {
	return repository.runWithEnvironment(git, stdin, nil, args...)
}

func (repository compositionRepository) runWithEnvironment(
	git gitCommand,
	stdin []byte,
	environment []string,
	args ...string,
) (gitResult, error) {
	command := []string{"--git-dir=" + repository.gitDir, "--work-tree=" + repository.workTree}
	command = append(command, args...)
	return git.RunWithEnvironment("", stdin, environment, command...)
}

func (repository compositionRepository) mergeArgv(base, ours, theirs string) []string {
	argv := []string{
		"--git-dir=.git",
		"--work-tree=../worktree",
	}
	return append(argv, compositionMergeArgv(base, ours, theirs)...)
}

func (repository compositionRepository) environment(
	git gitCommand,
	version, format string,
) (CompositionEnvironment, error) {
	config, err := repository.run(git, nil, "config", "--null", "--get-regexp", "^merge\\.")
	if err != nil {
		return CompositionEnvironment{}, err
	}
	if config.exitCode != 0 && config.exitCode != 1 {
		return CompositionEnvironment{}, gitFailure("read merge config", config)
	}
	mergeConfig, err := parseCompositionConfig(config.stdout)
	if err != nil {
		return CompositionEnvironment{}, err
	}
	renormalize := false
	for _, pair := range mergeConfig {
		if pair.Key != "merge.renormalize" {
			continue
		}
		if pair.Value != "true" && pair.Value != "false" {
			return CompositionEnvironment{}, fmt.Errorf("invalid merge.renormalize %q", pair.Value)
		}
		renormalize = pair.Value == "true"
	}
	return CompositionEnvironment{
		GitVersionString: version,
		ObjectFormat:     format,
		MergeRenormalize: renormalize,
		MergeConfig:      mergeConfig,
	}, nil
}

func compositionMergeEnvironment(ours string) []string {
	environment := compositionStaticEnvironment()
	return append(environment, "GIT_ATTR_SOURCE="+ours)
}

func compositionStaticEnvironment() []string {
	return []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
}

func populateCompositionRepository(
	git gitCommand,
	sourceObjects string,
	target compositionRepository,
	objects compositionObjects,
) error {
	input := []byte(strings.Join(objects.all, "\n") + "\n")
	// GIT_OBJECT_DIRECTORY may consult that directory's alternates. This is an
	// intentional object-access boundary for the explicitly rooted input trees,
	// not a read of the source repository configuration.
	packed, err := target.runWithEnvironment(
		git,
		input,
		[]string{"GIT_OBJECT_DIRECTORY=" + sourceObjects},
		"pack-objects", "--stdout", "--revs",
	)
	if err != nil {
		return err
	}
	if packed.exitCode != 0 {
		return gitFailure("pack composition objects", packed)
	}
	indexed, err := target.run(git, packed.stdout, "index-pack", "--stdin", "--fix-thin")
	if err != nil {
		return err
	}
	if indexed.exitCode != 0 {
		return gitFailure("index composition objects", indexed)
	}
	return nil
}

func compositionObjectDirectory(root string) (string, error) {
	gitDir, err := compositionGitDirectory(root)
	if err != nil {
		return "", err
	}
	commonDir := gitDir
	if contents, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		value := strings.TrimSpace(string(contents))
		if value == "" {
			return "", errors.New("source git common directory is absent")
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(gitDir, value)
		}
		commonDir = filepath.Clean(value)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	objects := filepath.Join(commonDir, "objects")
	info, err := os.Stat(objects)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("source object directory is not a directory")
	}
	return objects, nil
}

func compositionGitDirectory(root string) (string, error) {
	marker := filepath.Join(root, ".git")
	info, err := os.Stat(marker)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return marker, nil
	}
	contents, err := os.ReadFile(marker)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(contents))
	if !strings.HasPrefix(value, "gitdir: ") {
		return "", errors.New("source .git file is invalid")
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(value, "gitdir: "))
	if gitDir == "" {
		return "", errors.New("source git directory is absent")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	return filepath.Clean(gitDir), nil
}

func rejectCompositionMergeDrivers(
	git gitCommand,
	repository compositionRepository,
	trees ...string,
) error {
	paths := make(map[string]struct{})
	for _, tree := range trees {
		listed, err := repository.run(git, nil, "ls-tree", "-r", "-z", "--name-only", tree)
		if err != nil {
			return err
		}
		if listed.exitCode != 0 {
			return gitFailure("list composition tree paths", listed)
		}
		for _, path := range splitNUL(listed.stdout) {
			paths[path] = struct{}{}
		}
	}
	if len(paths) == 0 {
		return nil
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	slices.Sort(ordered)
	stdin := []byte(strings.Join(ordered, "\x00") + "\x00")
	attributes, err := repository.run(git, stdin, "check-attr", "--source="+trees[1], "--stdin", "-z", "merge")
	if err != nil {
		return err
	}
	if attributes.exitCode != 0 {
		return gitFailure("check composition merge attributes", attributes)
	}
	fields := splitNUL(attributes.stdout)
	if len(fields)%3 != 0 {
		return errors.New("git check-attr returned an invalid record")
	}
	for index := 0; index < len(fields); index += 3 {
		if !allowedMergeDriver(fields[index+2]) {
			return fmt.Errorf("%w: path=%s merge=%s", ErrExternalMergeDriver, fields[index], fields[index+2])
		}
	}
	return nil
}

func compositionMergeArgv(base, ours, theirs string) []string {
	return []string{
		"merge-tree", "--write-tree", "--merge-base=" + base,
		"--name-only", "-z", "--no-messages", ours, theirs,
	}
}

func parseCompositionConfig(output []byte) ([]CompositionPair, error) {
	fields := splitNUL(output)
	pairs := make([]CompositionPair, len(fields))
	for index, field := range fields {
		key, value, found := strings.Cut(field, "\n")
		if !found || key == "" {
			return nil, errors.New("git config returned an invalid record")
		}
		pairs[index] = CompositionPair{Key: key, Value: value}
	}
	slices.SortFunc(pairs, func(left, right CompositionPair) int {
		if left.Key == right.Key {
			return strings.Compare(left.Value, right.Value)
		}
		return strings.Compare(left.Key, right.Key)
	})
	return pairs, nil
}

func compositionEnvironmentPairs(environment []string) []CompositionPair {
	pairs := make([]CompositionPair, 0, len(environment))
	for _, value := range environment {
		key, entry, found := strings.Cut(value, "=")
		if !found {
			continue
		}
		pairs = append(pairs, CompositionPair{Key: key, Value: entry})
	}
	slices.SortFunc(pairs, func(left, right CompositionPair) int {
		return strings.Compare(left.Key, right.Key)
	})
	return pairs
}

func compositionFailureResult(operation string, status *int, err error) CompositionResult {
	return CompositionResult{Failure: &CompositionFailure{
		ExitStatus: status, Diagnostic: compositionDiagnostic(operation, err),
	}}
}

func compositionFailureWithEnvironment(
	result CompositionResult,
	operation string,
	status *int,
	err error,
) CompositionResult {
	result.ResultTree = ""
	result.Conflict = nil
	result.Failure = &CompositionFailure{ExitStatus: status, Diagnostic: compositionDiagnostic(operation, err)}
	return result
}

func compositionDiagnostic(operation string, err error) string {
	if errors.Is(err, ErrExternalMergeDriver) {
		return operation + ": " + ErrExternalMergeDriver.Error()
	}
	if err == nil {
		return operation
	}
	// Git's raw diagnostic can contain source paths, attributes, and arbitrary
	// repository-controlled text. Evidence retains this bounded classification,
	// not subprocess output.
	return operation + ": composition execution failed"
}

func intPointer(value int) *int { return &value }
