//go:build mutation

package main

import (
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

// TestMutationFixedPointConvergenceClassification anchors every branch,
// conjunct, and comparison operator in the classified projection check. The
// child targets are untagged leaf tests: mutationtest.Child builds without
// tags, so a tagged fixture would be a non-result rather than coverage.
func TestMutationFixedPointConvergenceClassification(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, before, after, target, signature string
	}{
		{
			name:      "waiting-human is an explicit branch",
			before:    "if state.Run == runstate.RunWaitingHuman {",
			after:     "if false { // mutation: skip WAITING_HUMAN branch",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/waiting_human_declares_command_specific_recovery",
			signature: `negative control error=non-halted fixed point lifecycle = "WAITING_HUMAN", want signature "WAITING_HUMAN fixed point declares"`,
		},
		{
			name:      "waiting-human rejects a command-specific declaration",
			before:    "if fixture.commandSpecificRecovery != fixedPointRecoveryNone {",
			after:     "if false { // mutation: admit command-specific WAITING_HUMAN",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/waiting_human_declares_command_specific_recovery",
			signature: `negative control error=<nil>, want signature "WAITING_HUMAN fixed point declares"`,
		},
		{
			name:      "ordinary convergence requires a terminal lifecycle",
			before:    "\t\treturn fixedPointCommandSpecificProjectionError(fixedPointRecoveryNone, state)\n\t}\n\tif !state.Run.Terminal() {\n\t\treturn fmt.Errorf(\"non-halted fixed point lifecycle = %q\", state.Run)\n\t}\n\treturn fixedPointCommandSpecificProjectionError",
			after:     "\t\treturn fixedPointCommandSpecificProjectionError(fixedPointRecoveryNone, state)\n\t}\n\tif false { // mutation: admit nonterminal recovery\n\t\treturn fmt.Errorf(\"non-halted fixed point lifecycle = %q\", state.Run)\n\t}\n\treturn fixedPointCommandSpecificProjectionError",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/nonterminal_run_is_not_a_fixed_point",
			signature: `negative control error=<nil>, want signature "non-halted fixed point lifecycle"`,
		},
		{
			name:      "none rejects APPLYING",
			before:    "case fixedPointRecoveryNone:\n\t\t// Keep the pre-reconciliation assertion exactly: ordinary convergence\n\t\t// admits neither in-progress nor recovery-required command state.\n\t\tif state.Application.State == runstate.ApplicationApplying || state.Application.State == runstate.ApplicationRecoveryRequired {",
			after:     "case fixedPointRecoveryNone:\n\t\t// mutation: retain only RECOVERY_REQUIRED\n\t\tif state.Application.State == runstate.ApplicationRecoveryRequired {",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/undeclared_application_applying",
			signature: `negative control error=<nil>, want signature "unsettled application projection"`,
		},
		{
			name:      "none rejects application RECOVERY_REQUIRED",
			before:    "case fixedPointRecoveryNone:\n\t\t// Keep the pre-reconciliation assertion exactly: ordinary convergence\n\t\t// admits neither in-progress nor recovery-required command state.\n\t\tif state.Application.State == runstate.ApplicationApplying || state.Application.State == runstate.ApplicationRecoveryRequired {",
			after:     "case fixedPointRecoveryNone:\n\t\t// mutation: retain only APPLYING\n\t\tif state.Application.State == runstate.ApplicationApplying {",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/undeclared_application_recovery_required",
			signature: `negative control error=<nil>, want signature "unsettled application projection"`,
		},
		{
			name:      "none combines application guards with OR",
			before:    "case fixedPointRecoveryNone:\n\t\t// Keep the pre-reconciliation assertion exactly: ordinary convergence\n\t\t// admits neither in-progress nor recovery-required command state.\n\t\tif state.Application.State == runstate.ApplicationApplying || state.Application.State == runstate.ApplicationRecoveryRequired {",
			after:     "case fixedPointRecoveryNone:\n\t\t// mutation: conjunction weakens the ordinary branch\n\t\tif state.Application.State == runstate.ApplicationApplying && declaration == fixedPointRecoveryNone {",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/undeclared_application_recovery_required",
			signature: `negative control error=<nil>, want signature "unsettled application projection"`,
		},
		{
			name:      "none rejects PROMOTING",
			before:    "\t\tif state.Promotion.State == runstate.PromotionPromoting || state.Promotion.State == runstate.PromotionRecoveryRequired {\n\t\t\treturn fmt.Errorf(\"unsettled promotion projection after fixed-point recovery = %+v\", state.Promotion)\n\t\t}\n\t\treturn nil\n\tcase fixedPointRecoveryApplication:",
			after:     "\t\tif state.Promotion.State == runstate.PromotionRecoveryRequired {\n\t\t\treturn fmt.Errorf(\"unsettled promotion projection after fixed-point recovery = %+v\", state.Promotion)\n\t\t}\n\t\treturn nil\n\tcase fixedPointRecoveryApplication:",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/undeclared_promotion_promoting",
			signature: `negative control error=<nil>, want signature "unsettled promotion projection"`,
		},
		{
			name:      "none rejects promotion RECOVERY_REQUIRED",
			before:    "\t\tif state.Promotion.State == runstate.PromotionPromoting || state.Promotion.State == runstate.PromotionRecoveryRequired {\n\t\t\treturn fmt.Errorf(\"unsettled promotion projection after fixed-point recovery = %+v\", state.Promotion)\n\t\t}\n\t\treturn nil\n\tcase fixedPointRecoveryApplication:",
			after:     "\t\tif state.Promotion.State == runstate.PromotionPromoting {\n\t\t\treturn fmt.Errorf(\"unsettled promotion projection after fixed-point recovery = %+v\", state.Promotion)\n\t\t}\n\t\treturn nil\n\tcase fixedPointRecoveryApplication:",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/undeclared_promotion_recovery_required",
			signature: `negative control error=<nil>, want signature "unsettled promotion projection"`,
		},
		{
			name:      "application declaration requires RECOVERY_REQUIRED rather than APPLYING",
			before:    "if state.Application.State != runstate.ApplicationRecoveryRequired {",
			after:     "if state.Application.State != runstate.ApplicationApplying {",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/applying_is_not_recovery_required",
			signature: `negative control error=<nil>, want signature "application recovery declaration requires"`,
		},
		{
			name:      "application declaration rejects PROMOTING",
			before:    "\t\tif state.Promotion.State == runstate.PromotionPromoting || state.Promotion.State == runstate.PromotionRecoveryRequired {\n\t\t\treturn fmt.Errorf(\"application recovery declaration retains unsettled promotion projection = %+v\", state.Promotion)\n\t\t}\n\t\treturn nil\n\tcase fixedPointRecoveryPromotion:",
			after:     "\t\tif state.Promotion.State == runstate.PromotionRecoveryRequired {\n\t\t\treturn fmt.Errorf(\"application recovery declaration retains unsettled promotion projection = %+v\", state.Promotion)\n\t\t}\n\t\treturn nil\n\tcase fixedPointRecoveryPromotion:",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/declared_application_with_promotion_unsettled",
			signature: `negative control error=<nil>, want signature "application recovery declaration retains unsettled promotion"`,
		},
		{
			name:      "application declaration rejects promotion RECOVERY_REQUIRED",
			before:    "\t\tif state.Promotion.State == runstate.PromotionPromoting || state.Promotion.State == runstate.PromotionRecoveryRequired {\n\t\t\treturn fmt.Errorf(\"application recovery declaration retains unsettled promotion projection = %+v\", state.Promotion)\n\t\t}\n\t\treturn nil\n\tcase fixedPointRecoveryPromotion:",
			after:     "\t\tif state.Promotion.State == runstate.PromotionPromoting {\n\t\t\treturn fmt.Errorf(\"application recovery declaration retains unsettled promotion projection = %+v\", state.Promotion)\n\t\t}\n\t\treturn nil\n\tcase fixedPointRecoveryPromotion:",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/declared_application_with_promotion_recovery_required",
			signature: `negative control error=<nil>, want signature "application recovery declaration retains unsettled promotion"`,
		},
		{
			name:      "promotion declaration rejects APPLYING",
			before:    "\tcase fixedPointRecoveryPromotion:\n\t\tif state.Application.State == runstate.ApplicationApplying || state.Application.State == runstate.ApplicationRecoveryRequired {",
			after:     "\tcase fixedPointRecoveryPromotion:\n\t\tif state.Application.State == runstate.ApplicationRecoveryRequired {",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/declared_promotion_with_application_unsettled",
			signature: `negative control error=<nil>, want signature "promotion recovery declaration retains unsettled application"`,
		},
		{
			name:      "promotion declaration rejects application RECOVERY_REQUIRED",
			before:    "\tcase fixedPointRecoveryPromotion:\n\t\tif state.Application.State == runstate.ApplicationApplying || state.Application.State == runstate.ApplicationRecoveryRequired {",
			after:     "\tcase fixedPointRecoveryPromotion:\n\t\tif state.Application.State == runstate.ApplicationApplying {",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/declared_promotion_with_application_recovery_required",
			signature: `negative control error=<nil>, want signature "promotion recovery declaration retains unsettled application"`,
		},
		{
			name:      "promotion declaration requires RECOVERY_REQUIRED rather than PROMOTING",
			before:    "if state.Promotion.State != runstate.PromotionRecoveryRequired {",
			after:     "if state.Promotion.State != runstate.PromotionPromoting {",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/promoting_is_not_recovery_required",
			signature: `negative control error=<nil>, want signature "promotion recovery declaration requires"`,
		},
		{
			name:      "only the three fixture declarations are accepted",
			before:    "\tdefault:\n\t\treturn fmt.Errorf(\"unknown command-specific recovery declaration %q\", declaration)\n\t}\n}\n\nfunc assertSettledLifecycle",
			after:     "\tdefault:\n\t\treturn nil // mutation: accept undeclared command recovery\n\t}\n}\n\nfunc assertSettledLifecycle",
			target:    "TestFixedPointRecoveryClassificationRejectsUnsettledProjections/both_declarations_and_both_projections_unsettled",
			signature: `negative control error=<nil>, want signature "unknown command-specific recovery declaration"`,
		},
	} {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			result := assertPrepareQuiesceMutationKilledWithTargets(t, environment,
				"cmd/partitur/kill_harness_test.go", mutation.before, mutation.after,
				"./cmd/partitur", mutation.target, []string{mutation.target})
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
			}
			if output := decodedChildOutput(result.Output); !strings.Contains(output, mutation.signature) {
				t.Fatalf("mutation failed for the wrong reason; want decoded signature %q\n%s", mutation.signature, result.Diagnostic())
			}
			t.Logf("mutation outcome=%s matched decoded signature=%q", result.Outcome, mutation.signature)
		})
	}
}

func TestMutationFixedPointConvergenceEffects(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, before, after, target, signature string
	}{
		{
			name:      "completed attempt requires the movement-success journal effect",
			before:    "if !hasEventAfter(events, index, runstate.EventMovementSucceeded, event.MovementID, event.AttemptID) ||\n\t\t\t\tstate.Movements[event.MovementID] != runstate.MovementSucceeded {",
			after:     "if false ||\n\t\t\t\tstate.Movements[event.MovementID] != runstate.MovementSucceeded {",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/completed_attempt_requires_movement_success",
			signature: `negative control error=<nil>, want signature "completed attempt"`,
		},
		{
			name:      "completed attempt requires the projected movement-success effect",
			before:    "state.Movements[event.MovementID] != runstate.MovementSucceeded {",
			after:     "state.Movements[event.MovementID] == runstate.MovementSucceeded {",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/completed_attempt_requires_projected_movement_success",
			signature: `negative control error=<nil>, want signature "completed attempt"`,
		},
		{
			name:      "failed movement requires the FAILED projection",
			before:    "if state.Run != runstate.RunFailed {",
			after:     "if state.Run == runstate.RunFailed {",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/failed_movement_requires_projected_run_failure",
			signature: `negative control error=<nil>, want signature "failed movement"`,
		},
		{
			name:      "failed movement requires a durable run failure",
			before:    "if hasEventAfter(events, index, runstate.EventRunFailed, \"\", \"\") {",
			after:     "if true { // mutation: invent run.failed",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/failed_movement_requires_durable_run_failure",
			signature: `negative control error=<nil>, want signature "failed movement"`,
		},
		{
			name:      "only criterion ERROR requires acceptance failure",
			before:    "case runstate.EventCriterionCompleted:\n\t\t\tpayload, err := decodeFixedPointPayload(event)\n\t\t\tif err != nil {\n\t\t\t\treturn err\n\t\t\t}\n\t\t\tif payload[\"outcome\"] != \"ERROR\" {",
			after:     "case runstate.EventCriterionCompleted:\n\t\t\tpayload, err := decodeFixedPointPayload(event)\n\t\t\tif err != nil {\n\t\t\t\treturn err\n\t\t\t}\n\t\t\tif payload[\"outcome\"] == \"ERROR\" {",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/criterion_error_requires_acceptance_failure",
			signature: `negative control error=<nil>, want signature "criterion ERROR"`,
		},
		{
			name:      "criterion error requires the acceptance-failure journal effect",
			before:    "if !hasEventAfter(events, index, runstate.EventAcceptanceFailed, event.MovementID, event.AttemptID) ||\n\t\t\t\t!present || attempt.State != runstate.AttemptFailed {",
			after:     "if false ||\n\t\t\t\t!present || attempt.State != runstate.AttemptFailed {",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/criterion_error_requires_durable_acceptance_failure",
			signature: `negative control error=<nil>, want signature "criterion ERROR"`,
		},
		{
			name:      "criterion error requires the failed-attempt projection",
			before:    "!present || attempt.State != runstate.AttemptFailed {",
			after:     "false && (!present || attempt.State != runstate.AttemptFailed) { // mutation: ignore attempt projection",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/criterion_error_requires_projected_failed_attempt",
			signature: `negative control error=<nil>, want signature "criterion ERROR"`,
		},
		{
			name:      "always gate requires a request",
			before:    "gateRequired := gateMode == \"always\" || (gateMode == \"on_contested\" && payload[\"review_outcome\"] == \"CONTESTED\")",
			after:     "gateRequired := gateMode == \"never\" || (gateMode == \"on_contested\" && payload[\"review_outcome\"] == \"CONTESTED\")",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/always_gate_requires_human_gate_request",
			signature: `negative control error=<nil>, want signature "completed evaluation"`,
		},
		{
			name:      "on-contested gate requires a contested request",
			before:    "gateRequired := gateMode == \"always\" || (gateMode == \"on_contested\" && payload[\"review_outcome\"] == \"CONTESTED\")",
			after:     "gateRequired := gateMode == \"always\" || (false && payload[\"review_outcome\"] == \"CONTESTED\")",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/completed_evaluation_requires_durable_human_gate_request",
			signature: `negative control error=<nil>, want signature "completed evaluation"`,
		},
		{
			name:      "completed evaluation requires the durable request",
			before:    "if !hasHumanGateDecisionAfter(events, index, event.MovementID, event.AttemptID) || !humanGateProjects(state, event.MovementID, event.AttemptID) {",
			after:     "if false || !humanGateProjects(state, event.MovementID, event.AttemptID) {",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/completed_evaluation_requires_durable_human_gate_request",
			signature: `negative control error=<nil>, want signature "completed evaluation"`,
		},
		{
			name:      "completed evaluation requires the projected request",
			before:    "if !hasHumanGateDecisionAfter(events, index, event.MovementID, event.AttemptID) || !humanGateProjects(state, event.MovementID, event.AttemptID) {",
			after:     "if !hasHumanGateDecisionAfter(events, index, event.MovementID, event.AttemptID) || false {",
			target:    "TestRecoveryOutcomeImplicationsRequireDurableEffects/completed_evaluation_requires_projected_human_gate",
			signature: `negative control error=<nil>, want signature "completed evaluation"`,
		},
	} {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			result := assertPrepareQuiesceMutationKilledWithTargets(t, environment,
				"cmd/partitur/kill_harness_test.go", mutation.before, mutation.after,
				"./cmd/partitur", mutation.target, []string{mutation.target})
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
			}
			if output := decodedChildOutput(result.Output); !strings.Contains(output, mutation.signature) {
				t.Fatalf("mutation failed for the wrong reason; want decoded signature %q\n%s", mutation.signature, result.Diagnostic())
			}
		})
	}
}

func TestMutationResumeOwnedResidueEnumeration(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, before, after, signature string
	}{
		{
			name:      "lease family is enumerated",
			before:    "if _, err := os.Lstat(lease); err == nil {\n\t\tresidues = append(residues, resumeOwnedResidue{family: \"lease\", path: lease})\n\t} else if !os.IsNotExist(err) {\n\t\treturn nil, fmt.Errorf(\"stat fixed-point driver lease: %w\", err)\n\t}",
			after:     "if _, err := os.Lstat(lease); err == nil && false { // mutation: omit lease\n\t\tresidues = append(residues, resumeOwnedResidue{family: \"lease\", path: lease})\n\t} else if !os.IsNotExist(err) && err != nil {\n\t\treturn nil, fmt.Errorf(\"stat fixed-point driver lease: %w\", err)\n\t}",
			signature: "resume-owned residue family \"lease\" was not enumerated",
		},
		{
			name:      "sidecar family requires regular entries",
			before:    "if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), \"driver.quiesced.\") && entry.Name() != \"driver.quiesced.\" {",
			after:     "if false && strings.HasPrefix(entry.Name(), \"driver.quiesced.\") && entry.Name() != \"driver.quiesced.\" { // mutation: omit sidecar",
			signature: "resume-owned residue family \"sidecar\" was not enumerated",
		},
		{
			name:      "sidecar family requires the quiesce prefix",
			before:    "if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), \"driver.quiesced.\") && entry.Name() != \"driver.quiesced.\" {",
			after:     "if entry.Type().IsRegular() && false && entry.Name() != \"driver.quiesced.\" { // mutation: omit prefix",
			signature: "resume-owned residue family \"sidecar\" was not enumerated",
		},
		{
			name:      "bare sidecar prefix stays out of scope",
			before:    "if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), \"driver.quiesced.\") && entry.Name() != \"driver.quiesced.\" {",
			after:     "if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), \"driver.quiesced.\") && (entry.Name() != \"driver.quiesced.\" || entry.Name() == \"driver.quiesced.\") {",
			signature: "out-of-scope residue was enumerated",
		},
		{
			name:      "prepare staging requires regular files",
			before:    "if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), \".json\") {",
			after:     "if false && strings.HasSuffix(entry.Name(), \".json\") { // mutation: omit plan",
			signature: "resume-owned residue family \"prepare staging\" was not enumerated",
		},
		{
			name:      "prepare staging requires JSON plans",
			before:    "if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), \".json\") {",
			after:     "if entry.Type().IsRegular() && false { // mutation: omit JSON plan",
			signature: "resume-owned residue family \"prepare staging\" was not enumerated",
		},
		{
			name:      "attempt staging tree is enumerated",
			before:    "if _, err := os.Lstat(work); err == nil {\n\t\tresidues = append(residues, resumeOwnedResidue{family: \"attempt staging\", path: work})\n\t} else if !os.IsNotExist(err) {\n\t\treturn nil, fmt.Errorf(\"stat fixed-point attempt staging: %w\", err)\n\t}",
			after:     "if _, err := os.Lstat(work); err == nil && false { // mutation: omit attempt tree\n\t\tresidues = append(residues, resumeOwnedResidue{family: \"attempt staging\", path: work})\n\t} else if !os.IsNotExist(err) && err != nil {\n\t\treturn nil, fmt.Errorf(\"stat fixed-point attempt staging: %w\", err)\n\t}",
			signature: "resume-owned residue family \"attempt staging\" was not enumerated",
		},
	} {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			result := assertPrepareQuiesceMutationKilledWithTargets(t, environment,
				"cmd/partitur/kill_harness_test.go", mutation.before, mutation.after,
				"./cmd/partitur", "TestResumeOwnedResidueEnumeration", []string{"TestResumeOwnedResidueEnumeration"})
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
			}
			if output := decodedChildOutput(result.Output); !strings.Contains(output, mutation.signature) {
				t.Fatalf("mutation failed for the wrong reason; want decoded signature %q\n%s", mutation.signature, result.Diagnostic())
			}
		})
	}
}
