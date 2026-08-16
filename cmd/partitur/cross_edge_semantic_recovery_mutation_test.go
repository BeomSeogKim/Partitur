//go:build mutation

package main

import (
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

// TestMutationCrossEdgeSemanticRecovery gives every retained comparison
// conjunct, empty-domain guard, generated-id class, and process-identity
// normalizer branch its own leaf oracle. The comparison would otherwise be
// especially vulnerable to a plausible-looking but vacuous normalizer.
func TestMutationCrossEdgeSemanticRecovery(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, before, after, target, signature string
	}{
		{
			name:      "ordered journal events remain compared",
			before:    "\tif !reflect.DeepEqual(left.Events, right.Events) {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryComparisonRejectsRetainedSeeds/idempotence_retained_reason",
			signature: `negative control error=<nil>, want signature "ordered events"`,
		},
		{
			name:      "durable projection remains compared",
			before:    "\tif !reflect.DeepEqual(left.Projection, right.Projection) {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryComparisonRejectsRetainedSeeds/determinism_retained_result_tree",
			signature: `negative control error=<nil>, want signature "durable projection"`,
		},
		{
			name:      "failure kind remains compared",
			before:    "\tif !reflect.DeepEqual(left.Events, right.Events) {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryComparisonRejectsRetainedSeeds/retained_failure_kind",
			signature: `negative control error=<nil>, want signature "ordered events"`,
		},
		{
			name:      "error detail remains compared",
			before:    "\tif !reflect.DeepEqual(left.Events, right.Events) {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryComparisonRejectsRetainedSeeds/retained_error_detail",
			signature: `negative control error=<nil>, want signature "ordered events"`,
		},
		{
			name:      "recovery action trace remains compared",
			before:    "\tif !reflect.DeepEqual(left.Actions, right.Actions) {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryComparisonRejectsRetainedSeeds/retained_action_kind",
			signature: `negative control error=<nil>, want signature "recovery actions"`,
		},
		{
			name:      "command exit class remains compared",
			before:    "\tif left.Command.ExitClass != right.Command.ExitClass {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryComparisonRejectsRetainedSeeds/retained_exit_class",
			signature: `negative control error=<nil>, want signature "recovery exit class"`,
		},
		{
			name:      "named command halt remains compared",
			before:    "\tif left.Command.Halt != right.Command.Halt {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryComparisonRejectsRetainedSeeds/retained_halt",
			signature: `negative control error=<nil>, want signature "recovery halt"`,
		},
		{
			name:      "event domain cannot be empty",
			before:    "\tif len(snapshot.Events) == 0 {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryComparisonRejectsEmptyDomains/events",
			signature: "empty events error=semantic ordered events differ:",
		},
		{
			name:      "projection domain cannot be empty",
			before:    "\tif snapshot.Projection == nil {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryComparisonRejectsEmptyDomains/projection",
			signature: "empty projection error=semantic durable projection differ:",
		},
		{
			name:      "action domain cannot be empty",
			before:    "\tif len(snapshot.Actions) == 0 {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryComparisonRejectsEmptyDomains/actions",
			signature: "empty actions error=semantic recovery actions differ:",
		},
		{
			name:      "run ids alpha rename",
			before:    "\t\"run_id\":                 semanticIDRun,\n",
			after:     "\t\"run_id\":                 \"\", // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/generated_id_classes",
			signature: "alpha-renamed generated identities differ:",
		},
		{
			name:      "event and causation ids alpha rename",
			before:    "\t\"event_id\":               semanticIDEvent,\n",
			after:     "\t\"event_id\":               \"\", // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/generated_id_classes",
			signature: "alpha-renamed generated identities differ:",
		},
		{
			name:      "attempt ids alpha rename",
			before:    "\t\"attempt_id\":             semanticIDAttempt,\n",
			after:     "\t\"attempt_id\":             \"\", // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/generated_id_classes",
			signature: "alpha-renamed generated identities differ:",
		},
		{
			name:      "prepare ids alpha rename",
			before:    "\t\"prepare_id\":             semanticIDPrepare,\n",
			after:     "\t\"prepare_id\":             \"\", // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/generated_id_classes",
			signature: "alpha-renamed generated identities differ:",
		},
		{
			name:      "proposal ids alpha rename",
			before:    "\t\"proposal_id\":            semanticIDProposal,\n",
			after:     "\t\"proposal_id\":            \"\", // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/generated_id_classes",
			signature: "alpha-renamed generated identities differ:",
		},
		{
			name:      "decision ids alpha rename",
			before:    "\t\"decision_id\":            semanticIDDecision,\n",
			after:     "\t\"decision_id\":            \"\", // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/generated_id_classes",
			signature: "alpha-renamed generated identities differ:",
		},
		{
			name:      "transaction ids alpha rename",
			before:    "\t\"txn_id\":                 semanticIDTxn,\n",
			after:     "\t\"txn_id\":                 \"\", // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/generated_id_classes",
			signature: "alpha-renamed generated identities differ:",
		},
		{
			name:      "candidate ids alpha rename",
			before:    "\t\"candidate_id\":           semanticIDCandidate,\n",
			after:     "\t\"candidate_id\":           \"\", // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/generated_id_classes",
			signature: "alpha-renamed generated identities differ:",
		},
		{
			name:      "interval ids alpha rename",
			before:    "\t\"interval_id\":            semanticIDInterval,\n",
			after:     "\t\"interval_id\":            \"\", // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/generated_id_classes",
			signature: "alpha-renamed generated identities differ:",
		},
		{
			name:      "recorded process identities alpha rename",
			before:    "\tif hasPID && hasStart {\n",
			after:     "\tif false && hasPID && hasStart { // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/generated_id_classes",
			signature: "alpha-renamed generated identities differ:",
		},
		{
			name:      "timestamp value is replaced but key remains",
			before:    "\tcase \"ts\", \"prepared_at\", \"latest_quiesce_observed_at\", \"wall_start\", \"observed_at\":\n",
			after:     "\tcase \"prepared_at\", \"latest_quiesce_observed_at\", \"wall_start\", \"observed_at\": // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/timestamp_presence",
			signature: "timestamp values differ after normalization:",
		},
		{
			name:      "timestamp-derived charge value is normalized",
			before:    "\t\tcase context.clampedExecutionStopped && isTimestampDerivedChargePath(fieldPath):\n",
			after:     "\t\tcase false && context.clampedExecutionStopped && isTimestampDerivedChargePath(fieldPath): // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/timestamp_derived_charge",
			signature: "timestamp-derived charges differ after normalization:",
		},
		{
			name:      "timestamp-derived charge key remains",
			before:    "\t\t\tresult[key] = semanticTimestampDerivedCharge\n",
			after:     "\t\t\tdelete(result, key) // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/timestamp_derived_charge_presence",
			signature: "missing timestamp-derived charge error=<nil>, want ordered event difference",
		},
		{
			name:      "timestamp-derived budget projection is normalized",
			before:    "\t\tcase normalizer.timestampDerivedChargeSeen && isTimestampDerivedBudgetPath(fieldPath):\n",
			after:     "\t\tcase false && normalizer.timestampDerivedChargeSeen && isTimestampDerivedBudgetPath(fieldPath): // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/timestamp_derived_charge_failure_classification_projection",
			signature: "timestamp-derived failure classification budgets differ after normalization:",
		},
		{
			name:      "failure-classification budget path remains declared",
			before:    "\tsemanticPath(\"projection.current_head_attempt.failure_classification.remaining_time_ms\"): {\n\t\tnormalization: semanticTimestampDerivedBudget,\n\t},\n",
			after:     "\tsemanticPath(\"projection.current_head_attempt.failure_classification.remaining_time_ms\"): {}, // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/timestamp_derived_charge_failure_classification_projection",
			signature: "timestamp-derived failure classification budgets differ after normalization:",
		},
		{
			name:      "budget and clock fields are collected",
			before:    "\t\t\tif semanticBudgetClockValueClassFor(normalizedKey, item) != semanticBudgetClockLiteral {\n",
			after:     "\t\t\tif false { // mutation\n",
			target:    "TestSemanticRecoveryBudgetClockCompleteness/synthetic_derived_projection_field",
			signature: "synthetic derived field error=<nil>, want unclassified projection path",
		},
		{
			name:      "timestamps share the normalizer and completeness classifier",
			before:    "\tif isTimestampValue(key, value) {\n",
			after:     "\tif false { // mutation\n",
			target:    "TestSemanticRecoveryBudgetClockCompleteness/synthetic_unclassified_timestamp_field",
			signature: "synthetic timestamp field error=<nil>, want unclassified projection path",
		},
		{
			name:      "millisecond-shaped fields are collected",
			before:    "\t\tstrings.HasSuffix(key, \"_ms\") ||\n",
			after:     "\t\tfalse || // mutation\n",
			target:    "TestSemanticRecoveryBudgetClockCompleteness/time_shaped_fields_are_collected",
			signature: `time-shaped field "event.payload.quiesce_silence_limit_ms" was not collected`,
		},
		{
			name:      "unclassified budget and clock fields are rejected",
			before:    "\t\tif !declared {\n",
			after:     "\t\tif false { // mutation\n\t\t\t_ = declared\n",
			target:    "TestSemanticRecoveryBudgetClockCompleteness/synthetic_derived_projection_field",
			signature: "synthetic derived field error=budget/clock-derived field at projection.scheduler.unclassified_budget_ms has neither normalization nor literal reason",
		},
		{
			name:      "literal budget and clock fields retain a reason",
			before:    "\t\tliteralReason: \"retries consumed is a count, not a clock-derived magnitude\",\n",
			after:     "\t\tliteralReason: \"\", // mutation\n",
			target:    "TestSemanticRecoveryBudgetClockCompleteness/literal_allow_list",
			signature: "literal budget/clock fields require stated allow-list reasons:",
		},
		{
			name:      "open execution wall start remains declared",
			before:    "\tsemanticPath(\"projection.state.open_execution.wall_start\"): {\n\t\tliteralReason: \"open execution timestamp is normalized by the timestamp rule, not as a derived magnitude\",\n\t},\n",
			after:     "\tsemanticPath(\"projection.state.open_execution.wall_start\"): {}, // mutation\n",
			target:    "TestSemanticRecoveryBudgetClockCompleteness/declared_normalized_classes",
			signature: "declared budget/clock field classes do not cover fixture: budget/clock-derived field at projection.state.open_execution.wall_start has neither normalization nor literal reason",
		},
		{
			name:      "timestamp-derived charge formula is checked",
			before:    "\tif charged != want {\n",
			after:     "\tif false && charged != want { // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/timestamp_derived_charge_formula",
			signature: "invalid timestamp-derived charge error=<nil>",
		},
		{
			name:      "negative elapsed charge takes the lower clamp",
			before:    "\t\twant = 0\n",
			after:     "\t\twant = -1 // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/timestamp_derived_charge_formula_negative_elapsed",
			signature: "negative elapsed timestamp-derived charge: timestamp-derived charged_duration=0",
		},
		{
			name:      "negative elapsed lower-clamp comparison retains its operator",
			before:    "\tif want < 0 {\n",
			after:     "\tif want > 0 { // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/timestamp_derived_charge_formula_negative_elapsed",
			signature: "negative elapsed timestamp-derived charge: timestamp-derived charged_duration=0",
		},
		{
			name:      "elapsed beyond remaining takes the upper clamp",
			before:    "\t\twant = interval.remainingAtStart\n",
			after:     "\t\twant = interval.remainingAtStart - 1 // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/timestamp_derived_charge_formula_beyond_remaining_at_start",
			signature: "beyond remaining-at-start timestamp-derived charge: timestamp-derived charged_duration=1000",
		},
		{
			name:      "remaining ceiling comparison retains its operator",
			before:    "\tif want > interval.remainingAtStart {\n",
			after:     "\tif want < interval.remainingAtStart { // mutation\n",
			target:    "TestSemanticRecoveryNormalizer/timestamp_derived_charge_formula_beyond_remaining_at_start",
			signature: "beyond remaining-at-start timestamp-derived charge: timestamp-derived charged_duration=1000",
		},
		{
			name:      "log events are diagnostics only",
			before:    "\treturn eventType == string(runstate.EventLog) || eventType == string(runstate.EventProgress)\n",
			after:     "\treturn false || eventType == string(runstate.EventProgress) // mutation\n",
			target:    "TestSemanticRecoveryJournalExcludesOnlyDiagnostics",
			signature: "want only attempt.failed",
		},
		{
			name:      "progress events are diagnostics only",
			before:    "\treturn eventType == string(runstate.EventLog) || eventType == string(runstate.EventProgress)\n",
			after:     "\treturn eventType == string(runstate.EventLog) || false // mutation\n",
			target:    "TestSemanticRecoveryJournalExcludesOnlyDiagnostics",
			signature: "want only attempt.failed",
		},
	} {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			result := assertPrepareQuiesceMutationKilledWithTargets(t, environment,
				"cmd/partitur/cross_edge_semantic_recovery_test.go", mutation.before, mutation.after,
				"./cmd/partitur", mutation.target, []string{mutation.target})
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
			}
			if output := decodedChildOutput(result.Output); !strings.Contains(output, mutation.signature) {
				t.Fatalf("mutation failed for the wrong reason; want signature %q\n%s", mutation.signature, result.Diagnostic())
			}
			t.Logf("mutation outcome=%s matched signature=%q", result.Outcome, mutation.signature)
		})
	}
}
