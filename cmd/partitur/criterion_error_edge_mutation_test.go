//go:build mutation

package main

import (
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationCriterionErrorEndpointAssertions(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, before, after, testName, wantFailure string
	}{
		{
			name:        "requires exactly one criterion error",
			before:      "\tif errorCount != 1 {",
			after:       "\tif false && errorCount != 1 { // mutation: exact error count unchecked",
			testName:    "TestValidateCriterionErrorEndpointSynthetic/exactly_one_error",
			wantFailure: "ERROR count=2",
		},
		{
			name:        "requires workspace verification detail",
			before:      "\tif errorDetail != \"workspace_verification_failed\" {",
			after:       "\tif false && errorDetail != \"workspace_verification_failed\" { // mutation: detail unchecked",
			testName:    "TestValidateCriterionErrorEndpointSynthetic/verification_detail",
			wantFailure: `detail="other"`,
		},
		{
			name:        "binds the erroring criterion",
			before:      "\tif errorCriterionID != wantCriterionID {",
			after:       "\tif false && errorCriterionID != wantCriterionID { // mutation: error criterion unchecked",
			testName:    "TestValidateCriterionErrorEndpointSynthetic/erroring_criterion",
			wantFailure: `criterion_id="other"`,
		},
		{
			name:        "forbids a later criterion start",
			before:      "\t\tif event.Type == runstate.EventCriterionStarted {",
			after:       "\t\tif false && event.Type == runstate.EventCriterionStarted { // mutation: post-error start unchecked",
			testName:    "TestValidateCriterionErrorEndpointSynthetic/no_post_error_start",
			wantFailure: "criterion.started appears after",
		},
		{
			name:        "left forbids acceptance failure",
			before:      "\t\tif failureCount != 0 {",
			after:       "\t\tif false && failureCount != 0 { // mutation: left failure unchecked",
			testName:    "TestValidateCriterionErrorEndpointSynthetic/left_has_no_failure",
			wantFailure: "left endpoint",
		},
		{
			name:        "right requires exactly one acceptance failure",
			before:      "\tif failureCount != 1 {",
			after:       "\tif false && failureCount != 1 { // mutation: right failure count unchecked",
			testName:    "TestValidateCriterionErrorEndpointSynthetic/right_has_one_failure",
			wantFailure: "right endpoint",
		},
		{
			name:        "right binds failure to erroring criterion",
			before:      "\tif failureCriterionID != errorCriterionID {",
			after:       "\tif false && failureCriterionID != errorCriterionID { // mutation: failure criterion unchecked",
			testName:    "TestValidateCriterionErrorEndpointSynthetic/failure_binds_criterion",
			wantFailure: "want erroring criterion",
		},
		{
			name:        "right requires criterion errored reason",
			before:      "\tif failureReason != \"criterion_errored\" {",
			after:       "\tif false && failureReason != \"criterion_errored\" { // mutation: failure reason unchecked",
			testName:    "TestValidateCriterionErrorEndpointSynthetic/failure_reason",
			wantFailure: `reason="other"`,
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			result := assertPrepareQuiesceMutationKilled(t, environment, "cmd/partitur/kill_harness_test.go", mutation.before, mutation.after, "./cmd/partitur", mutation.testName)
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
			}
			if !strings.Contains(result.Output, `error=\u003cnil\u003e`) {
				t.Fatalf("mutation failed for the wrong reason; invalid fixture should have been accepted\n%s", result.Diagnostic())
			}
			t.Logf("isolated failure: mutation accepted invalid fixture that normally reports %s", mutation.wantFailure)
		})
	}
}
