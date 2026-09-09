package runstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDerivedEventSourceAppendWalkIgnoresPartiturStateDirectory(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	partiturDirectory := filepath.Join(repository, ".partitur")
	workDirectory := filepath.Join(partiturDirectory, "work")
	fixtureRoot := filepath.Join(workDirectory, "derived-event-source-append-walk-test")
	fixtureDirectory := filepath.Join(fixtureRoot, "worktree", "internal", "recovery")

	partiturExisted := runstatePathExists(t, partiturDirectory)
	workExisted := runstatePathExists(t, workDirectory)
	if runstatePathExists(t, fixtureRoot) {
		t.Fatalf("fixture path already exists: %s", fixtureRoot)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(fixtureRoot); err != nil {
			t.Errorf("remove fixture: %v", err)
		}
		if !workExisted {
			if err := os.Remove(workDirectory); err != nil && !os.IsNotExist(err) {
				t.Errorf("remove work directory: %v", err)
			}
		}
		if !partiturExisted {
			if err := os.Remove(partiturDirectory); err != nil && !os.IsNotExist(err) {
				t.Errorf("remove Partitur directory: %v", err)
			}
		}
	})

	if err := os.MkdirAll(fixtureDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDirectory, "broken.go"), []byte("package recovery\n\nfunc broken( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !hasNonTestAppendSite(t, string(EventRunCancelled)) {
		t.Fatal("run.cancelled has no non-test append site")
	}
}

func runstatePathExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatal(err)
	return false
}
