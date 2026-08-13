//go:build mutation

package main

import (
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationCrossEdgePendingPrepareCheck(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name, before, after, target, signature string
		leaves                                 []string
	}{
		{
			name:      "payload decode failure is rejected",
			before:    "if err := json.Unmarshal(event.Payload, &payload); err != nil {\n\t\t\treturn observation, fmt.Errorf(\"event %d %s payload: %w\", index, event.Type, err)\n\t\t}",
			after:     "if err := json.Unmarshal(event.Payload, &payload); err != nil {\n\t\t\tcontinue // mutation: accept malformed payload\n\t\t}",
			target:    "TestCrossEdgePreparePayloadDecodeOracleFixture",
			signature: "malformed cross-edge payload passed",
		},
		{
			name:   "relevant events require a nonempty proposal ID",
			before: "if proposalID == \"\" {",
			after:  "if false { // mutation: accept an empty proposal ID",
			target: "TestCrossEdgePrepareNonemptyProposalIDOracleFixtures",
			// The fixture reports per subtest, so the parent alone is not a target
			// the harness recognises. Naming all three leaves also strengthens the
			// mutation: accepting an empty proposal_id has to break every event
			// type, not just one.
			leaves: []string{
				"TestCrossEdgePrepareNonemptyProposalIDOracleFixtures/prepare",
				"TestCrossEdgePrepareNonemptyProposalIDOracleFixtures/abandon",
				"TestCrossEdgePrepareNonemptyProposalIDOracleFixtures/approve",
			},
			signature: "empty proposal_id passed",
		},
		{
			name:      "pending prepare rejects a second live prepare",
			before:    "if observation.pendingID != \"\" {",
			after:     "if false { // mutation: accept a second pending prepare",
			target:    "TestPendingPrepareCrossEdgeOracleFixtures",
			signature: "two simultaneously pending prepares passed",
		},
		{
			name:      "abandon must name the pending proposal",
			before:    "case runstate.EventAmendmentApprovalAbandoned:\n\t\t\tobservation.terminal++\n\t\t\tif observation.pendingID != runstate.ProposalID(proposalID) {",
			after:     "case runstate.EventAmendmentApprovalAbandoned:\n\t\t\tobservation.terminal++\n\t\t\tif false { // mutation: accept mismatched abandon",
			target:    "TestCrossEdgePrepareAbandonOrderingOracleFixture",
			signature: "abandon for a different proposal passed",
		},
		{
			name:      "approval must name the pending proposal",
			before:    "case runstate.EventAmendmentApproved:\n\t\t\tobservation.terminal++\n\t\t\tobservation.approved++\n\t\t\tif observation.pendingID != runstate.ProposalID(proposalID) {",
			after:     "case runstate.EventAmendmentApproved:\n\t\t\tobservation.terminal++\n\t\t\tobservation.approved++\n\t\t\tif false { // mutation: accept mismatched approval",
			target:    "TestCrossEdgePrepareApprovalOrderingOracleFixture",
			signature: "approval for a different proposal passed",
		},
		{
			name:      "per-proposal approval rejects a duplicate",
			before:    "if approvalCounts[runstate.ProposalID(proposalID)] > 1 {",
			after:     "if false { // mutation: accept a duplicate approval",
			target:    "TestApprovalPerProposalCrossEdgeOracleFixtures",
			signature: "two approvals for one proposal passed",
		},
		{
			name:      "per-proposal approval boundary excludes the first approval",
			before:    "if approvalCounts[runstate.ProposalID(proposalID)] > 1 {",
			after:     "if approvalCounts[runstate.ProposalID(proposalID)] >= 1 { // mutation",
			target:    "TestApprovalPerProposalCrossEdgeOracleFixtures",
			signature: "different-proposal positive control observation=",
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			result := assertCrossEdgeMutationKilled(t, environment, mutation.before, mutation.after,
				mutation.target, mutation.leaves)
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation was not killed: %s\n%s", result.Reason, result.Diagnostic())
			}
			if output := decodedChildOutput(result.Output); !strings.Contains(output, mutation.signature) {
				t.Fatalf("mutation failed for the wrong reason; want signature %q\n%s", mutation.signature, result.Diagnostic())
			}
		})
	}
}

// assertCrossEdgeMutationKilled targets leaf subtests when the fixture has them.
// mutationtest treats a failure whose name is neither a target nor an ancestor
// of one as a non-target failure, so naming only the parent of a table test
// turns a correct kill into a non-result - and Killed requires every target to
// fail, which is what makes a multi-leaf target the stronger assertion.
func assertCrossEdgeMutationKilled(t *testing.T, environment mutationtest.GoEnvironment, before, after, parent string, leaves []string) mutationtest.Result {
	t.Helper()
	if len(leaves) == 0 {
		return assertPrepareQuiesceMutationKilled(t, environment,
			"cmd/partitur/cross_edge_pending_prepare_test.go", before, after, "./cmd/partitur", parent)
	}
	return assertPrepareQuiesceMutationKilledWithTargets(t, environment,
		"cmd/partitur/cross_edge_pending_prepare_test.go", before, after, "./cmd/partitur", parent, leaves)
}
