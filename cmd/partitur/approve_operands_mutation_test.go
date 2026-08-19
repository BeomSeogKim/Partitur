//go:build mutation

package main

import (
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationApproveOperands(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, before, after string
		testNames           []string
	}{
		{
			name:   "non-human approval accepts override",
			before: "\tif len(overridden) != 0 {\n",
			after:  "\tif false { // mutation: accept override\n",
			testNames: []string{
				"TestApproveOperandsBySelectedDecisionType/amendment_approval_rejects_override",
				"TestApproveOperandsBySelectedDecisionType/finalization_approval_rejects_override",
			},
		},
		{
			name:   "non-human rejection accepts empty reason",
			before: "\tif !approved && reason == \"\" {\n",
			after:  "\tif false { // mutation: accept empty reason\n",
			testNames: []string{
				"TestApproveOperandsBySelectedDecisionType/amendment_rejection_requires_reason",
				"TestApproveOperandsBySelectedDecisionType/finalization_rejection_requires_reason",
			},
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			result := assertPrepareQuiesceMutationKilledWithTargets(
				t,
				environment,
				"cmd/partitur/main.go",
				mutation.before,
				mutation.after,
				"./cmd/partitur",
				"TestApproveOperandsBySelectedDecisionType",
				mutation.testNames,
			)
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation outcome=%s, want %s: %s\n%s", result.Outcome, mutationtest.Killed, result.Reason, result.Diagnostic())
			}
		})
	}
}
