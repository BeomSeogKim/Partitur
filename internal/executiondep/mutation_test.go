//go:build mutation

package executiondep

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

// These mutations prove the independently specified A.5 closure and each
// recorded-tuple refusal. The child runner verifies that the named test ran.
func TestMutationV3RecordedTupleGuards(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, before, after, test string
	}{
		{"recorded outer dispatcher", "case canonical.ProjectionVersionExecutionDependency:", "case 0: // mutation", "TestRecomputeDispatchesOnlyCompleteRecordedV3Tuple"},
		{"outer A.5 domain", "canonical.DomainExecutionDependency,\n\t\tcanonical.DomainAcceptanceSpec,", "canonical.DomainPatchOperations,\n\t\tcanonical.DomainAcceptanceSpec,", "TestV3ProjectionDomainsFollowReachedA5Hashes"},
		{"acceptance chain domain", "canonical.DomainAcceptanceSpec,", "canonical.DomainPatchOperations,", "TestV3ProjectionDomainsFollowReachedA5Hashes"},
		{"criterion chain domain", "canonical.DomainCriterionSpec,", "canonical.DomainPatchOperations,", "TestV3ProjectionDomainsFollowReachedA5Hashes"},
		{"composition conditional domain", "domains = append(domains, canonical.DomainMovementComposition)", "domains = append(domains, canonical.DomainPatchOperations)", "TestV3ProjectionDomainsFollowReachedA5Hashes"},
		{"score conditional domain", "domains = append(domains, canonical.DomainScore)", "domains = append(domains, canonical.DomainPatchOperations)", "TestV3ProjectionDomainsFollowReachedA5Hashes"},
		{"resolution conditional domain", "domains = append(domains, canonical.DomainResolutionBody)", "domains = append(domains, canonical.DomainPatchOperations)", "TestV3ProjectionDomainsFollowReachedA5Hashes"},
		{"encoding lower version", "tuple.CanonicalEncoding != want.CanonicalEncoding", "tuple.CanonicalEncoding > want.CanonicalEncoding", "TestRecomputeDispatchesOnlyCompleteRecordedV3Tuple"},
		{"encoding higher version", "tuple.CanonicalEncoding != want.CanonicalEncoding", "tuple.CanonicalEncoding < want.CanonicalEncoding", "TestRecomputeDispatchesOnlyCompleteRecordedV3Tuple"},
		{"inner lower version", "tuple.Projections[string(domain)] != want.Projection", "tuple.Projections[string(domain)] > want.Projection", "TestRecomputeDispatchesOnlyCompleteRecordedV3Tuple"},
		{"inner higher version", "tuple.Projections[string(domain)] != want.Projection", "tuple.Projections[string(domain)] < want.Projection", "TestRecomputeDispatchesOnlyCompleteRecordedV3Tuple"},
		{"incomplete inner tuple", "if err := tuple.requireV3(domains); err != nil {", "if err := tuple.requireV3(domains); false && err != nil { // mutation", "TestRecomputeDispatchesOnlyCompleteRecordedV3Tuple"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertMutationKilled(t, environment, mutation.before, mutation.after, mutation.test)
		})
	}
}

func TestMutationCollectedAttemptGuards(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, before, after, test string
	}{
		{"completed successful predicate", "attempt.State == runstate.AttemptCompleted && state.Movements[attempt.MovementID] == runstate.MovementSucceeded", "attempt.State != runstate.AttemptCompleted && state.Movements[attempt.MovementID] == runstate.MovementSucceeded // mutation", "TestEligibleIsExactlyCompletedSuccessful"},
		{"actual cast adapter extension", "performer.Extensions[adapterID]", "map[string]any{}[adapterID]", "TestCollectUsesJournaledActualAdapterInputs"},
		{"selection-time input instance", "state.MovementResults[producers[artifactID]]", "state.MovementResults[\"mutation-missing-producer\"]", "TestCollectUsesJournaledActualAdapterInputs"},
		{"recorded probe hash", "value.attempt.RecordedHash = runstate.Hash(observed)", "_ = observed\n\tvalue.attempt.RecordedHash = \"\"", "TestCollectUsesJournaledActualAdapterInputs"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertCollectorMutationKilled(t, environment, mutation.before, mutation.after, mutation.test)
		})
	}
}

func assertCollectorMutationKilled(t *testing.T, environment mutationtest.GoEnvironment, before, after, testName string) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve executiondep test source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyRepository(copyRoot, repository); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(copyRoot, "internal", "executiondep", "collector.go")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count=%d, want 1 for %q", count, before)
	}
	if err := os.WriteFile(source, []byte(strings.Replace(string(contents), before, after, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	mutated, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mutated), after) || strings.Contains(string(mutated), before) {
		t.Fatal("mutation did not apply")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "executiondep"),
		Package:     ".",
		TestPattern: testName,
		TestNames:   []string{testName},
		Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation survived: %s", result.Diagnostic())
	}
}

func assertMutationKilled(t *testing.T, environment mutationtest.GoEnvironment, before, after, testName string) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve executiondep test source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyRepository(copyRoot, repository); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(copyRoot, "internal", "executiondep", "executiondep.go")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count=%d, want 1 for %q", count, before)
	}
	if err := os.WriteFile(source, []byte(strings.Replace(string(contents), before, after, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	mutated, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mutated), after) || strings.Contains(string(mutated), before) {
		t.Fatal("mutation did not apply")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         filepath.Join(copyRoot, "internal", "executiondep"),
		Package:     ".",
		TestPattern: testName,
		TestNames:   []string{testName},
		Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation survived: %s", result.Diagnostic())
	}
}

func copyRepository(destination, source string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	})
}
