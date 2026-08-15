//go:build faultprobe

package main

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryobs"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

// TestCrossEdgeSemanticRecovery supplies the missing two forms of HARNESS
// check 3 and HARNESS check 4. The prefix stops before criterion.started, so
// each production resume must discard the copied unjournaled launch and spawn
// a fresh criterion process. That makes the ProcessIdentity alpha class a
// required part of this comparison rather than unused normalizer machinery.
func TestCrossEdgeSemanticRecovery(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, check := range []struct{ name string }{
		// HARNESS check 3's second form: one clone recovers once and the
		// other recovers twice from one cloned pre-recovery prefix.
		{name: "idempotence_first_recovery_from_cloned_prefix"},
		{name: "deterministic_independent_recoveries"},
	} {
		check := check
		t.Run(check.name, func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Fatal("semantic criterion-recovery fixture requires non-root permission enforcement")
			}
			repository, environment := criterionErrorKillHarnessRepository(t, bin, vendor)
			runID := killAtPoint(t, partitur, repository, environment, faultpoint.PointLaunchCriterionIdentityPublished)
			assertCrashedStateBeforeResume(t, repository, runID, faultpoint.PointLaunchCriterionIdentityPublished)
			writeCloneExecutableFixture(t, repository)

			original := recoveryClassification(t, repository, runID)
			first := cloneRecoveryPrefix(t, repository, runID, "first", recoveryPrefixCopyExpectations(runID, false))
			second := cloneRecoveryPrefix(t, repository, runID, "second", recoveryPrefixCopyExpectations(runID, false))
			assertCloneClassification(t, original, first, runID)

			firstRecovery := recoverSemanticClone(t, partitur, first, environment, runID)
			assertSemanticCloneFixedPoint(t, partitur, first, environment, runID, firstRecovery.journal)

			assertCloneClassification(t, original, second, runID)
			secondRecovery := recoverSemanticClone(t, partitur, second, environment, runID)
			// This is the second resume in check 3's cloned-prefix form. The
			// fixed-point oracle remains distinct from the comparison below.
			assertSemanticCloneFixedPoint(t, partitur, second, environment, runID, secondRecovery.journal)

			if err := compareSemanticRecovery(firstRecovery.snapshot, secondRecovery.snapshot); err != nil {
				t.Fatalf("%s semantic comparison: %v", check.name, err)
			}
		})
	}
}

func TestCrossEdgeSemanticRecoveryCopiesPendingPreparePrefix(t *testing.T) {
	root := repositoryRoot(t)
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	repository, _, runID, driver, approver := preparedHumanApproval(t, partitur, bin, vendor)
	defer driver.stop(t)
	defer approver.stop(t)
	assertPendingPrepare(t, repository, runID)
	writeCloneExecutableFixture(t, repository)
	destination := filepath.Join(t.TempDir(), "pending-prepare")
	if err := copyRecoveryTree(repository, destination); err != nil {
		t.Fatalf("copy pending-prepare recovery prefix: %v", err)
	}
	assertRecoveryPrefixCopied(t, repository, destination, string(runID), recoveryPrefixCopyExpectations(string(runID), true))
}

type recoveredSemanticClone struct {
	journal  []byte
	snapshot semanticRecoverySnapshot
}

func recoverSemanticClone(t *testing.T, binary, repository string, environment []string, runID string) recoveredSemanticClone {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "recovery-decisions.jsonl")
	code, stdout, stderr := runCommandBinary(t, binary, repository, replaceEnvironment(environment, map[string]string{
		recoveryTraceFileEnvironment: trace,
	}), "resume", runID)
	if (code != 0 && code != 4 && code != 5) || stdout != "" {
		t.Fatalf("first cloned recovery exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal := readHarnessJournal(t, repository, runID)
	snapshot, err := extractSemanticRecovery(repository, runID, trace, code, stderr)
	if err != nil {
		t.Fatalf("extract semantic recovery: %v", err)
	}
	return recoveredSemanticClone{journal: journal, snapshot: snapshot}
}

func assertSemanticCloneFixedPoint(t *testing.T, binary, repository string, environment []string, runID string, first []byte) {
	t.Helper()
	assertFixedPointReplay(t, binary, repository, environment, runID, first)
}

func recoveryClassification(t *testing.T, repository, runID string) recovery.Decision {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	durable, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	observations, err := recoveryobs.Collect(store, runstate.RunID(runID), durable.Projection)
	if err != nil {
		t.Fatal(err)
	}
	if observations.Lease.Owner != recovery.OwnerDead {
		t.Fatalf("pre-recovery lease owner=%q, want owner-dead", observations.Lease.Owner)
	}
	return recovery.Plan(recovery.Input{Projection: durable.Projection, Observations: observations})
}

func assertCloneClassification(t *testing.T, original recovery.Decision, clone, runID string) {
	t.Helper()
	if got := recoveryClassification(t, clone, runID); !reflect.DeepEqual(got, original) {
		t.Fatalf("clone pre-recovery classification=%+v, want original %+v", got, original)
	}
}

// cloneRecoveryPrefix copies the complete ordinary checkout (never a Git
// worktree), keeps bytes and modes intact, then repairs linked-worktree admin
// paths before any recovery observation. A copied worktree/.git otherwise
// points into the original checkout and changes C.1 classification.
func cloneRecoveryPrefix(t *testing.T, source, runID, name string, copyExpectations []recoveryPrefixCopyExpectation) string {
	t.Helper()
	gitDir, err := os.Stat(filepath.Join(source, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if !gitDir.IsDir() {
		t.Fatal("recovery clone source must be an ordinary checkout, not a linked worktree")
	}
	destination := filepath.Join(t.TempDir(), name)
	if err := copyRecoveryTree(source, destination); err != nil {
		t.Fatalf("copy recovery prefix: %v", err)
	}
	assertRecoveryPrefixCopied(t, source, destination, runID, copyExpectations)
	worktrees, err := filepath.Glob(filepath.Join(destination, ".partitur", "work", runID, "*", "worktree"))
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) == 0 {
		t.Fatal("recovery prefix has no linked attempt worktree to repair")
	}
	arguments := append([]string{"worktree", "repair"}, worktrees...)
	command := exec.Command("git", arguments...)
	command.Dir = destination
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return destination
}

func copyRecoveryTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			if err := os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported recovery-prefix entry %s (%s)", relative, entry.Type())
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		readCloseErr := in.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if readCloseErr != nil {
			return readCloseErr
		}
		return os.Chmod(target, info.Mode().Perm())
	})
}

func writeCloneExecutableFixture(t *testing.T, repository string) {
	t.Helper()
	path := filepath.Join(repository, "clone-mode-fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

type recoveryPrefixCopyExpectation struct {
	pattern       string
	mustMatch     bool
	absenceReason string
}

func recoveryPrefixCopyExpectations(runID string, pendingPrepare bool) []recoveryPrefixCopyExpectation {
	runRoot := filepath.Join(".partitur", "runs", runID)
	return []recoveryPrefixCopyExpectation{
		{
			pattern:       filepath.Join(runRoot, "prepares", "*.json"),
			mustMatch:     pendingPrepare,
			absenceReason: "criterion-launch crash has no durable pending prepare",
		},
		{
			pattern:   filepath.Join(runRoot, "scores", "revision-*.yaml"),
			mustMatch: true,
		},
		{
			pattern:   filepath.Join(runRoot, "driver.lease"),
			mustMatch: true,
		},
		{
			pattern:       filepath.Join(runRoot, "driver.quiesced.*"),
			mustMatch:     false,
			absenceReason: "both fixtures hold the live driver before quiesce acknowledgement",
		},
		{
			pattern:       filepath.Join(runRoot, "quarantine", "*"),
			mustMatch:     false,
			absenceReason: "neither pre-recovery fixture has discarded an artifact",
		},
	}
}

func assertRecoveryPrefixCopied(t *testing.T, source, destination, runID string, copyExpectations []recoveryPrefixCopyExpectation) {
	t.Helper()
	for _, relative := range []string{
		filepath.Join(".partitur", "runs", runID, "journal.jsonl"),
		"clone-mode-fixture",
	} {
		assertCopiedPath(t, filepath.Join(source, relative), filepath.Join(destination, relative))
	}
	for _, expectation := range copyExpectations {
		if !expectation.mustMatch && expectation.absenceReason == "" {
			t.Fatalf("copy expectation for %q permits no matches without a reason", expectation.pattern)
		}
		paths, err := filepath.Glob(filepath.Join(source, expectation.pattern))
		if err != nil {
			t.Fatal(err)
		}
		if len(paths) == 0 {
			if expectation.mustMatch {
				t.Fatalf("copied recovery prefix has no matches for required %q", expectation.pattern)
			}
			continue
		}
		if !expectation.mustMatch {
			t.Fatalf("copied recovery prefix unexpectedly matches %q: %s", expectation.pattern, expectation.absenceReason)
		}
		for _, path := range paths {
			relative, err := filepath.Rel(source, path)
			if err != nil {
				t.Fatal(err)
			}
			assertCopiedPath(t, path, filepath.Join(destination, relative))
		}
	}
	assertCopiedRunRefs(t, source, destination, runID)
}

func assertCopiedPath(t *testing.T, source, destination string) {
	t.Helper()
	info, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	destinationInfo, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != destinationInfo.Mode() {
		t.Fatalf("copied mode for %s=%#o, want %#o", source, destinationInfo.Mode(), info.Mode())
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		before, err := os.Readlink(source)
		if err != nil {
			t.Fatal(err)
		}
		after, err := os.Readlink(destination)
		if err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatalf("copied link differs for %s", source)
		}
		return
	}
	if info.IsDir() {
		entries, err := os.ReadDir(source)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			assertCopiedPath(t, filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name()))
		}
		return
	}
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("copied bytes differ for %s", source)
	}
}

func assertCopiedRunRefs(t *testing.T, source, destination, runID string) {
	t.Helper()
	refs := func(root string) string {
		command := exec.Command("git", "for-each-ref", "--format=%(refname) %(objectname)", "refs/partitur/runs/"+runID)
		command.Dir = root
		output, err := command.Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(output)
	}
	before, after := refs(source), refs(destination)
	if before == "" || !strings.Contains(before, "refs/partitur/runs/"+runID+"/base ") {
		t.Fatalf("prefix has no base ref: %q", before)
	}
	if after != before {
		t.Fatalf("copied run refs=%q, want %q", after, before)
	}
}
