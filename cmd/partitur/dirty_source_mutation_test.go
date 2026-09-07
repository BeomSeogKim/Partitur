//go:build mutation

package main

import (
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationDirtySourceGuidance(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	result := assertPrepareQuiesceMutationKilled(t, environment,
		"cmd/partitur/main.go",
		`fmt.Fprintf(stderr, "run validation failed: %v hint=%q\n", result.Err, dirtySourceHint)`,
		`fmt.Fprintf(stderr, "run validation failed: %v\n", result.Err)`,
		"./cmd/partitur", "TestInitScaffoldingDirtySourceRefusalExplainsTracking")
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}
