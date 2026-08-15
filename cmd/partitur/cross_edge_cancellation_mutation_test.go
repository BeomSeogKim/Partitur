//go:build mutation

package main

import (
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationCrossEdgeCancellationCheck(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, before, after, target, signature string
	}{
		{
			name:      "cancellation request cardinality is exact",
			before:    "observation.cancellationRequests == 1 &&\n",
			after:     "observation.cancellationRequests != 1 && // mutation\n",
			target:    "TestCrossEdgeCancellationObservationFixtures/duplicate_cancel_requested",
			signature: "validator error=<nil>, wantError=true",
		},
		{
			name:      "cancellation disposition cardinality is exact",
			before:    "observation.cancelled == 1 &&\n",
			after:     "observation.cancelled != 1 && // mutation\n",
			target:    "TestCrossEdgeCancellationObservationFixtures/duplicate_run_cancelled",
			signature: "validator error=<nil>, wantError=true",
		},
		{
			name:      "cancellation precedes its disposition",
			before:    "observation.cancellationIndex < observation.cancelledIndex &&\n",
			after:     "observation.cancellationIndex > observation.cancelledIndex && // mutation\n",
			target:    "TestCrossEdgeCancellationObservationFixtures/positive_cancellation_without_approval",
			signature: "validator error=cancellation does not outrank approval:",
		},
		{
			name:      "approval after cancellation is rejected",
			before:    "(observation.approvals == 0 || observation.lastApprovalIndex < observation.cancellationIndex)\n",
			after:     "(observation.approvals != 0 || observation.lastApprovalIndex < observation.cancellationIndex) // mutation\n",
			target:    "TestCrossEdgeCancellationSyntheticApprovalNegativeControl/approval_after_durable_cancellation",
			signature: "synthetic approval after durable cancellation passed",
		},
		{
			name:      "approval order is checked in the opposite direction",
			before:    "observation.lastApprovalIndex < observation.cancellationIndex",
			after:     "observation.lastApprovalIndex > observation.cancellationIndex",
			target:    "TestCrossEdgeCancellationObservationFixtures/approval_before_cancellation",
			signature: "validator error=cancellation does not outrank approval:",
		},
		{
			name:      "the retained approval is the last one",
			before:    "observation.lastApprovalIndex = index\n",
			after:     "observation.lastApprovalIndex = min(observation.lastApprovalIndex, index) // mutation\n",
			target:    "TestCrossEdgeCancellationObservationFixtures/approval_before_and_after_cancellation",
			signature: "validator error=<nil>, wantError=true",
		},
		{
			name:      "all cancellation conjuncts are required",
			before:    "return observation.cancellationRequests == 1 &&\n",
			after:     "return observation.cancellationRequests == 1 || // mutation\n",
			target:    "TestCrossEdgeCancellationObservationFixtures/duplicate_cancel_requested",
			signature: "validator error=<nil>, wantError=true",
		},
		{
			name:      "approval absence and prior approval are alternatives",
			before:    "(observation.approvals == 0 || observation.lastApprovalIndex < observation.cancellationIndex)\n",
			after:     "(observation.approvals == 0 && observation.lastApprovalIndex < observation.cancellationIndex) // mutation\n",
			target:    "TestCrossEdgeCancellationObservationFixtures/positive_cancellation_without_approval",
			signature: "validator error=cancellation does not outrank approval:",
		},
	} {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			result := assertPrepareQuiesceMutationKilledWithTargets(t, environment,
				"cmd/partitur/cross_edge_cancellation_test.go", mutation.before, mutation.after,
				"./cmd/partitur", mutation.target, []string{mutation.target})
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation was not killed: %s\n%s", result.Reason, result.Diagnostic())
			}
			if output := decodedChildOutput(result.Output); !strings.Contains(output, mutation.signature) {
				t.Fatalf("mutation failed for the wrong reason; want signature %q\n%s", mutation.signature, result.Diagnostic())
			}
		})
	}
}
