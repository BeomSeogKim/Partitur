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
			name:        "acceptance plan requires its spec identity",
			source:      "internal/acceptance/acceptance.go",
			before:      "recorded.SpecHash != plan.specHash",
			after:       "recorded.SpecHash == plan.specHash",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRequireVerifiedRefusesAcceptancePlanMismatch/acceptance_hash_does_not_match_the_final_movement",
		},
		{
			name:        "acceptance plan requires every compiled criterion",
			source:      "internal/acceptance/acceptance.go",
			before:      "if len(ids) != len(plan.criteria) {",
			after:       "if false {",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRequireVerifiedRefusesAcceptancePlanMissingCoreGeneratedCriterion",
		},
		{
			name:        "acceptance plan requires compiled order",
			source:      "internal/acceptance/acceptance.go",
			before:      "if id != runstate.CriterionID(plan.criteria[index].id) || !passed(index, id) {",
			after:       "if false && index == -1 && id == \"\" {",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRequireVerifiedRefusesAcceptancePlanMismatch/plan_reverses_the_compiled_order",
		},
		{
			name:        "acceptance plan requires completed records",
			source:      "internal/acceptance/acceptance.go",
			before:      "record.Completed && record.Outcome == \"PASS\"",
			after:       "true && record.Outcome == \"PASS\"",
			packagePath: "./internal/acceptance",
			testName:    "TestPlanSatisfiesRecordedAcceptance/incomplete_planned_criterion",
		},
		{
			name:        "acceptance plan requires pass records",
			source:      "internal/acceptance/acceptance.go",
			before:      "record.Completed && record.Outcome == \"PASS\"",
			after:       "record.Completed && true",
			packagePath: "./internal/acceptance",
			testName:    "TestPlanSatisfiesRecordedAcceptance/non-pass_planned_criterion",
		},
		{
			name:        "acceptance plan requires each compiled criterion identity",
			source:      "internal/acceptance/acceptance.go",
			before:      "record.SpecHash == plan.criteria[index].specHash",
			after:       "true",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRequireVerifiedRefusesCriterionSpecHashMismatch",
		},
		{
			name:        "verified requires declared hard criteria",
			source:      "internal/acceptance/acceptance.go",
			before:      "return plan != nil && plan.declaredHard > 0",
			after:       "return plan != nil && plan.declaredHard == 0",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRequireVerifiedMaterializesCandidate",
		},
		{
			name:   "reviewed requires a compiled review criterion",
			source: "internal/acceptance/acceptance.go",
			before: "for _, criterion := range plan.criteria {\n" +
				"\t\tif criterion.review {",
			after: "for _, criterion := range plan.criteria {\n" +
				"\t\tif false && criterion.review {",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRequireReviewedAndPredicateMaterializeCandidate",
		},
		{
			name:        "approved does not depend on acceptance satisfaction",
			source:      "internal/runstore/application.go",
			before:      "if resolution.Disposition == \"approved\" && resolution.ScoreRevision == state.ScoreHead.Revision && resolution.Scope.SubjectTree == subjectTree {",
			after:       "if satisfiesPlan && resolution.Disposition == \"approved\" && resolution.ScoreRevision == state.ScoreHead.Revision && resolution.Scope.SubjectTree == subjectTree {",
			packagePath: "./cmd/partitur",
			testName:    "TestApplyRequireApprovedGrantsAndWithholds/valid_approval_survives_an_acceptance_defect",
		},
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
			name:   "apply parser accepts only the recover spelling",
			source: "cmd/partitur/main.go",
			// Anchored through the following function name: `promote-score` parses
			// its own `--recover` with the identical clause, so the bare text
			// appears twice and the harness refuses an ambiguous anchor.
			before: "if args[2] != \"--recover\" {\n" +
				"\t\treturn \"\", false, false\n" +
				"\t}\n" +
				"\treturn args[1], true, true\n" +
				"}\n\nfunc runApply",
			after: "if args[2] == \"--recover\" {\n" +
				"\t\treturn \"\", false, false\n" +
				"\t}\n" +
				"\treturn args[1], true, true\n" +
				"}\n\nfunc runApply",
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
