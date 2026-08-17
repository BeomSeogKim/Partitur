//go:build mutation

package recovery

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationUnitOwnedDeferralBoundaryRejectsPopulation(t *testing.T) {
	runUnitOwnedDeferralMutation(t,
		filepath.Join("internal", "recovery", "unit_deferral.go"),
		"var unitOwnedDeferrals []UnitOwnedDeferral\n",
		"var unitOwnedDeferrals = []UnitOwnedDeferral{{}} // mutation\n",
	)
}

// TestMutationUnitOwnedDeferralBoundaryRejectsSecondRepresentation pins the
// half of the boundary the population check cannot reach: another production
// file naming the type at all. Without this, a second representation could be
// introduced next to the empty one and the lock would stay green.
func TestMutationUnitOwnedDeferralBoundaryRejectsSecondRepresentation(t *testing.T) {
	runUnitOwnedDeferralMutation(t,
		filepath.Join("internal", "recoveryexec", "handlers.go"),
		"func defaultKinds() map[recovery.ActionKind]StepHandler {\n",
		"var _ = []recovery.UnitOwnedDeferral(nil) // mutation\n\nfunc defaultKinds() map[recovery.ActionKind]StepHandler {\n",
	)
}

// TestMutationUnitOwnedDeferralBoundaryRejectsSameFileRegistry pins the one
// place the naming scan cannot reach by construction. The declaration file is
// exempt from that scan, so a second registry beside the first would leave the
// accessor returning the empty slice while a populated parallel registry
// exists -- both other checks green.
func TestMutationUnitOwnedDeferralBoundaryRejectsSameFileRegistry(t *testing.T) {
	runUnitOwnedDeferralMutation(t,
		filepath.Join("internal", "recovery", "unit_deferral.go"),
		"// UnitOwnedDeferrals returns a copy for completeness checks.\n",
		"var unitOwnedDeferralsExtra = []UnitOwnedDeferral{{}} // mutation\n\n// UnitOwnedDeferrals returns a copy for completeness checks.\n",
	)
}

func runUnitOwnedDeferralMutation(t *testing.T, relative, before, after string) {
	t.Helper()

	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve recovery mutation source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyRecoveryMutationRepository(copyRoot, repository); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(copyRoot, relative)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count = %d in %s, want 1", count, relative)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(contents), before, after, 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     "./internal/recovery",
		TestPattern: "TestUnitOwnedDeferralBoundary",
		TestNames:   []string{"TestUnitOwnedDeferralBoundary"},
		Environment: environment.ChildEnvironment(os.Environ()),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result for %s: %s\n%s", relative, result.Reason, result.Diagnostic())
	}
}
