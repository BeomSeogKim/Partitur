package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// promotionFixture creates a terminal candidate through the same CLI-facing
// fixture used by apply, then optionally records the required apply.completed
// transaction. Every assertion below invokes promote-score through run rather
// than calling its judgment or executor directly.
func promotionFixture(t *testing.T, applied bool) (string, *runstore.Store, []byte) {
	t.Helper()
	root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
	target, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Chdir(root)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"apply", "run-1"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("apply fixture exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
	if err := os.WriteFile(filepath.Join(root, "partitur.yaml"), target, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, store, target
}

// promotionRecoveryFixture advances a running score to revision two before
// recording its terminal candidate. Its root therefore remains the revision
// one bytes which the run originally recorded, while promotion targets the
// distinct revision two snapshot.
func promotionRecoveryFixture(t *testing.T) (string, *runstore.Store, []byte, []byte) {
	t.Helper()
	root, store := resumeFixtureWithInputs(
		t,
		"",
		resumeApprovedScore(1, "promotion recovery input"),
		[]byte("cast: \"0.1\"\nperformers:\n  reviewer:\n    adapter: adapter\n    model: model\nbindings:\n  reviewer:\n    performer: reviewer\n"),
	)
	expected, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	appendResumeApprovedSnapshot(t, store)
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		candidate, err := fixtureSucceededCandidate(root, input.BaseCommit, input.BaseTree)
		if err != nil {
			return err
		}
		candidateID := candidate["candidate_id"].(string)
		event := resumeEvent("run-1", runstate.EventRunSucceeded, map[string]any{
			"candidate": candidate, "waiver": map[string]any{"reason": "fixture"},
			"identity_versions": resumeIdentityVersions(),
		})
		event.ScoreRevision = 2
		if _, err := tx.At("fixture.revision-two.succeeded").Append(event); err != nil {
			return err
		}
		for _, event := range []runstate.Event{
			{RunID: "run-1", ScoreRevision: 2, Type: runstate.EventApplyStarted, Payload: resumePayload(t, map[string]any{
				"txn_id": "fixture-apply", "candidate_id": candidateID, "before_tree": input.BaseTree, "result_tree": input.BaseTree,
				"touched_paths": []any{}, "recovery": map[string]any{}, "identity_versions": resumeIdentityVersions(),
			})},
			{RunID: "run-1", ScoreRevision: 2, Type: runstate.EventApplyCompleted, Payload: resumePayload(t, map[string]any{
				"txn_id": "fixture-apply", "candidate_id": candidateID, "result_tree": input.BaseTree, "identity_versions": resumeIdentityVersions(),
			})},
		} {
			if _, err := tx.At("fixture.revision-two.applied").Append(event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "partitur.yaml"), expected, 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(expected, target) {
		t.Fatal("recovery fixture requires distinct expected and target score bytes")
	}
	return root, store, expected, target
}

func promoteScoreCLI(t *testing.T, root string, recoverOnly bool) (int, string, string) {
	t.Helper()
	t.Chdir(root)
	arguments := []string{"promote-score", "run-1"}
	if recoverOnly {
		arguments = append(arguments, "--recover")
	}
	var stdout, stderr bytes.Buffer
	return run(arguments, &stdout, &stderr), stdout.String(), stderr.String()
}

func TestPromoteScoreRequiresAppliedCandidateAndWritesExactSnapshotBytes(t *testing.T) {
	root, store, target := promotionFixture(t, false)
	before, err := os.ReadFile(filepath.Join(root, "partitur.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := promoteScoreCLI(t, root, false)
	if code != 2 || stdout != "" || !strings.Contains(stderr, "has not completed application") {
		t.Fatalf("unapplied promote exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if after, err := os.ReadFile(filepath.Join(root, "partitur.yaml")); err != nil || !bytes.Equal(after, before) {
		t.Fatalf("unapplied promotion changed root: err=%v after=%q", err, after)
	}

	// Complete the precondition through the CLI, then prove byte identity (not
	// a semantic parse) and the one-start/one-complete journal transaction.
	if err := os.Remove(filepath.Join(root, "partitur.yaml")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var applyOut, applyErr bytes.Buffer
	if code := run([]string{"apply", "run-1"}, &applyOut, &applyErr); code != 0 || applyOut.Len() != 0 || applyErr.Len() != 0 {
		t.Fatalf("apply exit=%d stdout=%q stderr=%q", code, applyOut.String(), applyErr.String())
	}
	if err := os.WriteFile(filepath.Join(root, "partitur.yaml"), target, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = promoteScoreCLI(t, root, false)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("promote exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if rootBytes, err := os.ReadFile(filepath.Join(root, "partitur.yaml")); err != nil || !bytes.Equal(rootBytes, target) {
		t.Fatalf("promoted root is not byte-identical to snapshot: err=%v root=%q target=%q", err, rootBytes, target)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionStarted) != 1 || countEvents(journal.Events, runstate.EventScorePromoted) != 1 {
		t.Fatalf("promotion journal=%v", eventKinds(journal.Events))
	}
}

func TestPromoteScoreRefusesPreStartRootHashConflictAndAlreadyPromotedWithoutChangingRoot(t *testing.T) {
	root, store, _ := promotionFixture(t, true)
	changed := []byte("score: changed by another writer\n")
	if err := os.WriteFile(filepath.Join(root, "partitur.yaml"), changed, 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := promoteScoreCLI(t, root, false)
	if code != 2 || stdout != "" || !strings.Contains(stderr, "does not match expected") {
		t.Fatalf("pre-start root-hash conflict exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if rootBytes, err := os.ReadFile(filepath.Join(root, "partitur.yaml")); err != nil || !bytes.Equal(rootBytes, changed) {
		t.Fatalf("pre-start root-hash conflict changed root: err=%v root=%q", err, rootBytes)
	}

	// Restore the recorded operand, promote once, then show a second normal
	// invocation is an idempotent success without a second transaction.
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "partitur.yaml"), initial, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr = promoteScoreCLI(t, root, false); code != 0 || stderr != "" {
		t.Fatalf("promote after restore exit=%d stderr=%q", code, stderr)
	}
	if code, _, stderr = promoteScoreCLI(t, root, false); code != 0 || stderr != "" {
		t.Fatalf("already promoted exit=%d stderr=%q", code, stderr)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionStarted) != 1 || countEvents(journal.Events, runstate.EventScorePromoted) != 1 || input.Projection.State.Run != runstate.RunSucceeded {
		t.Fatalf("promotion journal=%v run=%q", eventKinds(journal.Events), input.Projection.State.Run)
	}
}

// TestPromoteScoreRefusesPreStartPinnedTargetSnapshotFailures proves that a
// target snapshot is validated before promotion becomes a transaction. A
// directory is a portable unreadable-file fixture: unlike chmod, it does not
// depend on the test user or filesystem permission model.
func TestPromoteScoreRefusesPreStartPinnedTargetSnapshotFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
		want   []string
	}{
		{
			name: "missing",
			mutate: func(t *testing.T, snapshot string) {
				t.Helper()
				if err := os.Remove(snapshot); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"required promotion target snapshot", "is missing"},
		},
		{
			name: "unreadable",
			mutate: func(t *testing.T, snapshot string) {
				t.Helper()
				if err := os.Remove(snapshot); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(snapshot, 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"required promotion target snapshot", "is unreadable"},
		},
		{
			name: "hash mismatched",
			mutate: func(t *testing.T, snapshot string) {
				t.Helper()
				if err := os.WriteFile(snapshot, []byte("mismatched promotion snapshot\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"promotion target hash", "does not match pinned head"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, store, _, _ := promotionRecoveryFixture(t)
			rootPath := filepath.Join(root, "partitur.yaml")
			before, err := os.ReadFile(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-2.yaml"))

			code, stdout, stderr := promoteScoreCLI(t, root, false)
			if code != 2 || stdout != "" {
				t.Fatalf("pre-start %s target exit=%d stdout=%q stderr=%q", test.name, code, stdout, stderr)
			}
			for _, want := range test.want {
				if !strings.Contains(stderr, want) {
					t.Fatalf("pre-start %s target stderr=%q, want %q", test.name, stderr, want)
				}
			}
			if after, err := os.ReadFile(rootPath); err != nil || !bytes.Equal(after, before) {
				t.Fatalf("pre-start %s target changed root: err=%v after=%q", test.name, err, after)
			}
			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if countEvents(journal.Events, runstate.EventScorePromotionStarted) != 0 {
				t.Fatalf("pre-start %s target started promotion: journal=%v", test.name, eventKinds(journal.Events))
			}
		})
	}
}

func TestPromoteScoreRenameTimeRootChangeLeavesUserRootAndHalts(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")
	root, store, expected, _ := promotionRecoveryFixture(t)
	userRoot := append(append([]byte(nil), expected...), []byte("# formatting-only edit\n")...)
	release := pauseAtPoint(t, partitur, root, applyKillEnvironment(t), faultpoint.PointPromotionBeforeRootRename, "promote-score", "run-1")
	if err := os.WriteFile(filepath.Join(root, "partitur.yaml"), userRoot, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := release(); code != 5 {
		t.Fatalf("rename-time conflict exit=%d, want 5", code)
	}
	if rootBytes, err := os.ReadFile(filepath.Join(root, "partitur.yaml")); err != nil || !bytes.Equal(rootBytes, userRoot) {
		t.Fatalf("rename-time conflict overwrote user root: err=%v root=%q", err, rootBytes)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionStarted) != 1 || countEvents(journal.Events, runstate.EventScorePromotionRecoveryRequired) != 1 || countEvents(journal.Events, runstate.EventScorePromoted) != 0 {
		t.Fatalf("rename-time conflict journal=%v", eventKinds(journal.Events))
	}
}

func TestParsePromoteScoreArgs(t *testing.T) {
	for _, test := range []struct {
		args              []string
		wantRun           string
		wantRecover, want bool
	}{
		{args: []string{"promote-score", "run-1"}, wantRun: "run-1", want: true},
		{args: []string{"promote-score", "run-1", "--recover"}, wantRun: "run-1", wantRecover: true, want: true},
		{args: nil}, {args: []string{"promote-score"}}, {args: []string{"promote-score", ""}},
		{args: []string{"promote-score", "--run"}}, {args: []string{"promote-score", "run-1", "--resume"}},
		{args: []string{"apply", "run-1"}}, {args: []string{"promote-score", "run-1", "--recover", "extra"}},
	} {
		runID, recoverOnly, ok := parsePromoteScoreArgs(test.args)
		if runID != test.wantRun || recoverOnly != test.wantRecover || ok != test.want {
			t.Fatalf("parsePromoteScoreArgs(%v) = (%q, %t, %t), want (%q, %t, %t)", test.args, runID, recoverOnly, ok, test.wantRun, test.wantRecover, test.want)
		}
	}
}

func TestPromoteScoreKillCutsRecoverToPromotedFixedPoint(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")
	for _, cut := range []struct {
		name  string
		point faultpoint.PointID
	}{
		{name: "before root rename", point: faultpoint.PointPromotionBeforeRootRename},
		{name: "after root rename", point: faultpoint.PointPromotionRootRenamed},
	} {
		t.Run(cut.name, func(t *testing.T) {
			root, store, expected, target := promotionRecoveryFixture(t)
			environment := applyKillEnvironment(t)
			killAtPoint(t, partitur, partiturRepository(t, root), environment, cut.point, "promote-score", "run-1")
			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if countEvents(journal.Events, runstate.EventScorePromotionStarted) != 1 || countEvents(journal.Events, runstate.EventScorePromoted) != 0 {
				t.Fatalf("crash journal=%v", eventKinds(journal.Events))
			}
			txnID := promotionTransactionIDFromJournal(t, journal.Events)
			rootBefore, err := os.ReadFile(filepath.Join(root, "partitur.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if cut.point == faultpoint.PointPromotionBeforeRootRename && !bytes.Equal(rootBefore, expected) {
				t.Fatalf("expected-hash recovery root=%q, want %q", rootBefore, expected)
			}
			if cut.point == faultpoint.PointPromotionRootRenamed && !bytes.Equal(rootBefore, target) {
				t.Fatalf("target-hash recovery root=%q, want %q", rootBefore, target)
			}
			code, stdout, stderr := runCommandBinary(t, partitur, root, environment, "promote-score", "run-1", "--recover")
			if code != 0 || stdout != "" || stderr != "" {
				t.Fatalf("recover exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if rootBytes, err := os.ReadFile(filepath.Join(root, "partitur.yaml")); err != nil || !bytes.Equal(rootBytes, target) {
				t.Fatalf("recovered root err=%v root=%q target=%q", err, rootBytes, target)
			}
			if leftovers, err := filepath.Glob(filepath.Join(root, ".partitur.yaml.promote-*")); err != nil || len(leftovers) != 0 {
				t.Fatalf("promotion temporaries after recovery = %q, err=%v", leftovers, err)
			}
			journal, err = store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if countEvents(journal.Events, runstate.EventScorePromotionStarted) != 1 || countEvents(journal.Events, runstate.EventScorePromoted) != 1 || promotionTransactionIDFromJournal(t, journal.Events) != txnID || promotionCompletedTransactionIDFromJournal(t, journal.Events) != txnID {
				t.Fatalf("recovery did not complete original transaction: events=%v", eventKinds(journal.Events))
			}
			before := applyReadJournalBytes(t, root)
			code, _, stderr = runCommandBinary(t, partitur, root, environment, "promote-score", "run-1", "--recover")
			if code != 2 || !strings.Contains(stderr, "--recover is refused from PROMOTED") {
				t.Fatalf("second recover exit=%d stderr=%q", code, stderr)
			}
			if after := applyReadJournalBytes(t, root); after != before {
				t.Fatalf("second recover rewrote journal:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

func TestPromoteScoreRecoveryHaltLeavesJournalFixed(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")
	root, store, _, _ := promotionRecoveryFixture(t)
	environment := applyKillEnvironment(t)
	killAtPoint(t, partitur, partiturRepository(t, root), environment, faultpoint.PointPromotionBeforeRootRename, "promote-score", "run-1")
	if err := os.WriteFile(filepath.Join(root, "partitur.yaml"), []byte("third root state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCommandBinary(t, partitur, root, environment, "promote-score", "run-1", "--recover")
	if code != 5 || stdout != "" || !strings.Contains(stderr, "matches neither expected nor target") {
		t.Fatalf("halt exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionRecoveryRequired) != 1 {
		t.Fatalf("halt journal=%v", eventKinds(journal.Events))
	}
	before := applyReadJournalBytes(t, root)
	code, _, stderr = runCommandBinary(t, partitur, root, environment, "promote-score", "run-1", "--recover")
	if code != 5 || !strings.Contains(stderr, "matches neither expected nor target") {
		t.Fatalf("second halt exit=%d stderr=%q", code, stderr)
	}
	if after := applyReadJournalBytes(t, root); after != before {
		t.Fatalf("second halt rewrote journal:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestPromoteScoreRecoverHaltsMissingTargetSnapshot(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")
	root, store, _, _ := promotionRecoveryFixture(t)
	environment := applyKillEnvironment(t)
	killAtPoint(t, partitur, partiturRepository(t, root), environment, faultpoint.PointPromotionBeforeRootRename, "promote-score", "run-1")
	snapshot := filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-2.yaml")
	if err := os.Remove(snapshot); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCommandBinary(t, partitur, root, environment, "promote-score", "run-1", "--recover")
	if code != 5 || stdout != "" || !strings.Contains(stderr, "required promotion target snapshot") || !strings.Contains(stderr, "is missing") || strings.Contains(stderr, "is unreadable") || !strings.Contains(stderr, "revision-2.yaml") {
		t.Fatalf("missing target recovery exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionStarted) != 1 || countEvents(journal.Events, runstate.EventScorePromoted) != 0 || countEvents(journal.Events, runstate.EventScorePromotionRecoveryRequired) != 1 {
		t.Fatalf("missing target recovery journal=%v", eventKinds(journal.Events))
	}
	before := applyReadJournalBytes(t, root)
	code, _, stderr = runCommandBinary(t, partitur, root, environment, "promote-score", "run-1", "--recover")
	if code != 5 || !strings.Contains(stderr, "required promotion target snapshot") || !strings.Contains(stderr, "is missing") {
		t.Fatalf("second missing target recovery exit=%d stderr=%q", code, stderr)
	}
	journal, err = store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionRecoveryRequired) != 1 {
		t.Fatalf("second missing target recovery journal=%v", eventKinds(journal.Events))
	}
	if after := applyReadJournalBytes(t, root); after != before {
		t.Fatalf("second missing target recovery rewrote journal:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestPromoteScoreRecoverHaltsMismatchedTargetSnapshot(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")
	root, store, _, _ := promotionRecoveryFixture(t)
	environment := applyKillEnvironment(t)
	killAtPoint(t, partitur, partiturRepository(t, root), environment, faultpoint.PointPromotionBeforeRootRename, "promote-score", "run-1")
	snapshot := filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-2.yaml")
	if err := os.WriteFile(snapshot, []byte("mismatched promotion snapshot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCommandBinary(t, partitur, root, environment, "promote-score", "run-1", "--recover")
	if code != 5 || stdout != "" || !strings.Contains(stderr, "promotion target hash") || !strings.Contains(stderr, "does not match pinned head") {
		t.Fatalf("mismatched target recovery exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionStarted) != 1 || countEvents(journal.Events, runstate.EventScorePromoted) != 0 || countEvents(journal.Events, runstate.EventScorePromotionRecoveryRequired) != 1 {
		t.Fatalf("mismatched target recovery journal=%v", eventKinds(journal.Events))
	}
	before := applyReadJournalBytes(t, root)
	code, _, stderr = runCommandBinary(t, partitur, root, environment, "promote-score", "run-1", "--recover")
	if code != 5 || !strings.Contains(stderr, "promotion target hash") || !strings.Contains(stderr, "does not match pinned head") {
		t.Fatalf("second mismatched target recovery exit=%d stderr=%q", code, stderr)
	}
	journal, err = store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionRecoveryRequired) != 1 {
		t.Fatalf("second mismatched target recovery journal=%v", eventKinds(journal.Events))
	}
	if after := applyReadJournalBytes(t, root); after != before {
		t.Fatalf("second mismatched target recovery rewrote journal:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestPromoteScoreRecoverRequiresOriginalCandidate(t *testing.T) {
	root, store, expected, target := promotionRecoveryFixture(t)
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]any{}
	if err := json.Unmarshal(input.Projection.State.ApplicationCandidate.IdentityVersions, &versions); err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		payload := resumePayload(t, map[string]any{
			"txn_id": "promotion-wrong-candidate", "candidate_id": "other-candidate", "identity_versions": versions,
			"expected_root_file_hash": resumeHash(expected), "target_snapshot_file_hash": resumeHash(target), "target_revision": 2,
		})
		_, err := tx.At("fixture.wrong-promotion-candidate").Append(runstate.Event{RunID: "run-1", ScoreRevision: 2, Type: runstate.EventScorePromotionStarted, Payload: payload})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := applyReadJournalBytes(t, root)
	code, stdout, stderr := promoteScoreCLI(t, root, true)
	if code != 5 || stdout != "" || !strings.Contains(stderr, "promotion candidate is unavailable") {
		t.Fatalf("wrong candidate recover exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if after := applyReadJournalBytes(t, root); after == before {
		t.Fatal("wrong candidate recovery did not record a durable halt")
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionRecoveryRequired) != 1 {
		t.Fatalf("wrong candidate recovery journal=%v", eventKinds(journal.Events))
	}
	before = applyReadJournalBytes(t, root)
	code, _, stderr = promoteScoreCLI(t, root, true)
	if code != 5 || !strings.Contains(stderr, "promotion candidate is unavailable") {
		t.Fatalf("second wrong candidate recovery exit=%d stderr=%q", code, stderr)
	}
	journal, err = store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionRecoveryRequired) != 1 {
		t.Fatalf("second wrong candidate recovery journal=%v", eventKinds(journal.Events))
	}
	if after := applyReadJournalBytes(t, root); after != before {
		t.Fatalf("second wrong candidate recovery rewrote journal:\nbefore=%s\nafter=%s", before, after)
	}
}

func promotionTransactionIDFromJournal(t *testing.T, events []runstate.Event) string {
	t.Helper()
	for _, event := range events {
		if event.Type != runstate.EventScorePromotionStarted {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if txnID, ok := payload["txn_id"].(string); ok && txnID != "" {
			return txnID
		}
	}
	t.Fatal("promotion started transaction id is absent")
	return ""
}

func promotionCompletedTransactionIDFromJournal(t *testing.T, events []runstate.Event) string {
	t.Helper()
	for _, event := range events {
		if event.Type != runstate.EventScorePromoted {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if txnID, ok := payload["txn_id"].(string); ok && txnID != "" {
			return txnID
		}
	}
	t.Fatal("promotion completed transaction id is absent")
	return ""
}

// TestProductionPromoteScoreIgnoresArmedProbeDescriptors proves that the
// shipping build does not accidentally acquire faultprobe blocking behavior.
// The marker is deliberately non-affirmative: production rejects only "1",
// while a faultprobe build would arm from the live descriptors alone.
func TestProductionPromoteScoreIgnoresArmedProbeDescriptors(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildProductionE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")
	root, store, target := promotionFixture(t, true)
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer notifyRead.Close()
	defer notifyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, partitur, "promote-score", "run-1")
	command.Dir = root
	command.Env = replaceEnvironment(applyKillEnvironment(t), map[string]string{
		"PARTITUR_FAULTPOINT_HARNESS": "0", "PARTITUR_FAULTPOINT_NOTIFY_FD": strconv.Itoa(3), "PARTITUR_FAULTPOINT_RELEASE_FD": strconv.Itoa(4),
	})
	command.ExtraFiles = []*os.File{notifyWrite, releaseRead}
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := notifyWrite.Close(); err != nil {
		t.Fatal(err)
	}
	if err := releaseRead.Close(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		if ctx.Err() != nil {
			t.Fatalf("production promote-score blocked on armed descriptors: %v", ctx.Err())
		}
		t.Fatalf("production promote-score: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("production promote-score stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if rootBytes, err := os.ReadFile(filepath.Join(root, "partitur.yaml")); err != nil || !bytes.Equal(rootBytes, target) {
		t.Fatalf("production promoted root err=%v root=%q target=%q", err, rootBytes, target)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventScorePromotionStarted) != 1 || countEvents(journal.Events, runstate.EventScorePromoted) != 1 {
		t.Fatalf("production journal=%v", eventKinds(journal.Events))
	}
}
