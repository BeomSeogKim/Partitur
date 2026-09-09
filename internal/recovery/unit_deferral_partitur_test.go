package recovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnitOwnedDeferralBoundaryIgnoresPartiturStateDirectory(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	partiturDirectory := filepath.Join(repository, ".partitur")
	workDirectory := filepath.Join(partiturDirectory, "work")
	fixtureRoot := filepath.Join(workDirectory, "unit-owned-deferral-boundary-test")
	fixtureDirectory := filepath.Join(fixtureRoot, "worktree", "internal", "recovery")

	partiturExisted := pathExists(t, partiturDirectory)
	workExisted := pathExists(t, workDirectory)
	if pathExists(t, fixtureRoot) {
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
	source, err := os.ReadFile("unit_deferral.go")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtureDirectory, "unit_deferral.go"), source, 0o644); err != nil {
		t.Fatal(err)
	}

	declarations, _ := unitOwnedDeferralBoundary(t)
	if len(declarations) != 1 {
		t.Fatalf("UnitOwnedDeferral declarations = %d, want 1", len(declarations))
	}
	if declarations[0] != unitOwnedDeferralDeclaration {
		t.Fatalf("UnitOwnedDeferral declaration = %s, want %s", declarations[0], unitOwnedDeferralDeclaration)
	}
}

func pathExists(t *testing.T, path string) bool {
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
