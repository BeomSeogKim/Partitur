package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// applyProtectedPathContents is what every case in the table below writes, so
// the candidates differ in the name of the added path and in nothing else.
const applyProtectedPathContents = "injected\n"

// TestApplyRefusesACandidateThatWritesAProtectedPath measures §8: before
// `apply.started` the core computes the raw candidate diff, and a diff naming a
// protected worktree path is refused as a precondition with no application
// event. The two protected cases are the two §2 worktree names, which failed
// differently before this refusal existed: the root score is deliberately kept
// inside the comparison projection, so tree equality never objected to it and
// the candidate applied; the state directory is projected out, so equality
// caught it only after the seam and reported it as a clean failure.
//
// Exit 2 with no application event is what every precondition refusal looks
// like, so the refusal is attributed to the path rather than read off the
// diagnostic, which §8 does not specify. Attribution needs the candidates to
// differ in one thing only, so each protected case is paired with a control
// that adds a path of the same shape under a name §2 does not protect: same
// contents, same touched-path count, same depth, same leading dot. A refusal
// keyed on how many paths the candidate touches, or on the path being nested,
// or on the directory beginning with a dot, applies the control and is caught.
// Cleanliness permits state-directory residue, so the state-directory case also
// asserts that its candidate path is absent after refusal. A before/after state-
// directory comparison would include the command's legitimate journal writes.
func TestApplyRefusesACandidateThatWritesAProtectedPath(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		path    string
		refused bool
	}{
		{name: "unprotected root file", path: "unprotected.yaml"},
		{name: "root score", path: "partitur.yaml", refused: true},
		{name: "unprotected dot directory", path: ".unprotected/injected.txt"},
		{name: "state directory", path: ".partitur/injected.txt", refused: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			files := append(slices.Clone(applyFixtureCandidateFiles),
				applyFixtureFile{name: testCase.path, contents: applyProtectedPathContents})
			root, store, _ := applyRequireFixtureWithFiles(t, applyGate{require: []string{"verified"}}, files)

			code, contents, stderr := applyRequireCheckout(t, root)

			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			started := countEvents(journal.Events, runstate.EventApplyStarted)
			if !testCase.refused {
				if code != 0 || contents != "candidate result\n" || stderr != "" || started != 1 {
					t.Fatalf("apply exit=%d contents=%q stderr=%q started=%d", code, contents, stderr, started)
				}
				return
			}
			if code != 2 || contents != "" {
				t.Fatalf("apply exit=%d contents=%q stderr=%q", code, contents, stderr)
			}
			applyFixtureApplicationClean(t, root)
			if testCase.path == ".partitur/injected.txt" {
				if _, err := os.Stat(filepath.Join(root, testCase.path)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("refused apply materialized protected path %q: %v", testCase.path, err)
				}
			}
			if started != 0 {
				t.Fatal("refused apply opened a transaction")
			}
		})
	}
}

func TestApplyRefusesACandidateThatRenamesAProtectedPath(t *testing.T) {
	root, store, _ := applyRequireFixtureWithBaseFiles(t, applyGate{require: []string{"verified"}}, []applyFixtureFile{
		{name: "partitur.yaml", contents: applyProtectedPathContents},
	}, []applyFixtureFile{
		{name: "partitur.yaml", inBase: true, remove: true},
		{name: "unprotected.yaml", contents: applyProtectedPathContents},
	})

	code, _, _ := applyRequireCheckout(t, root)
	if code != 2 {
		t.Fatalf("apply exit=%d, want 2", code)
	}
	applyFixtureApplicationClean(t, root)
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventApplyStarted) != 0 {
		t.Fatal("refused apply opened a transaction")
	}
}

func TestApplyRenameRecordsBothTouchedPaths(t *testing.T) {
	root, store, _ := applyRequireFixtureWithBaseFiles(t, applyGate{require: []string{"verified"}}, []applyFixtureFile{
		{name: "rename-source.yaml", contents: applyProtectedPathContents},
	}, []applyFixtureFile{
		{name: "rename-source.yaml", inBase: true, remove: true},
		{name: "rename-destination.yaml", contents: applyProtectedPathContents},
	})

	code, _, stderr := applyRequireCheckout(t, root)
	if code != 0 || stderr != "" {
		t.Fatalf("apply exit=%d stderr=%q", code, stderr)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type != runstate.EventApplyStarted {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		touched, ok := payload["touched_paths"].([]any)
		if !ok || !slices.Equal(touched, []any{"rename-destination.yaml", "rename-source.yaml"}) {
			t.Fatalf("apply.started touched_paths=%#v", payload["touched_paths"])
		}
		return
	}
	t.Fatal("apply.started is missing")
}
