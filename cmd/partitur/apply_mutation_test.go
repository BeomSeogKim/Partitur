//go:build mutation

package main

import (
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

// TestMutationApplyConjuncts gives every apply guard operand a separate,
// exact-name oracle. The child runner also rejects a failure from any test
// other than the named one, so a broad package failure cannot pin a mutation.
func TestMutationApplyConjuncts(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, source, before, after, packagePath, testName string
		wantOutcome                                        mutationtest.Outcome
	}{
		{
			name:        "apply parser rejects a two-argument length as malformed",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) == 2 && len(args) != 3) || len(args) == 0 || args[0] != "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParseApplyArgs",
		},
		{
			name:        "apply parser rejects a three-argument recovery form as malformed",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) != 2 && len(args) == 3) || len(args) == 0 || args[0] != "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParseApplyArgs",
		},
		{
			name:        "apply parser guards the empty argument vector before indexing",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) != 2 && len(args) != 3) || false || args[0] != "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParseApplyArgs",
			// This arm is intentionally retained as a measured survivor: the first
			// length predicate already rejects an empty slice before args is indexed.
			wantOutcome: mutationtest.Survived,
		},
		{
			name:        "apply parser requires the apply command word",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] == "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParseApplyArgs",
		},
		{
			name:        "apply parser rejects an empty run identifier",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "apply" || args[1] != "" || strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParseApplyArgs",
		},
		{
			name:        "apply parser rejects a flag in the run identifier position",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "apply" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "apply" || args[1] == "" || !strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParseApplyArgs",
		},
		{
			name:        "apply parser accepts only the recover spelling",
			source:      "cmd/partitur/main.go",
			before:      `args[2] != "--recover"`,
			after:       `args[2] == "--recover"`,
			packagePath: "./cmd/partitur",
			testName:    "TestParseApplyArgs",
		},
		{
			name:        "normal apply admits the not-applied state",
			source:      "internal/runstore/application.go",
			before:      "state.Application.State != runstate.ApplicationNotApplied && ",
			after:       "",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRequireVerifiedMaterializesCandidate",
		},
		{
			name:        "normal apply admits the failed-clean state",
			source:      "internal/runstore/application.go",
			before:      " && state.Application.State != runstate.ApplicationFailedClean",
			after:       "",
			packagePath: "./cmd/partitur",
			testName:    "TestApplySabotagedPatchFailsCleanly",
		},
		{
			name:        "recover admits the applying state",
			source:      "internal/runstore/application.go",
			before:      "state.Application.State != runstate.ApplicationApplying && ",
			after:       "",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRecoverRecordsRequiredThenResolvesBaseTree",
		},
		{
			name:        "recover admits the recovery-required state",
			source:      "internal/runstore/application.go",
			before:      " && state.Application.State != runstate.ApplicationRecoveryRequired",
			after:       "",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyKillLeavingAnUnverifiableCheckoutHalts",
		},
		{
			name:        "completion requires a successful result-tree observation",
			source:      "internal/runstore/application.go",
			before:      "\t\tif treeErr == nil && afterTree == candidate.ResultTree {",
			after:       "\t\tif treeErr != nil && afterTree == candidate.ResultTree {",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRequireVerifiedMaterializesCandidate",
		},
		{
			name:        "completion requires the candidate result tree",
			source:      "internal/runstore/application.go",
			before:      "\t\tif treeErr == nil && afterTree == candidate.ResultTree {",
			after:       "\t\tif treeErr == nil && afterTree != candidate.ResultTree {",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRequireVerifiedMaterializesCandidate",
		},
		{
			name:        "rollback only runs after a readable tree observation",
			source:      "internal/runstore/application.go",
			before:      "err == nil && restored != candidate.BaseTree",
			after:       "err != nil && restored != candidate.BaseTree",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRollbackRestoresTouchedPathsBeforeReportingUnverifiable",
		},
		{
			name:        "rollback runs only when the checkout differs from the base tree",
			source:      "internal/runstore/application.go",
			before:      "err == nil && restored != candidate.BaseTree",
			after:       "err == nil && restored == candidate.BaseTree",
			packagePath: "./cmd/partitur",
			testName:    "TestApplySabotagedPatchFailsCleanly",
		},
		{
			name:        "unexpected apply failures are projected after the transaction",
			source:      "internal/runstore/application.go",
			before:      "err == nil || errors.Is(err, ErrApplicationNotAllowed)",
			after:       "err != nil || errors.Is(err, ErrApplicationNotAllowed)",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRollbackFailureContinuationsStayRecoverable/the_working_tree_cannot_be_recomputed",
		},
		{
			name:        "application-not-allowed failures are not reclassified as interrupted",
			source:      "internal/runstore/application.go",
			before:      "\t\tif err == nil || errors.Is(err, ErrApplicationNotAllowed) {",
			after:       "\t\tif err == nil || errors.Is(err, ErrApplicationNotAllowed) == false {",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRecoverRecordsRequiredThenResolvesBaseTree",
		},
		{
			name:   "apply-gate demands are read before the waived absence return",
			source: "internal/score/access.go",
			before: "\t\tresult.ApplyGateRequire = slices.Clone(expectation.ApplyGate.Require)\n" +
				"\t\tresult.ApplyGatePredicates = slices.Clone(expectation.ApplyGate.Predicates)\n" +
				"\t\tif expectation.ApplyGate.Waived == nil {\n\t\t\treturn result\n\t\t}",
			after: "\t\tif expectation.ApplyGate.Waived == nil {\n\t\t\treturn result\n\t\t}\n" +
				"\t\tresult.ApplyGateRequire = slices.Clone(expectation.ApplyGate.Require)\n" +
				"\t\tresult.ApplyGatePredicates = slices.Clone(expectation.ApplyGate.Predicates)",
			packagePath: "./internal/score",
			testName:    "TestExecutionReadSurface",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			want := mutationtest.Killed
			if mutation.wantOutcome != "" {
				want = mutation.wantOutcome
			}
			result := assertPrepareQuiesceMutationKilled(t, environment, mutation.source, mutation.before, mutation.after, mutation.packagePath, mutation.testName)
			if result.Outcome != want {
				t.Fatalf("mutation outcome=%s, want %s: %s\n%s", result.Outcome, want, result.Reason, result.Diagnostic())
			}
		})
	}
}
