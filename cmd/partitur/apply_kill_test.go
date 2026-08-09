package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// TestApplyKillCutsResolveToBothSides kills a real `apply` subprocess on each
// side of its durable seam. The seam is what makes recovery decidable: the
// transaction is recorded before the checkout is touched, so a crash either
// left the base tree in place or left the result tree in place, and
// `--recover` must reach the outcome the §8 exit table names for each.
func TestApplyKillCutsResolveToBothSides(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")

	for _, cut := range []struct {
		name        string
		point       faultpoint.PointID
		recoverCode int
		applied     string
		resolution  runstate.EventType
	}{
		{
			name:  "crash before the checkout is touched rolls back",
			point: faultpoint.PointApplyTransactionStarted,
			// The base tree is still in place, so the rollback is verified, not assumed.
			recoverCode: 4, applied: "", resolution: runstate.EventApplyRecoveryResolved,
		},
		{
			name:  "crash after the checkout is written completes",
			point: faultpoint.PointApplyCheckoutMutated,
			// The candidate is already on disk; recovery owes it the missing event.
			recoverCode: 0, applied: "candidate result\n", resolution: runstate.EventApplyCompleted,
		},
	} {
		t.Run(cut.name, func(t *testing.T) {
			root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
			environment := applyKillEnvironment(t)
			killAtPoint(t, partitur, partiturRepository(t, root), environment, cut.point, "apply", "run-1")

			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if countEvents(journal.Events, runstate.EventApplyStarted) != 1 ||
				countEvents(journal.Events, runstate.EventApplyCompleted) != 0 ||
				countEvents(journal.Events, runstate.EventApplyFailed) != 0 {
				t.Fatalf("crash left journal=%v", eventKinds(journal.Events))
			}

			// APPLYING is not a normal entry state: the interrupted transaction has
			// to be named before anything else may touch the checkout.
			code, stdout, stderr := runCommandBinary(t, partitur, root, environment, "apply", "run-1")
			if code != 2 || stdout != "" || !strings.Contains(stderr, "normal apply is refused") {
				t.Fatalf("normal apply after crash exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}

			code, stdout, stderr = runCommandBinary(t, partitur, root, environment, "apply", "run-1", "--recover")
			if code != cut.recoverCode || stdout != "" {
				t.Fatalf("recover exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if contents := applyReadFile(t, root, "applied.txt"); contents != cut.applied {
				t.Fatalf("checkout after recovery=%q, want %q", contents, cut.applied)
			}
			journal, err = store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if last := journal.Events[len(journal.Events)-1].Type; last != cut.resolution {
				t.Fatalf("recovery journal tail=%v", eventKinds(journal.Events))
			}

			// Recovery is a fixed point: a second pass changes no bytes.
			before := applyReadJournalBytes(t, root)
			runCommandBinary(t, partitur, root, environment, "apply", "run-1", "--recover")
			if after := applyReadJournalBytes(t, root); after != before {
				t.Fatalf("second recover rewrote the journal:\nbefore=%s\nafter=%s", before, after)
			}
		})
	}
}

// TestApplyKillLeavingAnUnverifiableCheckoutHalts covers the third outcome: a
// checkout matching neither candidate tree. Recovery may not guess, so it names
// the halt durably and every later pass reproduces it unchanged.
func TestApplyKillLeavingAnUnverifiableCheckoutHalts(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	partitur := buildE2EBinary(t, repositoryRoot, t.TempDir(), "partitur")
	root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
	environment := applyKillEnvironment(t)
	killAtPoint(t, partitur, partiturRepository(t, root), environment, faultpoint.PointApplyCheckoutMutated, "apply", "run-1")

	// Something else edited the checkout between the crash and the recovery, so
	// it is now neither the base tree nor the result tree.
	if err := os.WriteFile(filepath.Join(root, "applied.txt"), []byte("third state\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCommandBinary(t, partitur, root, environment, "apply", "run-1", "--recover")
	if code != 5 || stdout != "" || !strings.Contains(stderr, "matches neither candidate tree") {
		t.Fatalf("recover exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventApplyRecoveryRequired) != 1 ||
		countEvents(journal.Events, runstate.EventApplyCompleted) != 0 {
		t.Fatalf("halt journal=%v", eventKinds(journal.Events))
	}

	// The halt is the fixed point: repeating it appends no second cause.
	before := applyReadJournalBytes(t, root)
	code, _, stderr = runCommandBinary(t, partitur, root, environment, "apply", "run-1", "--recover")
	if code != 5 || !strings.Contains(stderr, "matches neither candidate tree") {
		t.Fatalf("second recover exit=%d stderr=%q", code, stderr)
	}
	if after := applyReadJournalBytes(t, root); after != before {
		t.Fatalf("second recover rewrote the journal:\nbefore=%s\nafter=%s", before, after)
	}
}

func applyKillEnvironment(t *testing.T) []string {
	t.Helper()
	return replaceEnvironment(os.Environ(), map[string]string{"HOME": t.TempDir()})
}

// partiturRepository is the fixture root, named for what killAtPoint expects.
func partiturRepository(t *testing.T, root string) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, ".partitur", "runs", "run-1")); err != nil {
		t.Fatal(err)
	}
	return root
}

func applyReadFile(t *testing.T, root, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(contents)
}

func applyReadJournalBytes(t *testing.T, root string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
