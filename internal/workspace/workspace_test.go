package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/validate"
)

const (
	testRunID     = "018f05c8-1b4a-7abc-8def-0123456789ab"
	testAttemptID = "018f05c8-1b4b-7abc-8def-0123456789ab"
	testAttempt2  = "018f05c8-1b4c-7abc-8def-0123456789ab"
)

func TestStartPublishesInputsBeforeReturningDurableRunID(t *testing.T) {
	repository, preparation := prepareRepository(t)
	git := newRecordingGit(t)
	result := startFixture(t, preparation, git, testRunID)

	if result.RunID != testRunID {
		t.Fatalf("run id = %q, want %q", result.RunID, testRunID)
	}
	if result.Receipt.Address != receiptRunStarted ||
		result.Receipt.Mutation.Kind != faultpoint.JournalAppend ||
		result.Receipt.Mutation.EventType != string(runstate.EventRunStarted) {
		t.Fatalf("receipt = %#v", result.Receipt)
	}
	events := readJournal(t, repository, result.RunID)
	if len(events) != 1 || events[0].Type != runstate.EventRunStarted {
		t.Fatalf("events = %#v", events)
	}
	payload := decodePayload(t, events[0])
	assertQualifiedGitObject(t, payload["base_commit"])
	assertQualifiedGitObject(t, payload["base_tree"])

	scoreSource := preparation.ScoreSource()
	scorePath := filepath.Join(
		repository,
		".partitur",
		"runs",
		testRunID,
		"scores",
		"revision-1.yaml",
	)
	if got := readFile(t, scorePath); !bytes.Equal(got, scoreSource) {
		t.Fatalf("score snapshot = %q, want %q", got, scoreSource)
	}
	if payload["score_file_hash"] != rawHash(scoreSource) {
		t.Fatalf("score_file_hash = %v", payload["score_file_hash"])
	}
	scoreHash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if payload["score_hash"] != scoreHash {
		t.Fatalf("score_hash = %v, want %q", payload["score_hash"], scoreHash)
	}

	castBytes, err := preparation.Cast.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	castPath := filepath.Join(
		repository,
		".partitur",
		"runs",
		testRunID,
		"resolved-cast.yaml",
	)
	if got := readFile(t, castPath); !bytes.Equal(got, castBytes) {
		t.Fatalf("resolved cast = %s, want %s", got, castBytes)
	}
	castHash, err := preparation.Cast.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if payload["resolved_cast_hash"] != castHash {
		t.Fatalf("resolved_cast_hash = %v, want %q", payload["resolved_cast_hash"], castHash)
	}

	baseCommit := gitText(t, repository, "rev-parse", "HEAD^{commit}")
	if got := gitText(t, repository, "show-ref", "--hash", baseRef(result.RunID)); got != baseCommit {
		t.Fatalf("base ref = %q, want %q", got, baseCommit)
	}
	assertDurableRefUpdate(t, git.calls, baseRef(result.RunID))
}

func TestEnsureRefReturnsAddressableDurabilityReceipt(t *testing.T) {
	repository, _ := prepareRepository(t)
	git := newRecordingGit(t)
	const address = faultpoint.ReceiptAddress("test.ref")
	ref := "refs/partitur/tests/durable"
	receipt, err := ensureRef(
		git,
		repository,
		ref,
		gitText(t, repository, "rev-parse", "HEAD"),
		testRunID,
		address,
		refExistingMustMatchObject,
	)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Address != address ||
		receipt.Mutation.Kind != faultpoint.GitRefCreation ||
		receipt.Mutation.RunID != testRunID ||
		receipt.Mutation.Path != ref {
		t.Fatalf("receipt = %#v", receipt)
	}
	assertDurableRefUpdate(t, git.calls, ref)
}

func TestEnsureRefStrictPolicyRejectsExistingDifferentObject(t *testing.T) {
	repository, _ := prepareRepository(t)
	git := newRecordingGit(t)
	ref := "refs/partitur/tests/strict"
	object := gitText(t, repository, "rev-parse", "HEAD")
	if _, err := ensureRef(
		git, repository, ref, object, testRunID,
		faultpoint.ReceiptAddress("test.strict.initial"), refExistingMustMatchObject,
	); err != nil {
		t.Fatal(err)
	}
	_, err := ensureRef(
		git, repository, ref, strings.Repeat("0", len(object)), testRunID,
		faultpoint.ReceiptAddress("test.strict.collision"), refExistingMustMatchObject,
	)
	if !errors.Is(err, ErrRunIDCollision) {
		t.Fatalf("strict ref policy error = %v, want ErrRunIDCollision", err)
	}
}

func TestEnsureRefMovementBasePolicyAcceptsSameTreeWrapperOnly(t *testing.T) {
	repository, _ := prepareRepository(t)
	git := newRecordingGit(t)
	ref := "refs/partitur/tests/movement-base"
	base := gitText(t, repository, "rev-parse", "HEAD")
	tree := gitText(t, repository, "rev-parse", "HEAD^{tree}")
	first := gitText(t, repository, "commit-tree", tree, "-p", base, "-m", "first wrapper")
	second := gitText(t, repository, "commit-tree", tree, "-p", base, "-m", "second wrapper")
	if _, err := ensureRef(git, repository, ref, first, testRunID, "test.movement.initial", refExistingMustMatchTree); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureRef(git, repository, ref, second, testRunID, "test.movement.same-tree", refExistingMustMatchTree); err != nil {
		t.Fatalf("same-tree wrapper rejected: %v", err)
	}
	otherTree := gitText(t, repository, "mktree")
	other := gitText(t, repository, "commit-tree", otherTree, "-p", base, "-m", "other wrapper")
	if _, err := ensureRef(git, repository, ref, other, testRunID, "test.movement.other-tree", refExistingMustMatchTree); !errors.Is(err, ErrRunIDCollision) {
		t.Fatalf("different-tree wrapper error = %v, want ErrRunIDCollision", err)
	}
}

func TestStartRejectsIncompletePreparation(t *testing.T) {
	for _, preparation := range []*validate.Preparation{
		nil,
		{},
	} {
		_, err := start(preparation, startDependencies{
			git:   newRecordingGit(t),
			probe: faultpoint.Nop{},
			newID: idSequence(testRunID),
		})
		if !errors.Is(err, ErrIncompletePreparation) {
			t.Fatalf("error = %v, want ErrIncompletePreparation", err)
		}
	}
}

func TestStartRejectsRepositoryPreconditionFailures(t *testing.T) {
	t.Run("git floor", func(t *testing.T) {
		_, preparation := prepareRepository(t)
		git := newRecordingGit(t)
		git.overrideVersion = "git version 2.46.9\n"
		_, err := startWithID(preparation, git, testRunID)
		if !errors.Is(err, ErrGitTooOld) {
			t.Fatalf("error = %v, want ErrGitTooOld", err)
		}
	})

	t.Run("invocation directory is exact root", func(t *testing.T) {
		repository, preparation := prepareRepository(t)
		child := filepath.Join(repository, "child")
		if err := os.Mkdir(child, 0o700); err != nil {
			t.Fatal(err)
		}
		preparation.RepositoryRoot = child
		_, err := startWithID(preparation, newRecordingGit(t), testRunID)
		if !errors.Is(err, ErrNotRepository) {
			t.Fatalf("error = %v, want ErrNotRepository", err)
		}
	})

	t.Run("bare repository", func(t *testing.T) {
		repository, preparation := prepareRepository(t)
		bare := filepath.Join(t.TempDir(), "bare.git")
		gitRun(t, repository, "clone", "--bare", repository, bare)
		preparation.RepositoryRoot = bare
		_, err := startWithID(preparation, newRecordingGit(t), testRunID)
		if !errors.Is(err, ErrBareRepository) {
			t.Fatalf("error = %v, want ErrBareRepository", err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "tracked source change",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "README.md"), []byte("changed\n"), 0o600)
			},
		},
		{
			name: "non-ignored untracked source",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, "untracked.txt"), []byte("new\n"), 0o600)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, preparation := prepareRepository(t)
			test.mutate(t, repository)
			_, err := startWithID(preparation, newRecordingGit(t), testRunID)
			if !errors.Is(err, ErrDirtySource) {
				t.Fatalf("error = %v, want ErrDirtySource", err)
			}
		})
	}
}

func TestStartRejectsEveryExternalMergeDriverSource(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "repository driver command",
			mutate: func(t *testing.T, root string) {
				gitRun(t, root, "config", "merge.custom.driver", "false")
			},
		},
		{
			name: "repository default driver",
			mutate: func(t *testing.T, root string) {
				gitRun(t, root, "config", "merge.default", "custom")
			},
		},
		{
			name: "in-tree attribute driver",
			mutate: func(t *testing.T, root string) {
				writeFile(t, filepath.Join(root, ".gitattributes"), []byte("README.md merge=custom\n"), 0o600)
				gitRun(t, root, "add", ".gitattributes")
				gitRun(t, root, "commit", "-m", "add attributes")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, preparation := prepareRepository(t)
			test.mutate(t, repository)
			_, err := startWithID(preparation, newRecordingGit(t), testRunID)
			if !errors.Is(err, ErrExternalMergeDriver) {
				t.Fatalf("error = %v, want ErrExternalMergeDriver", err)
			}
		})
	}
}

func TestGitSubprocessEnvironmentIsAllowlisted(t *testing.T) {
	t.Setenv("PARTITUR_TEST_SECRET", "must-not-leak")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "merge.custom.driver")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	environment := gitEnvironment()
	names := make([]string, 0, len(environment))
	for _, entry := range environment {
		names = append(names, strings.SplitN(entry, "=", 2)[0])
	}
	want := []string{
		"GIT_CONFIG_NOSYSTEM",
		"GIT_CONFIG_SYSTEM",
		"GIT_CONFIG_GLOBAL",
		"GIT_TERMINAL_PROMPT",
		"LANG",
		"LC_ALL",
		"PATH",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("environment names = %#v, want %#v", names, want)
	}
}

func TestStartDoesNotInheritGitConfigInjection(t *testing.T) {
	_, preparation := prepareRepository(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.bare")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing.git"))
	if _, err := startWithID(preparation, newRecordingGit(t), testRunID); err != nil {
		t.Fatalf("ambient Git config reached subprocess: %v", err)
	}
}

func TestStartRejectsEachRunNamespaceCollision(t *testing.T) {
	t.Run("authoritative directory", func(t *testing.T) {
		repository, preparation := prepareRepository(t)
		if err := os.MkdirAll(filepath.Join(
			repository, ".partitur", "runs", testRunID,
		), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := startWithID(preparation, newRecordingGit(t), testRunID)
		if !errors.Is(err, ErrRunIDCollision) {
			t.Fatalf("error = %v, want ErrRunIDCollision", err)
		}
	})

	t.Run("owned base ref", func(t *testing.T) {
		repository, preparation := prepareRepository(t)
		gitRun(
			t,
			repository,
			"update-ref",
			baseRef(testRunID),
			gitText(t, repository, "rev-parse", "HEAD"),
		)
		_, err := startWithID(preparation, newRecordingGit(t), testRunID)
		if !errors.Is(err, ErrRunIDCollision) {
			t.Fatalf("error = %v, want ErrRunIDCollision", err)
		}
		if _, statErr := os.Stat(filepath.Join(
			repository, ".partitur", "runs", testRunID,
		)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("run directory exists after ref collision: %v", statErr)
		}
	})
}

func TestStartDoesNotAuthorizeMissingImmutableInput(t *testing.T) {
	repository, preparation := prepareRepository(t)
	runRoot := filepath.Join(repository, ".partitur", "runs", testRunID)
	git := newRecordingGit(t)
	git.afterSecondBaseRefInspection = func() {
		writeFile(
			t,
			filepath.Join(runRoot, "resolved-cast.yaml"),
			[]byte("wrong"),
			0o600,
		)
	}
	_, err := startWithID(preparation, git, testRunID)
	if err == nil {
		t.Fatal("start with conflicting resolved cast succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(runRoot, "journal.jsonl")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("run.started exists after input publication failure: %v", statErr)
	}
}

func TestZeroWriterCandidateUsesIdentityWithoutRunningMerge(t *testing.T) {
	repository, preparation := prepareRepository(t)
	git := newRecordingGit(t)
	started := startFixture(t, preparation, git, testRunID)
	git.calls = nil

	candidate, err := started.Run.RecordZeroWriterCandidate()
	if err != nil {
		t.Fatal(err)
	}
	if candidate.BaseTree != candidate.ResultTree {
		t.Fatalf("candidate trees differ: %#v", candidate)
	}
	for _, call := range git.calls {
		for _, argument := range call {
			if argument == "merge" || argument == "merge-tree" {
				t.Fatalf("identity candidate invoked merge: %#v", git.calls)
			}
		}
	}
	events := readJournal(t, repository, started.RunID)
	event := events[len(events)-1]
	if event.Type != runstate.EventApplicationCandidateRecorded {
		t.Fatalf("last event = %s", event.Type)
	}
	payload := decodePayload(t, event)
	if !reflect.DeepEqual(payload["ordered_change_sets"], []any{}) ||
		!reflect.DeepEqual(payload["contributors"], []any{}) {
		t.Fatalf("candidate lists = %#v", payload)
	}
	composition := map[string]any{
		"composition_mode":              "identity",
		"base_tree":                     candidate.BaseTree,
		"contributors":                  []any{},
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
	}
	wantHash, err := canonical.Hash(canonical.DomainCandidateComposition, composition)
	if err != nil {
		t.Fatal(err)
	}
	if payload["candidate_composition_dependency_hash"] != wantHash ||
		candidate.CompositionDependencyHash != wantHash {
		t.Fatalf("composition hash = %#v, want %q", payload, wantHash)
	}
	if candidate.CompositionEnvironmentHash != "" {
		t.Fatalf("identity candidate environment hash = %q, want absent", candidate.CompositionEnvironmentHash)
	}
	if bytes.Contains(event.Payload, []byte("composition_environment_hash")) ||
		bytes.Contains(event.Payload, []byte(`"composition_mode":"merge"`)) {
		t.Fatalf("identity event fabricated merge facts: %s", event.Payload)
	}
	assertDurableRefUpdate(t, git.calls, candidateRef(started.RunID))
	replay, err := started.Run.store.Replay(
		started.RunID,
		[]runstate.MovementSeed{{
			ID:      "inspect",
			Initial: runstate.MovementPending,
		}},
		"test.repair",
	)
	if err != nil {
		t.Fatal(err)
	}
	if replay.State.ApplicationCandidate == nil ||
		replay.State.ApplicationCandidate.ID != candidate.ID ||
		replay.State.ApplicationCandidate.CompositionDependencyHash !=
			runstate.Hash(wantHash) {
		t.Fatalf("projected candidate = %#v", replay.State.ApplicationCandidate)
	}
}

func TestZeroWriterCandidateRejectsWriterMovement(t *testing.T) {
	_, preparation := prepareRepositoryWithScore(t, writerScore())
	started := startFixture(t, preparation, newRecordingGit(t), testRunID)
	_, err := started.Run.RecordZeroWriterCandidate()
	if !errors.Is(err, ErrWriterMovement) {
		t.Fatalf("error = %v, want ErrWriterMovement", err)
	}
}

func TestCreateAttemptUsesFreshBaseAndSeparatesOutput(t *testing.T) {
	repository, preparation := prepareRepository(t)
	started := startFixture(t, preparation, newRecordingGit(t), testRunID)
	started.Run.newID = idSequence(testAttemptID, testAttempt2)
	movement := preparation.Score.Movements()[0]

	attempt, err := started.Run.CreateAttempt(movement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitText(t, attempt.Worktree, "rev-parse", "HEAD^{commit}"); got != started.Run.baseCommit {
		t.Fatalf("attempt base = %q, want %q", got, started.Run.baseCommit)
	}
	wantOutput := filepath.Join(
		repository,
		".partitur",
		"work",
		testRunID,
		testAttemptID,
		"output",
	)
	if attempt.OutputDir != wantOutput {
		t.Fatalf("output dir = %q, want %q", attempt.OutputDir, wantOutput)
	}
	assertOutside(t, attempt.OutputDir, attempt.Worktree)
	assertOutside(
		t,
		attempt.OutputDir,
		filepath.Join(repository, ".partitur", "runs", testRunID),
	)
	if _, err := os.Stat(attempt.OutputDir); err != nil {
		t.Fatalf("output dir: %v", err)
	}
	second, err := started.Run.CreateAttempt(movement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Worktree == attempt.Worktree || second.OutputDir == attempt.OutputDir {
		t.Fatalf("attempt workspaces alias: first=%#v second=%#v", attempt, second)
	}
	if got := gitText(t, second.Worktree, "rev-parse", "HEAD^{commit}"); got != started.Run.baseCommit {
		t.Fatalf("second attempt base = %q, want %q", got, started.Run.baseCommit)
	}
	started.Run.newID = idSequence(testAttemptID)
	if _, err := started.Run.CreateAttempt(movement.ID); !errors.Is(err, ErrAttemptIDCollision) {
		t.Fatalf("attempt collision error = %v, want ErrAttemptIDCollision", err)
	}
}

func TestCaptureChangeSetStagesUntrackedContentPinsCheckpointAndIsIdempotent(t *testing.T) {
	repository, preparation := prepareRepositoryWithScore(t, writerScore())
	git := newRecordingGit(t)
	started := startFixture(t, preparation, git, testRunID)
	started.Run.newID = idSequence(testAttemptID)
	attempt, err := started.Run.CreateAttempt(preparation.Score.Movements()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(attempt.Worktree, "new.txt"), []byte("new\n"), 0o600)
	if err := os.Symlink("new.txt", filepath.Join(attempt.Worktree, "new-link")); err != nil {
		t.Fatal(err)
	}

	first, err := attempt.CaptureChangeSet()
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseTree == first.ResultTree {
		t.Fatal("capture discarded non-ignored untracked content")
	}
	if got := gitText(t, repository, "show-ref", "--hash", first.Ref); got != first.Commit {
		t.Fatalf("change set ref = %q, want %q", got, first.Commit)
	}
	if got := gitText(t, attempt.Worktree, "ls-tree", recoveryGitObject(first.ResultTree), "--", "new.txt"); !strings.Contains(got, "new.txt") {
		t.Fatalf("captured tree omits untracked file: %q", got)
	}
	if got := gitText(t, attempt.Worktree, "ls-tree", recoveryGitObject(first.ResultTree), "--", "new-link"); !strings.Contains(got, "120000") {
		t.Fatalf("captured tree omits symlink mode: %q", got)
	}
	if got := gitText(t, attempt.Worktree, "show", "-s", "--format=%an|%ae|%ad|%cn|%ce|%cd|%P|%s", "--date=iso-strict", first.Commit); got != "Partitur|partitur@invalid|1970-01-01T00:00:00Z|Partitur|partitur@invalid|1970-01-01T00:00:00Z||partitur: change set" {
		t.Fatalf("checkpoint metadata = %q", got)
	}
	second, err := attempt.CaptureChangeSet()
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Commit != first.Commit {
		t.Fatalf("second capture = %#v, want same id and commit as %#v", second, first)
	}
	assertDurableRefUpdate(t, git.calls, changesetRef(testRunID, testAttemptID))
}

func TestCaptureChangeSetRecapturesChangedWorktreeWithCompareAndSwap(t *testing.T) {
	repository, preparation := prepareRepositoryWithScore(t, writerScore())
	git := newRecordingGit(t)
	started := startFixture(t, preparation, git, testRunID)
	started.Run.newID = idSequence(testAttemptID)
	attempt, err := started.Run.CreateAttempt(preparation.Score.Movements()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(attempt.Worktree, "surviving.txt")
	writeFile(t, path, []byte("before crash\n"), 0o600)
	first, err := attempt.CaptureChangeSet()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, []byte("after crash\n"), 0o600)
	second, err := attempt.CaptureChangeSet()
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.Commit == first.Commit || second.ResultTree == first.ResultTree {
		t.Fatalf("changed worktree recapture = %#v, want a new checkpoint after %#v", second, first)
	}
	if got := gitText(t, repository, "show-ref", "--hash", second.Ref); got != second.Commit {
		t.Fatalf("change set ref = %q, want later commit %q", got, second.Commit)
	}
	if got := gitText(t, attempt.Worktree, "show", second.Commit+":surviving.txt"); got != "after crash" {
		t.Fatalf("later checkpoint content = %q, want surviving worktree content", got)
	}
	assertDurableRefCompareAndSwap(t, git.calls, second.Ref, second.Commit, first.Commit)
}

func TestCaptureChangeSetRecordsNoOpAndExcludesProtectedPartiturContent(t *testing.T) {
	t.Run("no-op", func(t *testing.T) {
		_, preparation := prepareRepositoryWithScore(t, writerScore())
		started := startFixture(t, preparation, newRecordingGit(t), testRunID)
		started.Run.newID = idSequence(testAttemptID)
		attempt, err := started.Run.CreateAttempt(preparation.Score.Movements()[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		changeSet, err := attempt.CaptureChangeSet()
		if err != nil {
			t.Fatal(err)
		}
		if changeSet.BaseTree != changeSet.ResultTree {
			t.Fatalf("no-op trees = %q and %q, want equal", changeSet.BaseTree, changeSet.ResultTree)
		}
	})

	t.Run("partitur is excluded from the staged tree", func(t *testing.T) {
		_, preparation := prepareRepositoryWithScore(t, writerScore())
		started := startFixture(t, preparation, newRecordingGit(t), testRunID)
		started.Run.newID = idSequence(testAttemptID)
		attempt, err := started.Run.CreateAttempt(preparation.Score.Movements()[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(attempt.Worktree, ".partitur", "runtime.txt"), []byte("run data\n"), 0o600)
		gitRun(t, attempt.Worktree, "add", ".partitur/runtime.txt")
		changeSet, err := attempt.CaptureChangeSet()
		if err != nil {
			t.Fatal(err)
		}
		if changeSet.BaseTree != changeSet.ResultTree {
			t.Fatalf("capture included protected .partitur content: %q and %q", changeSet.BaseTree, changeSet.ResultTree)
		}
	})

}

func TestCaptureChangeSetExcludesProtectedRootScore(t *testing.T) {
	_, preparation := prepareRepositoryWithScore(t, writerScore())
	started := startFixture(t, preparation, newRecordingGit(t), testRunID)
	started.Run.newID = idSequence(testAttemptID)
	attempt, err := started.Run.CreateAttempt(preparation.Score.Movements()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(attempt.Worktree, "partitur.yaml"), []byte("changed\n"), 0o600)
	changeSet, err := attempt.CaptureChangeSet()
	if err != nil {
		t.Fatal(err)
	}
	if changeSet.BaseTree != changeSet.ResultTree {
		t.Fatalf("capture included protected root score: %q and %q", changeSet.BaseTree, changeSet.ResultTree)
	}
}

func TestProtectedPathsPresentFindsExistingProtectedWorktreePaths(t *testing.T) {
	worktree := t.TempDir()
	writeFile(t, filepath.Join(worktree, "partitur.yaml"), []byte("score\n"), 0o600)
	if err := os.Mkdir(filepath.Join(worktree, ".partitur"), 0o700); err != nil {
		t.Fatal(err)
	}

	paths, err := protectedPathsPresent(worktree)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"partitur.yaml", ".partitur"}
	if !slices.Equal(paths, want) {
		t.Fatalf("protected paths = %v, want %v", paths, want)
	}
}

func TestCaptureAcceptanceSubjectIncludesIgnoredProtectedContentAndMatchesWorktree(t *testing.T) {
	repository, preparation := prepareRepositoryWithScore(t, writerScore())
	started := startFixture(t, preparation, newRecordingGit(t), testRunID)
	started.Run.newID = idSequence(testAttemptID)
	attempt, err := started.Run.CreateAttempt(preparation.Score.Movements()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(attempt.Worktree, "subject.txt"), []byte("subject\n"), 0o600)
	writeFile(t, filepath.Join(attempt.Worktree, ".partitur", "runs", "x"), []byte("ignored but protected\n"), 0o600)

	subject, err := attempt.CaptureAcceptanceSubject()
	if err != nil {
		t.Fatal(err)
	}
	if got := gitText(t, repository, "rev-parse", subject.Ref+"^{tree}"); got != recoveryGitObject(subject.Tree) {
		t.Fatalf("subject ref tree = %q, want %q", got, subject.Tree)
	}
	if got := gitText(t, attempt.Worktree, "show", recoveryGitObject(subject.Tree)+":.partitur/runs/x"); got != "ignored but protected" {
		t.Fatalf("subject tree omitted ignored protected file: %q", got)
	}
	if got := gitText(t, attempt.Worktree, "show", recoveryGitObject(subject.Tree)+":subject.txt"); got != "subject" {
		t.Fatalf("subject tree omitted non-ignored untracked file: %q", got)
	}
	matched, err := VerifyRecoverySubject(repository, attempt.Worktree, subject.Tree)
	if err != nil || !matched {
		t.Fatalf("VerifyRecoverySubject(recorded subject) = (%v, %v), want (true, nil)", matched, err)
	}
}

func TestVerifyRecoverySubjectChecksCompleteInvariant(t *testing.T) {
	t.Run("matching linked worktree", func(t *testing.T) {
		repository, worktree, subjectTree := recoverySubjectFixture(t)
		matched, err := VerifyRecoverySubject(repository, worktree, subjectTree)
		if err != nil || !matched {
			t.Fatalf("VerifyRecoverySubject() = (%v, %v), want (true, nil)", matched, err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "tracked content",
			mutate: func(t *testing.T, worktree string) {
				writeFile(t, filepath.Join(worktree, "README.md"), []byte("changed\n"), 0o600)
			},
		},
		{
			name: "non-ignored untracked file",
			mutate: func(t *testing.T, worktree string) {
				writeFile(t, filepath.Join(worktree, "new.txt"), []byte("new\n"), 0o600)
			},
		},
		{
			name: "file mode",
			mutate: func(t *testing.T, worktree string) {
				if err := os.Chmod(filepath.Join(worktree, "script.sh"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink target",
			mutate: func(t *testing.T, worktree string) {
				path := filepath.Join(worktree, "link")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("script.sh", path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "protected path",
			mutate: func(t *testing.T, worktree string) {
				writeFile(t, filepath.Join(worktree, "partitur.yaml"), []byte("altered\n"), 0o600)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, worktree, subjectTree := recoverySubjectFixture(t)
			test.mutate(t, worktree)
			matched, err := VerifyRecoverySubject(repository, worktree, subjectTree)
			if err != nil || matched {
				t.Fatalf("VerifyRecoverySubject() = (%v, %v), want (false, nil)", matched, err)
			}
		})
	}

	t.Run("replaced gitdir indirection is unverified", func(t *testing.T) {
		repository, worktree, subjectTree := recoverySubjectFixture(t)
		alternate := filepath.Join(t.TempDir(), "alternate")
		gitRun(t, worktree, "worktree", "add", "--detach", alternate, "HEAD")
		contents := readFile(t, filepath.Join(alternate, ".git"))
		writeFile(t, filepath.Join(worktree, ".git"), contents, 0o600)
		matched, err := VerifyRecoverySubject(repository, worktree, subjectTree)
		if err == nil || matched {
			t.Fatalf("VerifyRecoverySubject() = (%v, %v), want unverified", matched, err)
		}
	})

	t.Run("foreign self-consistent gitdir is unverified", func(t *testing.T) {
		repository, worktree, subjectTree := recoverySubjectFixture(t)
		foreign := t.TempDir()
		gitRun(t, foreign, "init", "-b", "main")
		gitRun(t, foreign, "config", "user.name", "Partitur Test")
		gitRun(t, foreign, "config", "user.email", "partitur@example.invalid")
		writeFile(t, filepath.Join(foreign, "README.md"), []byte("base\n"), 0o600)
		writeFile(t, filepath.Join(foreign, "script.sh"), []byte("#!/bin/sh\n"), 0o600)
		if err := os.Symlink("README.md", filepath.Join(foreign, "link")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(foreign, "partitur.yaml"), readFile(t, filepath.Join(repository, "partitur.yaml")), 0o600)
		writeFile(t, filepath.Join(foreign, ".partitur", "cast.yaml"), readFile(t, filepath.Join(repository, ".partitur", "cast.yaml")), 0o600)
		writeFile(t, filepath.Join(foreign, ".gitignore"), readFile(t, filepath.Join(repository, ".gitignore")), 0o600)
		gitRun(t, foreign, "add", ".")
		gitRun(t, foreign, "commit", "-m", "foreign fixture")
		if err := os.RemoveAll(worktree); err != nil {
			t.Fatal(err)
		}
		gitRun(t, foreign, "worktree", "add", "--detach", worktree, "HEAD")

		matched, err := VerifyRecoverySubject(repository, worktree, subjectTree)
		if err == nil || matched {
			t.Fatalf("VerifyRecoverySubject() = (%v, %v), want unverified foreign repository", matched, err)
		}
	})
}

func recoverySubjectFixture(t *testing.T) (string, string, string) {
	t.Helper()
	_, preparation := prepareRepository(t)
	started := startFixture(t, preparation, newRecordingGit(t), testRunID)
	started.Run.newID = idSequence(testAttemptID)
	attempt, err := started.Run.CreateAttempt(preparation.Score.Movements()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return preparation.RepositoryRoot, attempt.Worktree, gitText(t, attempt.Worktree, "rev-parse", "HEAD^{tree}")
}

func TestCreateAttemptRejectsMovementOutsidePinnedScore(t *testing.T) {
	_, preparation := prepareRepository(t)
	started := startFixture(t, preparation, newRecordingGit(t), testRunID)
	started.Run.newID = idSequence(testAttemptID)
	if _, err := started.Run.CreateAttempt("invented"); !errors.Is(err, ErrMovementNotFound) {
		t.Fatalf("error = %v, want ErrMovementNotFound", err)
	}
}

func TestReadOnlyVerificationRejectsEachInvariantClause(t *testing.T) {
	tests := []struct {
		name       string
		wantReason string
		wantPath   string
		mutate     func(*testing.T, *AttemptWorkspace)
	}{
		{
			name:       "tracked content",
			wantReason: "read_only_violation",
			wantPath:   "README.md",
			mutate: func(t *testing.T, attempt *AttemptWorkspace) {
				writeFile(t, filepath.Join(attempt.Worktree, "README.md"), []byte("changed\n"), 0o600)
			},
		},
		{
			name:       "non-ignored untracked file",
			wantReason: "read_only_violation",
			wantPath:   "new.txt",
			mutate: func(t *testing.T, attempt *AttemptWorkspace) {
				writeFile(t, filepath.Join(attempt.Worktree, "new.txt"), []byte("new\n"), 0o600)
			},
		},
		{
			name:       "symlink target",
			wantReason: "read_only_violation",
			wantPath:   "link",
			mutate: func(t *testing.T, attempt *AttemptWorkspace) {
				path := filepath.Join(attempt.Worktree, "link")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other-target", path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "file mode",
			wantReason: "read_only_violation",
			wantPath:   "script.sh",
			mutate: func(t *testing.T, attempt *AttemptWorkspace) {
				if err := os.Chmod(filepath.Join(attempt.Worktree, "script.sh"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "protected path",
			wantReason: "protected_path_violation",
			wantPath:   "partitur.yaml",
			mutate: func(t *testing.T, attempt *AttemptWorkspace) {
				writeFile(t, filepath.Join(attempt.Worktree, "partitur.yaml"), []byte("corrupt\n"), 0o600)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, attempt := newAttemptFixture(t)
			test.mutate(t, attempt)
			_, err := attempt.VerifyReadOnlyAndRecord()
			var verification *VerificationError
			if !errors.As(err, &verification) {
				t.Fatalf("error = %v, want VerificationError", err)
			}
			if verification.Reason != test.wantReason ||
				!slices.Contains(verification.Paths, test.wantPath) {
				t.Fatalf("verification = %#v, want reason=%q path=%q", verification, test.wantReason, test.wantPath)
			}
		})
	}
}

func TestVerifyProtectedPathsRejectsChangedGitDirIndirection(t *testing.T) {
	_, _, attempt := newAttemptFixture(t)
	writeFile(t, filepath.Join(attempt.Worktree, ".git"), []byte("gitdir: /tmp/foreign\n"), 0o600)

	err := attempt.VerifyProtectedPaths()
	var verification *VerificationError
	if !errors.As(err, &verification) {
		t.Fatalf("error = %v, want VerificationError", err)
	}
	if verification.Reason != "protected_path_violation" || !slices.Equal(verification.Paths, []string{".git"}) {
		t.Fatalf("verification = %#v, want protected .git violation", verification)
	}
}

func TestReadOnlyVerificationIgnoresIgnoredFilesAndRecordsEmptyPayload(t *testing.T) {
	repository, started, attempt := newAttemptFixture(t)
	writeFile(t, filepath.Join(attempt.Worktree, "ignored.tmp"), []byte("cache\n"), 0o600)
	appendAttemptVerifyingHistory(t, started.Run, attempt)

	receipt, err := attempt.VerifyReadOnlyAndRecord()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Mutation.EventType != string(runstate.EventVerificationPassed) {
		t.Fatalf("receipt = %#v", receipt)
	}
	events := readJournal(t, repository, started.RunID)
	event := events[len(events)-1]
	if event.Type != runstate.EventVerificationPassed || string(event.Payload) != "{}" {
		t.Fatalf("verification event = type=%s payload=%s", event.Type, event.Payload)
	}
	replay, err := started.Run.store.Replay(
		started.RunID,
		[]runstate.MovementSeed{{
			ID:      attempt.MovementID,
			Initial: runstate.MovementPending,
		}},
		"test.repair",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.State.VerifiedAttempts[attempt.AttemptID] {
		t.Fatal("verification.passed did not project")
	}
}

func TestReadOnlyVerificationRejectsWriterAttempt(t *testing.T) {
	_, preparation := prepareRepositoryWithScore(t, writerScore())
	started := startFixture(t, preparation, newRecordingGit(t), testRunID)
	started.Run.newID = idSequence(testAttemptID)
	attempt, err := started.Run.CreateAttempt(preparation.Score.Movements()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.VerifyReadOnlyAndRecord(); !errors.Is(err, ErrReadOnlyRequired) {
		t.Fatalf("error = %v, want ErrReadOnlyRequired", err)
	}
}

func TestUUIDv7UsesTimestampVersionVariantAndRandomness(t *testing.T) {
	now := time.UnixMilli(0x0123456789ab)
	id, err := uuidv7(now, bytes.NewReader(bytes.Repeat([]byte{0xff}, 10)))
	if err != nil {
		t.Fatal(err)
	}
	if id != "01234567-89ab-7fff-bfff-ffffffffffff" {
		t.Fatalf("uuid = %q", id)
	}
	if _, err := uuidv7(now, io.LimitReader(bytes.NewReader(nil), 0)); err == nil {
		t.Fatal("short randomness succeeded")
	}
	if _, err := uuidv7(time.UnixMilli(-1), bytes.NewReader(make([]byte, 10))); err == nil {
		t.Fatal("negative timestamp succeeded")
	}
}

func TestGitVersionParserHonours247Floor(t *testing.T) {
	tests := []struct {
		input string
		old   bool
	}{
		{"git version 2.46.9", true},
		{"git version 2.47.0", false},
		{"git version 2.50.1 (Apple Git-155)", false},
	}
	for _, test := range tests {
		version, err := parseGitVersion(test.input)
		if err != nil {
			t.Fatal(err)
		}
		if got := version.lessThan(2, 47); got != test.old {
			t.Fatalf("%q old = %t, want %t", test.input, got, test.old)
		}
	}
	if _, err := parseGitVersion("git 2.47"); err == nil {
		t.Fatal("malformed version succeeded")
	}
}

type recordingGit struct {
	delegate                     gitCommand
	calls                        [][]string
	overrideVersion              string
	baseRefInspections           int
	afterSecondBaseRefInspection func()
}

func newRecordingGit(t *testing.T) *recordingGit {
	t.Helper()
	delegate, err := newSystemGit()
	if err != nil {
		t.Fatal(err)
	}
	return &recordingGit{delegate: delegate}
}

func (git *recordingGit) Run(root string, stdin []byte, args ...string) (gitResult, error) {
	git.calls = append(git.calls, slices.Clone(args))
	if len(args) == 1 && args[0] == "--version" && git.overrideVersion != "" {
		return gitResult{stdout: []byte(git.overrideVersion)}, nil
	}
	result, err := git.delegate.Run(root, stdin, args...)
	if len(args) == 4 && args[0] == "show-ref" &&
		args[1] == "--verify" && args[2] == "--quiet" &&
		strings.HasSuffix(args[3], "/base") {
		git.baseRefInspections++
		if git.baseRefInspections == 2 &&
			git.afterSecondBaseRefInspection != nil {
			git.afterSecondBaseRefInspection()
		}
	}
	return result, err
}

func (git *recordingGit) RunWithEnvironment(root string, stdin []byte, environment []string, args ...string) (gitResult, error) {
	git.calls = append(git.calls, slices.Clone(args))
	return git.delegate.RunWithEnvironment(root, stdin, environment, args...)
}

func prepareRepository(t *testing.T) (string, *validate.Preparation) {
	t.Helper()
	return prepareRepositoryWithScore(t, readOnlyScore())
}

func prepareRepositoryWithScore(
	t *testing.T,
	scoreDocument map[string]any,
) (string, *validate.Preparation) {
	t.Helper()
	repository := t.TempDir()
	gitRun(t, repository, "init", "-b", "main")
	gitRun(t, repository, "config", "user.name", "Partitur Test")
	gitRun(t, repository, "config", "user.email", "partitur@example.invalid")
	gitRun(t, repository, "config", "core.fileMode", "false")

	writeJSON(t, filepath.Join(repository, "partitur.yaml"), scoreDocument)
	writeJSON(t, filepath.Join(repository, ".partitur", "cast.yaml"), castFixture())
	writeFile(t, filepath.Join(repository, ".gitignore"), []byte(
		".partitur/runs/\n.partitur/work/\nignored.tmp\n",
	), 0o600)
	writeFile(t, filepath.Join(repository, "README.md"), []byte("base\n"), 0o600)
	writeFile(t, filepath.Join(repository, "script.sh"), []byte("#!/bin/sh\n"), 0o600)
	if err := os.Symlink("README.md", filepath.Join(repository, "link")); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repository, "add", ".")
	gitRun(t, repository, "commit", "-m", "fixture")

	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repository); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	t.Setenv("HOME", t.TempDir())
	preparation, result := validate.Prepare()
	if result.Refusal != nil || result.HasDiagnostics() || preparation == nil {
		t.Fatalf("prepare result = %#v, preparation=%#v", result, preparation)
	}
	return preparation.RepositoryRoot, preparation
}

func readOnlyScore() map[string]any {
	return map[string]any{
		"score":    "0.2",
		"name":     "workspace-fixture",
		"revision": float64(1),
		"status":   "finalized",
		"goal":     "Produce one report.",
		"verification": map[string]any{
			"expectation": map[string]any{
				"intent": "pass-existing-tests",
				"apply_gate": map[string]any{
					"require": []any{"verified"},
				},
			},
			"final_movement": "inspect",
		},
		"parts": map[string]any{
			"reader": map[string]any{
				"capabilities": []any{"repo_read"},
				"read_only":    true,
			},
		},
		"movements": []any{
			map[string]any{
				"id":          "inspect",
				"part":        "reader",
				"grants":      []any{"repo_read"},
				"instruction": "Write the report.",
				"outputs": []any{
					map[string]any{"id": "report", "kind": "artifact"},
				},
				"acceptance": map[string]any{
					"hard": []any{
						map[string]any{"id": "report-present", "artifact": "report"},
					},
				},
			},
		},
		"policy": map[string]any{
			"allowed_paths": []any{"**"},
			"budget": map[string]any{
				"active_wall_clock_min": float64(10),
			},
		},
	}
}

func writerScore() map[string]any {
	score := readOnlyScore()
	score["verification"].(map[string]any)["expectation"].(map[string]any)["apply_gate"] =
		map[string]any{"waived": true, "reason": "writer workspace fixture"}
	delete(score["verification"].(map[string]any), "final_movement")
	part := score["parts"].(map[string]any)["reader"].(map[string]any)
	part["capabilities"] = []any{"repo_read", "repo_write"}
	delete(part, "read_only")
	movement := score["movements"].([]any)[0].(map[string]any)
	movement["grants"] = []any{"repo_read", "repo_write"}
	movement["outputs"] = []any{
		map[string]any{"id": "change-set", "kind": "change_set"},
	}
	movement["acceptance"] = map[string]any{
		"hard": []any{
			map[string]any{"id": "tests", "run": []any{"true"}},
		},
	}
	return score
}

func castFixture() map[string]any {
	return map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"worker": map[string]any{
				"adapter": "codex",
				"model":   "fixture",
			},
		},
		"bindings": map[string]any{
			"reader": map[string]any{"performer": "worker"},
		},
	}
}

func startFixture(
	t *testing.T,
	preparation *validate.Preparation,
	git gitCommand,
	id string,
) StartResult {
	t.Helper()
	result, err := startWithID(preparation, git, id)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func startWithID(
	preparation *validate.Preparation,
	git gitCommand,
	id string,
) (StartResult, error) {
	return start(preparation, startDependencies{
		git:   git,
		probe: faultpoint.Nop{},
		newID: idSequence(id),
	})
}

func newAttemptFixture(t *testing.T) (string, StartResult, *AttemptWorkspace) {
	t.Helper()
	repository, preparation := prepareRepository(t)
	started := startFixture(t, preparation, newRecordingGit(t), testRunID)
	started.Run.newID = idSequence(testAttemptID)
	attempt, err := started.Run.CreateAttempt(preparation.Score.Movements()[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return repository, started, attempt
}

func idSequence(ids ...string) func() (string, error) {
	index := 0
	return func() (string, error) {
		if index == len(ids) {
			return "", io.EOF
		}
		id := ids[index]
		index++
		return id, nil
	}
}

func appendAttemptVerifyingHistory(t *testing.T, run *Run, attempt *AttemptWorkspace) {
	t.Helper()
	events := []runstate.Event{
		attemptEvent(run, attempt, runstate.EventMovementReady, map[string]any{}),
		attemptEvent(run, attempt, runstate.EventMovementStarted, map[string]any{}),
		attemptEvent(run, attempt, runstate.EventPerformerSelected, map[string]any{
			"reason": "initial", "performer_id": "worker", "adapter_id": "codex", "model": "fixture",
		}),
		attemptEvent(run, attempt, runstate.EventAttemptStarted, map[string]any{
			"attempt_number": 1,
			"adapter_process": map[string]any{
				"pid": 10, "session_id": 10,
				"start_identity": map[string]any{
					"platform": "linux", "boot_id": "boot", "start_ticks": "12",
				},
			},
			"granted_authority": map[string]any{
				"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false,
			},
			"identity_versions": testIdentityVersions(),
		}),
		attemptEvent(run, attempt, runstate.EventAdapterProbed, map[string]any{
			"adapter_version": "1",
			"capabilities": map[string]any{
				"repo_read": true, "repo_write": false, "shell": false,
				"network": false, "resumable_sessions": false,
			},
			"enforcement": map[string]any{
				"path_grants": true, "read_only": true, "network_grants": false,
				"shell_grants": false, "read_grants": true,
			},
			"negotiated_features":       []any{},
			"truncated_resolutions":     []any{},
			"delivered_resolutions":     []any{},
			"delivered_feedback":        []any{},
			"advisory_dimensions":       []any{},
			"execution_dependency_hash": "sha256:dependency",
			"identity_versions":         testIdentityVersions(),
		}),
		attemptEvent(run, attempt, runstate.EventPerformerCompleted, map[string]any{
			"session_hint_stored": false,
		}),
	}
	for index, event := range events {
		err := run.store.Mutate(run.id, "", func(transaction *runstore.Txn) error {
			_, err := transaction.At(faultpoint.ReceiptAddress(
				"test.history." + string(event.Type),
			)).Append(event)
			return err
		})
		if err != nil {
			t.Fatalf("append prerequisite %d %s: %v", index, event.Type, err)
		}
	}
}

func attemptEvent(
	run *Run,
	attempt *AttemptWorkspace,
	eventType runstate.EventType,
	payload any,
) runstate.Event {
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	event := runstate.Event{
		RunID:         run.id,
		ScoreRevision: run.scoreRevision,
		Type:          eventType,
		Payload:       encoded,
	}
	if eventType != runstate.EventMovementReady &&
		eventType != runstate.EventMovementStarted {
		event.AttemptID = attempt.AttemptID
	}
	event.MovementID = attempt.MovementID
	event.PartID = attempt.PartID
	return event
}

func testIdentityVersions() map[string]any {
	return map[string]any{
		"canonical_encoding": float64(canonical.CanonicalEncodingVersion),
		"projections":        map[string]any{},
	}
}

func readJournal(t *testing.T, root string, runID runstate.RunID) []runstate.Event {
	t.Helper()
	contents := readFile(t, filepath.Join(
		root,
		".partitur",
		"runs",
		string(runID),
		"journal.jsonl",
	))
	lines := bytes.Split(bytes.TrimSpace(contents), []byte{'\n'})
	events := make([]runstate.Event, len(lines))
	for index, line := range lines {
		if err := json.Unmarshal(line, &events[index]); err != nil {
			t.Fatalf("decode journal line %d: %v", index, err)
		}
	}
	return events
}

func decodePayload(t *testing.T, event runstate.Event) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(event.Payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertQualifiedGitObject(t *testing.T, value any) {
	t.Helper()
	text, ok := value.(string)
	if !ok || (!strings.HasPrefix(text, "git-sha1:") &&
		!strings.HasPrefix(text, "git-sha256:")) {
		t.Fatalf("Git object = %#v", value)
	}
}

func assertOutside(t *testing.T, path, parent string) {
	t.Helper()
	relative, err := filepath.Rel(parent, path)
	if err != nil {
		t.Fatal(err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		t.Fatalf("%q is inside %q", path, parent)
	}
}

func assertDurableRefUpdate(t *testing.T, calls [][]string, ref string) {
	t.Helper()
	for _, call := range calls {
		if len(call) == 8 && slices.Equal(call[:6], []string{
			"-c", "core.fsync=reference",
			"-c", "core.fsyncMethod=fsync",
			"update-ref", ref,
		}) {
			return
		}
	}
	t.Fatalf("no durable update-ref for %q in %#v", ref, calls)
}

func assertDurableRefCompareAndSwap(t *testing.T, calls [][]string, ref, object, expected string) {
	t.Helper()
	for _, call := range calls {
		if len(call) == 8 && slices.Equal(call[:6], []string{
			"-c", "core.fsync=reference",
			"-c", "core.fsyncMethod=fsync",
			"update-ref", ref,
		}) && call[6] == object && call[7] == expected {
			return
		}
	}
	t.Fatalf("no durable compare-and-swap update-ref for %q from %q to %q in %#v", ref, expected, object, calls)
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, contents, 0o600)
}

func writeFile(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func gitRun(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func gitText(t *testing.T, root string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(string(gitRun(t, root, args...)))
}
