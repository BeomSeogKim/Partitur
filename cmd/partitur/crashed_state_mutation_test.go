//go:build mutation

package main

import (
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

// TestMutationCrashedStatePrefixAssertions moves each probe across the durable
// prefix asserted by its kill-harness leaf. Each child therefore dies at the
// same named endpoint with one required durable fact absent (or, for the
// session-sweep boundary, its forbidden interval close already present).
func TestMutationCrashedStatePrefixAssertions(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name, source, before, after, pattern, signature string
		targets                                         []string
	}{
		{
			name:      "authority granted requires its journal authority before the lease",
			source:    "internal/runstore/driver.go",
			before:    "\t\tif _, err := transaction.At(receiptAuthorityGranted).Append(event); err != nil {\n\t\t\treturn err\n\t\t}\n\t\tstore.probe.Reached(faultpoint.PointAuthorityGranted)",
			after:     "\t\tstore.probe.Reached(faultpoint.PointAuthorityGranted)\n\t\tif _, err := transaction.At(receiptAuthorityGranted).Append(event); err != nil { // mutation: pause before durable authority\n\t\t\treturn err\n\t\t}",
			pattern:   "TestSubprocessKillHarness/authority.granted_to_lease_created/default/before",
			targets:   []string{"TestSubprocessKillHarness/authority.granted_to_lease_created/default/before"},
			signature: "crashed authority.granted has no durable authority",
		},
		{
			name:      "authority lease requires its durable lease before selection",
			source:    "internal/runstore/driver.go",
			before:    "\t\tif _, err := transaction.At(receiptDriverLease).CreateLease(true, acquired); err != nil {\n\t\t\treturn err\n\t\t}",
			after:     "\t\tstore.probe.Reached(faultpoint.PointAuthorityLeaseCreated)\n\t\tif _, err := transaction.At(receiptDriverLease).CreateLease(true, acquired); err != nil { // mutation: pause before durable lease\n\t\t\treturn err\n\t\t}",
			pattern:   "TestSubprocessKillHarness/authority.granted_to_lease_created/default/after",
			targets:   []string{"TestSubprocessKillHarness/authority.granted_to_lease_created/default/after"},
			signature: "crashed authority.lease_created lacks durable authority lease",
		},
		{
			name:      "adapter marker requires its durable marker before identity publication",
			source:    "internal/launch/trampoline_unix.go",
			before:    "\tmarker, err := acquireMarker(configuration.LaunchDir, configuration.Nonce)\n\tif err != nil {\n\t\treturn err\n\t}\n\tdefer marker.Close()\n\tif err := keepAcrossExec(marker); err != nil {\n\t\treturn fmt.Errorf(\"preserve launch marker across exec: %w\", err)\n\t}\n\tprobe.Reached(markerPoint(configuration.Kind))",
			after:     "\tprobe.Reached(markerPoint(configuration.Kind))\n\tmarker, err := acquireMarker(configuration.LaunchDir, configuration.Nonce)\n\tif err != nil {\n\t\treturn err\n\t}\n\tdefer marker.Close()\n\tif err := keepAcrossExec(marker); err != nil {\n\t\treturn fmt.Errorf(\"preserve launch marker across exec: %w\", err)\n\t}",
			pattern:   "TestSubprocessKillHarness/launch.adapter.marker_held_to_identity_published/default/before",
			targets:   []string{"TestSubprocessKillHarness/launch.adapter.marker_held_to_identity_published/default/before"},
			signature: "crashed adapter handoff directories=0, want one",
		},
		{
			name:      "adapter identity publication requires its durable handoff before journal record",
			source:    "internal/launch/trampoline_unix.go",
			before:    "\tif err := publishIdentity(\n\t\tfilepath.Join(configuration.LaunchDir, identityName),\n\t\tcontents,\n\t\tsyncDirectory,\n\t); err != nil {\n\t\treturn err\n\t}\n\tprobe.Reached(identityPoint(configuration.Kind))",
			after:     "\tprobe.Reached(identityPoint(configuration.Kind))\n\tif err := publishIdentity(\n\t\tfilepath.Join(configuration.LaunchDir, identityName),\n\t\tcontents,\n\t\tsyncDirectory,\n\t); err != nil {\n\t\treturn err\n\t}",
			pattern:   "TestSubprocessKillHarness/launch.adapter.marker_held_to_identity_published/default/after",
			targets:   []string{"TestSubprocessKillHarness/launch.adapter.marker_held_to_identity_published/default/after"},
			signature: "crashed adapter identity-published has no published handoff identity",
		},
		{
			name:      "adapter identity record requires attempt started before adapter probe",
			source:    "internal/launch/launch_unix.go",
			before:    "\tjournalReceipt, err := request.RecordIdentity(identity)\n\tif err != nil {",
			after:     "\treach(request.Probe, recordedPoint(request.Kind))\n\tjournalReceipt, err := request.RecordIdentity( // mutation: pause before durable identity record\n\t\tidentity)\n\tif err != nil {",
			pattern:   "TestSubprocessKillHarness/launch.adapter.identity_published_to_recorded/default/after",
			targets:   []string{"TestSubprocessKillHarness/launch.adapter.identity_published_to_recorded/default/after"},
			signature: "crashed adapter identity-recorded launches=0, want one",
		},
		{
			name:      "adapter gate observes the recorded identity prefix",
			source:    "internal/launch/launch_unix.go",
			before:    "\tjournalReceipt, err := request.RecordIdentity(identity)\n\tif err != nil {",
			after:     "\treach(request.Probe, gatePoint(request.Kind))\n\tjournalReceipt, err := request.RecordIdentity( // mutation: expose gate before identity record\n\t\tidentity)\n\tif err != nil {",
			pattern:   "TestSubprocessKillHarness/launch.adapter.recorded_to_gate/default/after",
			targets:   []string{"TestSubprocessKillHarness/launch.adapter.recorded_to_gate/default/after"},
			signature: "crashed adapter identity-recorded launches=0, want one",
		},
		{
			name:      "adapter sweep excludes a durable interval stop",
			source:    "internal/adapter/execute.go",
			before:    "\treach(plan.Probe, faultpoint.PointExecuteAdapterSwept)\n\tif err := c.recordStop(&state); err != nil {\n\t\treturn state.report, err\n\t}",
			after:     "\tif err := c.recordStop(&state); err != nil { // mutation: stop before sweep boundary\n\t\treturn state.report, err\n\t}\n\treach(plan.Probe, faultpoint.PointExecuteAdapterSwept)",
			pattern:   "TestSubprocessKillHarness/execute.adapter_swept_to_interval_stopped/default/before",
			targets:   []string{"TestSubprocessKillHarness/execute.adapter_swept_to_interval_stopped/default/before"},
			signature: "crashed adapter sweep has no durable open adapter execution",
		},
		{
			name:      "execution stop requires its durable closed interval before outcome",
			source:    "internal/adapter/execute.go",
			before:    "\treceipt, err := state.plan.Recorder.RecordExecutionStopped(ExecutionStop{\n\t\tIntervalID: state.plan.IntervalID, Reason: \"normal\", Charging: \"measured\",",
			after:     "\treach(state.plan.Probe, faultpoint.PointExecuteIntervalStopped)\n\treceipt, err := state.plan.Recorder.RecordExecutionStopped(ExecutionStop{ // mutation: pause before durable interval stop\n\t\tIntervalID: state.plan.IntervalID, Reason: \"normal\", Charging: \"measured\",",
			pattern:   "TestSubprocessKillHarness/execute.interval_stopped_to_outcome/default/before",
			targets:   []string{"TestSubprocessKillHarness/execute.interval_stopped_to_outcome/default/before"},
			signature: "crashed execution-stopped lacks closed durable adapter interval",
		},
		{
			name:      "adapter outcome requires its durable completion before acceptance",
			source:    "internal/adapter/execute.go",
			before:    "\treceipt, err := state.plan.Recorder.RecordOutcome(OutcomeObservation{\n\t\tEventType: eventType, Result: result, FailureReason: reason,",
			after:     "\treach(state.plan.Probe, faultpoint.PointExecuteOutcomeRecorded)\n\treceipt, err := state.plan.Recorder.RecordOutcome(OutcomeObservation{ // mutation: pause before durable adapter outcome\n\t\tEventType: eventType, Result: result, FailureReason: reason,",
			pattern:   "TestSubprocessKillHarness/execute.interval_stopped_to_outcome/default/after",
			targets:   []string{"TestSubprocessKillHarness/execute.interval_stopped_to_outcome/default/after"},
			signature: "crashed adapter outcome lacks durable completed adapter result",
		},
		{
			name:      "attempt completion requires its durable event before movement success",
			source:    "internal/driver/driver.go",
			before:    "\t\treceipt, err := authority.Append(event, faultpoint.ReceiptAddress(address))\n\t\tif err != nil {",
			after:     "\t\tif eventType == runstate.EventAttemptCompleted {\n\t\t\tdependencies.probe.Reached(faultpoint.PointLifecycleAttemptCompleted)\n\t\t}\n\t\treceipt, err := authority.Append( // mutation: pause before durable attempt completion\n\t\t\tevent, faultpoint.ReceiptAddress(address))\n\t\tif err != nil {",
			pattern:   "TestSubprocessKillHarness/lifecycle.attempt_completed_to_movement_succeeded/default/before",
			targets:   []string{"TestSubprocessKillHarness/lifecycle.attempt_completed_to_movement_succeeded/default/before"},
			signature: "crashed attempt-completed lacks durable completed attempt",
		},
		{
			name:      "movement success requires its durable result before lease release",
			source:    "internal/driver/driver.go",
			before:    "\t\treceipt, err := authority.Append(event, faultpoint.ReceiptAddress(address))\n\t\tif err != nil {",
			after:     "\t\tif eventType == runstate.EventMovementSucceeded {\n\t\t\tdependencies.probe.Reached(faultpoint.PointLifecycleMovementSucceeded)\n\t\t}\n\t\treceipt, err := authority.Append( // mutation: pause before durable movement success\n\t\t\tevent, faultpoint.ReceiptAddress(address))\n\t\tif err != nil {",
			pattern:   "TestSubprocessKillHarness/lifecycle.attempt_completed_to_movement_succeeded/default/after",
			targets:   []string{"TestSubprocessKillHarness/lifecycle.attempt_completed_to_movement_succeeded/default/after"},
			signature: "crashed movement-succeeded lacks durable final movement success",
		},
		{
			name:    "acceptance evaluation requires its durable completion before human gate",
			source:  "internal/driver/driver.go",
			before:  "\tevaluation, err := acceptance.EvaluateStarted(plan, evaluationInput)\n\tif err != nil {",
			after:   "\tdependencies.probe.Reached(faultpoint.PointAcceptanceEvaluationCompleted)\n\tevaluation, err := acceptance.EvaluateStarted( // mutation: pause before durable acceptance evaluation\n\t\tplan, evaluationInput)\n\tif err != nil {",
			pattern: "TestSubprocessKillHarness/acceptance.evaluation_completed_to_decision_requested",
			targets: []string{
				"TestSubprocessKillHarness/acceptance.evaluation_completed_to_decision_requested/always/before",
				"TestSubprocessKillHarness/acceptance.evaluation_completed_to_decision_requested/on_contested/before",
			},
			signature: "crashed acceptance evaluation has no durable completed evaluation",
		},
		{
			name:    "human gate request requires its durable decision before attempt completion",
			source:  "internal/driver/driver.go",
			before:  "\t\tif _, err := appendEvent(runstate.EventDecisionRequested, payload, \"acceptance.decision.requested.human_gate\"); err != nil {\n\t\t\treturn stopped(result, err)",
			after:   "\t\tdependencies.probe.Reached(faultpoint.PointHumanGateDecisionRequested)\n\t\tif _, err := appendEvent( // mutation: pause before durable human gate\n\t\t\trunstate.EventDecisionRequested, payload, \"acceptance.decision.requested.human_gate\"); err != nil {\n\t\t\treturn stopped(result, err)",
			pattern: "TestSubprocessKillHarness/acceptance.evaluation_completed_to_decision_requested",
			targets: []string{
				"TestSubprocessKillHarness/acceptance.evaluation_completed_to_decision_requested/always/after",
				"TestSubprocessKillHarness/acceptance.evaluation_completed_to_decision_requested/on_contested/after",
			},
			signature: "crashed human-gate request has no durable pending gate",
		},
	}
	if len(mutations) == 0 {
		t.Fatal("crashed-state mutation domain is empty")
	}

	for _, mutation := range mutations {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			result := assertPrepareQuiesceMutationKilledWithTargets(t, environment,
				mutation.source, mutation.before, mutation.after, "./cmd/partitur", mutation.pattern, mutation.targets)
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
			}
			if output := decodedChildOutput(result.Output); !strings.Contains(output, mutation.signature) {
				t.Fatalf("mutation failed for the wrong reason; want decoded signature %q\n%s", mutation.signature, result.Diagnostic())
			}
			t.Logf("measured negative control: decoded output matched %q", mutation.signature)
		})
	}
}
