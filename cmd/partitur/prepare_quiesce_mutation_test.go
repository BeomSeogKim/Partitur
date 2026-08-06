//go:build mutation

package main

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

func TestMutationParentPrepareInjectionGuards(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, source, before, after, packagePath, testName string
	}{
		{
			name:        "acquisition pause is emitted after the transaction",
			source:      "internal/runstore/driver.go",
			before:      "store.probe.Reached(faultpoint.PointAuthorityLeaseCreated)",
			after:       "// mutation: parent has no post-acquisition pause",
			packagePath: "./cmd/partitur",
			testName:    "TestParentInjectsPrepareIntoPausedLiveDriver",
		},
		{
			name:        "lease move reaches its post-receipt boundary",
			source:      "internal/runstore/prepare.go",
			before:      "store.probe.Reached(faultpoint.PointQuiesceLeaseMoved)",
			after:       "// mutation: durable lease move has no harness boundary",
			packagePath: "./cmd/partitur",
			testName:    "TestParentInjectsPrepareIntoPausedLiveDriver",
		},
		{
			name:   "reclaim lease-created pause is emitted after the transaction",
			source: "internal/runstore/recovery.go",
			before: `		return nil
	})
	if err != nil {
		return nil, err
	}
	store.probe.Reached(faultpoint.PointAuthorityLeaseCreated)
	return &Driver{store: store, runID: runID, seed: movementSeed(input.Score), lease: acquired}, nil`,
			after: `		store.probe.Reached(faultpoint.PointAuthorityLeaseCreated)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Driver{store: store, runID: runID, seed: movementSeed(input.Score), lease: acquired}, nil`,
			packagePath: "./internal/runstore",
			testName:    "TestReclaimDeadRecoveryDriverReleasesStateLockBeforeLeaseCreatedBoundary",
		},
		{
			name:   "production command wires amendment dispositioner",
			source: "cmd/partitur/main.go",
			before: "\texecution.ProposalDisposition = amendmentexec.New()",
			after: "\texecution.ProposalDisposition = func() driver.ProposalDispositioner {\n" +
				"\t\t_ = amendmentexec.New\n" +
				"\t\treturn nil\n" +
				"\t}()",
			packagePath: "./cmd/partitur",
			testName:    "TestProductionExecutionDependenciesWireAmendmentDispositioner",
		},
		{
			name:        "run dispatches adapter proposals through production composition",
			source:      "cmd/partitur/main.go",
			before:      "\t\tproductionRunDriver,",
			after:       "\t\tdriver.Run,",
			packagePath: "./cmd/partitur",
			testName:    "TestRunRoutesAdapterProposalThroughProductionComposition",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			assertPrepareQuiesceMutationKilled(t, environment, mutation.source, mutation.before, mutation.after, mutation.packagePath, mutation.testName)
		})
	}
}

func assertPrepareQuiesceMutationKilled(t *testing.T, environment mutationtest.GoEnvironment, source, before, after, packagePath, testName string) {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve command test source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyPrepareQuiesceRepository(copyRoot, repository); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(copyRoot, source)
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count=%d, want 1 for %q", count, before)
	}
	if err := os.WriteFile(sourcePath, []byte(strings.Replace(string(contents), before, after, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	mutated, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mutated), after) || strings.Contains(string(mutated), before) {
		t.Fatal("mutation did not apply")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     packagePath,
		TestPattern: testName,
		TestNames:   []string{testName},
		Environment: environment.ChildEnvironment(os.Environ(), "PARTITUR_MUTATION_CHILD=1"),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}

func copyPrepareQuiesceRepository(destination, source string) error {
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
