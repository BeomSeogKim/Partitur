//go:build mutation

package main

import (
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

// TestMutationPromoteScoreConjuncts gives each parser, recovery guard, and
// three-way recovery decision its own exact-name oracle. A survivor is kept
// only where the earlier length guard makes the removed check redundant.
func TestMutationPromoteScoreConjuncts(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, source, before, after, packagePath, testName string
		wantOutcome                                        mutationtest.Outcome
	}{
		{
			name:        "promote-score parser rejects a two-argument length as malformed",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) == 2 && len(args) != 3) || len(args) == 0 || args[0] != "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParsePromoteScoreArgs",
		},
		{
			name:        "promote-score parser rejects a three-argument recovery form as malformed",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) != 2 && len(args) == 3) || len(args) == 0 || args[0] != "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParsePromoteScoreArgs",
		},
		{
			name:        "promote-score parser guards the empty argument vector before indexing",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) != 2 && len(args) != 3) || false || args[0] != "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParsePromoteScoreArgs",
			// The first length predicate rejects an empty slice before args is
			// indexed, so this is the same measured survivor as apply's guard.
			wantOutcome: mutationtest.Survived,
		},
		{
			name:        "promote-score parser requires the command word",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] == "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParsePromoteScoreArgs",
		},
		{
			name:        "promote-score parser rejects an empty run identifier",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "promote-score" || args[1] != "" || strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParsePromoteScoreArgs",
		},
		{
			name:        "promote-score parser rejects a flag in the run identifier position",
			source:      "cmd/partitur/main.go",
			before:      `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "promote-score" || args[1] == "" || strings.HasPrefix(args[1], "-") {`,
			after:       `if (len(args) != 2 && len(args) != 3) || len(args) == 0 || args[0] != "promote-score" || args[1] == "" || !strings.HasPrefix(args[1], "-") {`,
			packagePath: "./cmd/partitur",
			testName:    "TestParsePromoteScoreArgs",
		},
		{
			name:   "promote-score parser accepts only the recover spelling",
			source: "cmd/partitur/main.go",
			before: "if args[2] != \"--recover\" {\n" +
				"\t\treturn \"\", false, false\n" +
				"\t}\n" +
				"\treturn args[1], true, true\n" +
				"}\n\nfunc runPromoteScore",
			after: "if args[2] == \"--recover\" {\n" +
				"\t\treturn \"\", false, false\n" +
				"\t}\n" +
				"\treturn args[1], true, true\n" +
				"}\n\nfunc runPromoteScore",
			packagePath: "./cmd/partitur",
			testName:    "TestParsePromoteScoreArgs",
		},
		{
			name:        "recover admits only a promoting transaction",
			source:      "internal/runstore/promotion.go",
			before:      "state.Promotion.State != runstate.PromotionPromoting && ",
			after:       "",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreKillCutsRecoverToPromotedFixedPoint/after_root_rename",
		},
		{
			name:        "recover admits a recovery-required transaction",
			source:      "internal/runstore/promotion.go",
			before:      " && state.Promotion.State != runstate.PromotionRecoveryRequired",
			after:       "",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreRecoveryHaltLeavesJournalFixed",
		},
		{
			name:        "promotion checks the root hash before it starts",
			source:      "internal/runstore/promotion.go",
			before:      "if rawHash(root) != target.expectedHash {",
			after:       "if rawHash(root) == target.expectedHash {",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreRefusesPreStartRootHashConflictAndAlreadyPromotedWithoutChangingRoot",
		},
		{
			name:        "promotion rechecks the root hash at the rename boundary",
			source:      "internal/runstore/promotion.go",
			before:      "if observed != target.expectedHash {",
			after:       "if observed == target.expectedHash {",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreRenameTimeRootChangeLeavesUserRootAndHalts",
		},
		{
			name:        "recover requires the original promotion candidate",
			source:      "internal/runstore/promotion.go",
			before:      "if state.Promotion.CandidateID != target.candidate.ID {",
			after:       "if state.Promotion.CandidateID == target.candidate.ID {",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreRecoverRequiresOriginalCandidate",
		},
		{
			name:        "missing pinned promotion snapshot remains named",
			source:      "internal/runstore/promotion.go",
			before:      "if errors.Is(err, os.ErrNotExist) {",
			after:       "if !errors.Is(err, os.ErrNotExist) {",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreRecoverHaltsMissingTargetSnapshot",
		},
		{
			name:        "mismatched pinned promotion snapshot halts",
			source:      "internal/runstore/promotion.go",
			before:      "if hash := rawHash(contents); hash != string(state.ScoreHead.FileHash) {",
			after:       "if hash := rawHash(contents); hash == string(state.ScoreHead.FileHash) {",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreRecoverHaltsMismatchedTargetSnapshot",
		},
		{
			name:   "a started promotion records its target failure",
			source: "internal/runstore/promotion.go",
			before: "case runstate.PromotionPromoting:\n" +
				"\t\trecorded, err := store.promotionTransaction(transaction.runID, state.Promotion.TransactionID)",
			after: "case runstate.PromotionNotPromoted:\n" +
				"\t\trecorded, err := store.promotionTransaction(transaction.runID, state.Promotion.TransactionID)",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreRecoverHaltsMissingTargetSnapshot",
		},
		{
			name:   "recovery sweeps its own promotion temporaries",
			source: "internal/runstore/promotion.go",
			before: "if err := store.sweepPromotionTemporaries(state.Promotion.TransactionID); err != nil {\n" +
				"\t\treturn PromotionResult{}, err\n" +
				"\t}\n",
			after:       "",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreKillCutsRecoverToPromotedFixedPoint/before_root_rename",
		},
		{
			name:        "target-hash recovery completes the original transaction",
			source:      "internal/runstore/promotion.go",
			before:      "case recorded.targetHash:",
			after:       "case \"mutation-target-hash\":",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreKillCutsRecoverToPromotedFixedPoint/after_root_rename",
		},
		{
			name:        "expected-hash recovery resumes the original transaction",
			source:      "internal/runstore/promotion.go",
			before:      "case recorded.expectedHash:",
			after:       "case \"mutation-expected-hash\":",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreKillCutsRecoverToPromotedFixedPoint/before_root_rename",
		},
		{
			name:        "neither-hash recovery halts for an operator",
			source:      "internal/runstore/promotion.go",
			before:      "return PromotionResult{Outcome: PromotionOutcomeRecoveryRequired, Detail: detail}, nil",
			after:       "return PromotionResult{Outcome: PromotionOutcomePromoted}, nil",
			packagePath: "./cmd/partitur",
			testName:    "TestPromoteScoreRecoveryHaltLeavesJournalFixed",
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
