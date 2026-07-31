package workspace

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "composition-recording-wrapper" {
		runCompositionRecordingWrapper()
		return
	}
	os.Exit(m.Run())
}

func TestComposeUsesEachChangeSetRecordedBaseAndDeduplicatesContent(t *testing.T) {
	repository := newCompositionFixture(t)
	baseCommit := gitText(t, repository, "rev-parse", "HEAD")
	base := compositionFixtureTree(t, repository)

	writeFile(t, filepath.Join(repository, "value.txt"), []byte("one\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "first change")
	first := compositionFixtureTree(t, repository)
	firstCommit := gitText(t, repository, "rev-parse", "HEAD")

	writeFile(t, filepath.Join(repository, "value.txt"), []byte("two\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "second change")
	second := compositionFixtureTree(t, repository)

	duplicateCommit := gitText(t, repository, "commit-tree", strings.TrimPrefix(first, "git-sha1:"), "-p", baseCommit, "-m", "duplicate storage")
	if duplicateCommit == firstCommit {
		t.Fatal("fixture checkpoint commits unexpectedly share storage identity")
	}
	recording := &compositionRecordingGit{delegate: mustSystemGit(t)}
	result := compose(recording, CompositionInput{
		RepositoryRoot: repository,
		BaseTree:       base,
		Contributors: []CompositionContributor{
			{MovementID: "one", ChangeSetID: "same-content", BaseTree: base, ResultTree: first},
			{MovementID: "two", ChangeSetID: "second", BaseTree: first, ResultTree: second},
			{MovementID: "three", ChangeSetID: "same-content", BaseTree: base, ResultTree: first},
		},
	})
	if result.Failure != nil || result.Conflict != nil {
		t.Fatalf("compose result = %#v, want clean result", result)
	}
	if result.ResultTree != second {
		t.Fatalf("result tree = %q, want %q; the second change must merge against its own base", result.ResultTree, second)
	}
	if merges := recording.mergeTreeCalls(); merges != 2 {
		t.Fatalf("merge-tree calls = %d, want 2; duplicate content identity was applied", merges)
	}
}

func TestCandidateCompositionHashRetainsDuplicateAndNoOpWriters(t *testing.T) {
	contributors := []CompositionContributor{
		{MovementID: "write", ChangeSetID: "sha256:one"},
		{MovementID: "noop", ChangeSetID: "sha256:one"},
	}
	got, err := CandidateCompositionHash("git-sha1:base", contributors, "sha256:environment")
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical.Hash(canonical.DomainCandidateComposition, map[string]any{
		"composition_mode": "merge", "base_tree": "git-sha1:base",
		"contributors": []any{
			map[string]any{"movement_id": "write", "change_set_id": "sha256:one"},
			map[string]any{"movement_id": "noop", "change_set_id": "sha256:one"},
		},
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
		"composition_environment_hash":  "sha256:environment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("candidate composition hash = %q, want full merge preimage %q", got, want)
	}
	withoutNoOp, err := CandidateCompositionHash("git-sha1:base", contributors[:1], "sha256:environment")
	if err != nil {
		t.Fatal(err)
	}
	if got == withoutNoOp {
		t.Fatal("candidate composition hash deduplicated its full contributor sequence")
	}
}

func TestComposeConflictPreservesNULDelimitedNewlinePath(t *testing.T) {
	repository := newCompositionFixture(t)
	base := compositionFixtureTree(t, repository)
	const path = "line\nbreak.txt"
	writeFile(t, filepath.Join(repository, path), []byte("one\n"), 0o600)
	gitRun(t, repository, "add", "--", path)
	gitRun(t, repository, "commit", "-m", "first writer")
	ours := compositionFixtureTree(t, repository)

	gitRun(t, repository, "reset", "--hard", "HEAD~1")
	writeFile(t, filepath.Join(repository, path), []byte("two\n"), 0o600)
	gitRun(t, repository, "add", "--", path)
	gitRun(t, repository, "commit", "-m", "second writer")
	theirs := compositionFixtureTree(t, repository)

	result := Compose(CompositionInput{
		RepositoryRoot: repository,
		BaseTree:       base,
		Contributors: []CompositionContributor{
			{MovementID: "one", ChangeSetID: "one", BaseTree: base, ResultTree: ours},
			{MovementID: "two", ChangeSetID: "two", BaseTree: base, ResultTree: theirs},
		},
	})
	if result.Failure != nil || result.Conflict == nil || result.ResultTree != "" {
		t.Fatalf("compose result = %#v, want conflict verdict", result)
	}
	if !slices.Contains(result.Conflict.Paths, path) {
		t.Fatalf("conflicted paths = %#v, want newline path %q", result.Conflict.Paths, path)
	}
}

func TestComposeNeverConsultsSourceDriverOrInfoAttributes(t *testing.T) {
	repository := newCompositionFixture(t)
	base := compositionFixtureTree(t, repository)
	writeFile(t, filepath.Join(repository, "value.txt"), []byte("ours\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "ours")
	ours := compositionFixtureTree(t, repository)
	gitRun(t, repository, "reset", "--hard", "HEAD~1")
	writeFile(t, filepath.Join(repository, "value.txt"), []byte("theirs\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "theirs")
	theirs := compositionFixtureTree(t, repository)

	gitRun(t, repository, "config", "merge.choice.driver", "cp %B %A")
	gitDir := gitText(t, repository, "rev-parse", "--git-dir")
	writeFile(t, filepath.Join(repository, gitDir, "info", "attributes"), []byte("*.txt merge=choice\n"), 0o600)

	result := Compose(CompositionInput{
		RepositoryRoot: repository,
		BaseTree:       base,
		Contributors: []CompositionContributor{
			{MovementID: "one", ChangeSetID: "one", BaseTree: base, ResultTree: ours},
			{MovementID: "two", ChangeSetID: "two", BaseTree: base, ResultTree: theirs},
		},
	})
	if result.Failure != nil || result.Conflict == nil {
		t.Fatalf("compose result = %#v, want built-in-driver conflict", result)
	}
	if !slices.Contains(result.Conflict.Paths, "value.txt") {
		t.Fatalf("conflicted paths = %#v, want value.txt", result.Conflict.Paths)
	}
}

func TestComposeTransfersGitlinkObjectsNeededForMerge(t *testing.T) {
	repository := newCompositionFixture(t)
	emptyTree := compositionGitInput(t, repository, "", "mktree", "-z")
	childBase := gitText(t, repository, "commit-tree", emptyTree, "-m", "child base")
	childNext := gitText(t, repository, "commit-tree", emptyTree, "-p", childBase, "-m", "child next")
	base := "git-sha1:" + compositionGitInput(
		t, repository, "160000 commit "+childBase+"\tsub\x00", "mktree", "-z",
	)
	resultTree := "git-sha1:" + compositionGitInput(
		t, repository, "160000 commit "+childNext+"\tsub\x00", "mktree", "-z",
	)

	result := Compose(CompositionInput{
		RepositoryRoot: repository,
		BaseTree:       base,
		Contributors: []CompositionContributor{{
			MovementID: "one", ChangeSetID: "gitlink", BaseTree: base, ResultTree: resultTree,
		}},
	})
	if result.Failure != nil || result.Conflict != nil {
		t.Fatalf("gitlink composition = %#v, want clean result", result)
	}
	if result.ResultTree != resultTree {
		t.Fatalf("gitlink result tree = %q, want %q", result.ResultTree, resultTree)
	}
}

func TestComposeRejectsInTreeExternalMergeDriverBeforeMerge(t *testing.T) {
	repository := newCompositionFixture(t)
	writeFile(t, filepath.Join(repository, ".gitattributes"), []byte("*.txt merge=custom\n"), 0o600)
	gitRun(t, repository, "add", ".gitattributes")
	gitRun(t, repository, "commit", "-m", "external driver attribute")
	base := compositionFixtureTree(t, repository)
	writeFile(t, filepath.Join(repository, "value.txt"), []byte("changed\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "change")
	changed := compositionFixtureTree(t, repository)
	recording := &compositionRecordingGit{delegate: mustSystemGit(t)}

	result := compose(recording, CompositionInput{
		RepositoryRoot: repository,
		BaseTree:       base,
		Contributors: []CompositionContributor{{
			MovementID: "one", ChangeSetID: "one", BaseTree: base, ResultTree: changed,
		}},
	})
	if result.Failure == nil || result.Conflict != nil || !strings.Contains(result.Failure.Diagnostic, ErrExternalMergeDriver.Error()) {
		t.Fatalf("compose result = %#v, want rejected-driver no-verdict failure", result)
	}
	if merges := recording.mergeTreeCalls(); merges != 0 {
		t.Fatalf("merge-tree calls = %d, want 0 after driver rejection", merges)
	}
	if result.Environment == nil {
		t.Fatal("rejected-driver result omitted the composition environment")
	}
	if len(result.Environment.Argv) != 0 || len(result.Environment.Env) != 0 {
		t.Fatalf("rejected-driver invocations = argv:%#v env:%#v, want none", result.Environment.Argv, result.Environment.Env)
	}
}

func TestNewCompositionRepositoryIgnoresTemplateMergeConfigAndHooks(t *testing.T) {
	maliciousTemplate := t.TempDir()
	hooks := filepath.Join(maliciousTemplate, "hooks")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(maliciousTemplate, "config")
	hook := filepath.Join(hooks, "pre-commit")
	writeFile(t, config, []byte("[merge]\n\tdefault = external\n"), 0o600)
	writeFile(t, hook, []byte("#!/bin/sh\nexit 1\n"), 0o700)
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("template config fixture is absent: %v", err)
	}
	if info, err := os.Stat(hook); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("template hook fixture is not executable: info=%#v err=%v", info, err)
	}

	git := &compositionTemplateGit{delegate: mustSystemGit(t), template: maliciousTemplate}
	repository, err := newCompositionRepository(git, "sha1")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(repository.root)
	defaultDriver, err := repository.run(git, nil, "config", "--local", "--get", "merge.default")
	if err != nil {
		t.Fatal(err)
	}
	if defaultDriver.exitCode != 1 {
		t.Fatalf("composition merge.default exit status = %d, want unset status 1: %s", defaultDriver.exitCode, defaultDriver.stderr)
	}
	if _, err := os.Stat(filepath.Join(repository.gitDir, "hooks", "pre-commit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("template hook entered composition repository: %v", err)
	}
	if git.injectedTemplate {
		t.Fatal("composition initialization did not provide its own empty template")
	}
}

func TestComposeDistinguishesNonVerdictMergeFailureFromConflict(t *testing.T) {
	repository := newCompositionFixture(t)
	base := compositionFixtureTree(t, repository)
	writeFile(t, filepath.Join(repository, "value.txt"), []byte("changed\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "change")
	changed := compositionFixtureTree(t, repository)

	wrapper := compositionExitWrapper(t, 2)
	result := compose(systemGit{path: wrapper, env: gitEnvironment()}, CompositionInput{
		RepositoryRoot: repository,
		BaseTree:       base,
		Contributors: []CompositionContributor{{
			MovementID: "one", ChangeSetID: "one", BaseTree: base, ResultTree: changed,
		}},
	})
	if result.Conflict != nil || result.ResultTree != "" || result.Failure == nil {
		t.Fatalf("compose result = %#v, want no-verdict failure", result)
	}
	if result.Failure.ExitStatus == nil || *result.Failure.ExitStatus != 2 {
		t.Fatalf("failure exit status = %#v, want 2", result.Failure.ExitStatus)
	}
	if !strings.Contains(result.Failure.Diagnostic, "merge-tree") {
		t.Fatalf("failure diagnostic = %q, want merge-tree context", result.Failure.Diagnostic)
	}
}

func TestComposeRecordsOnlyExecutedMergeInvocations(t *testing.T) {
	repository := newCompositionFixture(t)
	base := compositionFixtureTree(t, repository)
	writeFile(t, filepath.Join(repository, "value.txt"), []byte("changed\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "change")
	changed := compositionFixtureTree(t, repository)
	input := CompositionInput{
		RepositoryRoot: repository,
		BaseTree:       base,
		Contributors: []CompositionContributor{{
			MovementID: "one", ChangeSetID: "one", BaseTree: base, ResultTree: changed,
		}},
	}

	t.Run("launch failure is not recorded", func(t *testing.T) {
		git := &compositionMergeOutcomeGit{
			delegate: mustSystemGit(t),
			err:      errors.New("launch merge-tree"),
		}
		result := compose(git, input)
		if result.Failure == nil || result.Failure.ExitStatus != nil {
			t.Fatalf("compose result = %#v, want launch failure without exit status", result)
		}
		if len(git.invocations) != 1 {
			t.Fatalf("merge invocations = %d, want one launch attempt", len(git.invocations))
		}
		if result.Environment == nil {
			t.Fatal("launch failure omitted composition environment")
		}
		if len(result.Environment.Argv) != 0 || len(result.Environment.Env) != 0 || result.EnvironmentHash != "" {
			t.Fatalf("launch failure recorded invocation = argv:%#v env:%#v hash:%q, want none", result.Environment.Argv, result.Environment.Env, result.EnvironmentHash)
		}
	})

	for _, test := range []struct {
		name           string
		merge          gitResult
		wantConflict   bool
		wantExitStatus int
	}{
		{
			name:         "conflict exit is recorded",
			merge:        gitResult{stdout: []byte("tree\\x00value.txt\\x00"), exitCode: 1},
			wantConflict: true,
		},
		{
			name:           "no-verdict exit is recorded",
			merge:          gitResult{exitCode: 2},
			wantExitStatus: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			git := &compositionMergeOutcomeGit{delegate: mustSystemGit(t), result: test.merge}
			result := compose(git, input)
			if test.wantConflict {
				if result.Conflict == nil || result.Failure != nil {
					t.Fatalf("compose result = %#v, want conflict verdict", result)
				}
			} else if result.Failure == nil || result.Failure.ExitStatus == nil || *result.Failure.ExitStatus != test.wantExitStatus {
				t.Fatalf("compose result = %#v, want exit status %d", result, test.wantExitStatus)
			}
			if len(git.invocations) != 1 {
				t.Fatalf("merge invocations = %d, want one", len(git.invocations))
			}
			if result.Environment == nil || len(result.Environment.Argv) != 1 || len(result.Environment.Env) != 1 || result.EnvironmentHash == "" {
				t.Fatalf("executed merge was not fully recorded: %#v", result)
			}
			invocation := git.invocations[0]
			if !slices.Equal(result.Environment.Argv[0], invocation.argv) {
				t.Fatalf("recorded argv = %#v, executed argv = %#v", result.Environment.Argv[0], invocation.argv)
			}
			if !slices.Equal(result.Environment.Env[0], compositionEnvironmentPairs(invocation.environment)) {
				t.Fatalf("recorded env = %#v, executed env = %#v", result.Environment.Env[0], compositionEnvironmentPairs(invocation.environment))
			}
		})
	}
}

func TestCompositionEnvironmentAndMergeIdentityAreDeterministic(t *testing.T) {
	repository := newCompositionFixture(t)
	base := compositionFixtureTree(t, repository)
	writeFile(t, filepath.Join(repository, "value.txt"), []byte("one\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "one")
	first := compositionFixtureTree(t, repository)
	writeFile(t, filepath.Join(repository, "value.txt"), []byte("two\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "two")
	second := compositionFixtureTree(t, repository)
	contributors := []CompositionContributor{
		{MovementID: "one", ChangeSetID: "one", BaseTree: base, ResultTree: first},
		{MovementID: "two", ChangeSetID: "two", BaseTree: first, ResultTree: second},
	}
	input := CompositionInput{RepositoryRoot: repository, BaseTree: base, Contributors: contributors}
	firstResult := Compose(input)
	secondResult := Compose(input)
	if firstResult.Failure != nil || firstResult.Conflict != nil || secondResult.Failure != nil || secondResult.Conflict != nil {
		t.Fatalf("composition results = %#v, %#v", firstResult, secondResult)
	}
	if firstResult.ResultTree != secondResult.ResultTree || firstResult.EnvironmentHash != secondResult.EnvironmentHash {
		t.Fatalf("same composition changed: %#v then %#v", firstResult, secondResult)
	}
	hash, err := MovementCompositionHash("target", base, contributors, firstResult.EnvironmentHash)
	if err != nil {
		t.Fatal(err)
	}
	reversed := []CompositionContributor{contributors[1], contributors[0]}
	reversedHash, err := MovementCompositionHash("target", base, reversed, firstResult.EnvironmentHash)
	if err != nil {
		t.Fatal(err)
	}
	if hash == reversedHash {
		t.Fatal("movement composition hash does not bind pre-dedup contributor order")
	}

	// This locks the merge variant's A.4 preimage independently of the helper.
	expected, err := canonical.Hash(canonical.DomainMovementComposition, map[string]any{
		"composition_mode": "merge",
		"movement_id":      "target",
		"base_tree":        base,
		"contributors": []any{
			map[string]any{"movement_id": "one", "change_set_id": "one"},
			map[string]any{"movement_id": "two", "change_set_id": "two"},
		},
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
		"composition_environment_hash":  firstResult.EnvironmentHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if hash != expected {
		t.Fatalf("merge composition hash = %q, want A.4 preimage hash %q", hash, expected)
	}
	environmentArgv := make([]any, len(firstResult.Environment.Argv))
	for index, invocation := range firstResult.Environment.Argv {
		environmentArgv[index] = stringsToCompositionAny(invocation)
	}
	environmentExpected, err := canonical.Hash(canonical.DomainCompositionEnvironment, map[string]any{
		"git_version_string": firstResult.Environment.GitVersionString,
		"object_format":      firstResult.Environment.ObjectFormat,
		"argv":               environmentArgv,
		"env":                compositionEnvironmentsAny(firstResult.Environment.Env),
		"merge_renormalize":  firstResult.Environment.MergeRenormalize,
		"merge_config":       compositionPairsAny(firstResult.Environment.MergeConfig),
	})
	if err != nil {
		t.Fatal(err)
	}
	if firstResult.EnvironmentHash != environmentExpected {
		t.Fatalf("composition environment hash = %q, want exact environment preimage hash %q", firstResult.EnvironmentHash, environmentExpected)
	}
}

func TestCompositionEnvironmentRecordsExactlyWhatEachMergeSubprocessReceives(t *testing.T) {
	repository := newCompositionFixture(t)
	base := compositionFixtureTree(t, repository)
	writeFile(t, filepath.Join(repository, "value.txt"), []byte("one\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "one")
	first := compositionFixtureTree(t, repository)
	writeFile(t, filepath.Join(repository, "value.txt"), []byte("two\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "two")
	second := compositionFixtureTree(t, repository)

	recordingDirectory := t.TempDir()
	argvPath := filepath.Join(recordingDirectory, "merge-argv")
	envPath := filepath.Join(recordingDirectory, "merge-env")
	wrapper := compositionRecordingWrapper(t, argvPath, envPath)
	result := compose(systemGit{path: wrapper, env: gitEnvironment()}, CompositionInput{
		RepositoryRoot: repository,
		BaseTree:       base,
		Contributors: []CompositionContributor{
			{MovementID: "one", ChangeSetID: "one", BaseTree: base, ResultTree: first},
			{MovementID: "two", ChangeSetID: "two", BaseTree: first, ResultTree: second},
		},
	})
	if result.Failure != nil || result.Conflict != nil {
		t.Fatalf("compose result = %#v, want clean result", result)
	}
	actualArgv := readCompositionInvocationRecords(t, argvPath)
	actualEnv := readCompositionInvocationRecords(t, envPath)
	if len(actualArgv) != 2 || len(actualEnv) != 2 {
		t.Fatalf("recorded subprocesses = argv:%d env:%d, want two", len(actualArgv), len(actualEnv))
	}
	if !slices.EqualFunc(result.Environment.Argv, actualArgv, func(left, right []string) bool {
		return slices.Equal(left, right)
	}) {
		t.Fatalf("recorded argv = %#v, subprocess argv = %#v", result.Environment.Argv, actualArgv)
	}
	for index, environment := range actualEnv {
		pairs := compositionEnvironmentPairs(environment)
		attrSource := compositionEnvironmentValue(pairs, "GIT_ATTR_SOURCE")
		if attrSource == "" || attrSource != actualArgv[index][len(actualArgv[index])-2] {
			t.Fatalf("merge %d GIT_ATTR_SOURCE = %q, argv ours = %#v", index, attrSource, actualArgv[index])
		}
		if !slices.Equal(result.Environment.Env[index], pairs) {
			t.Fatalf("recorded env = %#v, subprocess env = %#v", result.Environment.Env[index], pairs)
		}
	}
}

func TestMovementCompositionHashRejectsDisjointModeSentinels(t *testing.T) {
	contributor := CompositionContributor{MovementID: "one", ChangeSetID: "one"}
	if _, err := MovementCompositionHash("target", "git-sha1:tree", nil, "sha256:environment"); err == nil {
		t.Fatal("identity composition accepted environment hash")
	}
	if _, err := MovementCompositionHash("target", "git-sha1:tree", []CompositionContributor{contributor}, ""); err == nil {
		t.Fatal("merge composition accepted absent environment hash")
	}
}

type compositionRecordingGit struct {
	delegate compositionGitCommand
	calls    [][]string
}

type compositionMergeInvocation struct {
	argv        []string
	environment []string
}

type compositionMergeOutcomeGit struct {
	delegate    compositionGitCommand
	result      gitResult
	err         error
	invocations []compositionMergeInvocation
}

type compositionTemplateGit struct {
	delegate         compositionGitCommand
	template         string
	injectedTemplate bool
}

func (git *compositionTemplateGit) Run(root string, stdin []byte, args ...string) (gitResult, error) {
	return git.delegate.Run(root, stdin, git.withTemplate(args)...)
}

func (git *compositionTemplateGit) RunWithEnvironment(root string, stdin []byte, environment []string, args ...string) (gitResult, error) {
	return git.delegate.RunWithEnvironment(root, stdin, environment, git.withTemplate(args)...)
}

func (git *compositionTemplateGit) RunComposition(workingDirectory string, stdin []byte, environment []string, args ...string) (gitResult, error) {
	return git.delegate.RunComposition(workingDirectory, stdin, environment, args...)
}

func (git *compositionTemplateGit) withTemplate(args []string) []string {
	if !slices.Contains(args, "init") || slices.ContainsFunc(args, func(value string) bool {
		return strings.HasPrefix(value, "--template=")
	}) {
		return args
	}
	git.injectedTemplate = true
	result := make([]string, 0, len(args)+1)
	for _, argument := range args {
		result = append(result, argument)
		if argument == "init" {
			result = append(result, "--template="+git.template)
		}
	}
	return result
}

func (git *compositionRecordingGit) Run(root string, stdin []byte, args ...string) (gitResult, error) {
	git.calls = append(git.calls, slices.Clone(args))
	return git.delegate.Run(root, stdin, args...)
}

func (git *compositionRecordingGit) RunWithEnvironment(root string, stdin []byte, environment []string, args ...string) (gitResult, error) {
	git.calls = append(git.calls, slices.Clone(args))
	return git.delegate.RunWithEnvironment(root, stdin, environment, args...)
}

func (git *compositionRecordingGit) RunComposition(workingDirectory string, stdin []byte, environment []string, args ...string) (gitResult, error) {
	git.calls = append(git.calls, slices.Clone(args))
	return git.delegate.RunComposition(workingDirectory, stdin, environment, args...)
}

func (git *compositionRecordingGit) mergeTreeCalls() int {
	count := 0
	for _, call := range git.calls {
		if slices.Contains(call, "merge-tree") {
			count++
		}
	}
	return count
}

func (git *compositionMergeOutcomeGit) Run(root string, stdin []byte, args ...string) (gitResult, error) {
	return git.delegate.Run(root, stdin, args...)
}

func (git *compositionMergeOutcomeGit) RunWithEnvironment(root string, stdin []byte, environment []string, args ...string) (gitResult, error) {
	return git.delegate.RunWithEnvironment(root, stdin, environment, args...)
}

func (git *compositionMergeOutcomeGit) RunComposition(workingDirectory string, stdin []byte, environment []string, args ...string) (gitResult, error) {
	git.invocations = append(git.invocations, compositionMergeInvocation{
		argv:        slices.Clone(args),
		environment: slices.Clone(environment),
	})
	return git.result, git.err
}

func newCompositionFixture(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	gitRun(t, repository, "init", "-b", "main")
	gitRun(t, repository, "config", "user.name", "Partitur Test")
	gitRun(t, repository, "config", "user.email", "partitur@example.invalid")
	writeFile(t, filepath.Join(repository, "value.txt"), []byte("base\n"), 0o600)
	gitRun(t, repository, "add", "value.txt")
	gitRun(t, repository, "commit", "-m", "base")
	return repository
}

func compositionFixtureTree(t *testing.T, repository string) string {
	t.Helper()
	return "git-sha1:" + gitText(t, repository, "rev-parse", "HEAD^{tree}")
}

func compositionGitInput(t *testing.T, repository, input string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func mustSystemGit(t *testing.T) compositionGitCommand {
	t.Helper()
	git, err := newSystemGit()
	if err != nil {
		t.Fatal(err)
	}
	return git
}

func compositionRecordingWrapper(t *testing.T, argvPath, envPath string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(argvPath)
	if filepath.Dir(envPath) != directory {
		t.Fatal("recording paths must share a directory")
	}
	if err := os.WriteFile(filepath.Join(directory, "git-path"), []byte(realGit), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "composition-recording-wrapper")
	if err := os.Symlink(os.Args[0], path); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCompositionRecordingWrapper() {
	directory := filepath.Dir(os.Args[0])
	if slices.Contains(os.Args[1:], "merge-tree") {
		recordCompositionInvocation(filepath.Join(directory, "merge-argv"), os.Args[1:])
		recordCompositionInvocation(filepath.Join(directory, "merge-env"), os.Environ())
	}
	realGit, err := os.ReadFile(filepath.Join(directory, "git-path"))
	if err != nil {
		os.Exit(1)
	}
	command := exec.Command(strings.TrimSpace(string(realGit)), os.Args[1:]...)
	command.Env = os.Environ()
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		os.Exit(1)
	}
}

func recordCompositionInvocation(path string, values []string) {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(1)
	}
	defer file.Close()
	_, _ = file.Write([]byte(strings.Join(values, "\x00") + "\x00\x01"))
}

func readCompositionInvocationRecords(t *testing.T, path string) [][]string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	records := bytes.Split(bytes.TrimSuffix(contents, []byte{1}), []byte{1})
	result := make([][]string, len(records))
	for index, record := range records {
		fields := bytes.Split(bytes.TrimSuffix(record, []byte{0}), []byte{0})
		result[index] = make([]string, len(fields))
		for fieldIndex, field := range fields {
			result[index][fieldIndex] = string(field)
		}
	}
	return result
}

func compositionEnvironmentValue(environment []CompositionPair, key string) string {
	for _, pair := range environment {
		if pair.Key == key {
			return pair.Value
		}
	}
	return ""
}

func compositionExitWrapper(t *testing.T, status int) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "git-exit-wrapper")
	script := "#!/bin/sh\nfor argument in \"$@\"; do\n" +
		"  if [ \"$argument\" = merge-tree ]; then exit " + strconv.Itoa(status) + "; fi\n" +
		"done\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
