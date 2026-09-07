//go:build mutation

package main

import (
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationAnswerOperandUsage(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	result := assertPrepareQuiesceMutationKilled(t, environment,
		"cmd/partitur/main.go",
		"  partitur answer  <decision-id> --answer <text> | --answer-file <path>",
		"  partitur answer",
		"./cmd/partitur", "TestAnswerSourceUsage")
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
}
